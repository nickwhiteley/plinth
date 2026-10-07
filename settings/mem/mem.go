// Package mem is the in-memory settings.Store. It keeps the history a Postgres store reads from
// the shadow log, attributed the same way, from actor.From.
package mem

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/nickwhiteley/plinth/actor"
	"github.com/nickwhiteley/plinth/settings"
)

// Store is safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	rows    map[string]settings.Row
	history []settings.Version
}

func New() *Store { return &Store{rows: map[string]settings.Row{}} }

var _ settings.Store = (*Store)(nil)

func (s *Store) Settings(context.Context) ([]settings.Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]settings.Row, 0, len(s.rows))
	for _, r := range s.rows {
		r.Value = append([]byte(nil), r.Value...)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Store) record(ctx context.Context, key string, value []byte, at time.Time) {
	v := settings.Version{Key: key, Value: append([]byte(nil), value...), ModifiedAt: at}
	if a, ok := actor.From(ctx); ok {
		v.ModifiedBy = &a
	}
	s.history = append(s.history, v)
}

func (s *Store) InsertSetting(ctx context.Context, r settings.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[r.Key]; ok {
		return nil
	}
	r.UpdatedAt = time.Now()
	r.Value = append([]byte(nil), r.Value...)
	s.rows[r.Key] = r
	s.record(ctx, r.Key, r.Value, r.UpdatedAt)
	return nil
}

func (s *Store) SetSetting(ctx context.Context, key string, value []byte, keyVersion int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[key]
	if !ok {
		return settings.ErrNotFound.With("key", key)
	}
	r.Value, r.KeyVersion, r.UpdatedAt = append([]byte(nil), value...), keyVersion, time.Now()
	s.rows[key] = r
	s.record(ctx, key, r.Value, r.UpdatedAt)
	return nil
}

func (s *Store) SettingHistory(_ context.Context, limit int) ([]settings.Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []settings.Version
	for i := len(s.history) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.history[i])
	}
	return out, nil
}
