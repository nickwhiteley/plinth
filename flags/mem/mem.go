// Package mem is the in-memory flags.Store.
package mem

import (
	"context"
	"sort"
	"sync"

	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
)

type override struct {
	account ids.UUID
	key     string
}

// Store is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	tiers     map[ids.UUID]flags.Tier
	states    map[string]flags.State
	overrides map[override]bool
}

func New() *Store {
	return &Store{tiers: map[ids.UUID]flags.Tier{}, states: map[string]flags.State{}, overrides: map[override]bool{}}
}

var _ flags.Store = (*Store)(nil)

func (s *Store) Tiers(context.Context) ([]flags.Tier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]flags.Tier, 0, len(s.tiers))
	for _, t := range s.tiers {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}

func (s *Store) CreateTier(_ context.Context, t flags.Tier) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.tiers {
		if o.Key == t.Key || o.Order == t.Order || o.ID == t.ID {
			return flags.ErrTierExists
		}
	}
	s.tiers[t.ID] = t
	return nil
}

func (s *Store) SetTierEnabled(_ context.Context, id ids.UUID, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tiers[id]
	if !ok {
		return flags.ErrNotFound
	}
	t.Enabled = enabled
	s.tiers[id] = t
	return nil
}

func (s *Store) Flags(context.Context) ([]flags.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]flags.State, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Store) InsertFlag(_ context.Context, st flags.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[st.Key]; !ok {
		s.states[st.Key] = st
	}
	return nil
}

func (s *Store) SetFlag(_ context.Context, key string, enabled bool, minTier *ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[key]
	if !ok {
		return flags.ErrNotFound
	}
	if minTier != nil {
		if _, ok := s.tiers[*minTier]; !ok {
			return flags.ErrNotFound
		}
		m := *minTier
		minTier = &m
	}
	st.Enabled, st.MinTierID = enabled, minTier
	s.states[key] = st
	return nil
}

func (s *Store) SetRetired(_ context.Context, key string, retired bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[key]
	if !ok {
		return flags.ErrNotFound
	}
	st.Retired = retired
	s.states[key] = st
	return nil
}

func (s *Store) AccountFlags(_ context.Context, account ids.UUID) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for o, v := range s.overrides {
		if o.account == account {
			out[o.key] = v
		}
	}
	return out, nil
}

func (s *Store) SetAccountFlag(_ context.Context, account ids.UUID, key string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[key]; !ok {
		return flags.ErrNotFound
	}
	s.overrides[override{account, key}] = enabled
	return nil
}

func (s *Store) ClearAccountFlag(_ context.Context, account ids.UUID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.overrides, override{account, key})
	return nil
}
