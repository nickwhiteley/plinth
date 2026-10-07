// Package pg is the Postgres email.Store, on plinth's communication_log table.
package pg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/email"
	"github.com/nickwhiteley/plinth/ids"
)

// Store is safe for concurrent use.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ email.Store = (*Store)(nil)

func null(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Store) Record(ctx context.Context, c email.Communication) error {
	return db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO communication_log (id, kind, category, account_id, recipient, subject, body, is_body_withheld,
			related_type, related_id, status, provider, provider_message_id, error, created_at, sent_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			c.ID, string(c.Kind), string(c.Category), c.AccountID, null(c.Recipient), c.Subject, null(c.Body), c.BodyWithheld,
			null(c.RelatedType), null(c.RelatedID), string(c.Status), c.Provider, null(c.ProviderMessageID), null(c.Error), c.CreatedAt, c.SentAt)
		return err
	})
}

func (s *Store) Finish(ctx context.Context, id ids.UUID, o email.Outcome) error {
	var sent *time.Time
	if o.Status == email.StatusSent {
		sent = &o.At
	}
	return db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE communication_log SET status = $2, provider_message_id = $3, error = $4, sent_at = $5 WHERE id = $1`,
			id, string(o.Status), null(o.ProviderMessageID), null(o.Error), sent)
		if err == nil && tag.RowsAffected() == 0 {
			return email.ErrNotFound
		}
		return err
	})
}

func (s *Store) Communications(ctx context.Context, q email.Query) ([]email.Communication, error) {
	limit := q.Limit
	if limit <= 0 || limit > email.PageSize {
		limit = email.PageSize
	}
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT id, kind, category, account_id, coalesce(recipient, ''), subject, coalesce(body, ''),
		is_body_withheld, coalesce(related_type, ''), coalesce(related_id, ''), status, provider, coalesce(provider_message_id, ''),
		coalesce(error, ''), created_at, sent_at
		FROM communication_log
		WHERE ($1 = '' OR category = $1) AND ($2 = '' OR kind = $2) AND ($3 = '' OR status = $3)
		  AND ($4::uuid IS NULL OR account_id = $4) AND ($5::uuid IS NULL OR id < $5)
		ORDER BY id DESC LIMIT $6`, string(q.Category), string(q.Kind), string(q.Status), q.AccountID, q.Before, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (email.Communication, error) {
		var c email.Communication
		var kind, cat, status string
		err := r.Scan(&c.ID, &kind, &cat, &c.AccountID, &c.Recipient, &c.Subject, &c.Body, &c.BodyWithheld, &c.RelatedType, &c.RelatedID,
			&status, &c.Provider, &c.ProviderMessageID, &c.Error, &c.CreatedAt, &c.SentAt)
		c.Kind, c.Category, c.Status = email.Kind(kind), email.Category(cat), email.Status(status)
		return c, err
	})
}

func (s *Store) Prune(ctx context.Context, cat email.Category, before time.Time) (int, error) {
	if time.Since(before) < email.MinRetention {
		return 0, email.ErrTooRecent
	}
	var n int64
	err := db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM communication_log WHERE category = $1 AND created_at < $2`, string(cat), before)
		n = tag.RowsAffected()
		return err
	})
	return int(n), err
}
