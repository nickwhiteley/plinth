package account_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/account"
	accmem "github.com/nickwhiteley/plinth/account/mem"
	"github.com/nickwhiteley/plinth/identity"
	idnmem "github.com/nickwhiteley/plinth/identity/mem"
	"github.com/nickwhiteley/plinth/ids"
)

// Lifted from Bloomprint's auth tests: everything about sessions, run end to end through the real
// local provider, so "a reset ends your sessions" is tested as a person experiences it.

const pw = "a sufficiently long password"

var defaultTier = ids.New()

type rig struct {
	local    *identity.Local
	accounts *account.Service
	store    *accmem.Store
}

func newRig(t *testing.T, opts ...account.Option) rig {
	t.Helper()
	st := accmem.New()
	svc := account.New(st, append([]account.Option{account.WithDefaultTier(defaultTier)}, opts...)...)
	return rig{local: identity.NewLocal(idnmem.New(), svc, identity.WithReducedHashCost()), accounts: svc, store: st}
}

func (r rig) signup(t *testing.T, email string) identity.LocalIdentity {
	t.Helper()
	li, err := r.local.Signup(context.Background(), email, pw)
	if err != nil {
		t.Fatalf("Signup(%s): %v", email, err)
	}
	return li
}

// login is what a product's sign-in handler does: prove, then admit.
func (r rig) login(ctx context.Context, email, password string) (account.Admission, error) {
	id, err := r.local.Login(ctx, email, password)
	if err != nil {
		return account.Admission{}, err
	}
	return r.accounts.Admit(ctx, id, account.NewProfile{})
}

func (r rig) mustLogin(t *testing.T, email string) account.Admission {
	t.Helper()
	ad, err := r.login(context.Background(), email, pw)
	if err != nil {
		t.Fatalf("login(%s): %v", email, err)
	}
	return ad
}

func TestLoginAndAuthenticate(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.signup(t, "nick@example.com")
	ad := r.mustLogin(t, "Nick@Example.com")
	if !ad.Created || !ad.Account.Active || ad.Account.IdentityIssuer != "local" || !ad.Account.ID.IsV7() {
		t.Errorf("the first sign-in: %+v", ad)
	}
	if len(ad.Token) != 43 {
		t.Errorf("the token is %d characters, want 43 (256 bits, base64url)", len(ad.Token))
	}
	got, err := r.accounts.Authenticate(ctx, ad.Token)
	if err != nil || got.ID != ad.Account.ID {
		t.Fatalf("Authenticate = %+v, %v", got, err)
	}
	// Only the hash is stored.
	sess, err := r.store.Session(ctx, ids.HashToken(ad.Token))
	if err != nil || !sess.ExpiresAt.After(time.Now().Add(account.SessionTTL-time.Minute)) {
		t.Errorf("the stored session: %+v, %v", sess, err)
	}
	// A second sign-in finds the same account.
	again := r.mustLogin(t, "nick@example.com")
	if again.Created || again.Account.ID != ad.Account.ID {
		t.Errorf("a second sign-in made another account: %+v", again)
	}
}

func TestAdmitCreatesAProfileAndRecordsTheSignIn(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	ad, err := r.accounts.Admit(ctx, identity.Identity{Issuer: "local", Subject: "idn_x", Email: "Sam@Example.com", Name: "  Sam Maker "},
		account.NewProfile{TimeZone: "America/New_York", Locale: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := r.store.Profile(ctx, ad.Account.ID)
	if p.DisplayName != "Sam Maker" || p.Email != "sam@example.com" || p.TimeZone != "America/New_York" || p.Locale != "en-US" {
		t.Errorf("profile %+v", p)
	}
	if a, _ := r.store.AccountByID(ctx, ad.Account.ID); a.LastSignedInAt == nil {
		t.Error("the sign-in wasn't recorded")
	}
	// No name: the address's local part, and the defaults.
	ad, _ = r.accounts.Admit(ctx, identity.Identity{Issuer: "local", Subject: "idn_y", Email: "jo@example.com"}, account.NewProfile{})
	if p, _ := r.store.Profile(ctx, ad.Account.ID); p.DisplayName != "jo" || p.TimeZone != "Europe/London" || p.Locale != "en-GB" {
		t.Errorf("defaults %+v", p)
	}
}

func TestAdmitRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id := identity.Identity{Issuer: "local", Subject: "idn_x", Email: "x@example.com"}
	for name, c := range map[string]struct {
		id   identity.Identity
		np   account.NewProfile
		code error
	}{
		"no subject":  {identity.Identity{Issuer: "local"}, account.NewProfile{}, account.ErrIdentityInvalid},
		"no issuer":   {identity.Identity{Subject: "idn_x"}, account.NewProfile{}, account.ErrIdentityInvalid},
		"a time zone": {id, account.NewProfile{TimeZone: "Mars/Olympus"}, account.ErrTimeZoneInvalid},
		"Local":       {id, account.NewProfile{TimeZone: "Local"}, account.ErrTimeZoneInvalid},
		"a locale":    {id, account.NewProfile{Locale: "english"}, account.ErrLocaleInvalid},
	} {
		if _, err := r.accounts.Admit(ctx, c.id, c.np); !errors.Is(err, c.code) {
			t.Errorf("%s: %v, want %v", name, err, c.code)
		}
	}
}

// A first sign-in racing itself makes one account, and every caller gets a session on it.
func TestConcurrentFirstSignInsMakeOneAccount(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id := identity.Identity{Issuer: "local", Subject: "idn_race", Email: "race@example.com"}
	var wg sync.WaitGroup
	got := make([]account.Admission, 16)
	errs := make([]error, 16)
	for i := range got {
		wg.Add(1)
		go func() { defer wg.Done(); got[i], errs[i] = r.accounts.Admit(ctx, id, account.NewProfile{}) }()
	}
	wg.Wait()
	created := 0
	for i := range got {
		if errs[i] != nil || got[i].Account.ID != got[0].Account.ID {
			t.Fatalf("caller %d: %+v, %v", i, got[i].Account, errs[i])
		}
		if got[i].Created {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d callers created the account, want 1", created)
	}
}

// An identity whose account was never made (a crash after sign-up) gets one at its next sign-in.
func TestASignUpWithoutAnAccountHeals(t *testing.T) {
	r := newRig(t)
	r.signup(t, "nick@example.com") // the product crashed before admitting it
	if ad := r.mustLogin(t, "nick@example.com"); !ad.Created {
		t.Error("the account wasn't created at the next sign-in")
	}
}

func TestAdmitRefreshesTheCachedEmail(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	li := r.signup(t, "nick@example.com")
	ad := r.mustLogin(t, "nick@example.com")
	if _, err := r.local.ChangeEmail(ctx, li.ID, pw, "moved@example.com"); err != nil {
		t.Fatal(err)
	}
	r.mustLogin(t, "moved@example.com")
	if p, _ := r.store.Profile(ctx, ad.Account.ID); p.Email != "moved@example.com" {
		t.Errorf("the profile still caches %q", p.Email)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.signup(t, "nick@example.com")
	ad := r.mustLogin(t, "nick@example.com")
	if err := r.accounts.Logout(ctx, ad.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := r.accounts.Authenticate(ctx, ad.Token); !errors.Is(err, account.ErrUnauthenticated) {
		t.Errorf("Authenticate after logout = %v", err)
	}
	if err := r.accounts.Logout(ctx, ad.Token); err != nil {
		t.Errorf("a second Logout = %v: it must be idempotent", err)
	}
}

// The point of a reset is to end whatever access prompted it. A new hash that leaves the old
// cookie working is a remedy in name only.
func TestAResetEndsEverySession(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.signup(t, "nick@example.com")
	r.signup(t, "someone@example.com")
	mine := []string{r.mustLogin(t, "nick@example.com").Token, r.mustLogin(t, "nick@example.com").Token}
	theirs := r.mustLogin(t, "someone@example.com").Token

	token, _, err := r.local.BeginReset(ctx, "nick@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.local.CompleteReset(ctx, token, "a different long password"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range mine {
		if _, err := r.accounts.Authenticate(ctx, tok); !errors.Is(err, account.ErrUnauthenticated) {
			t.Errorf("a session survived the reset: %v", err)
		}
	}
	if _, err := r.accounts.Authenticate(ctx, theirs); err != nil {
		t.Errorf("the reset took somebody else's session: %v", err)
	}
	if _, err := r.login(ctx, "nick@example.com", "a different long password"); err != nil {
		t.Errorf("the new password: %v", err)
	}
}

func TestAPasswordChangeEndsEverySession(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	li := r.signup(t, "nick@example.com")
	tokens := []string{r.mustLogin(t, "nick@example.com").Token, r.mustLogin(t, "nick@example.com").Token}
	if err := r.local.ChangePassword(ctx, li.ID, pw, "a different long password"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if _, err := r.accounts.Authenticate(ctx, tok); !errors.Is(err, account.ErrUnauthenticated) {
			t.Errorf("a session survived the password change: %v", err)
		}
	}
}

// Deactivation stops both doors: signing in, and a session already held.
func TestADeactivatedAccountCannotSignInOrUseASession(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.signup(t, "nick@example.com")
	ad := r.mustLogin(t, "nick@example.com")
	if err := r.accounts.Deactivate(ctx, ad.Account.ID); err != nil {
		t.Fatal(err)
	}
	// The wrong-password code: a "deactivated" reply would be an enumeration oracle.
	if _, err := r.login(ctx, "nick@example.com", pw); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("signing in while deactivated = %v", err)
	}
	if _, err := r.accounts.Authenticate(ctx, ad.Token); !errors.Is(err, account.ErrUnauthenticated) {
		t.Errorf("a session held across deactivation = %v", err)
	}
	if err := r.accounts.Reactivate(ctx, ad.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.login(ctx, "nick@example.com", pw); err != nil {
		t.Errorf("signing in after reactivation: %v", err)
	}
}

// The belt to Deactivate's braces: a session that somehow survives deactivation still doesn't
// resolve.
func TestAuthenticateChecksTheAccountIsActive(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.signup(t, "nick@example.com")
	ad := r.mustLogin(t, "nick@example.com")
	if err := r.store.SetActive(ctx, ad.Account.ID, false, time.Now()); err != nil { // without ending sessions
		t.Fatal(err)
	}
	if _, err := r.accounts.Authenticate(ctx, ad.Token); !errors.Is(err, account.ErrUnauthenticated) {
		t.Errorf("Authenticate on an inactive account = %v", err)
	}
}

func TestAuthenticateRefusesUnknownEmptyAndExpiredTokens(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.signup(t, "nick@example.com")
	ad := r.mustLogin(t, "nick@example.com")
	expired := account.Session{TokenHash: ids.HashToken("expired"), AccountID: ad.Account.ID, CreatedAt: time.Now().Add(-2 * account.SessionTTL),
		LastSeenAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Hour)}
	if err := r.store.CreateSession(ctx, expired); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", "deadbeef", "expired"} {
		if _, err := r.accounts.Authenticate(ctx, tok); !errors.Is(err, account.ErrUnauthenticated) {
			t.Errorf("Authenticate(%q) = %v", tok, err)
		}
	}
}

func TestAuthenticateSlidesTheExpiryAndStampsLastSeen(t *testing.T) {
	ctx := context.Background()
	clock := time.Now()
	r := newRig(t, account.WithClock(func() time.Time { return clock }))
	r.signup(t, "nick@example.com")
	ad := r.mustLogin(t, "nick@example.com")
	hash := ids.HashToken(ad.Token)
	issued, _ := r.store.Session(ctx, hash)

	// Within the minute: nothing is written.
	clock = clock.Add(30 * time.Second)
	if _, err := r.accounts.Authenticate(ctx, ad.Token); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.store.Session(ctx, hash); !s.LastSeenAt.Equal(issued.LastSeenAt) || !s.ExpiresAt.Equal(issued.ExpiresAt) {
		t.Errorf("a request within the minute wrote the session: %+v", s)
	}
	// After a minute: last seen moves, and the expiry doesn't (it was set less than a day ago).
	clock = clock.Add(time.Minute)
	if _, err := r.accounts.Authenticate(ctx, ad.Token); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.store.Session(ctx, hash); !s.LastSeenAt.Equal(clock) || !s.ExpiresAt.Equal(issued.ExpiresAt) {
		t.Errorf("after a minute: %+v", s)
	}
	// A day or more since the expiry was last set: it slides to a full TTL from now.
	clock = clock.Add(2 * 24 * time.Hour)
	if _, err := r.accounts.Authenticate(ctx, ad.Token); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.store.Session(ctx, hash); !s.ExpiresAt.Equal(clock.Add(account.SessionTTL)) {
		t.Errorf("the expiry didn't slide: %v", s.ExpiresAt)
	}
}

func google() identity.ExternalIdentity {
	return identity.ExternalIdentity{Subject: "108000000000000000001", Email: "sam@gmail.com", EmailVerified: true, Name: "Sam Maker"}
}

func (r rig) googleSignIn(t *testing.T) account.Admission {
	t.Helper()
	ctx := context.Background()
	id, _, err := r.local.SignInWithGoogle(ctx, google())
	if err != nil {
		t.Fatalf("SignInWithGoogle: %v", err)
	}
	ad, err := r.accounts.Admit(ctx, id, account.NewProfile{})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return ad
}

// The pre-hijacking case, end to end: the planted password's sessions end, and the session the
// Google sign-in is then given isn't caught in the sweep.
func TestGoogleEndsAPlantedPasswordsSessions(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.signup(t, "sam@gmail.com")
	planted := r.mustLogin(t, "sam@gmail.com")
	ad := r.googleSignIn(t)
	if ad.Account.ID != planted.Account.ID {
		t.Fatalf("the accounts weren't combined")
	}
	if _, err := r.accounts.Authenticate(ctx, planted.Token); !errors.Is(err, account.ErrUnauthenticated) {
		t.Errorf("the planted session is still live: %v", err)
	}
	if _, err := r.accounts.Authenticate(ctx, ad.Token); err != nil {
		t.Errorf("the new session was swept: %v", err)
	}
	if p, _ := r.store.Profile(ctx, ad.Account.ID); p.DisplayName != "sam" {
		t.Errorf("the profile made at the first sign-in was overwritten: %+v", p)
	}
}

func TestGoogleCannotSignInToADeactivatedAccount(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	ad := r.googleSignIn(t)
	if p, _ := r.store.Profile(ctx, ad.Account.ID); p.DisplayName != "Sam Maker" {
		t.Errorf("Google's name wasn't used: %+v", p)
	}
	if err := r.accounts.Deactivate(ctx, ad.Account.ID); err != nil {
		t.Fatal(err)
	}
	id, _, err := r.local.SignInWithGoogle(ctx, google())
	if err != nil {
		t.Fatal(err) // a linked subject proves the identity; the account refuses it
	}
	if _, err := r.accounts.Admit(ctx, id, account.NewProfile{}); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Errorf("Admit on a deactivated account = %v", err)
	}
}

func TestANewAccountStartsOnTheDefaultTierWithItsProfilesZone(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	ad, err := r.accounts.Admit(ctx, identity.Identity{Issuer: "local", Subject: "idn_x", Email: "x@example.com"}, account.NewProfile{TimeZone: "Asia/Tokyo"})
	if err != nil {
		t.Fatal(err)
	}
	if ad.Account.TierID != defaultTier || ad.Account.EffectiveTier() != defaultTier || ad.Account.QuotaTimeZone != "Asia/Tokyo" {
		t.Errorf("a new account: %+v", ad.Account)
	}
	// Support moves the quota day; the zone must be real.
	if err := r.accounts.SetQuotaTimeZone(ctx, ad.Account.ID, "Mars/Olympus"); !errors.Is(err, account.ErrTimeZoneInvalid) {
		t.Errorf("an unknown zone = %v", err)
	}
	if err := r.accounts.SetQuotaTimeZone(ctx, ad.Account.ID, "Europe/Paris"); err != nil {
		t.Fatal(err)
	}
	// A service with no default tier can't create accounts.
	bare := account.New(accmem.New())
	if _, err := bare.Admit(ctx, identity.Identity{Issuer: "local", Subject: "idn_y", Email: "y@example.com"}, account.NewProfile{}); !errors.Is(err, account.ErrNoDefaultTier) {
		t.Errorf("Admit with no default tier = %v", err)
	}
}
