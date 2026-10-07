package quotas

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
)

// Store persists quota keys, tier limits and overrides (spec.md §3). Tiers are flags.Store's.
type Store interface {
	QuotaKeys(ctx context.Context) ([]KeyState, error)
	// InsertQuotaKey adds a key that isn't stored; a stored key is a no-op.
	InsertQuotaKey(ctx context.Context, k KeyState) error
	SetRetired(ctx context.Context, key string, retired bool) error

	TierLimits(ctx context.Context) ([]TierLimit, error)
	// SetTierLimit sets a tier's limit (nil for an explicit unlimited). ErrNotFound for an unknown
	// key or tier.
	SetTierLimit(ctx context.Context, l TierLimit) error
	// ClearTierLimit removes a tier's row, so it inherits from below again.
	ClearTierLimit(ctx context.Context, tier ids.UUID, key string) error

	// Overrides returns an account's overrides, live or expired, sorted by key.
	Overrides(ctx context.Context, account ids.UUID) ([]Override, error)
	// SetOverride creates or replaces an account's override for a key. ErrNotFound for an unknown key.
	SetOverride(ctx context.Context, o Override) error
	// ClearOverride removes one; removing one that isn't there is not an error.
	ClearOverride(ctx context.Context, account ids.UUID, key string) error
}

// Service reconciles and resolves quotas. Tiers come from the flags store.
type Service struct {
	reg   *Registry
	store Store
	tiers flags.Store
	now   func() time.Time
}

func NewService(reg *Registry, store Store, tiers flags.Store) *Service {
	return &Service{reg: reg, store: store, tiers: tiers, now: time.Now}
}

// WithClock sets the service's clock. For tests.
func (s *Service) WithClock(now func() time.Time) *Service { s.now = now; return s }

// Reconcile inserts newly declared quota keys and marks retired ones. Safe on every boot.
func (s *Service) Reconcile(ctx context.Context) (added int, retired []string, err error) {
	stored, err := s.store.QuotaKeys(ctx)
	if err != nil {
		return 0, nil, err
	}
	for _, k := range s.reg.Reconcile(stored) {
		if err := s.store.InsertQuotaKey(ctx, k); err != nil {
			return 0, nil, err
		}
		added++
	}
	retired = s.reg.Stale(stored)
	stale := map[string]bool{}
	for _, k := range retired {
		stale[k] = true
	}
	for _, k := range stored {
		if k.Retired != stale[k.Key] {
			if err := s.store.SetRetired(ctx, k.Key, stale[k.Key]); err != nil {
				return 0, nil, err
			}
		}
	}
	return added, retired, nil
}

// Limits resolves every declared quota for an account on a tier (nil: none). nil means unlimited.
func (s *Service) Limits(ctx context.Context, account ids.UUID, tier *ids.UUID) (map[string]*int64, error) {
	tiers, err := s.tiers.Tiers(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.TierLimits(ctx)
	if err != nil {
		return nil, err
	}
	overrides, err := s.store.Overrides(ctx, account)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*Override{}
	for i := range overrides {
		byKey[overrides[i].Key] = &overrides[i]
	}
	now := s.now()
	out := map[string]*int64{}
	for _, d := range s.reg.decls {
		out[d.Key] = Limit(d, rows, tiers, tier, byKey[d.Key], now)
	}
	return out, nil
}

// Limit resolves one quota.
func (s *Service) Limit(ctx context.Context, account ids.UUID, tier *ids.UUID, key string) (*int64, error) {
	if _, ok := s.reg.Lookup(key); !ok {
		return nil, ErrUnknownQuota.With("key", key)
	}
	all, err := s.Limits(ctx, account, tier)
	if err != nil {
		return nil, err
	}
	return all[key], nil
}

// Grant sets an account's override, after checking the limit and the reason. Who granted it is
// recorded; the shadow log records the rest.
func (s *Service) Grant(ctx context.Context, o Override) error {
	d, ok := s.reg.Lookup(o.Key)
	if !ok {
		return ErrUnknownQuota.With("key", o.Key)
	}
	if err := d.Check(o.Limit); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(o.Reason)); n < 1 || n > 500 {
		return ErrReasonInvalid
	}
	if o.GrantedBy.IsZero() {
		return ErrGrantorRequired
	}
	if o.GrantedAt.IsZero() {
		o.GrantedAt = s.now()
	}
	return s.store.SetOverride(ctx, o)
}
