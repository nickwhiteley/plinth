// Package pg is the Postgres settings.Store, on plinth's app_setting table. History is read from
// its shadow log, so it is the audit trail itself, attributed the same way every write is.
package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/settings"
)

// Store is safe for concurrent use.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ settings.Store = (*Store)(nil)

func (s *Store) Settings(ctx context.Context) ([]settings.Row, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT setting_key, value_enc, key_version, updated_at FROM app_setting ORDER BY setting_key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (settings.Row, error) {
		var row settings.Row
		err := r.Scan(&row.Key, &row.Value, &row.KeyVersion, &row.UpdatedAt)
		return row, err
	})
}

func (s *Store) InsertSetting(ctx context.Context, r settings.Row) error {
	return db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO app_setting (setting_key, value_enc, key_version) VALUES ($1, $2, $3) ON CONFLICT (setting_key) DO NOTHING`,
			r.Key, r.Value, r.KeyVersion)
		return err
	})
}

func (s *Store) SetSetting(ctx context.Context, key string, value []byte, keyVersion int16) error {
	return db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE app_setting SET value_enc = $2, key_version = $3 WHERE setting_key = $1`, key, value, keyVersion)
		if err == nil && tag.RowsAffected() == 0 {
			return settings.ErrNotFound.With("key", key)
		}
		return err
	})
}

// SettingHistory reads the shadow log, newest first. The log schema is <schema>_log, found from
// the connection's own schema, so the store never names one.
func (s *Store) SettingHistory(ctx context.Context, limit int) ([]settings.Version, error) {
	q := db.Q(ctx, s.pool)
	var schema string
	if err := q.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT setting_key, value_enc, logged_at, modified_by FROM `+pgx.Identifier{schema + "_log", "app_setting_log"}.Sanitize()+`
		WHERE op IN ('I', 'U') ORDER BY txid DESC, log_id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (settings.Version, error) {
		var v settings.Version
		err := r.Scan(&v.Key, &v.Value, &v.ModifiedAt, &v.ModifiedBy)
		return v, err
	})
}
