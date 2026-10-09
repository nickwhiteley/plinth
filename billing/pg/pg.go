// Package pg is the Postgres billing.Store, on plinth's billing_* tables.
package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/ids"
)

// Store is safe for concurrent use.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ billing.Store = (*Store)(nil)

func mapErr(err error) error {
	switch db.Constraint(err) {
	case "":
		return err
	case "billing_price_tier_fk", "billing_checkout_tier_fk", "billing_subscription_tier_fk", "billing_customer_account_fk",
		"billing_checkout_account_fk", "billing_subscription_account_fk":
		return billing.ErrNotFound
	case "billing_price_plan_ck", "billing_price_amount_ck", "billing_price_currency_ck", "billing_price_interval_ck", "billing_price_provider_ck":
		return billing.ErrPriceInvalid
	}
	return err
}

func (s *Store) run(ctx context.Context, sql string, args ...any) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}))
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return billing.ErrNotFound
	}
	return err
}

func (s *Store) Prices(ctx context.Context) ([]billing.Price, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT p.tier_id, p.interval, p.provider, p.plan_ref, p.amount_minor, p.currency, p.is_enabled, p.updated_at
		FROM billing_price p JOIN tier t ON t.id = p.tier_id ORDER BY t.sort_order, p.interval, p.provider`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (p billing.Price, err error) {
		var iv string
		err = r.Scan(&p.TierID, &iv, &p.Provider, &p.PlanRef, &p.AmountMinor, &p.Currency, &p.Enabled, &p.UpdatedAt)
		p.Interval = billing.Interval(iv)
		return
	})
}

func (s *Store) SetPrice(ctx context.Context, p billing.Price) error {
	return s.run(ctx, `INSERT INTO billing_price (tier_id, interval, provider, plan_ref, amount_minor, currency, is_enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tier_id, interval, provider) DO UPDATE SET plan_ref = EXCLUDED.plan_ref, amount_minor = EXCLUDED.amount_minor,
			currency = EXCLUDED.currency, is_enabled = EXCLUDED.is_enabled`,
		p.TierID, string(p.Interval), p.Provider, p.PlanRef, p.AmountMinor, p.Currency, p.Enabled)
}

func (s *Store) Customer(ctx context.Context, account ids.UUID, provider string) (string, bool, error) {
	var ref string
	err := db.Q(ctx, s.pool).QueryRow(ctx, `SELECT customer_ref FROM billing_customer WHERE account_id = $1 AND provider = $2`, account, provider).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return ref, err == nil, err
}

func (s *Store) PutCustomer(ctx context.Context, account ids.UUID, provider, ref string) error {
	return s.run(ctx, `INSERT INTO billing_customer (account_id, provider, customer_ref) VALUES ($1, $2, $3)
		ON CONFLICT (account_id, provider) DO UPDATE SET customer_ref = EXCLUDED.customer_ref`, account, provider, ref)
}

const checkoutCols = `id, account_id, provider, tier_id, interval, amount_minor, currency, coalesce(subscription_ref, ''), coalesce(checkout_url, ''), state, expires_at, created_at`

func scanCheckout(r pgx.Row) (c billing.Checkout, err error) {
	var iv, st string
	err = r.Scan(&c.ID, &c.AccountID, &c.Provider, &c.TierID, &iv, &c.AmountMinor, &c.Currency, &c.SubscriptionRef, &c.URL, &st, &c.ExpiresAt, &c.CreatedAt)
	c.Interval, c.State = billing.Interval(iv), billing.CheckoutState(st)
	return
}

func (s *Store) CreateCheckout(ctx context.Context, c billing.Checkout) error {
	return s.run(ctx, `INSERT INTO billing_checkout (id, account_id, provider, tier_id, interval, amount_minor, currency, subscription_ref, checkout_url, state, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), nullif($9, ''), $10, $11)`,
		c.ID, c.AccountID, c.Provider, c.TierID, string(c.Interval), c.AmountMinor, c.Currency, c.SubscriptionRef, c.URL, string(c.State), c.ExpiresAt)
}

func (s *Store) Checkout(ctx context.Context, id ids.UUID) (billing.Checkout, error) {
	c, err := scanCheckout(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+checkoutCols+` FROM billing_checkout WHERE id = $1`, id))
	return c, notFound(err)
}

func (s *Store) CheckoutByRef(ctx context.Context, provider, ref string) (billing.Checkout, error) {
	c, err := scanCheckout(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+checkoutCols+` FROM billing_checkout WHERE provider = $1 AND subscription_ref = $2`, provider, ref))
	return c, notFound(err)
}

func (s *Store) PendingCheckouts(ctx context.Context) ([]billing.Checkout, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT `+checkoutCols+` FROM billing_checkout WHERE state = 'pending' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (billing.Checkout, error) { return scanCheckout(r) })
}

func (s *Store) SetCheckoutState(ctx context.Context, id ids.UUID, state billing.CheckoutState) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE billing_checkout SET state = $2 WHERE id = $1`, id, string(state))
		if err == nil && tag.RowsAffected() == 0 {
			return billing.ErrNotFound
		}
		return err
	}))
}

const subCols = `id, account_id, provider, subscription_ref, tier_id, interval, state, current_period_end, amount_minor, currency,
	cancel_requested_at, last_reconciled_at, started_at, ended_at`

func scanSub(r pgx.Row) (x billing.Subscription, err error) {
	var iv, st string
	err = r.Scan(&x.ID, &x.AccountID, &x.Provider, &x.Ref, &x.TierID, &iv, &st, &x.CurrentPeriodEnd, &x.AmountMinor, &x.Currency,
		&x.CancelRequestedAt, &x.LastReconciledAt, &x.StartedAt, &x.EndedAt)
	x.Interval, x.State = billing.Interval(iv), billing.ProviderState(st)
	return
}

func (s *Store) Subscription(ctx context.Context, account ids.UUID) (billing.Subscription, error) {
	x, err := scanSub(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+subCols+` FROM billing_subscription WHERE account_id = $1 AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`, account))
	return x, notFound(err)
}

func (s *Store) SubscriptionByRef(ctx context.Context, provider, ref string) (billing.Subscription, error) {
	x, err := scanSub(db.Q(ctx, s.pool).QueryRow(ctx, `SELECT `+subCols+` FROM billing_subscription WHERE provider = $1 AND subscription_ref = $2`, provider, ref))
	return x, notFound(err)
}

func (s *Store) PutSubscription(ctx context.Context, x billing.Subscription) error {
	return s.run(ctx, `INSERT INTO billing_subscription (id, account_id, provider, subscription_ref, tier_id, interval, state, current_period_end, amount_minor, currency,
			cancel_requested_at, last_reconciled_at, started_at, ended_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (provider, subscription_ref) DO UPDATE SET tier_id = EXCLUDED.tier_id, interval = EXCLUDED.interval, state = EXCLUDED.state,
			current_period_end = EXCLUDED.current_period_end, amount_minor = EXCLUDED.amount_minor, currency = EXCLUDED.currency,
			cancel_requested_at = EXCLUDED.cancel_requested_at, last_reconciled_at = EXCLUDED.last_reconciled_at, ended_at = EXCLUDED.ended_at`,
		x.ID, x.AccountID, x.Provider, x.Ref, x.TierID, string(x.Interval), string(x.State), x.CurrentPeriodEnd, x.AmountMinor, x.Currency,
		x.CancelRequestedAt, x.LastReconciledAt, x.StartedAt, x.EndedAt)
}

func (s *Store) Subscriptions(ctx context.Context) ([]billing.Subscription, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT `+subCols+` FROM billing_subscription WHERE ended_at IS NULL ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (billing.Subscription, error) { return scanSub(r) })
}
