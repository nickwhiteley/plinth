package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nickwhiteley/plinth/identity"
	"github.com/nickwhiteley/plinth/settings"
)

func googleRegistry() *settings.Registry {
	return settings.NewRegistry().Declare(settings.Core...).Declare(identity.Settings...).Rule(identity.SettingsRule)
}

func TestGoogleSettingsRule(t *testing.T) {
	r := googleRegistry()
	ctx := context.Background()
	check := func(rows ...settings.State) []settings.Problem { s := r.Snapshot(rows); return r.Validate(ctx, s, s) }

	if ps := check(); len(ps) != 0 {
		t.Errorf("nothing configured: %+v", ps)
	}
	// Both halves without an application URL: the redirect URI would be a guess. Fatal.
	ps := check(settings.State{Key: identity.KeyGoogleClientID, Value: "id"}, settings.State{Key: identity.KeyGoogleClientSecret, Value: "s"})
	if p, fatal := settings.FirstFatal(ps); !fatal || !errors.Is(p.Err, identity.ErrSettingsGoogleNeedsAppURL) {
		t.Errorf("no app_url: %+v", ps)
	}
	// One half, with the URL: a banner, not a refusal.
	ps = check(settings.State{Key: settings.KeyAppURL, Value: "https://app.example"}, settings.State{Key: identity.KeyGoogleClientID, Value: "id"})
	if _, fatal := settings.FirstFatal(ps); fatal || len(ps) != 1 || !errors.Is(ps[0].Err, identity.ErrSettingsGoogleHalfConfigured) {
		t.Errorf("one half: %+v", ps)
	}
}

func TestGoogleFromSnapshot(t *testing.T) {
	r := googleRegistry()
	if g, err := identity.GoogleFromSnapshot(r.Snapshot(nil), "/auth/google/callback"); g != nil || err != nil {
		t.Errorf("unconfigured = %v, %v: want nil, nil", g, err)
	}
	s := r.Snapshot([]settings.State{{Key: settings.KeyAppURL, Value: "https://app.example/"},
		{Key: identity.KeyGoogleClientID, Value: "id"}, {Key: identity.KeyGoogleClientSecret, Value: "secret"}})
	g, err := identity.GoogleFromSnapshot(s, "/auth/google/callback")
	if err != nil || g.RedirectURI() != "https://app.example/auth/google/callback" {
		t.Fatalf("GoogleFromSnapshot = %v, %v", g, err)
	}
}
