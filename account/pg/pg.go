// Package pg is the Postgres account.Store, on plinth's account, account_profile and session
// tables. storetest.Run holds it to the same behaviour as the in-memory store.
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/account"
	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/ids"
)

// Store is safe for concurrent use.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ account.Store = (*Store)(nil)

func mapErr(err error) error {
	switch db.Constraint(err) {
	case "":
		return err
	case "account_identity_uq":
		return account.ErrIdentityTaken
	case "session_account_fk", "account_tier_fk", "account_tier_override_fk", "account_profile_account_fk":
		return account.ErrNotFound
	case "account_quota_tz_ck", "account_profile_tz_ck":
		return account.ErrTimeZoneInvalid
	case "account_profile_locale_fk":
		return account.ErrLocaleInvalid
	}
	return err
}

func (s *Store) write(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return mapErr(db.Run(ctx, s.pool, fn))
}

// one runs a write that must touch exactly one row.
func (s *Store) one(ctx context.Context, sql string, args ...any) error {
	return s.write(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err == nil && tag.RowsAffected() == 0 {
			return account.ErrNotFound
		}
		return err
	})
}

func (s *Store) CreateAccount(ctx context.Context, a account.Account, p account.Profile) error {
	return s.write(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO account (id, identity_issuer, identity_subject, tier_id, tier_override_id, quota_time_zone, is_active, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, true, $7)`, a.ID, a.IdentityIssuer, a.IdentitySubject, a.TierID, a.TierOverrideID, a.QuotaTimeZone, a.CreatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO account_profile (account_id, display_name, time_zone, locale, email, refreshed_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			a.ID, p.DisplayName, p.TimeZone, p.Locale, p.Email, p.RefreshedAt)
		return err
	})
}

const accountCols = `id, identity_issuer, identity_subject, tier_id, tier_override_id, quota_time_zone, is_active, deactivated_at, last_signed_in_at, created_at`

func scanAccount(row pgx.Row) (account.Account, error) {
	var a account.Account
	err := row.Scan(&a.ID, &a.IdentityIssuer, &a.IdentitySubject, &a.TierID, &a.TierOverrideID, &a.QuotaTimeZone, &a.Active, &a.DeactivatedAt, &a.LastSignedInAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, account.ErrNotFound
	}
	return a, err
}

func (s *Store) AccountByID(ctx context.Context, id ids.UUID) (account.Account, error) {
	return scanAccount(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+accountCols+` FROM account WHERE id = $1 AND deleted_at IS NULL`, id))
}

func (s *Store) AccountByIdentity(ctx context.Context, issuer, subject string) (account.Account, error) {
	return scanAccount(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+accountCols+` FROM account
		WHERE identity_issuer = $1 AND identity_subject = $2 AND deleted_at IS NULL`, issuer, subject))
}

func (s *Store) SetActive(ctx context.Context, id ids.UUID, active bool, at time.Time) error {
	var deactivated *time.Time
	if !active {
		deactivated = &at
	}
	return s.one(ctx, `UPDATE account SET is_active = $2, deactivated_at = $3 WHERE id = $1 AND deleted_at IS NULL`, id, active, deactivated)
}

func (s *Store) TouchSignIn(ctx context.Context, id ids.UUID, at time.Time) error {
	return s.one(ctx, `UPDATE account SET last_signed_in_at = $2 WHERE id = $1 AND deleted_at IS NULL`, id, at)
}

func (s *Store) SetTier(ctx context.Context, id ids.UUID, tier ids.UUID, override *ids.UUID) error {
	return s.one(ctx, `UPDATE account SET tier_id = $2, tier_override_id = $3 WHERE id = $1 AND deleted_at IS NULL`, id, tier, override)
}

func (s *Store) SetQuotaTimeZone(ctx context.Context, id ids.UUID, zone string) error {
	return s.one(ctx, `UPDATE account SET quota_time_zone = $2 WHERE id = $1 AND deleted_at IS NULL`, id, zone)
}

func (s *Store) Profile(ctx context.Context, id ids.UUID) (account.Profile, error) {
	var p account.Profile
	err := db.Q(ctx, s.pool).QueryRow(ctx, `SELECT account_id, display_name, time_zone, locale, email, refreshed_at FROM account_profile WHERE account_id = $1`, id).
		Scan(&p.AccountID, &p.DisplayName, &p.TimeZone, &p.Locale, &p.Email, &p.RefreshedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, account.ErrNotFound
	}
	return p, err
}

func (s *Store) RefreshProfileEmail(ctx context.Context, id ids.UUID, email string, at time.Time) error {
	return s.one(ctx, `UPDATE account_profile SET email = $2, refreshed_at = $3 WHERE account_id = $1`, id, email, at)
}

func (s *Store) CreateSession(ctx context.Context, sess account.Session) error {
	return s.write(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO session (token_hash, account_id, created_at, last_seen_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
			sess.TokenHash[:], sess.AccountID, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt)
		return err
	})
}

func (s *Store) Session(ctx context.Context, hash [32]byte) (account.Session, error) {
	sess := account.Session{TokenHash: hash}
	err := db.Q(ctx, s.pool).QueryRow(ctx, `SELECT account_id, created_at, last_seen_at, expires_at FROM session WHERE token_hash = $1 AND expires_at > now()`, hash[:]).
		Scan(&sess.AccountID, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sess, account.ErrNotFound
	}
	return sess, err
}

func (s *Store) TouchSession(ctx context.Context, hash [32]byte, lastSeen, expires time.Time) error {
	return s.one(ctx, `UPDATE session SET last_seen_at = $2, expires_at = $3 WHERE token_hash = $1`, hash[:], lastSeen, expires)
}

func (s *Store) DeleteSession(ctx context.Context, hash [32]byte) error {
	return s.write(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM session WHERE token_hash = $1`, hash[:])
		return err
	})
}

func (s *Store) DeleteSessionsForAccount(ctx context.Context, id ids.UUID) error {
	return s.write(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM session WHERE account_id = $1`, id)
		return err
	})
}
