// Package storetest is the conformance suite every account.Store must pass (spec.md §3). Lifted
// from Bloomprint's store suite: the session and sign-in cases, plus accounts and profiles.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/account"
	"github.com/nickwhiteley/plinth/ids"
)

// Run runs the suite. factory returns an empty store for each case.
func Run(t *testing.T, factory func(t *testing.T) account.Store) {
	for name, f := range map[string]func(*testing.T, account.Store){
		"accounts": testAccounts,
		"profiles": testProfiles,
		"sessions": testSessions,
	} {
		t.Run(name, func(t *testing.T) { f(t, factory(t)) })
	}
}

func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func mustAccount(t *testing.T, s account.Store, subject string) account.Account {
	t.Helper()
	a := account.Account{ID: ids.New(), IdentityIssuer: "local", IdentitySubject: subject, TierID: ids.New(), QuotaTimeZone: "Europe/London", Active: true, CreatedAt: now()}
	p := account.Profile{AccountID: a.ID, DisplayName: subject, TimeZone: "Europe/London", Locale: "en-GB", Email: subject + "@example.com", RefreshedAt: now()}
	if err := s.CreateAccount(context.Background(), a, p); err != nil {
		t.Fatalf("CreateAccount(%s): %v", subject, err)
	}
	return a
}

func testAccounts(t *testing.T, s account.Store) {
	ctx := context.Background()
	a := mustAccount(t, s, "idn_one")
	got, err := s.AccountByIdentity(ctx, "local", "idn_one")
	if err != nil || got.ID != a.ID || !got.Active || !got.CreatedAt.Equal(a.CreatedAt) {
		t.Fatalf("AccountByIdentity = %+v, %v", got, err)
	}
	if got.LastSignedInAt != nil || got.DeactivatedAt != nil {
		t.Errorf("a new account has times it shouldn't: %+v", got)
	}
	if got.TierID != a.TierID || got.TierOverrideID != nil || got.QuotaTimeZone != "Europe/London" || got.EffectiveTier() != a.TierID {
		t.Errorf("tier and quota zone: %+v", got)
	}

	// The tier and its override, set and cleared.
	pro, trial := ids.New(), ids.New()
	if err := s.SetTier(ctx, a.ID, pro, &trial); err != nil {
		t.Fatal(err)
	}
	if moved, _ := s.AccountByID(ctx, a.ID); moved.TierID != pro || moved.TierOverrideID == nil || *moved.TierOverrideID != trial || moved.EffectiveTier() != trial {
		t.Errorf("SetTier with an override: %+v", moved)
	}
	if err := s.SetTier(ctx, a.ID, pro, nil); err != nil {
		t.Fatal(err)
	}
	if moved, _ := s.AccountByID(ctx, a.ID); moved.TierOverrideID != nil || moved.EffectiveTier() != pro {
		t.Errorf("clearing the override: %+v", moved)
	}
	if err := s.SetQuotaTimeZone(ctx, a.ID, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	if moved, _ := s.AccountByID(ctx, a.ID); moved.QuotaTimeZone != "America/New_York" {
		t.Errorf("SetQuotaTimeZone: %q", moved.QuotaTimeZone)
	}
	if err := s.SetTier(ctx, ids.New(), pro, nil); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("SetTier(unknown) = %v", err)
	}
	if err := s.SetQuotaTimeZone(ctx, ids.New(), "UTC"); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("SetQuotaTimeZone(unknown) = %v", err)
	}
	if byID, err := s.AccountByID(ctx, a.ID); err != nil || byID.IdentitySubject != "idn_one" {
		t.Errorf("AccountByID = %+v, %v", byID, err)
	}
	// The identity is matched exactly: another issuer, or another subject, is another account.
	if _, err := s.AccountByIdentity(ctx, "google", "idn_one"); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("another issuer matched: %v", err)
	}
	if _, err := s.AccountByIdentity(ctx, "local", "IDN_ONE"); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("a subject in another case matched: %v", err)
	}
	if _, err := s.AccountByID(ctx, ids.New()); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("AccountByID(unknown) = %v", err)
	}
	// One live account per identity.
	dup := account.Account{ID: ids.New(), IdentityIssuer: "local", IdentitySubject: "idn_one", TierID: a.TierID, QuotaTimeZone: "UTC", Active: true, CreatedAt: now()}
	if err := s.CreateAccount(ctx, dup, account.Profile{DisplayName: "x", TimeZone: "UTC", Locale: "en-GB", Email: "x@example.com", RefreshedAt: now()}); !errors.Is(err, account.ErrIdentityTaken) {
		t.Errorf("a second account for one identity = %v", err)
	}

	// Deactivation stamps the time and reactivation clears it; both survive a re-read. A backend
	// that drops is_active silently readmits somebody.
	at := now()
	if err := s.SetActive(ctx, a.ID, false, at); err != nil {
		t.Fatal(err)
	}
	if off, _ := s.AccountByID(ctx, a.ID); off.Active || off.DeactivatedAt == nil || !off.DeactivatedAt.Equal(at) {
		t.Errorf("deactivated: %+v", off)
	}
	if err := s.SetActive(ctx, a.ID, true, now()); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.AccountByID(ctx, a.ID); !on.Active || on.DeactivatedAt != nil {
		t.Errorf("reactivated: %+v", on)
	}
	if err := s.SetActive(ctx, ids.New(), false, at); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("SetActive(unknown) = %v", err)
	}

	if err := s.TouchSignIn(ctx, a.ID, at); err != nil {
		t.Fatal(err)
	}
	if signed, _ := s.AccountByID(ctx, a.ID); signed.LastSignedInAt == nil || !signed.LastSignedInAt.Equal(at) {
		t.Errorf("TouchSignIn: %+v", signed.LastSignedInAt)
	}
	if err := s.TouchSignIn(ctx, ids.New(), at); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("TouchSignIn(unknown) = %v", err)
	}
}

func testProfiles(t *testing.T, s account.Store) {
	ctx := context.Background()
	a := mustAccount(t, s, "idn_one")
	p, err := s.Profile(ctx, a.ID)
	if err != nil || p.AccountID != a.ID || p.DisplayName != "idn_one" || p.TimeZone != "Europe/London" || p.Locale != "en-GB" || p.Email != "idn_one@example.com" {
		t.Fatalf("Profile = %+v, %v", p, err)
	}
	at := now().Add(time.Minute)
	if err := s.RefreshProfileEmail(ctx, a.ID, "moved@example.com", at); err != nil {
		t.Fatal(err)
	}
	if moved, _ := s.Profile(ctx, a.ID); moved.Email != "moved@example.com" || !moved.RefreshedAt.Equal(at) || moved.DisplayName != "idn_one" {
		t.Errorf("after refresh: %+v", moved)
	}
	if _, err := s.Profile(ctx, ids.New()); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("Profile(unknown) = %v", err)
	}
	if err := s.RefreshProfileEmail(ctx, ids.New(), "x@example.com", at); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("RefreshProfileEmail(unknown) = %v", err)
	}
}

func testSessions(t *testing.T, s account.Store) {
	ctx := context.Background()
	one := mustAccount(t, s, "idn_one")
	future := now().Add(time.Hour)
	live := account.Session{TokenHash: ids.HashToken("live"), AccountID: one.ID, CreatedAt: now(), LastSeenAt: now(), ExpiresAt: future}
	if err := s.CreateSession(ctx, live); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := s.Session(ctx, live.TokenHash)
	if err != nil || got.AccountID != one.ID || !got.ExpiresAt.Equal(future) {
		t.Fatalf("Session = %+v, %v", got, err)
	}
	// A session names an account that exists.
	if err := s.CreateSession(ctx, account.Session{TokenHash: ids.HashToken("orphan"), AccountID: ids.New(), CreatedAt: now(), LastSeenAt: now(), ExpiresAt: future}); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("a session for an unknown account = %v", err)
	}
	// An expired session reads as absent, not as valid.
	dead := account.Session{TokenHash: ids.HashToken("dead"), AccountID: one.ID, CreatedAt: now().Add(-2 * time.Hour), LastSeenAt: now().Add(-2 * time.Hour), ExpiresAt: now().Add(-time.Minute)}
	if err := s.CreateSession(ctx, dead); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, dead.TokenHash); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("an expired session = %v", err)
	}
	// Sliding expiry, and the last-seen stamp.
	seen, later := now().Add(time.Minute), now().Add(48*time.Hour)
	if err := s.TouchSession(ctx, live.TokenHash, seen, later); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Session(ctx, live.TokenHash); !got.ExpiresAt.Equal(later) || !got.LastSeenAt.Equal(seen) {
		t.Errorf("TouchSession: %+v", got)
	}
	if err := s.DeleteSession(ctx, live.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, live.TokenHash); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("a deleted session = %v", err)
	}
	if err := s.DeleteSession(ctx, ids.HashToken("never")); err != nil {
		t.Errorf("deleting an unknown session = %v: logout must be idempotent", err)
	}

	// Revoking one account's sessions takes all of them and none of anyone else's. The second half
	// is the one worth testing: sweeping the whole table passes the first half perfectly.
	two := mustAccount(t, s, "idn_two")
	for _, tok := range []string{"mine-1", "mine-2"} {
		if err := s.CreateSession(ctx, account.Session{TokenHash: ids.HashToken(tok), AccountID: one.ID, CreatedAt: now(), LastSeenAt: now(), ExpiresAt: future}); err != nil {
			t.Fatal(err)
		}
	}
	theirs := account.Session{TokenHash: ids.HashToken("theirs"), AccountID: two.ID, CreatedAt: now(), LastSeenAt: now(), ExpiresAt: future}
	if err := s.CreateSession(ctx, theirs); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSessionsForAccount(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"mine-1", "mine-2"} {
		if _, err := s.Session(ctx, ids.HashToken(tok)); !errors.Is(err, account.ErrNotFound) {
			t.Errorf("%s survived the revocation: %v", tok, err)
		}
	}
	if _, err := s.Session(ctx, theirs.TokenHash); err != nil {
		t.Errorf("revoking one account took another's session: %v", err)
	}
	if err := s.DeleteSessionsForAccount(ctx, ids.New()); err != nil {
		t.Errorf("revoking an account with no sessions = %v", err)
	}
}
