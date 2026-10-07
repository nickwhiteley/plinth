package identity_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/identity"
	"github.com/nickwhiteley/plinth/identity/mem"
	"github.com/nickwhiteley/plinth/ids"
)

// Lifted from Bloomprint's auth tests: the rules that are identity's alone. The ones that need a
// real session (a reset ends them, an inactive account can't sign in) are in package account's
// tests, against this provider.

const pw = "a sufficiently long password"

// accounts is a fake identity.Accounts: it records revocations and can mark a subject inactive.
type accounts struct {
	revoked  []string
	inactive map[string]bool
}

func (a *accounts) Active(_ context.Context, _, subject string) (bool, error) {
	return !a.inactive[subject], nil
}

func (a *accounts) Revoke(_ context.Context, _, subject string) error {
	a.revoked = append(a.revoked, subject)
	return nil
}

func newLocal(t *testing.T, opts ...identity.Option) (*identity.Local, *mem.Store, *accounts) {
	t.Helper()
	m, a := mem.New(), &accounts{inactive: map[string]bool{}}
	return identity.NewLocal(m, a, append([]identity.Option{identity.WithReducedHashCost()}, opts...)...), m, a
}

func signup(t *testing.T, l *identity.Local, email string) identity.LocalIdentity {
	t.Helper()
	li, err := l.Signup(context.Background(), email, pw)
	if err != nil {
		t.Fatalf("Signup(%s): %v", email, err)
	}
	return li
}

func subject(li identity.LocalIdentity) string { return ids.Identity.Format(li.ID) }

func TestSignup(t *testing.T) {
	ctx := context.Background()
	l, m, _ := newLocal(t)
	li := signup(t, l, "  Nick@Example.com ")
	if li.Email != "nick@example.com" || li.EmailVerified() || !li.ID.IsV7() {
		t.Errorf("signed up %+v", li)
	}
	if li.PasswordHash == "" || strings.Contains(li.PasswordHash, "password") {
		t.Error("the password was not hashed")
	}
	if stored, err := m.IdentityByID(ctx, li.ID); err != nil || stored.Email != li.Email {
		t.Errorf("not persisted: %v", err)
	}
	if got := li.Identity(); got.Issuer != "local" || got.Subject != subject(li) || got.Email != li.Email {
		t.Errorf("Identity() = %+v", got)
	}
}

func TestSignupRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLocal(t)
	for _, email := range []string{"not-an-email", "Nick <nick@example.com>", ""} {
		if _, err := l.Signup(ctx, email, pw); !errors.Is(err, identity.ErrInvalidEmail) {
			t.Errorf("Signup(%q) = %v, want ErrInvalidEmail", email, err)
		}
	}
	if _, err := l.Signup(ctx, "nick@example.com", "short"); !errors.Is(err, identity.ErrWeakPassword) {
		t.Errorf("a short password = %v, want ErrWeakPassword", err)
	}
	signup(t, l, "nick@example.com")
	if _, err := l.Signup(ctx, "NICK@Example.com", "another long enough password"); !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("a duplicate in another casing = %v, want ErrEmailTaken", err)
	}
}

func TestLogin(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLocal(t)
	li := signup(t, l, "nick@example.com")
	got, err := l.Login(ctx, "Nick@Example.com", pw)
	if err != nil || got.Subject != subject(li) || got.Issuer != "local" {
		t.Fatalf("Login = %+v, %v", got, err)
	}
	// Both failures are the same code, so neither says which addresses exist.
	_, wrong := l.Login(ctx, "nick@example.com", "the wrong password entirely")
	_, unknown := l.Login(ctx, "nobody@example.com", pw)
	if !errors.Is(wrong, identity.ErrInvalidCredentials) || !errors.Is(unknown, identity.ErrInvalidCredentials) || wrong.Error() != unknown.Error() {
		t.Errorf("wrong password %v, unknown address %v: want the same ErrInvalidCredentials", wrong, unknown)
	}
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	l, _, a := newLocal(t)
	li := signup(t, l, "nick@example.com")
	const next = "a different long password"

	// A session isn't proof of who is at the keyboard, so the current password is required, and
	// a wrong one changes nothing and ends nothing.
	if err := l.ChangePassword(ctx, li.ID, "not it", next); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("a wrong current password = %v", err)
	}
	if err := l.ChangePassword(ctx, li.ID, pw, "short"); !errors.Is(err, identity.ErrWeakPassword) {
		t.Errorf("a short new password = %v", err)
	}
	if _, err := l.Login(ctx, "nick@example.com", pw); err != nil || len(a.revoked) != 0 {
		t.Fatalf("a refused change disturbed the identity: %v, revoked %v", err, a.revoked)
	}
	if err := l.ChangePassword(ctx, li.ID, pw, next); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	// Every session ends: somebody may have had the old password.
	if len(a.revoked) != 1 || a.revoked[0] != subject(li) {
		t.Errorf("revoked %v, want the account's sessions ended", a.revoked)
	}
	if _, err := l.Login(ctx, "nick@example.com", pw); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Error("the old password still works")
	}
	if _, err := l.Login(ctx, "nick@example.com", next); err != nil {
		t.Errorf("the new password doesn't: %v", err)
	}
}

func TestResetSetsThePasswordAndEndsSessions(t *testing.T) {
	ctx := context.Background()
	l, _, a := newLocal(t)
	li := signup(t, l, "nick@example.com")
	signup(t, l, "someone@example.com")

	token, got, err := l.BeginReset(ctx, "Nick@Example.com")
	if err != nil || token == "" || got.ID != li.ID {
		t.Fatalf("BeginReset = %q, %v", token, err)
	}
	if _, err := l.CompleteReset(ctx, token, "short"); !errors.Is(err, identity.ErrWeakPassword) {
		t.Errorf("a short password = %v", err)
	}
	if _, err := l.CompleteReset(ctx, token, "a different long password"); err != nil {
		t.Fatalf("CompleteReset: %v", err)
	}
	if len(a.revoked) != 1 || a.revoked[0] != subject(li) {
		t.Errorf("revoked %v, want exactly this account's sessions", a.revoked)
	}
	if _, err := l.Login(ctx, "nick@example.com", "a different long password"); err != nil {
		t.Errorf("the new password: %v", err)
	}
	// Single use.
	if _, err := l.CompleteReset(ctx, token, "yet another long password"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("a spent token = %v", err)
	}
}

func TestBeginResetSaysNothingAboutUnknownAddresses(t *testing.T) {
	l, _, _ := newLocal(t)
	token, li, err := l.BeginReset(context.Background(), "nobody@example.com")
	if token != "" || !li.ID.IsZero() || err != nil {
		t.Errorf("BeginReset(unknown) = %q, %v, %v: want nothing, and no error", token, li.ID, err)
	}
}

// ------------------------------------------------------------------------- verification

func verifiable(t *testing.T, l *identity.Local, email string) (identity.LocalIdentity, string) {
	t.Helper()
	li := signup(t, l, email)
	token, _, err := l.BeginVerification(context.Background(), li.ID)
	if err != nil {
		t.Fatalf("BeginVerification: %v", err)
	}
	return li, token
}

func TestVerificationRoundTrip(t *testing.T) {
	ctx := context.Background()
	l, m, _ := newLocal(t)
	li, token := verifiable(t, l, "nick@example.com")
	if !strings.HasPrefix(token, subject(li)+".") {
		t.Errorf("the token %q doesn't lead with the identity's id", token)
	}
	got, err := l.CompleteVerification(ctx, token)
	if err != nil || !got.EmailVerified() {
		t.Fatalf("CompleteVerification = %v", err)
	}
	if stored, _ := m.IdentityByID(ctx, li.ID); !stored.EmailVerified() {
		t.Error("the verification didn't reach the store")
	}
	if _, err := l.CompleteVerification(ctx, token); !errors.Is(err, identity.ErrInvalidToken) {
		t.Errorf("a second use = %v", err)
	}
	if _, _, err := l.BeginVerification(ctx, li.ID); !errors.Is(err, identity.ErrAlreadyVerified) {
		t.Errorf("verifying a verified address = %v", err)
	}
}

// The reason both kinds share one table must never become a takeover path.
func TestAVerificationTokenCannotBeSpentAsAReset(t *testing.T) {
	ctx := context.Background()
	l, m, _ := newLocal(t)
	li, token := verifiable(t, l, "nick@example.com")
	before, _ := m.IdentityByID(ctx, li.ID)
	_, secret, _ := strings.Cut(token, ".")
	for name, attempt := range map[string]string{"whole token": token, "secret only": secret} {
		if _, err := l.CompleteReset(ctx, attempt, "an attacker's new password"); !errors.Is(err, identity.ErrInvalidCredentials) {
			t.Errorf("CompleteReset with a verification %s = %v", name, err)
		}
	}
	if after, _ := m.IdentityByID(ctx, li.ID); after.PasswordHash != before.PasswordHash {
		t.Error("a verification token changed the password")
	}
}

func TestAResetTokenCannotBeSpentAsAVerification(t *testing.T) {
	ctx := context.Background()
	l, m, _ := newLocal(t)
	signup(t, l, "nick@example.com")
	reset, li, err := l.BeginReset(ctx, "nick@example.com")
	if err != nil {
		t.Fatal(err)
	}
	for name, attempt := range map[string]string{"bare": reset, "with an identity id": subject(li) + "." + reset} {
		if _, err := l.CompleteVerification(ctx, attempt); !errors.Is(err, identity.ErrInvalidToken) {
			t.Errorf("CompleteVerification with a %s reset token = %v", name, err)
		}
	}
	if stored, _ := m.IdentityByID(ctx, li.ID); stored.EmailVerified() {
		t.Error("a reset token verified the address")
	}
}

// Take a link sent to an address you control, move onto somebody else's address, then spend the
// link to mark theirs verified. If this passes, verification proves nothing.
func TestMovingTheAddressKillsAnOutstandingLink(t *testing.T) {
	ctx := context.Background()
	l, m, _ := newLocal(t)
	li, token := verifiable(t, l, "attacker@example.com")
	if _, err := l.ChangeEmail(ctx, li.ID, pw, "victim@example.com"); err != nil {
		t.Fatalf("ChangeEmail: %v", err)
	}
	if _, err := l.CompleteVerification(ctx, token); !errors.Is(err, identity.ErrInvalidToken) {
		t.Errorf("a link sent to the old address still verifies: %v", err)
	}
	if stored, _ := m.IdentityByID(ctx, li.ID); stored.EmailVerified() {
		t.Error("victim@example.com was verified by a link sent to attacker@example.com")
	}
	fresh, _, err := l.BeginVerification(ctx, li.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.CompleteVerification(ctx, fresh); err != nil {
		t.Errorf("a link issued after the move: %v", err)
	}
}

func TestChangeEmail(t *testing.T) {
	ctx := context.Background()
	l, m, _ := newLocal(t)
	li, token := verifiable(t, l, "nick@example.com")
	if _, err := l.CompleteVerification(ctx, token); err != nil {
		t.Fatal(err)
	}
	signup(t, l, "taken@example.com")
	if _, err := l.ChangeEmail(ctx, li.ID, "not it", "elsewhere@example.com"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("a wrong password = %v", err)
	}
	if _, err := l.ChangeEmail(ctx, li.ID, pw, "not an address"); !errors.Is(err, identity.ErrInvalidEmail) {
		t.Errorf("a bad address = %v", err)
	}
	if _, err := l.ChangeEmail(ctx, li.ID, pw, "Taken@example.com"); !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("a held address = %v", err)
	}
	if stored, _ := m.IdentityByID(ctx, li.ID); !stored.EmailVerified() || stored.Email != "nick@example.com" {
		t.Errorf("a refused move changed the identity: %+v", stored)
	}
	old, err := l.ChangeEmail(ctx, li.ID, pw, "Elsewhere@example.com")
	if err != nil || old != "nick@example.com" {
		t.Fatalf("ChangeEmail = %q, %v", old, err)
	}
	// The new address inherits nothing.
	if stored, _ := m.IdentityByID(ctx, li.ID); stored.EmailVerified() || stored.Email != "elsewhere@example.com" {
		t.Errorf("after the move: %+v", stored)
	}
}

func TestAVerificationLinkExpires(t *testing.T) {
	past := time.Now().Add(-identity.VerificationTTL - time.Hour)
	l, _, _ := newLocal(t, identity.WithClock(func() time.Time { return past }))
	_, token := verifiable(t, l, "nick@example.com")
	if _, err := l.CompleteVerification(context.Background(), token); !errors.Is(err, identity.ErrInvalidToken) {
		t.Errorf("an expired link = %v", err)
	}
}

func TestResendingLeavesTheEarlierLinkWorking(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLocal(t)
	li, first := verifiable(t, l, "nick@example.com")
	second, _, err := l.BeginVerification(ctx, li.ID)
	if err != nil || second == first {
		t.Fatalf("a resend: %v", err)
	}
	if _, err := l.CompleteVerification(ctx, first); err != nil {
		t.Errorf("the first link stopped working after a resend: %v", err)
	}
}

func TestMalformedVerificationTokensAreRefused(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLocal(t)
	li, _ := verifiable(t, l, "nick@example.com")
	for _, token := range []string{"", ".", "no-dot-at-all", "." + ids.Token(), subject(li) + ".",
		ids.Identity.Format(ids.New()) + "." + ids.Token(), ids.Account.Format(li.ID) + "." + ids.Token()} {
		if _, err := l.CompleteVerification(ctx, token); !errors.Is(err, identity.ErrInvalidToken) {
			t.Errorf("CompleteVerification(%q) = %v", token, err)
		}
	}
}

// ------------------------------------------------------------------------- Google

func google() identity.ExternalIdentity {
	return identity.ExternalIdentity{Subject: "108000000000000000001", Email: "sam@gmail.com", EmailVerified: true, Name: "Sam Maker"}
}

func TestGoogleCreatesAVerifiedIdentityWithNoPassword(t *testing.T) {
	ctx := context.Background()
	l, m, _ := newLocal(t)
	id, created, err := l.SignInWithGoogle(ctx, google())
	if err != nil || !created {
		t.Fatalf("SignInWithGoogle = %v, created %v", err, created)
	}
	if id.Issuer != "local" || id.Email != "sam@gmail.com" || !id.EmailVerified || id.Name != "Sam Maker" {
		t.Errorf("identity %+v", id)
	}
	li, _ := m.IdentityByEmail(ctx, "sam@gmail.com")
	if !li.EmailVerified() || li.PasswordHash != "" || li.GoogleSubject != google().Subject {
		t.Errorf("stored %+v", li)
	}
	// The second sign-in finds it by subject, and makes nothing.
	again, created, err := l.SignInWithGoogle(ctx, google())
	if err != nil || created || again.Subject != id.Subject {
		t.Errorf("a returning sign-in: %+v created %v, %v", again, created, err)
	}
}

// The link is keyed on the subject, so an address moving at Google doesn't strand the identity,
// and the local address isn't dragged along: Google isn't the authority on it.
func TestGoogleFollowsTheSubjectNotTheAddress(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLocal(t)
	first, _, _ := l.SignInWithGoogle(ctx, google())
	moved := google()
	moved.Email = "sam.maker@example.com"
	again, created, err := l.SignInWithGoogle(ctx, moved)
	if err != nil || created || again.Subject != first.Subject || again.Email != "sam@gmail.com" {
		t.Errorf("after the address moved: %+v created %v, %v", again, created, err)
	}
}

func TestGoogleCombinesWithAVerifiedIdentity(t *testing.T) {
	ctx := context.Background()
	l, m, a := newLocal(t)
	li, token := verifiable(t, l, "sam@gmail.com")
	if _, err := l.CompleteVerification(ctx, token); err != nil {
		t.Fatal(err)
	}
	id, created, err := l.SignInWithGoogle(ctx, google())
	if err != nil || created || id.Subject != subject(li) {
		t.Fatalf("combine: %+v created %v, %v", id, created, err)
	}
	stored, _ := m.IdentityByID(ctx, li.ID)
	if stored.GoogleSubject != google().Subject || stored.PasswordHash == "" || len(a.revoked) != 0 {
		t.Errorf("a verified identity must keep its password and sessions: %+v, revoked %v", stored, a.revoked)
	}
}

// The pre-hijacking case: the unproved password goes, and so do the sessions it opened.
func TestGoogleRevokesAnUnverifiedIdentitysPassword(t *testing.T) {
	ctx := context.Background()
	l, m, a := newLocal(t)
	planted := signup(t, l, "sam@gmail.com")
	id, created, err := l.SignInWithGoogle(ctx, google())
	if err != nil || created || id.Subject != subject(planted) {
		t.Fatalf("combine: %+v created %v, %v", id, created, err)
	}
	stored, _ := m.IdentityByID(ctx, planted.ID)
	if stored.PasswordHash != "" || !stored.EmailVerified() {
		t.Errorf("stored %+v: want no password, verified", stored)
	}
	if len(a.revoked) != 1 || a.revoked[0] != subject(planted) {
		t.Errorf("revoked %v, want the planted sessions ended", a.revoked)
	}
	if _, err := l.Login(ctx, "sam@gmail.com", pw); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("the revoked password still signs in: %v", err)
	}
}

func TestGoogleMatchesGooglemail(t *testing.T) {
	for _, c := range []struct{ registered, atGoogle string }{{"sam@googlemail.com", "sam@gmail.com"}, {"sam@gmail.com", "sam@googlemail.com"}} {
		t.Run(c.registered, func(t *testing.T) {
			l, _, _ := newLocal(t)
			li := signup(t, l, c.registered)
			g := google()
			g.Email = c.atGoogle
			id, created, err := l.SignInWithGoogle(context.Background(), g)
			if err != nil || created || id.Subject != subject(li) || id.Email != c.registered {
				t.Errorf("%+v created %v, %v", id, created, err)
			}
		})
	}
}

func TestGoogleRefusesAnUnverifiedAddress(t *testing.T) {
	ctx := context.Background()
	l, m, _ := newLocal(t)
	li := signup(t, l, "sam@gmail.com")
	g := google()
	g.EmailVerified = false
	if _, _, err := l.SignInWithGoogle(ctx, g); !errors.Is(err, identity.ErrGoogleUnverifiedEmail) {
		t.Fatalf("SignInWithGoogle = %v", err)
	}
	if stored, _ := m.IdentityByID(ctx, li.ID); stored.GoogleSubject != "" || stored.PasswordHash == "" {
		t.Error("a refused sign-in touched the identity")
	}
}

// An inactive account is refused before anything is written: a stranger's sign-in must not be
// able to remove its password.
func TestGoogleRefusesAnInactiveAccountBeforeWriting(t *testing.T) {
	ctx := context.Background()
	l, m, a := newLocal(t)
	li := signup(t, l, "sam@gmail.com")
	a.inactive[subject(li)] = true
	if _, _, err := l.SignInWithGoogle(ctx, google()); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("SignInWithGoogle = %v", err)
	}
	if stored, _ := m.IdentityByID(ctx, li.ID); stored.GoogleSubject != "" || stored.PasswordHash == "" || len(a.revoked) != 0 {
		t.Errorf("an inactive account was changed: %+v, revoked %v", stored, a.revoked)
	}
}

func TestAGoogleOnlyIdentityHasNoPassword(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLocal(t)
	id, _, err := l.SignInWithGoogle(ctx, google())
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []string{"", "        ", "any password at all"} {
		if _, err := l.Login(ctx, "sam@gmail.com", attempt); !errors.Is(err, identity.ErrInvalidCredentials) {
			t.Errorf("Login(%q) = %v", attempt, err)
		}
	}
	idnID, _ := ids.Identity.Parse(id.Subject)
	if err := l.ChangePassword(ctx, idnID, "", pw); !errors.Is(err, identity.ErrNoPassword) {
		t.Errorf("ChangePassword = %v, want ErrNoPassword", err)
	}
	if _, err := l.ChangeEmail(ctx, idnID, "", "elsewhere@example.com"); !errors.Is(err, identity.ErrNoPassword) {
		t.Errorf("ChangeEmail = %v, want ErrNoPassword", err)
	}
	// The reset flow is the way to a first password: it proves the address instead.
	token, _, err := l.BeginReset(ctx, "sam@gmail.com")
	if err != nil || token == "" {
		t.Fatalf("BeginReset = %q, %v", token, err)
	}
	if _, err := l.CompleteReset(ctx, token, pw); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Login(ctx, "sam@gmail.com", pw); err != nil {
		t.Errorf("the new password: %v", err)
	}
	if _, created, err := l.SignInWithGoogle(ctx, google()); err != nil || created {
		t.Errorf("Google after a reset: created %v, %v", created, err)
	}
}
