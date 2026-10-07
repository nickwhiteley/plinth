package settings_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/settings"
)

func b64std(b []byte) string    { return base64.StdEncoding.EncodeToString(b) }
func b64url(b []byte) string    { return base64.URLEncoding.EncodeToString(b) }
func b64rawstd(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }

func key(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, settings.EncryptionKeyBytes)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// Test keys, standing in for a product's.
const (
	kFrom     = "email_from"
	kToken    = "email_token"
	kSweep    = "sweep_interval"
	kStub     = "stub_secret"
	kProvider = "provider"
	kSecret   = "provider_secret"
	kContact  = "contact"
)

// registry is Core plus a product's worth of settings, with the two kinds of rule: a fatal guard
// on a switch, and an advisory banner on a half-filled pair.
func registry() *settings.Registry {
	r := settings.NewRegistry().Declare(settings.Core...).Declare(
		settings.Declaration{Key: kFrom, Scope: settings.ScopeAPI, Kind: settings.KindString},
		settings.Declaration{Key: kToken, Scope: settings.ScopeAPI, Kind: settings.KindString, Sensitive: true},
		settings.Declaration{Key: kSweep, Scope: settings.ScopeAPI, Kind: settings.KindDuration, Default: "0"},
		settings.Declaration{Key: kStub, Scope: settings.ScopeAPI, Kind: settings.KindString, Sensitive: true, Default: "stub-webhook-secret"},
		settings.Declaration{Key: kProvider, Scope: settings.ScopeAPI, Kind: settings.KindString},
		settings.Declaration{Key: kSecret, Scope: settings.ScopeAPI, Kind: settings.KindString, Sensitive: true},
		settings.Declaration{Key: kContact, Scope: settings.ScopeAPI, Kind: settings.KindString, Validate: settings.ValidAddress(kContact)},
	)
	return r.Rule(func(_ context.Context, _, next settings.Snapshot) []settings.Problem {
		var ps []settings.Problem
		if next.String(kProvider) == "real" && next.String(kSecret) == "" {
			ps = append(ps, settings.Problem{Key: kProvider, Err: code.New("test.provider_needs_secret"), Fatal: true})
		}
		if (next.String(kFrom) == "") != (next.String(kToken) == "") {
			ps = append(ps, settings.Problem{Key: kFrom, Err: code.New("test.mail_half_configured")})
		}
		return ps
	})
}
