package flags

import (
	"context"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/ids"
)

var (
	// ErrNotFound is a tier or flag that isn't stored.
	ErrNotFound = code.New("flags.not_found")
	// ErrTierExists is a tier key or order already taken.
	ErrTierExists = code.New("flags.tier_exists")
	// ErrTierInvalid is a tier with a malformed key, no name or a name over 120 characters, or an
	// order outside 0–1000000.
	ErrTierInvalid = code.New("flags.tier_invalid")
)

// Store persists tiers, flag states and account overrides (spec.md §3).
type Store interface {
	// Tiers returns the ladder, lowest first.
	Tiers(ctx context.Context) ([]Tier, error)
	// CreateTier adds a tier. ErrTierExists if its key or order is taken; ErrTierInvalid if it is
	// malformed.
	CreateTier(ctx context.Context, t Tier) error
	SetTierEnabled(ctx context.Context, id ids.UUID, enabled bool) error

	// Flags returns every stored flag state, sorted by key.
	Flags(ctx context.Context) ([]State, error)
	// InsertFlag adds a state for a key that isn't stored; a stored key is a no-op.
	InsertFlag(ctx context.Context, s State) error
	// SetFlag changes a flag's switch and minimum tier. ErrNotFound for an unknown key or tier.
	SetFlag(ctx context.Context, key string, enabled bool, minTier *ids.UUID) error
	SetRetired(ctx context.Context, key string, retired bool) error

	// AccountFlags returns an account's overrides by key.
	AccountFlags(ctx context.Context, account ids.UUID) (map[string]bool, error)
	// SetAccountFlag sets an override. ErrNotFound for an unknown flag.
	SetAccountFlag(ctx context.Context, account ids.UUID, key string, enabled bool) error
	// ClearAccountFlag removes an override; removing one that isn't there is not an error.
	ClearAccountFlag(ctx context.Context, account ids.UUID, key string) error
}

// ValidTier reports whether a tier may be stored, for a store that checks before it writes.
func ValidTier(t Tier) bool {
	n := len([]rune(t.Name))
	return keyRe.MatchString(t.Key) && n >= 1 && n <= 120 && t.Order >= 0 && t.Order <= 1000000
}

// Service reconciles and resolves flags against a store.
type Service struct {
	reg   *Registry
	store Store
}

func NewService(reg *Registry, store Store) *Service { return &Service{reg: reg, store: store} }

// Reconcile inserts newly declared flags at their defaults, and marks stored flags the source no
// longer declares as retired (and declared ones as not). Safe on every boot.
func (s *Service) Reconcile(ctx context.Context) (added int, retired []string, err error) {
	stored, err := s.store.Flags(ctx)
	if err != nil {
		return 0, nil, err
	}
	for _, st := range s.reg.Reconcile(stored) {
		if err := s.store.InsertFlag(ctx, st); err != nil {
			return 0, nil, err
		}
		added++
	}
	retired = s.reg.Stale(stored)
	stale := map[string]bool{}
	for _, k := range retired {
		stale[k] = true
	}
	for _, st := range stored {
		if st.Retired != stale[st.Key] {
			if err := s.store.SetRetired(ctx, st.Key, stale[st.Key]); err != nil {
				return 0, nil, err
			}
		}
	}
	return added, retired, nil
}

// For resolves every declared flag for an account on a tier (nil: no tier).
func (s *Service) For(ctx context.Context, account ids.UUID, tier *ids.UUID) (Set, error) {
	tiers, err := s.store.Tiers(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.store.Flags(ctx)
	if err != nil {
		return nil, err
	}
	overrides, err := s.store.AccountFlags(ctx, account)
	if err != nil {
		return nil, err
	}
	return s.reg.ResolveAll(states, tiers, tier, overrides), nil
}
