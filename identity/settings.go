package identity

import (
	"context"
	"strings"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/settings"
)

// The settings identity reads (spec.md §8).
const (
	KeyGoogleClientID     = "google_client_id"
	KeyGoogleClientSecret = "google_client_secret"
)

// Settings declares Google sign-in's credentials. The client id isn't sensitive: it travels in the
// redirect, so it's public by construction, and hiding it would only stop an administrator
// checking it against the console.
var Settings = []settings.Declaration{
	{Key: KeyGoogleClientID, Scope: settings.ScopeAPI, Kind: settings.KindString},
	{Key: KeyGoogleClientSecret, Scope: settings.ScopeAPI, Kind: settings.KindString, Sensitive: true},
}

var (
	// ErrSettingsGoogleNeedsAppURL: the redirect URI is derived from app_url, and a guessed one
	// fails at Google with an error this server never sees. Fatal.
	ErrSettingsGoogleNeedsAppURL = code.New("identity.settings.google_needs_app_url")
	// ErrSettingsGoogleHalfConfigured: both halves are needed, or the button isn't offered. A
	// banner, because it's the normal state between two saves.
	ErrSettingsGoogleHalfConfigured = code.New("identity.settings.google_half_configured")
)

// SettingsRule is Google sign-in's coherence rule. A product registers it with Settings.
func SettingsRule(_ context.Context, _, next settings.Snapshot) []settings.Problem {
	var ps []settings.Problem
	id, secret := next.String(KeyGoogleClientID) != "", next.String(KeyGoogleClientSecret) != ""
	if (id || secret) && next.String(settings.KeyAppURL) == "" {
		ps = append(ps, settings.Problem{Key: settings.KeyAppURL, Err: ErrSettingsGoogleNeedsAppURL, Fatal: true})
	}
	if id != secret {
		ps = append(ps, settings.Problem{Key: KeyGoogleClientID, Err: ErrSettingsGoogleHalfConfigured})
	}
	return ps
}

// GoogleFromSnapshot builds the Google client from a snapshot, for a settings.Binding, or returns
// nil when Google sign-in isn't configured, which is a legitimate answer: the product doesn't
// offer the button. callbackPath is the product's route, e.g. "/auth/google/callback".
func GoogleFromSnapshot(s settings.Snapshot, callbackPath string) (*Google, error) {
	id, secret := s.String(KeyGoogleClientID), s.String(KeyGoogleClientSecret)
	if id == "" || secret == "" {
		return nil, nil
	}
	return NewGoogle(id, secret, strings.TrimRight(s.String(settings.KeyAppURL), "/")+callbackPath)
}
