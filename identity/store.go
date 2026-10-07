package identity

import (
	"context"
	"time"

	"github.com/nickwhiteley/plinth/ids"
)

// LocalIdentity is a row of local_identity: an email address and the credentials that prove it.
type LocalIdentity struct {
	ID              ids.UUID
	Email           string
	EmailVerifiedAt *time.Time
	// PasswordHash is an Argon2id PHC string, or empty for an identity created through Google.
	// Empty, rather than a random hash nobody knows: every check that asks "does this identity
	// have a password" would otherwise have to know which hashes were pretend.
	PasswordHash string
	// GoogleSubject is Google's stable `sub`, never the address. Written only by Store.LinkGoogle.
	GoogleSubject string
	CreatedAt     time.Time
}

// EmailVerified reports whether the current address has been proved.
func (l LocalIdentity) EmailVerified() bool { return l.EmailVerifiedAt != nil }

// Identity is what this record proves.
func (l LocalIdentity) Identity() Identity {
	return Identity{Issuer: IssuerLocal, Subject: ids.Identity.Format(l.ID), Email: l.Email, EmailVerified: l.EmailVerified()}
}

// Purpose is what a token may be spent on.
type Purpose string

const (
	PurposeReset  Purpose = "reset"
	PurposeVerify Purpose = "verify"
)

// Token is a row of identity_token. Only the token's hash is stored.
type Token struct {
	Hash       [32]byte
	IdentityID ids.UUID
	Purpose    Purpose
	// Email is the address the token was sent to.
	Email     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Store persists local identities and their tokens (spec.md §3). Package mem and package pg
// implement it, and storetest.Run holds them to one behaviour.
//
// Email arguments are normalised by the store, so lookups ignore case and surrounding space.
// Every method returns ErrNotFound for a record that doesn't exist.
type Store interface {
	// CreateIdentity returns ErrEmailTaken if the address is held. GoogleSubject is ignored:
	// only LinkGoogle writes it, so the subject index can't be bypassed.
	CreateIdentity(ctx context.Context, l LocalIdentity) error
	IdentityByID(ctx context.Context, id ids.UUID) (LocalIdentity, error)
	IdentityByEmail(ctx context.Context, email string) (LocalIdentity, error)
	// IdentityByGoogleSubject resolves a linked Google account. An empty subject is always
	// ErrNotFound: every unlinked identity has one.
	IdentityByGoogleSubject(ctx context.Context, subject string) (LocalIdentity, error)
	// UpdateIdentity writes the password hash and verification. It never moves the address or
	// the Google link, which have their own methods because each is a unique claim.
	UpdateIdentity(ctx context.Context, l LocalIdentity) error
	// ChangeEmail moves an identity to a new address, atomically with its uniqueness claim.
	// ErrEmailTaken if another identity holds it; moving to the current address changes nothing.
	ChangeEmail(ctx context.Context, id ids.UUID, email string) error
	// LinkGoogle attaches a Google subject, atomically with its uniqueness claim. ErrGoogleLinked
	// if another identity holds it; relinking the same subject changes nothing. An empty subject
	// is ErrNotFound.
	LinkGoogle(ctx context.Context, id ids.UUID, subject string) error

	CreateToken(ctx context.Context, t Token) error
	// Token returns a live token of the given purpose. An expired token, or one issued for another
	// purpose, is ErrNotFound: expiry doesn't depend on anything having collected it.
	Token(ctx context.Context, hash [32]byte, purpose Purpose) (Token, error)
	// DeleteToken consumes a token. Deleting one that isn't there is not an error.
	DeleteToken(ctx context.Context, hash [32]byte) error
}
