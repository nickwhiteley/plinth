// Package pg is the Postgres quotas.Store, on plinth's quota_key, tier_quota and account_quota
// tables.
package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/quotas"
)

// Store is safe for concurrent use.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ quotas.Store = (*Store)(nil)

func mapErr(err error) error {
	switch db.Constraint(err) {
	case "":
		return err
	case "tier_quota_tier_fk", "tier_quota_quota_fk", "account_quota_quota_fk", "account_quota_account_fk", "account_quota_granted_by_fk":
		return quotas.ErrNotFound
	case "account_quota_reason_ck":
		return quotas.ErrReasonInvalid
	case "tier_quota_limit_ck", "account_quota_limit_ck":
		return quotas.ErrLimitInvalid
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

func (s *Store) QuotaKeys(ctx context.Context) ([]quotas.KeyState, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT key, unit, is_retired FROM quota_key ORDER BY key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (quotas.KeyState, error) {
		var k quotas.KeyState
		var unit string
		err := r.Scan(&k.Key, &unit, &k.Retired)
		k.Unit = quotas.Unit(unit)
		return k, err
	})
}

func (s *Store) InsertQuotaKey(ctx context.Context, k quotas.KeyState) error {
	_, err := s.run(ctx, `INSERT INTO quota_key (key, unit, is_retired) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`, k.Key, string(k.Unit), k.Retired)
	return err
}

func (s *Store) SetRetired(ctx context.Context, key string, retired bool) error {
	n, err := s.run(ctx, `UPDATE quota_key SET is_retired = $2 WHERE key = $1`, key, retired)
	if err == nil && n == 0 {
		return quotas.ErrNotFound
	}
	return err
}

func (s *Store) TierLimits(ctx context.Context) ([]quotas.TierLimit, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT tier_id, quota_key, limit_value FROM tier_quota ORDER BY quota_key, tier_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (quotas.TierLimit, error) {
		var l quotas.TierLimit
		err := r.Scan(&l.TierID, &l.Key, &l.Limit)
		return l, err
	})
}

func (s *Store) SetTierLimit(ctx context.Context, l quotas.TierLimit) error {
	_, err := s.run(ctx, `INSERT INTO tier_quota (tier_id, quota_key, limit_value) VALUES ($1, $2, $3)
		ON CONFLICT (tier_id, quota_key) DO UPDATE SET limit_value = EXCLUDED.limit_value`, l.TierID, l.Key, l.Limit)
	return err
}

func (s *Store) ClearTierLimit(ctx context.Context, tier ids.UUID, key string) error {
	_, err := s.run(ctx, `DELETE FROM tier_quota WHERE tier_id = $1 AND quota_key = $2`, tier, key)
	return err
}

func (s *Store) Overrides(ctx context.Context, account ids.UUID) ([]quotas.Override, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT account_id, quota_key, limit_value, reason, expires_at, granted_by, granted_at
		FROM account_quota WHERE account_id = $1 ORDER BY quota_key`, account)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (quotas.Override, error) {
		var o quotas.Override
		err := r.Scan(&o.AccountID, &o.Key, &o.Limit, &o.Reason, &o.ExpiresAt, &o.GrantedBy, &o.GrantedAt)
		return o, err
	})
}

func (s *Store) SetOverride(ctx context.Context, o quotas.Override) error {
	_, err := s.run(ctx, `INSERT INTO account_quota (account_id, quota_key, limit_value, reason, expires_at, granted_by, granted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (account_id, quota_key) DO UPDATE SET limit_value = EXCLUDED.limit_value, reason = EXCLUDED.reason,
		  expires_at = EXCLUDED.expires_at, granted_by = EXCLUDED.granted_by, granted_at = EXCLUDED.granted_at`,
		o.AccountID, o.Key, o.Limit, o.Reason, o.ExpiresAt, o.GrantedBy, o.GrantedAt)
	return err
}

func (s *Store) ClearOverride(ctx context.Context, account ids.UUID, key string) error {
	_, err := s.run(ctx, `DELETE FROM account_quota WHERE account_id = $1 AND quota_key = $2`, account, key)
	return err
}
