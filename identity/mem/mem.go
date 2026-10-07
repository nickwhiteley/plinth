// Package mem is the in-memory identity.Store, for tests and for running a product without a
// database. storetest.Run holds it to the same behaviour as the Postgres store.
package mem

import (
	"context"
	"sync"
	"time"

	"github.com/nickwhiteley/plinth/identity"
	"github.com/nickwhiteley/plinth/ids"
)

// Store is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	byID      map[ids.UUID]identity.LocalIdentity
	byEmail   map[string]ids.UUID
	bySubject map[string]ids.UUID
	tokens    map[[32]byte]identity.Token
}

func New() *Store {
	return &Store{byID: map[ids.UUID]identity.LocalIdentity{}, byEmail: map[string]ids.UUID{},
		bySubject: map[string]ids.UUID{}, tokens: map[[32]byte]identity.Token{}}
}

var _ identity.Store = (*Store)(nil)

func (s *Store) CreateIdentity(_ context.Context, l identity.LocalIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.Email = identity.NormaliseEmail(l.Email)
	if _, taken := s.byEmail[l.Email]; taken {
		return identity.ErrEmailTaken
	}
	l.GoogleSubject = ""
	s.byID[l.ID] = l
	s.byEmail[l.Email] = l.ID
	return nil
}

func (s *Store) IdentityByID(_ context.Context, id ids.UUID) (identity.LocalIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byID[id]
	if !ok {
		return identity.LocalIdentity{}, identity.ErrNotFound
	}
	return l, nil
}

func (s *Store) IdentityByEmail(ctx context.Context, email string) (identity.LocalIdentity, error) {
	s.mu.Lock()
	id, ok := s.byEmail[identity.NormaliseEmail(email)]
	s.mu.Unlock()
	if !ok {
		return identity.LocalIdentity{}, identity.ErrNotFound
	}
	return s.IdentityByID(ctx, id)
}

func (s *Store) IdentityByGoogleSubject(ctx context.Context, subject string) (identity.LocalIdentity, error) {
	s.mu.Lock()
	id, ok := s.bySubject[subject]
	s.mu.Unlock()
	if subject == "" || !ok {
		return identity.LocalIdentity{}, identity.ErrNotFound
	}
	return s.IdentityByID(ctx, id)
}

func (s *Store) UpdateIdentity(_ context.Context, l identity.LocalIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.byID[l.ID]
	if !ok {
		return identity.ErrNotFound
	}
	old.PasswordHash, old.EmailVerifiedAt = l.PasswordHash, l.EmailVerifiedAt
	s.byID[l.ID] = old
	return nil
}

func (s *Store) ChangeEmail(_ context.Context, id ids.UUID, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byID[id]
	if !ok {
		return identity.ErrNotFound
	}
	email = identity.NormaliseEmail(email)
	if holder, taken := s.byEmail[email]; taken {
		if holder == id {
			return nil
		}
		return identity.ErrEmailTaken
	}
	delete(s.byEmail, l.Email)
	l.Email = email
	s.byID[id] = l
	s.byEmail[email] = id
	return nil
}

func (s *Store) LinkGoogle(_ context.Context, id ids.UUID, subject string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.byID[id]
	if !ok || subject == "" {
		return identity.ErrNotFound
	}
	if holder, taken := s.bySubject[subject]; taken {
		if holder == id {
			return nil
		}
		return identity.ErrGoogleLinked
	}
	if l.GoogleSubject != "" {
		delete(s.bySubject, l.GoogleSubject)
	}
	l.GoogleSubject = subject
	s.byID[id] = l
	s.bySubject[subject] = id
	return nil
}

func (s *Store) CreateToken(_ context.Context, t identity.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[t.IdentityID]; !ok {
		return identity.ErrNotFound
	}
	t.Email = identity.NormaliseEmail(t.Email)
	s.tokens[t.Hash] = t
	return nil
}

func (s *Store) Token(_ context.Context, hash [32]byte, purpose identity.Purpose) (identity.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[hash]
	if !ok || t.Purpose != purpose || !t.ExpiresAt.After(time.Now()) {
		return identity.Token{}, identity.ErrNotFound
	}
	return t, nil
}

func (s *Store) DeleteToken(_ context.Context, hash [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, hash)
	return nil
}
