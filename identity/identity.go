// Package identity proves who someone is (spec.md §12). It knows nothing about accounts or
// sessions: a provider proves an Identity, and package account admits it.
//
// The local provider, Local, holds email-and-password identities, their reset and verification
// links, and the Google accounts linked to them. Lifted from Bloomprint's auth package, whose
// rules come across unchanged.
package identity

import (
	"context"
	"strings"

	"github.com/nickwhiteley/plinth/code"
)

// IssuerLocal is the local provider's issuer. Its subjects are idn_ ids.
const IssuerLocal = "local"

// Identity is what a provider proves about a person.
type Identity struct {
	Issuer  string
	Subject string
	Email   string
	// EmailVerified is the provider's claim that the address has been proved.
	EmailVerified bool
	// Name is a display name, and may be empty.
	Name string
}

// Accounts is what identity needs to know about the account an identity signs in to. Package
// account implements it; identity declares it so neither imports the other's internals.
//
// Both methods answer for an identity that has no account yet: Active reports true, because
// there is nothing to refuse, and Revoke does nothing.
type Accounts interface {
	// Active reports whether the identity's account may sign in.
	Active(ctx context.Context, issuer, subject string) (bool, error)
	// Revoke ends every session of the identity's account.
	Revoke(ctx context.Context, issuer, subject string) error
}

// The codes are the contract (spec.md §12). A product renders them in the reader's locale.
var (
	// ErrInvalidCredentials covers an unknown address, a wrong password, an account with no
	// password and an inactive account, so the reply can't be used to find out which addresses
	// are registered.
	ErrInvalidCredentials = code.New("identity.invalid_credentials")
	// ErrInvalidEmail is an address that doesn't parse.
	ErrInvalidEmail = code.New("identity.invalid_email")
	// ErrEmailTaken is signing up with, or moving to, an address another identity holds.
	ErrEmailTaken = code.New("identity.email_taken")
	// ErrGoogleLinked is a Google account already linked to another identity. Its own code rather
	// than ErrEmailTaken: the two claims are separate.
	ErrGoogleLinked = code.New("identity.google_linked")
	// ErrWeakPassword is a password shorter than MinPasswordLength. It carries "min".
	ErrWeakPassword = code.New("identity.weak_password")
	// ErrInvalidToken covers a link that is unknown, expired, already spent, issued for another
	// purpose, or issued to an address the identity has since left. One code for all of them:
	// telling them apart would say whether a token ever existed.
	ErrInvalidToken = code.New("identity.invalid_token")
	// ErrAlreadyVerified is asking to verify a verified address.
	ErrAlreadyVerified = code.New("identity.already_verified")
	// ErrNoPassword is an operation that confirms a password, on an identity that has none (one
	// created through Google). Distinct from ErrInvalidCredentials, safely: the caller is signed in
	// and asking about their own identity.
	ErrNoPassword = code.New("identity.no_password")
	// ErrGoogleExchange covers every way the round trip to Google can fail to produce a usable
	// identity. The detail goes to the log, wrapped around this code.
	ErrGoogleExchange = code.New("identity.google_exchange")
	// ErrGoogleUnverifiedEmail is an address Google doesn't vouch for.
	ErrGoogleUnverifiedEmail = code.New("identity.google_unverified_email")
	// ErrNotFound is a record that doesn't exist.
	ErrNotFound = code.New("identity.not_found")
)

// NormaliseEmail is the stored form of an address: trimmed and lower-cased.
func NormaliseEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// googleMailSynonyms are the two domains Google serves one mailbox on.
var googleMailSynonyms = map[string]string{"gmail.com": "googlemail.com", "googlemail.com": "gmail.com"}

// EmailAliases returns the addresses that reach the same mailbox as email, normalised, with the
// address as given first.
//
// One rule, and only Google's: gmail.com and googlemail.com. Dots and +tags are deliberately not
// folded: both would let one Google account match a local identity whose address was never typed
// the same way.
func EmailAliases(email string) []string {
	email = NormaliseEmail(email)
	local, domain, ok := strings.Cut(email, "@")
	if !ok {
		return []string{email}
	}
	other, ok := googleMailSynonyms[domain]
	if !ok {
		return []string{email}
	}
	return []string{email, local + "@" + other}
}
