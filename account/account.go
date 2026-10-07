// Package account admits an identity to an account and keeps its sessions (spec.md §12).
//
// An account refers to its identity by (issuer, subject), so a second identity provider needs no
// change here. Service implements identity.Accounts, which is how a password change or a reset
// ends the account's sessions. Lifted from the session half of Bloomprint's auth package.
package account

import (
	"context"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/identity"
	"github.com/nickwhiteley/plinth/ids"
)

// SessionTTL is how long a session lasts. It slides: use extends it.
const SessionTTL = 30 * 24 * time.Hour

// touchThreshold avoids rewriting the expiry on every request: it's set back to a full TTL once a
// day or more has passed since it was last set, so a session in use keeps sliding.
const touchThreshold = SessionTTL - 24*time.Hour

// seenInterval is how often last_seen_at is rewritten at most.
const seenInterval = time.Minute

// Defaults for a new account's profile, from the product (usually the browser's settings).
const (
	DefaultTimeZone = "Europe/London"
	DefaultLocale   = "en-GB"
)

var (
	// ErrNotFound is a record that doesn't exist.
	ErrNotFound = code.New("account.not_found")
	// ErrUnauthenticated is a session token that is empty, unknown, expired, or whose account is
	// inactive. One code: a product answers all of them by asking the person to sign in.
	ErrUnauthenticated = code.New("account.unauthenticated")
	// ErrIdentityTaken is a second live account for one identity.
	ErrIdentityTaken = code.New("account.identity_taken")
	// ErrIdentityInvalid is an Identity with no issuer or subject: a programming error upstream.
	ErrIdentityInvalid = code.New("account.identity_invalid")
	// ErrTimeZoneInvalid and ErrLocaleInvalid are bad profile defaults.
	ErrTimeZoneInvalid = code.New("account.time_zone_invalid")
	ErrLocaleInvalid   = code.New("account.locale_invalid")
)

// Account is a row of account: who uses the products, as distinct from how they prove it.
type Account struct {
	ID              ids.UUID
	IdentityIssuer  string
	IdentitySubject string
	// Active is false for a deactivated account, which can't sign in and holds no sessions.
	Active         bool
	DeactivatedAt  *time.Time
	LastSignedInAt *time.Time
	CreatedAt      time.Time
}

// Profile is a row of account_profile: a cache of facts the identity owns, plus the person's
// time zone and locale.
type Profile struct {
	AccountID   ids.UUID
	DisplayName string
	TimeZone    string
	Locale      string
	Email       string
	RefreshedAt time.Time
}

// Session is a row of session. Only the token's hash is stored, so a database read yields
// nothing that signs anyone in.
type Session struct {
	TokenHash  [32]byte
	AccountID  ids.UUID
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// Store persists accounts, profiles and sessions (spec.md §3). Every method returns ErrNotFound
// for a record that doesn't exist.
type Store interface {
	// CreateAccount creates an account with its profile, atomically. ErrIdentityTaken if a live
	// account already has the identity.
	CreateAccount(ctx context.Context, a Account, p Profile) error
	AccountByID(ctx context.Context, id ids.UUID) (Account, error)
	AccountByIdentity(ctx context.Context, issuer, subject string) (Account, error)
	// SetActive activates or deactivates an account, stamping or clearing DeactivatedAt.
	SetActive(ctx context.Context, id ids.UUID, active bool, at time.Time) error
	// TouchSignIn records a sign-in.
	TouchSignIn(ctx context.Context, id ids.UUID, at time.Time) error

	Profile(ctx context.Context, accountID ids.UUID) (Profile, error)
	// RefreshProfileEmail updates the cached email from the identity.
	RefreshProfileEmail(ctx context.Context, accountID ids.UUID, email string, at time.Time) error

	CreateSession(ctx context.Context, s Session) error
	// Session returns a live session. An expired one is ErrNotFound.
	Session(ctx context.Context, hash [32]byte) (Session, error)
	TouchSession(ctx context.Context, hash [32]byte, lastSeen, expires time.Time) error
	// DeleteSession ends a session; ending one that isn't there is not an error.
	DeleteSession(ctx context.Context, hash [32]byte) error
	// DeleteSessionsForAccount ends every session of an account, and none of anyone else's.
	DeleteSessionsForAccount(ctx context.Context, accountID ids.UUID) error
}

// Service admits identities and resolves sessions.
type Service struct {
	store Store
	now   func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithClock sets the service's clock. For tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

func New(store Store, opts ...Option) *Service {
	s := &Service{store: store, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

var _ identity.Accounts = (*Service)(nil)

// NewProfile is what a new account's profile starts with. Empty fields take the defaults.
type NewProfile struct {
	TimeZone string
	Locale   string
}

// Admission is the result of Admit: the session token to hand to the browser, and the account.
type Admission struct {
	Token   string
	Account Account
	// Created reports that the account was made by this admission.
	Created bool
}

var localeRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2}|-[A-Z]{2,3})?$`)

// Admit signs an identity in: it finds the account by (issuer, subject), or creates it with its
// profile; refuses an inactive account with identity.ErrInvalidCredentials, the code a wrong
// password gets; refreshes the profile's cached email; stamps the sign-in; and issues a session.
//
// Creating here is also what heals a crash between a sign-up's identity and its account: the next
// sign-in creates the account.
func (s *Service) Admit(ctx context.Context, id identity.Identity, np NewProfile) (Admission, error) {
	if id.Issuer == "" || id.Subject == "" {
		return Admission{}, ErrIdentityInvalid
	}
	now := s.now()
	created := false
	a, err := s.store.AccountByIdentity(ctx, id.Issuer, id.Subject)
	if errors.Is(err, ErrNotFound) {
		a, err = s.create(ctx, id, np, now)
		created = err == nil
		if errors.Is(err, ErrIdentityTaken) { // a concurrent first sign-in made it
			a, err = s.store.AccountByIdentity(ctx, id.Issuer, id.Subject)
		}
	}
	if err != nil {
		return Admission{}, err
	}
	if !a.Active {
		// The wrong-password code: a distinct "deactivated" reply would be an enumeration oracle,
		// and a deactivated person learns why from a person, not a sign-in form.
		return Admission{}, identity.ErrInvalidCredentials
	}
	if !created && id.Email != "" {
		email := identity.NormaliseEmail(id.Email)
		if p, err := s.store.Profile(ctx, a.ID); err == nil && p.Email != email {
			_ = s.store.RefreshProfileEmail(ctx, a.ID, email, now) // a cache: best effort
		}
	}
	token := ids.Token()
	sess := Session{TokenHash: ids.HashToken(token), AccountID: a.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(SessionTTL)}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return Admission{}, err
	}
	// Best effort, after the session exists: failing a sign-in over a bookkeeping column would be
	// absurd. One write per sign-in, never per request.
	if err := s.store.TouchSignIn(ctx, a.ID, now); err == nil {
		a.LastSignedInAt = &now
	}
	return Admission{Token: token, Account: a, Created: created}, nil
}

func (s *Service) create(ctx context.Context, id identity.Identity, np NewProfile, now time.Time) (Account, error) {
	if np.TimeZone == "" {
		np.TimeZone = DefaultTimeZone
	}
	if np.Locale == "" {
		np.Locale = DefaultLocale
	}
	if _, err := time.LoadLocation(np.TimeZone); err != nil || np.TimeZone == "Local" {
		return Account{}, ErrTimeZoneInvalid.With("time_zone", np.TimeZone)
	}
	if !localeRe.MatchString(np.Locale) {
		return Account{}, ErrLocaleInvalid.With("locale", np.Locale)
	}
	a := Account{ID: ids.NewAt(now, rand.Reader), IdentityIssuer: id.Issuer, IdentitySubject: id.Subject, Active: true, CreatedAt: now}
	email := identity.NormaliseEmail(id.Email)
	p := Profile{AccountID: a.ID, DisplayName: displayName(id.Name, email), TimeZone: np.TimeZone, Locale: np.Locale, Email: email, RefreshedAt: now}
	if err := s.store.CreateAccount(ctx, a, p); err != nil {
		return Account{}, err
	}
	return a, nil
}

// displayName is the provider's name if it gave one, otherwise the address's local part, within
// account_profile's 1–120 characters.
func displayName(name, email string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	if name == "" {
		name = email
	}
	if r := []rune(name); len(r) > 120 {
		name = string(r[:120])
	}
	return name
}

// Authenticate resolves a session token to its account, sliding the expiry. It refuses an empty,
// unknown or expired token, and an inactive account, with ErrUnauthenticated.
func (s *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	if token == "" {
		return Account{}, ErrUnauthenticated
	}
	hash := ids.HashToken(token)
	sess, err := s.store.Session(ctx, hash)
	if errors.Is(err, ErrNotFound) {
		return Account{}, ErrUnauthenticated
	} else if err != nil {
		return Account{}, err
	}
	a, err := s.store.AccountByID(ctx, sess.AccountID)
	if errors.Is(err, ErrNotFound) {
		return Account{}, ErrUnauthenticated
	} else if err != nil {
		return Account{}, err
	}
	// Deactivation ends sessions, so this shouldn't fire, but the record is in hand, and it closes
	// the gap for a session issued moments before the deactivation landed.
	if !a.Active {
		return Account{}, ErrUnauthenticated
	}
	now := s.now()
	expires := sess.ExpiresAt
	if expires.Before(now.Add(touchThreshold)) {
		expires = now.Add(SessionTTL)
	}
	if !expires.Equal(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) >= seenInterval {
		_ = s.store.TouchSession(ctx, hash, now, expires) // best effort: never fail a request over it
	}
	return a, nil
}

// Logout ends a session. It succeeds whether or not the token exists.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, ids.HashToken(token))
}

// Deactivate stops an account signing in and ends its sessions.
func (s *Service) Deactivate(ctx context.Context, id ids.UUID) error {
	if err := s.store.SetActive(ctx, id, false, s.now()); err != nil {
		return err
	}
	return s.store.DeleteSessionsForAccount(ctx, id)
}

// Reactivate lets a deactivated account sign in again.
func (s *Service) Reactivate(ctx context.Context, id ids.UUID) error {
	return s.store.SetActive(ctx, id, true, s.now())
}

// Active implements identity.Accounts. An identity with no account yet is active: there's
// nothing to refuse.
func (s *Service) Active(ctx context.Context, issuer, subject string) (bool, error) {
	a, err := s.store.AccountByIdentity(ctx, issuer, subject)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return a.Active, nil
}

// Revoke implements identity.Accounts: it ends every session of the identity's account.
func (s *Service) Revoke(ctx context.Context, issuer, subject string) error {
	a, err := s.store.AccountByIdentity(ctx, issuer, subject)
	if errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	return s.store.DeleteSessionsForAccount(ctx, a.ID)
}
