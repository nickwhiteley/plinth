// Package pg is the Postgres identity.Store, on plinth's local_identity and identity_token tables.
// storetest.Run holds it to the same behaviour as the in-memory store.
package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/identity"
	"github.com/nickwhiteley/plinth/ids"
)

// Store is safe for concurrent use.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ identity.Store = (*Store)(nil)

const cols = `id, email, email_verified_at, coalesce(password_hash, ''), coalesce(google_subject, ''), created_at`

func scan(row pgx.Row) (identity.LocalIdentity, error) {
	var l identity.LocalIdentity
	err := row.Scan(&l.ID, &l.Email, &l.EmailVerifiedAt, &l.PasswordHash, &l.GoogleSubject, &l.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, identity.ErrNotFound
	}
	return l, err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// mapErr turns the constraints a write can violate into identity's codes.
func mapErr(err error) error {
	switch db.Constraint(err) {
	case "":
		return err
	case "local_identity_email_uq":
		return identity.ErrEmailTaken
	case "local_identity_google_uq":
		return identity.ErrGoogleLinked
	case "identity_token_identity_fk":
		return identity.ErrNotFound
	}
	return err
}

func (s *Store) CreateIdentity(ctx context.Context, l identity.LocalIdentity) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO local_identity (id, email, email_verified_at, password_hash, created_at) VALUES ($1, $2, $3, $4, $5)`,
			l.ID, identity.NormaliseEmail(l.Email), l.EmailVerifiedAt, nullable(l.PasswordHash), l.CreatedAt)
		return err
	}))
}

func (s *Store) IdentityByID(ctx context.Context, id ids.UUID) (identity.LocalIdentity, error) {
	return scan(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+cols+` FROM local_identity WHERE id = $1 AND deleted_at IS NULL`, id))
}

func (s *Store) IdentityByEmail(ctx context.Context, email string) (identity.LocalIdentity, error) {
	return scan(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+cols+` FROM local_identity WHERE email = $1 AND deleted_at IS NULL`, identity.NormaliseEmail(email)))
}

func (s *Store) IdentityByGoogleSubject(ctx context.Context, subject string) (identity.LocalIdentity, error) {
	if subject == "" {
		return identity.LocalIdentity{}, identity.ErrNotFound
	}
	return scan(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+cols+` FROM local_identity WHERE google_subject = $1 AND deleted_at IS NULL`, subject))
}

// exec runs one write and reports ErrNotFound when it touched nothing.
func (s *Store) exec(ctx context.Context, sql string, args ...any) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err == nil && tag.RowsAffected() == 0 {
			return identity.ErrNotFound
		}
		return err
	}))
}

func (s *Store) UpdateIdentity(ctx context.Context, l identity.LocalIdentity) error {
	return s.exec(ctx, `UPDATE local_identity SET password_hash = $2, email_verified_at = $3 WHERE id = $1 AND deleted_at IS NULL`,
		l.ID, nullable(l.PasswordHash), l.EmailVerifiedAt)
}

func (s *Store) ChangeEmail(ctx context.Context, id ids.UUID, email string) error {
	// Setting it to itself is a no-op that touches the row, so it can't be ErrNotFound.
	return s.exec(ctx, `UPDATE local_identity SET email = $2 WHERE id = $1 AND deleted_at IS NULL`, id, identity.NormaliseEmail(email))
}

func (s *Store) LinkGoogle(ctx context.Context, id ids.UUID, subject string) error {
	if subject == "" {
		return identity.ErrNotFound
	}
	return s.exec(ctx, `UPDATE local_identity SET google_subject = $2 WHERE id = $1 AND deleted_at IS NULL`, id, subject)
}

func (s *Store) CreateToken(ctx context.Context, t identity.Token) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO identity_token (token_hash, identity_id, purpose, email, expires_at, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			t.Hash[:], t.IdentityID, string(t.Purpose), identity.NormaliseEmail(t.Email), t.ExpiresAt, t.CreatedAt)
		return err
	}))
}

func (s *Store) Token(ctx context.Context, hash [32]byte, purpose identity.Purpose) (identity.Token, error) {
	t := identity.Token{Hash: hash}
	var p string
	err := db.Q(ctx, s.pool).QueryRow(ctx, `SELECT identity_id, purpose, email, created_at, expires_at FROM identity_token
		WHERE token_hash = $1 AND purpose = $2 AND expires_at > now()`, hash[:], string(purpose)).
		Scan(&t.IdentityID, &p, &t.Email, &t.CreatedAt, &t.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Token{}, identity.ErrNotFound
	}
	t.Purpose = identity.Purpose(p)
	return t, err
}

func (s *Store) DeleteToken(ctx context.Context, hash [32]byte) error {
	return db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM identity_token WHERE token_hash = $1`, hash[:])
		return err
	})
}
