// Package storetest is the conformance suite every identity.Store must pass (spec.md §3). Lifted
// from Bloomprint's store suite: the user, email-change, Google-link, reset and verification
// cases.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/identity"
	"github.com/nickwhiteley/plinth/ids"
)

// Run runs the suite. factory returns an empty store for each case.
func Run(t *testing.T, factory func(t *testing.T) identity.Store) {
	for name, f := range map[string]func(*testing.T, identity.Store){
		"identities":   testIdentities,
		"email change": testEmailChange,
		"google link":  testGoogleLink,
		"tokens":       testTokens,
		"verification": testVerification,
	} {
		t.Run(name, func(t *testing.T) { f(t, factory(t)) })
	}
}

// A well-formed hash: a store may check the shape it holds.
const (
	hash      = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
	rehashed  = "$argon2id$v=19$m=65536,t=3,p=4$b3RoZXJzYWx0$b3RoZXJoYXNob3RoZXJoYXNob3RoZXJoYXNob3RoZXI"
	otherHash = "$argon2id$v=19$m=65536,t=3,p=4$YW5vdGhlcg$YW5vdGhlcmFub3RoZXJhbm90aGVyYW5vdGhlcmFub3RoZXI"
)

func mustIdentity(t *testing.T, s identity.Store, email string) identity.LocalIdentity {
	t.Helper()
	l := identity.LocalIdentity{ID: ids.New(), Email: email, PasswordHash: hash, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	if err := s.CreateIdentity(context.Background(), l); err != nil {
		t.Fatalf("CreateIdentity(%s): %v", email, err)
	}
	return l
}

func testIdentities(t *testing.T, s identity.Store) {
	ctx := context.Background()
	l := mustIdentity(t, s, "Nick@Example.com")

	// Email lookup ignores case and surrounding space.
	for _, probe := range []string{"Nick@Example.com", "nick@example.com", "  NICK@EXAMPLE.COM  "} {
		got, err := s.IdentityByEmail(ctx, probe)
		if err != nil || got.ID != l.ID {
			t.Errorf("IdentityByEmail(%q) = %v, %v", probe, got.ID, err)
		}
	}
	got, err := s.IdentityByID(ctx, l.ID)
	if err != nil {
		t.Fatalf("IdentityByID: %v", err)
	}
	if got.Email != "nick@example.com" || got.PasswordHash != hash || !got.CreatedAt.Equal(l.CreatedAt) {
		t.Errorf("round trip: %+v", got)
	}

	// An update persists the hash, and never moves the address: the uniqueness claim is keyed on
	// it, so moving it here would leave the claim on an address the identity no longer has.
	got.PasswordHash = rehashed
	got.Email = "somewhere-else@example.com"
	if err := s.UpdateIdentity(ctx, got); err != nil {
		t.Fatalf("UpdateIdentity: %v", err)
	}
	after, _ := s.IdentityByID(ctx, l.ID)
	if after.PasswordHash != rehashed || after.Email != "nick@example.com" {
		t.Errorf("after update: %+v", after)
	}
	// An empty hash is how an identity with no password is stored, and it must round-trip as
	// empty, not as a NULL that reads back as something else.
	after.PasswordHash = ""
	if err := s.UpdateIdentity(ctx, after); err != nil {
		t.Fatalf("UpdateIdentity(no password): %v", err)
	}
	if none, _ := s.IdentityByID(ctx, l.ID); none.PasswordHash != "" {
		t.Errorf("no password read back as %q", none.PasswordHash)
	}

	if err := s.UpdateIdentity(ctx, identity.LocalIdentity{ID: ids.New()}); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("UpdateIdentity(unknown) = %v", err)
	}
	if _, err := s.IdentityByID(ctx, ids.New()); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("IdentityByID(unknown) = %v", err)
	}
	if _, err := s.IdentityByEmail(ctx, "nobody@example.com"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("IdentityByEmail(unknown) = %v", err)
	}

	// A second identity on the address, in any casing, is refused.
	dup := identity.LocalIdentity{ID: ids.New(), Email: "NICK@example.com", PasswordHash: otherHash, CreatedAt: time.Now()}
	if err := s.CreateIdentity(ctx, dup); !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("duplicate CreateIdentity = %v, want ErrEmailTaken", err)
	}
}

func testEmailChange(t *testing.T, s identity.Store) {
	ctx := context.Background()
	one := mustIdentity(t, s, "one@example.com")
	two := mustIdentity(t, s, "two@example.com")

	if err := s.ChangeEmail(ctx, one.ID, "Moved@Example.com"); err != nil {
		t.Fatalf("ChangeEmail: %v", err)
	}
	// The new address resolves, and the old one resolves to nothing: an identity reachable by two
	// addresses is as wrong as one reachable by none.
	if moved, err := s.IdentityByEmail(ctx, "moved@example.com"); err != nil || moved.ID != one.ID || moved.Email != "moved@example.com" {
		t.Errorf("the new address: %+v, %v", moved, err)
	}
	if _, err := s.IdentityByEmail(ctx, "one@example.com"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("the old address still resolves: %v", err)
	}
	// The freed address can be claimed by somebody else.
	if err := s.ChangeEmail(ctx, two.ID, "one@example.com"); err != nil {
		t.Errorf("claiming a released address: %v", err)
	}
	// Somebody else's address is refused, and nothing moves.
	if err := s.ChangeEmail(ctx, one.ID, "one@example.com"); !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("taking a held address = %v", err)
	}
	if after, _ := s.IdentityByID(ctx, one.ID); after.Email != "moved@example.com" {
		t.Errorf("a refused change moved the identity to %q", after.Email)
	}
	// Your own address, in any casing, is a no-op, not a collision with yourself.
	if err := s.ChangeEmail(ctx, one.ID, "MOVED@example.com"); err != nil {
		t.Errorf("moving to your own address = %v", err)
	}
	if err := s.ChangeEmail(ctx, ids.New(), "who@example.com"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("ChangeEmail(unknown) = %v", err)
	}
}

func testGoogleLink(t *testing.T, s identity.Store) {
	ctx := context.Background()
	one := mustIdentity(t, s, "one@example.com")
	two := mustIdentity(t, s, "two@example.com")
	const sub = "108000000000000000001"

	// The empty subject finds nobody: every unlinked identity has one.
	if _, err := s.IdentityByGoogleSubject(ctx, ""); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("IdentityByGoogleSubject(\"\") = %v", err)
	}
	if _, err := s.IdentityByGoogleSubject(ctx, sub); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("an unlinked subject resolves: %v", err)
	}
	if err := s.LinkGoogle(ctx, one.ID, sub); err != nil {
		t.Fatalf("LinkGoogle: %v", err)
	}
	if linked, err := s.IdentityByGoogleSubject(ctx, sub); err != nil || linked.ID != one.ID || linked.GoogleSubject != sub {
		t.Errorf("by subject: %+v, %v", linked, err)
	}
	// Subjects are opaque, compared byte for byte.
	if _, err := s.IdentityByGoogleSubject(ctx, sub+" "); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("a subject with a trailing space resolved: %v", err)
	}
	// Relinking the same subject is a no-op, so the sign-in path can retry.
	if err := s.LinkGoogle(ctx, one.ID, sub); err != nil {
		t.Errorf("relinking = %v", err)
	}
	// Somebody else's subject is refused, and nothing moves.
	if err := s.LinkGoogle(ctx, two.ID, sub); !errors.Is(err, identity.ErrGoogleLinked) {
		t.Errorf("linking a claimed subject = %v", err)
	}
	if other, _ := s.IdentityByID(ctx, two.ID); other.GoogleSubject != "" {
		t.Errorf("a refused link attached %q", other.GoogleSubject)
	}
	// UpdateIdentity doesn't carry the link, in either direction.
	u, _ := s.IdentityByID(ctx, two.ID)
	u.GoogleSubject = sub
	if err := s.UpdateIdentity(ctx, u); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.IdentityByID(ctx, two.ID); after.GoogleSubject != "" {
		t.Errorf("UpdateIdentity attached a Google account")
	}
	u, _ = s.IdentityByID(ctx, one.ID)
	u.GoogleSubject = ""
	if err := s.UpdateIdentity(ctx, u); err != nil {
		t.Fatal(err)
	}
	if after, _ := s.IdentityByID(ctx, one.ID); after.GoogleSubject != sub {
		t.Errorf("UpdateIdentity dropped the Google link")
	}
	// Nor does CreateIdentity: only LinkGoogle maintains the subject's claim.
	three := identity.LocalIdentity{ID: ids.New(), Email: "three@example.com", CreatedAt: time.Now(), GoogleSubject: "108000000000000000002"}
	if err := s.CreateIdentity(ctx, three); err != nil {
		t.Fatal(err)
	}
	if created, _ := s.IdentityByID(ctx, three.ID); created.GoogleSubject != "" {
		t.Errorf("CreateIdentity stored a subject: %q", created.GoogleSubject)
	}
	// The address moving doesn't take the link with it.
	if err := s.ChangeEmail(ctx, one.ID, "elsewhere@example.com"); err != nil {
		t.Fatal(err)
	}
	if moved, err := s.IdentityByGoogleSubject(ctx, sub); err != nil || moved.ID != one.ID {
		t.Errorf("the link didn't survive an address change: %v", err)
	}
	if err := s.LinkGoogle(ctx, ids.New(), "108000000000000000009"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("LinkGoogle(unknown) = %v", err)
	}
	if err := s.LinkGoogle(ctx, two.ID, ""); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("LinkGoogle(no subject) = %v", err)
	}
}

func testTokens(t *testing.T, s identity.Store) {
	ctx := context.Background()
	l := mustIdentity(t, s, "one@example.com")
	now := time.Now().UTC().Truncate(time.Microsecond)
	live := identity.Token{Hash: ids.HashToken("live"), IdentityID: l.ID, Purpose: identity.PurposeReset, Email: "one@example.com", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateToken(ctx, live); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	got, err := s.Token(ctx, live.Hash, identity.PurposeReset)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got.IdentityID != l.ID || got.Email != "one@example.com" || !got.ExpiresAt.Equal(live.ExpiresAt) {
		t.Errorf("round trip: %+v", got)
	}
	// A token is found only under the purpose it was issued for.
	if _, err := s.Token(ctx, live.Hash, identity.PurposeVerify); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("a reset token was found as a verification: %v", err)
	}
	// An expired token reads as absent, so expiry doesn't depend on anything collecting it.
	dead := identity.Token{Hash: ids.HashToken("dead"), IdentityID: l.ID, Purpose: identity.PurposeReset, Email: "one@example.com", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute)}
	if err := s.CreateToken(ctx, dead); err != nil {
		t.Fatalf("CreateToken(expired): %v", err)
	}
	if _, err := s.Token(ctx, dead.Hash, identity.PurposeReset); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("an expired token = %v", err)
	}
	// Single use, and deleting twice isn't an error.
	if err := s.DeleteToken(ctx, live.Hash); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if _, err := s.Token(ctx, live.Hash, identity.PurposeReset); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("a deleted token = %v", err)
	}
	if err := s.DeleteToken(ctx, live.Hash); err != nil {
		t.Errorf("a second DeleteToken = %v", err)
	}
	if _, err := s.Token(ctx, ids.HashToken("never"), identity.PurposeReset); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("an unknown token = %v", err)
	}
	// A token names an identity that exists.
	orphan := identity.Token{Hash: ids.HashToken("orphan"), IdentityID: ids.New(), Purpose: identity.PurposeReset, Email: "x@example.com", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateToken(ctx, orphan); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("CreateToken for an unknown identity = %v", err)
	}
}

func testVerification(t *testing.T, s identity.Store) {
	ctx := context.Background()
	l := mustIdentity(t, s, "new@example.com")
	got, _ := s.IdentityByID(ctx, l.ID)
	if got.EmailVerified() {
		t.Error("a new identity is already verified")
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	got.EmailVerifiedAt = &at
	if err := s.UpdateIdentity(ctx, got); err != nil {
		t.Fatal(err)
	}
	// It survives the round trip by both lookups: sign-in reads by address, everything after by id.
	byID, _ := s.IdentityByID(ctx, l.ID)
	byEmail, _ := s.IdentityByEmail(ctx, "new@example.com")
	if !byID.EmailVerified() || !byID.EmailVerifiedAt.Equal(at) || !byEmail.EmailVerified() {
		t.Fatalf("verification didn't persist: %v / %v", byID.EmailVerifiedAt, byEmail.EmailVerifiedAt)
	}
	// And it can be taken away, which an address change needs.
	byID.EmailVerifiedAt = nil
	if err := s.UpdateIdentity(ctx, byID); err != nil {
		t.Fatal(err)
	}
	if cleared, _ := s.IdentityByID(ctx, l.ID); cleared.EmailVerified() {
		t.Error("clearing the verification didn't take")
	}
}
