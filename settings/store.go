package settings

import (
	"context"
	"time"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/ids"
)

// ErrNotFound is a key with no stored row.
var ErrNotFound = code.New("settings.not_found")

// Row is a row of app_setting. **Ciphertext in, ciphertext out**: the store never encrypts or
// decrypts, so the conformance suite tests storage rather than cryptography.
type Row struct {
	Key        string
	Value      []byte
	KeyVersion int16
	UpdatedAt  time.Time
}

// Version is one entry of a setting's history: the value it was set to, when, and by whom. The
// value stays encrypted here too. ModifiedBy is nil for a write nobody instructed (seeding, or the
// insert that reconciles a new key), which is honest: nobody did it.
type Version struct {
	Key        string
	Value      []byte
	ModifiedAt time.Time
	ModifiedBy *ids.UUID
}

// Store persists settings (spec.md §3). Writes are attributed to actor.From(ctx).
type Store interface {
	// Settings returns every stored row, sorted by key, whether or not anything still declares
	// it: a stale key is reported rather than hidden.
	Settings(ctx context.Context) ([]Row, error)
	// InsertSetting adds a key that isn't stored yet. It never overwrites: inserting a stored key
	// is a no-op, so the declared default applies once and an administrator's value outranks it.
	InsertSetting(ctx context.Context, r Row) error
	// SetSetting replaces one value, or ErrNotFound for a key that isn't stored.
	SetSetting(ctx context.Context, key string, value []byte, keyVersion int16) error
	// SettingHistory returns the most recent writes, newest first, at most limit of them.
	SettingHistory(ctx context.Context, limit int) ([]Version, error)
}
