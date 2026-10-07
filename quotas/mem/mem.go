// Package mem is the in-memory quotas.Store. Tiers live in the flags store it is given, so a tier
// limit can be checked against them as a foreign key would.
package mem

import (
	"context"
	"sort"
	"sync"

	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/quotas"
)

type tierKey struct {
	tier ids.UUID
	key  string
}

type accountKey struct {
	account ids.UUID
	key     string
}

// Store is safe for concurrent use.
type Store struct {
	tiers     flags.Store
	mu        sync.Mutex
	keys      map[string]quotas.KeyState
	limits    map[tierKey]quotas.TierLimit
	overrides map[accountKey]quotas.Override
}

func New(tiers flags.Store) *Store {
	return &Store{tiers: tiers, keys: map[string]quotas.KeyState{}, limits: map[tierKey]quotas.TierLimit{}, overrides: map[accountKey]quotas.Override{}}
}

var _ quotas.Store = (*Store)(nil)

func (s *Store) QuotaKeys(context.Context) ([]quotas.KeyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]quotas.KeyState, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Store) InsertQuotaKey(_ context.Context, k quotas.KeyState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[k.Key]; !ok {
		s.keys[k.Key] = k
	}
	return nil
}

func (s *Store) SetRetired(_ context.Context, key string, retired bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[key]
	if !ok {
		return quotas.ErrNotFound
	}
	k.Retired = retired
	s.keys[key] = k
	return nil
}

func (s *Store) TierLimits(context.Context) ([]quotas.TierLimit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]quotas.TierLimit, 0, len(s.limits))
	for _, l := range s.limits {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].TierID.String() < out[j].TierID.String()
	})
	return out, nil
}

func (s *Store) tierExists(ctx context.Context, id ids.UUID) bool {
	tiers, err := s.tiers.Tiers(ctx)
	if err != nil {
		return false
	}
	for _, t := range tiers {
		if t.ID == id {
			return true
		}
	}
	return false
}

func (s *Store) SetTierLimit(ctx context.Context, l quotas.TierLimit) error {
	if !s.tierExists(ctx, l.TierID) {
		return quotas.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[l.Key]; !ok {
		return quotas.ErrNotFound
	}
	if l.Limit != nil {
		v := *l.Limit
		l.Limit = &v
	}
	s.limits[tierKey{l.TierID, l.Key}] = l
	return nil
}

func (s *Store) ClearTierLimit(_ context.Context, tier ids.UUID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.limits, tierKey{tier, key})
	return nil
}

func (s *Store) Overrides(_ context.Context, account ids.UUID) ([]quotas.Override, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []quotas.Override
	for k, o := range s.overrides {
		if k.account == account {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Store) SetOverride(_ context.Context, o quotas.Override) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[o.Key]; !ok {
		return quotas.ErrNotFound
	}
	s.overrides[accountKey{o.AccountID, o.Key}] = o
	return nil
}

func (s *Store) ClearOverride(_ context.Context, account ids.UUID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.overrides, accountKey{account, key})
	return nil
}
