package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/nickwhiteley/plinth/ids"
)

// ResetTTL is how long a password reset link lasts. Short, unlike a session: a reset link is a
// standing permission to take over an identity, sitting in an inbox.
const ResetTTL = time.Hour

// VerificationTTL is how long a verification link lasts. Longer than ResetTTL, because it grants
// less (only "this address is mine") and is usually opened on another device, whenever somebody
// next reads their email.
const VerificationTTL = 48 * time.Hour

// Local is the local identity provider (issuer "local").
type Local struct {
	store    Store
	accounts Accounts
	now      func() time.Time
	// cost is the Argon2id work factor this provider hashes at. Verification doesn't read it: the
	// parameters are encoded in every stored hash.
	cost argon2Params
}

// Option configures a Local.
type Option func(*Local)

// WithReducedHashCost lowers the Argon2id work factor to something a test suite can afford.
//
// **Tests only.** A password hashed at this factor is not safe to store. An option on the
// provider rather than a global, so a process can't end up hashing cheaply because another
// package flipped a variable.
func WithReducedHashCost() Option { return func(l *Local) { l.cost = reducedParams() } }

// WithClock sets the provider's clock. For tests.
func WithClock(now func() time.Time) Option { return func(l *Local) { l.now = now } }

// NewLocal wires the local provider. accounts is required: a password change, a reset and Google
// combining all end the account's sessions through it.
func NewLocal(store Store, accounts Accounts, opts ...Option) *Local {
	if store == nil || accounts == nil {
		panic("identity: NewLocal needs a store and accounts")
	}
	l := &Local{store: store, accounts: accounts, now: time.Now, cost: defaultParams()}
	for _, o := range opts {
		o(l)
	}
	return l
}

func (l *Local) hash(password string) (string, error) { return hashPasswordWith(l.cost, password) }

func (l *Local) revoke(ctx context.Context, li LocalIdentity) error {
	return l.accounts.Revoke(ctx, IssuerLocal, ids.Identity.Format(li.ID))
}

func validEmail(email string) bool {
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email
}

// Signup creates an identity, unverified. It doesn't sign anyone in: the caller admits the
// identity to an account.
func (l *Local) Signup(ctx context.Context, email, password string) (LocalIdentity, error) {
	email = NormaliseEmail(email)
	if !validEmail(email) {
		return LocalIdentity{}, ErrInvalidEmail
	}
	hash, err := l.hash(password)
	if err != nil {
		return LocalIdentity{}, err
	}
	li := LocalIdentity{ID: ids.NewAt(l.now(), rand.Reader), Email: email, PasswordHash: hash, CreatedAt: l.now()}
	if err := l.store.CreateIdentity(ctx, li); err != nil {
		return LocalIdentity{}, err
	}
	return li, nil
}

// Login checks a password and returns the identity it proves.
//
// An unknown address, an identity with no password and a wrong password all return
// ErrInvalidCredentials after the same work, so neither the reply nor its timing says which
// addresses are registered. Whether the account may sign in is the account's business: Admit
// refuses an inactive one with the same code.
func (l *Local) Login(ctx context.Context, email, password string) (Identity, error) {
	li, err := l.store.IdentityByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		_, _ = l.hash(paddedPassword(password))
		return Identity{}, ErrInvalidCredentials
	} else if err != nil {
		return Identity{}, err
	}
	if li.PasswordHash == "" {
		_, _ = l.hash(paddedPassword(password))
		return Identity{}, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(password, li.PasswordHash)
	if err != nil {
		return Identity{}, fmt.Errorf("verifying the password of %s: %w", ids.Identity.Format(li.ID), err)
	}
	if !ok {
		return Identity{}, ErrInvalidCredentials
	}
	return li.Identity(), nil
}

// ChangePassword sets a new password for a signed-in identity.
//
// The current password is required even though the caller is signed in: a session cookie isn't
// proof of who is at the keyboard. **Every session of the account ends**, including the caller's:
// a deliberate change means somebody may have had the old password. Signing the caller straight
// back in is the product's job; it holds the new password.
func (l *Local) ChangePassword(ctx context.Context, id ids.UUID, current, next string) error {
	li, err := l.store.IdentityByID(ctx, id)
	if err != nil {
		return err
	}
	if err := l.confirm(li, current); err != nil {
		return err
	}
	encoded, err := l.hash(next) // enforces the length rule before anything is written
	if err != nil {
		return err
	}
	li.PasswordHash = encoded
	if err := l.store.UpdateIdentity(ctx, li); err != nil {
		return err
	}
	return l.revoke(ctx, li)
}

// confirm checks the current password of a signed-in identity.
func (l *Local) confirm(li LocalIdentity, password string) error {
	if li.PasswordHash == "" {
		// Nothing to confirm against. An identity created through Google sets its first password
		// through the reset flow, which proves the address instead.
		return ErrNoPassword
	}
	ok, err := VerifyPassword(password, li.PasswordHash)
	if err != nil {
		return fmt.Errorf("verifying the password of %s: %w", ids.Identity.Format(li.ID), err)
	}
	if !ok {
		return ErrInvalidCredentials
	}
	return nil
}

// ChangeEmail moves an identity to a new address and returns the old one, so the caller can tell
// it what happened.
//
// The password is required: the address is where recovery goes, so somebody with a borrowed
// session who could move it would own the account for good. The new address inherits no
// verification, and every link in flight for the old one dies with it: a verification link can no
// longer be derived (see verificationHash), and a reset link names the old address.
func (l *Local) ChangeEmail(ctx context.Context, id ids.UUID, password, newEmail string) (string, error) {
	newEmail = NormaliseEmail(newEmail)
	if !validEmail(newEmail) {
		return "", ErrInvalidEmail
	}
	li, err := l.store.IdentityByID(ctx, id)
	if err != nil {
		return "", err
	}
	if err := l.confirm(li, password); err != nil {
		return "", err
	}
	if err := l.store.ChangeEmail(ctx, li.ID, newEmail); err != nil {
		return "", err
	}
	// After the move, so a refused move leaves the verification alone.
	if li.EmailVerifiedAt != nil {
		li.EmailVerifiedAt = nil
		if err := l.store.UpdateIdentity(ctx, li); err != nil {
			return "", err
		}
	}
	return li.Email, nil
}

// --------------------------------------------------------------------------- reset

// BeginReset issues a reset token for an address.
//
// It returns an empty token, and no error, when no identity holds the address. The caller must
// behave identically either way: this endpoint is unauthenticated, and saying whether an address
// is registered would be an enumeration oracle.
func (l *Local) BeginReset(ctx context.Context, email string) (string, LocalIdentity, error) {
	li, err := l.store.IdentityByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		return "", LocalIdentity{}, nil
	} else if err != nil {
		return "", LocalIdentity{}, err
	}
	token := ids.Token()
	now := l.now()
	t := Token{Hash: ids.HashToken(token), IdentityID: li.ID, Purpose: PurposeReset, Email: li.Email, CreatedAt: now, ExpiresAt: now.Add(ResetTTL)}
	if err := l.store.CreateToken(ctx, t); err != nil {
		return "", LocalIdentity{}, err
	}
	return token, li, nil
}

// CompleteReset sets a new password and consumes the token. Every session of the account ends:
// a reset is what someone does when they think another person is in their account.
func (l *Local) CompleteReset(ctx context.Context, token, password string) (LocalIdentity, error) {
	hash := ids.HashToken(token)
	t, err := l.store.Token(ctx, hash, PurposeReset)
	if errors.Is(err, ErrNotFound) {
		return LocalIdentity{}, ErrInvalidCredentials
	} else if err != nil {
		return LocalIdentity{}, err
	}
	encoded, err := l.hash(password) // the length rule, before anything is written
	if err != nil {
		return LocalIdentity{}, err
	}
	li, err := l.store.IdentityByID(ctx, t.IdentityID)
	if errors.Is(err, ErrNotFound) {
		return LocalIdentity{}, ErrInvalidCredentials
	} else if err != nil {
		return LocalIdentity{}, err
	}
	// A reset link is tied to the address it was sent to, like a verification link: if the
	// identity has moved since, the link proves nothing about the address it now has.
	if NormaliseEmail(t.Email) != li.Email {
		return LocalIdentity{}, ErrInvalidCredentials
	}
	li.PasswordHash = encoded
	if err := l.store.UpdateIdentity(ctx, li); err != nil {
		return LocalIdentity{}, err
	}
	// Before the token is consumed, so a failure here leaves the link usable and the revocation
	// retryable.
	if err := l.revoke(ctx, li); err != nil {
		return LocalIdentity{}, err
	}
	// Consumed only after the password has changed. A failure to delete isn't reported: the
	// password did change, and the token expires within the hour.
	_ = l.store.DeleteToken(ctx, hash)
	return li, nil
}

// --------------------------------------------------------------------------- verification

// verificationPrefix domain-separates verification hashes from reset hashes, so neither kind of
// token can be found by the other's lookup even before the stored purpose is checked.
const verificationPrefix = "verify:"

// verificationHash binds a link to the address it was sent to. Move the address and every
// outstanding link becomes underivable, so it stops working without anything deleting it. That
// closes a real attack: move to somebody else's address, then spend an old link to mark it
// verified.
func verificationHash(email, secret string) [32]byte {
	return sha256.Sum256([]byte(verificationPrefix + NormaliseEmail(email) + ":" + secret))
}

// BeginVerification issues a verification link, or resends one. The token is the identity's
// idn_ id, a dot, and a secret: CompleteVerification needs the identity's current address before
// it can derive the hash. Neither half contains a dot.
//
// Resending doesn't revoke the earlier link: somebody who asked twice and then opened the first
// mail is the ordinary way this goes wrong.
func (l *Local) BeginVerification(ctx context.Context, id ids.UUID) (string, LocalIdentity, error) {
	li, err := l.store.IdentityByID(ctx, id)
	if err != nil {
		return "", LocalIdentity{}, err
	}
	if li.EmailVerified() {
		return "", li, ErrAlreadyVerified
	}
	secret := ids.Token()
	now := l.now()
	t := Token{Hash: verificationHash(li.Email, secret), IdentityID: li.ID, Purpose: PurposeVerify, Email: li.Email, CreatedAt: now, ExpiresAt: now.Add(VerificationTTL)}
	if err := l.store.CreateToken(ctx, t); err != nil {
		return "", LocalIdentity{}, err
	}
	return ids.Identity.Format(li.ID) + "." + secret, li, nil
}

// CompleteVerification marks the address verified and consumes the token. A spent token is
// ErrInvalidToken on a second click, rather than a pretend success that would hide a replay.
func (l *Local) CompleteVerification(ctx context.Context, token string) (LocalIdentity, error) {
	external, secret, ok := strings.Cut(token, ".")
	if !ok || secret == "" {
		return LocalIdentity{}, ErrInvalidToken
	}
	id, err := ids.Identity.Parse(external)
	if err != nil {
		return LocalIdentity{}, ErrInvalidToken
	}
	li, err := l.store.IdentityByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return LocalIdentity{}, ErrInvalidToken
	} else if err != nil {
		return LocalIdentity{}, err
	}
	hash := verificationHash(li.Email, secret)
	t, err := l.store.Token(ctx, hash, PurposeVerify)
	if errors.Is(err, ErrNotFound) {
		return LocalIdentity{}, ErrInvalidToken
	} else if err != nil {
		return LocalIdentity{}, err
	}
	// The address binding already makes a crossed record unreachable. One comparison means the
	// guarantee doesn't rest on the hash construction alone.
	if t.IdentityID != li.ID {
		return LocalIdentity{}, ErrInvalidToken
	}
	now := l.now()
	li.EmailVerifiedAt = &now
	if err := l.store.UpdateIdentity(ctx, li); err != nil {
		return LocalIdentity{}, err
	}
	_ = l.store.DeleteToken(ctx, hash) // verification happened; the token expires on its own
	return li, nil
}

// --------------------------------------------------------------------------- Google

// SignInWithGoogle resolves a Google identity to a local one, linking or creating as needed, and
// reports whether it created one (spec.md §12.1). In order, and the order is the design:
//
//  1. The Google account is already linked: that identity, whatever its address now is.
//  2. A local identity holds the address (gmail.com and googlemail.com as one mailbox): combine.
//  3. Nobody holds it: create a verified identity with no password.
//
// All three rest on Google having proved the address, so an unverified one is refused first.
//
// **Combining with an unverified identity removes its password and ends its account's sessions.**
// Without that, this is the pre-hijacking attack: register somebody's address, never confirm it,
// and keep a password into the account they later reach through Google.
func (l *Local) SignInWithGoogle(ctx context.Context, ext ExternalIdentity) (Identity, bool, error) {
	if ext.Subject == "" || ext.Email == "" {
		return Identity{}, false, ErrGoogleExchange
	}
	if !ext.EmailVerified {
		return Identity{}, false, ErrGoogleUnverifiedEmail
	}
	email := NormaliseEmail(ext.Email)
	if !validEmail(email) {
		return Identity{}, false, ErrInvalidEmail
	}
	withName := func(li LocalIdentity) Identity { id := li.Identity(); id.Name = ext.Name; return id }

	li, err := l.store.IdentityByGoogleSubject(ctx, ext.Subject)
	if err == nil {
		// A combine interrupted between linking and removing the planted password leaves a
		// linked, unverified identity holding the address Google has just proved. Finish it here,
		// or the planted password stays a way in (plinth#2 review). An identity that moved to a
		// new, unverified address keeps its password: Google hasn't proved that one.
		if !li.EmailVerified() && slices.Contains(EmailAliases(email), li.Email) {
			if li, err = l.strip(ctx, li); err != nil {
				return Identity{}, false, err
			}
		}
		return withName(li), false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Identity{}, false, err
	}

	for _, candidate := range EmailAliases(email) {
		existing, err := l.store.IdentityByEmail(ctx, candidate)
		if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return Identity{}, false, err
		}
		li, err := l.combine(ctx, existing, ext)
		if err != nil {
			return Identity{}, false, err
		}
		return withName(li), false, nil
	}

	now := l.now()
	li = LocalIdentity{ID: ids.NewAt(now, rand.Reader), Email: email, EmailVerifiedAt: &now, CreatedAt: now}
	if err := l.store.CreateIdentity(ctx, li); err != nil {
		return Identity{}, false, err
	}
	// A second write, because CreateIdentity doesn't write the subject. If it fails the identity
	// exists unlinked, and the next attempt finds it by address, verified, and links it.
	if err := l.store.LinkGoogle(ctx, li.ID, ext.Subject); err != nil {
		return Identity{}, false, err
	}
	li.GoogleSubject = ext.Subject
	return withName(li), true, nil
}

// combine links a Google account to the local identity holding its address.
func (l *Local) combine(ctx context.Context, li LocalIdentity, ext ExternalIdentity) (LocalIdentity, error) {
	// Refused before anything is written: a sign-in attempt against an inactive account must not
	// be able to remove its password on the way to being turned away.
	active, err := l.accounts.Active(ctx, IssuerLocal, ids.Identity.Format(li.ID))
	if err != nil {
		return LocalIdentity{}, err
	}
	if !active {
		return LocalIdentity{}, ErrInvalidCredentials
	}
	// The link first: it's the step that can be refused, so a refusal has changed nothing.
	if err := l.store.LinkGoogle(ctx, li.ID, ext.Subject); err != nil {
		return LocalIdentity{}, err
	}
	li.GoogleSubject = ext.Subject
	if li.EmailVerified() {
		return li, nil // the identity proved this address itself: its password and sessions stand
	}
	return l.strip(ctx, li)
}

// strip is the pre-hijacking defence: Google's proof of the address outranks an unproved password
// on it, so the password goes and the account's sessions end. Idempotent, so an interrupted
// combine can be finished by the next sign-in.
func (l *Local) strip(ctx context.Context, li LocalIdentity) (LocalIdentity, error) {
	active, err := l.accounts.Active(ctx, IssuerLocal, ids.Identity.Format(li.ID))
	if err != nil {
		return LocalIdentity{}, err
	}
	if !active {
		return LocalIdentity{}, ErrInvalidCredentials
	}
	now := l.now()
	li.EmailVerifiedAt = &now
	li.PasswordHash = ""
	if err := l.store.UpdateIdentity(ctx, li); err != nil {
		return LocalIdentity{}, err
	}
	// Before the caller admits the identity, so this doesn't end the session about to be issued.
	if err := l.revoke(ctx, li); err != nil {
		return LocalIdentity{}, err
	}
	return li, nil
}

// paddedPassword keeps the dummy hash on the unknown-address path within the length rule, so it
// does the same work as a real verification.
func paddedPassword(p string) string {
	for len([]rune(p)) < MinPasswordLength {
		p += "."
	}
	return p
}
