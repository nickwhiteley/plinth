// Package mem is the in-memory email.Store.
package mem

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"

	"github.com/nickwhiteley/plinth/email"
	"github.com/nickwhiteley/plinth/ids"
)

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	rows map[ids.UUID]email.Communication
}

func New() *Store { return &Store{rows: map[ids.UUID]email.Communication{}} }

var _ email.Store = (*Store)(nil)

func (s *Store) Record(_ context.Context, c email.Communication) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[c.ID] = c
	return nil
}

func (s *Store) Finish(_ context.Context, id ids.UUID, o email.Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.rows[id]
	if !ok {
		return email.ErrNotFound
	}
	c.Status, c.ProviderMessageID, c.Error, c.SentAt = o.Status, o.ProviderMessageID, o.Error, nil
	if o.Status == email.StatusSent {
		at := o.At
		c.SentAt = &at
	}
	s.rows[id] = c
	return nil
}

func (s *Store) Communications(_ context.Context, q email.Query) ([]email.Communication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []email.Communication
	for _, c := range s.rows {
		if (q.Category != "" && c.Category != q.Category) || (q.Kind != "" && c.Kind != q.Kind) || (q.Status != "" && c.Status != q.Status) {
			continue
		}
		if q.AccountID != nil && (c.AccountID == nil || *c.AccountID != *q.AccountID) {
			continue
		}
		if q.Before != nil && bytes.Compare(c.ID[:], q.Before[:]) >= 0 {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].ID[:], out[j].ID[:]) > 0 })
	limit := q.Limit
	if limit <= 0 || limit > email.PageSize {
		limit = email.PageSize
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) Prune(_ context.Context, cat email.Category, before time.Time) (int, error) {
	if time.Since(before) < email.MinRetention {
		return 0, email.ErrTooRecent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, c := range s.rows {
		if c.Category == cat && c.CreatedAt.Before(before) {
			delete(s.rows, id)
			n++
		}
	}
	return n, nil
}
