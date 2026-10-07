// Package pg is the Postgres flags.Store, on plinth's tier, feature_flag and account_flag tables.
package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
)

// Store is safe for concurrent use.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ flags.Store = (*Store)(nil)

func mapErr(err error) error {
	switch db.Constraint(err) {
	case "":
		return err
	case "tier_key_uq", "tier_sort_order_uq", "tier_pk":
		return flags.ErrTierExists
	case "feature_flag_min_tier_fk", "account_flag_flag_fk", "account_flag_account_fk":
		return flags.ErrNotFound
	case "tier_key_ck", "tier_name_ck", "tier_sort_order_ck":
		return flags.ErrTierInvalid
	}
	return err
}

func (s *Store) run(ctx context.Context, sql string, args ...any) (int64, error) {
	var n int64
	err := db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		n = tag.RowsAffected()
		return err
	})
	return n, mapErr(err)
}

func (s *Store) one(ctx context.Context, sql string, args ...any) error {
	n, err := s.run(ctx, sql, args...)
	if err == nil && n == 0 {
		return flags.ErrNotFound
	}
	return err
}

func (s *Store) Tiers(ctx context.Context) ([]flags.Tier, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT id, key, name, sort_order, is_enabled FROM tier ORDER BY sort_order`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (flags.Tier, error) {
		var t flags.Tier
		err := r.Scan(&t.ID, &t.Key, &t.Name, &t.Order, &t.Enabled)
		return t, err
	})
}

func (s *Store) CreateTier(ctx context.Context, t flags.Tier) error {
	_, err := s.run(ctx, `INSERT INTO tier (id, key, name, sort_order, is_enabled) VALUES ($1, $2, $3, $4, $5)`, t.ID, t.Key, t.Name, t.Order, t.Enabled)
	return err
}

func (s *Store) SetTierEnabled(ctx context.Context, id ids.UUID, enabled bool) error {
	return s.one(ctx, `UPDATE tier SET is_enabled = $2 WHERE id = $1`, id, enabled)
}

func (s *Store) Flags(ctx context.Context) ([]flags.State, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT key, is_enabled, min_tier_id, is_retired FROM feature_flag ORDER BY key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (flags.State, error) {
		var st flags.State
		err := r.Scan(&st.Key, &st.Enabled, &st.MinTierID, &st.Retired)
		return st, err
	})
}

func (s *Store) InsertFlag(ctx context.Context, st flags.State) error {
	_, err := s.run(ctx, `INSERT INTO feature_flag (key, is_enabled, min_tier_id, is_retired) VALUES ($1, $2, $3, $4) ON CONFLICT (key) DO NOTHING`,
		st.Key, st.Enabled, st.MinTierID, st.Retired)
	return err
}

func (s *Store) SetFlag(ctx context.Context, key string, enabled bool, minTier *ids.UUID) error {
	return s.one(ctx, `UPDATE feature_flag SET is_enabled = $2, min_tier_id = $3 WHERE key = $1`, key, enabled, minTier)
}

func (s *Store) SetRetired(ctx context.Context, key string, retired bool) error {
	return s.one(ctx, `UPDATE feature_flag SET is_retired = $2 WHERE key = $1`, key, retired)
}

func (s *Store) AccountFlags(ctx context.Context, account ids.UUID) (map[string]bool, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT flag_key, is_enabled FROM account_flag WHERE account_id = $1`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		var v bool
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Store) SetAccountFlag(ctx context.Context, account ids.UUID, key string, enabled bool) error {
	_, err := s.run(ctx, `INSERT INTO account_flag (account_id, flag_key, is_enabled) VALUES ($1, $2, $3)
		ON CONFLICT (account_id, flag_key) DO UPDATE SET is_enabled = EXCLUDED.is_enabled`, account, key, enabled)
	return err
}

func (s *Store) ClearAccountFlag(ctx context.Context, account ids.UUID, key string) error {
	_, err := s.run(ctx, `DELETE FROM account_flag WHERE account_id = $1 AND flag_key = $2`, account, key)
	return err
}
