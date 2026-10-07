package email

import (
	"context"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/settings"
)

// The settings email reads (spec.md §8).
const (
	KeyProviderKey     = "email_provider_key"
	KeyFrom            = "email_from"
	KeyBroadcastStream = "email_broadcast_stream"
)

// Settings declares the mailer's settings.
var Settings = []settings.Declaration{
	{Key: KeyProviderKey, Scope: settings.ScopeAPI, Kind: settings.KindString, Sensitive: true},
	{Key: KeyFrom, Scope: settings.ScopeAPI, Kind: settings.KindString, Validate: settings.ValidAddress(KeyFrom)},
	// Empty refuses marketing mail rather than sending it on the transactional stream.
	{Key: KeyBroadcastStream, Scope: settings.ScopeAPI, Kind: settings.KindString},
}

// ErrSettingsHalfConfigured: sending needs both the token and a sender address. A banner: it's
// the normal state between two saves.
var ErrSettingsHalfConfigured = code.New("email.settings.half_configured")

// SettingsRule is the mailer's coherence rule.
func SettingsRule(_ context.Context, _, next settings.Snapshot) []settings.Problem {
	if (next.String(KeyProviderKey) == "") != (next.String(KeyFrom) == "") {
		return []settings.Problem{{Key: KeyFrom, Err: ErrSettingsHalfConfigured}}
	}
	return nil
}

// SenderFromSnapshot builds the sender the settings describe, for a settings.Binding: Postmark
// when both halves are set, NoOp otherwise (which Discards reports, so a product can log links).
func SenderFromSnapshot(s settings.Snapshot) (Sender, error) {
	key, from := s.String(KeyProviderKey), s.String(KeyFrom)
	if key == "" || from == "" {
		return NoOp{}, nil
	}
	return NewPostmark(key, from, s.String(KeyBroadcastStream))
}
