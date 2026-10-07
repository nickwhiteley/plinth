// Package mem is the in-memory account.Store, for tests and for running a product without a
// database. storetest.Run holds it to the same behaviour as the Postgres store.
package mem

import (
	"context"
	"sync"
	"time"

	"github.com/nickwhiteley/plinth/account"
	"github.com/nickwhiteley/plinth/ids"
)

type identityKey struct{ issuer, subject string }

// Store is safe for concurrent use.
type Store struct {
	mu         sync.Mutex
	accounts   map[ids.UUID]account.Account
	byIdentity map[identityKey]ids.UUID
	profiles   map[ids.UUID]account.Profile
	sessions   map[[32]byte]account.Session
}

func New() *Store {
	return &Store{accounts: map[ids.UUID]account.Account{}, byIdentity: map[identityKey]ids.UUID{},
		profiles: map[ids.UUID]account.Profile{}, sessions: map[[32]byte]account.Session{}}
}

var _ account.Store = (*Store)(nil)

func (s *Store) CreateAccount(_ context.Context, a account.Account, p account.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := identityKey{a.IdentityIssuer, a.IdentitySubject}
	if _, taken := s.byIdentity[k]; taken {
		return account.ErrIdentityTaken
	}
	p.AccountID = a.ID
	s.accounts[a.ID], s.byIdentity[k], s.profiles[a.ID] = a, a.ID, p
	return nil
}

func (s *Store) AccountByID(_ context.Context, id ids.UUID) (account.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return account.Account{}, account.ErrNotFound
	}
	return a, nil
}

func (s *Store) AccountByIdentity(ctx context.Context, issuer, subject string) (account.Account, error) {
	s.mu.Lock()
	id, ok := s.byIdentity[identityKey{issuer, subject}]
	s.mu.Unlock()
	if !ok {
		return account.Account{}, account.ErrNotFound
	}
	return s.AccountByID(ctx, id)
}

func (s *Store) SetActive(_ context.Context, id ids.UUID, active bool, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return account.ErrNotFound
	}
	a.Active, a.DeactivatedAt = active, nil
	if !active {
		a.DeactivatedAt = &at
	}
	s.accounts[id] = a
	return nil
}

func (s *Store) TouchSignIn(_ context.Context, id ids.UUID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return account.ErrNotFound
	}
	a.LastSignedInAt = &at
	s.accounts[id] = a
	return nil
}

func (s *Store) SetTier(_ context.Context, id ids.UUID, tier ids.UUID, override *ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return account.ErrNotFound
	}
	if override != nil {
		o := *override
		override = &o
	}
	a.TierID, a.TierOverrideID = tier, override
	s.accounts[id] = a
	return nil
}

func (s *Store) SetQuotaTimeZone(_ context.Context, id ids.UUID, zone string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return account.ErrNotFound
	}
	a.QuotaTimeZone = zone
	s.accounts[id] = a
	return nil
}

func (s *Store) Profile(_ context.Context, id ids.UUID) (account.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		return account.Profile{}, account.ErrNotFound
	}
	return p, nil
}

func (s *Store) RefreshProfileEmail(_ context.Context, id ids.UUID, email string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		return account.ErrNotFound
	}
	p.Email, p.RefreshedAt = email, at
	s.profiles[id] = p
	return nil
}

func (s *Store) CreateSession(_ context.Context, sess account.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[sess.AccountID]; !ok {
		return account.ErrNotFound
	}
	s.sessions[sess.TokenHash] = sess
	return nil
}

func (s *Store) Session(_ context.Context, hash [32]byte) (account.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[hash]
	if !ok || !sess.ExpiresAt.After(time.Now()) {
		return account.Session{}, account.ErrNotFound
	}
	return sess, nil
}

func (s *Store) TouchSession(_ context.Context, hash [32]byte, lastSeen, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[hash]
	if !ok {
		return account.ErrNotFound
	}
	sess.LastSeenAt, sess.ExpiresAt = lastSeen, expires
	s.sessions[hash] = sess
	return nil
}

func (s *Store) DeleteSession(_ context.Context, hash [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hash)
	return nil
}

func (s *Store) DeleteSessionsForAccount(_ context.Context, id ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, sess := range s.sessions {
		if sess.AccountID == id {
			delete(s.sessions, h)
		}
	}
	return nil
}
