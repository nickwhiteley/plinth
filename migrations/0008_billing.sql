-- Billing (spec.md §2): prices per tier and interval, provider customers, checkouts and
-- subscriptions. The provider is the authority on a subscription's state; these rows are our copy
-- of it, reconciled, and our own intent (a requested cancellation).

CREATE TABLE billing_price (
  tier_id       uuid NOT NULL CONSTRAINT billing_price_tier_fk REFERENCES tier (id),
  interval      text NOT NULL CONSTRAINT billing_price_interval_ck CHECK (interval IN ('month', 'year')),
  provider      text NOT NULL CONSTRAINT billing_price_provider_ck CHECK (provider ~ '^[a-z][a-z0-9_-]{1,40}$'),
  plan_ref      text NOT NULL CONSTRAINT billing_price_plan_ck CHECK (length(plan_ref) BETWEEN 1 AND 200),
  amount_minor  bigint NOT NULL CONSTRAINT billing_price_amount_ck CHECK (amount_minor BETWEEN 0 AND 100000000),
  currency      text NOT NULL CONSTRAINT billing_price_currency_ck CHECK (currency ~ '^[A-Z]{3}$'),
  is_enabled    boolean NOT NULL DEFAULT true,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid CONSTRAINT billing_price_updated_by_fk REFERENCES account (id) DEFAULT nullif(current_setting('app.modified_by', true), '')::uuid,
  CONSTRAINT billing_price_pk PRIMARY KEY (tier_id, interval, provider)
);
COMMENT ON TABLE billing_price IS 'What a tier costs at an interval, at a provider. Amount and currency are the provider''s plan''s.';
COMMENT ON COLUMN billing_price.tier_id IS 'The tier.';
COMMENT ON COLUMN billing_price.interval IS 'month or year.';
COMMENT ON COLUMN billing_price.provider IS 'The payment provider.';
COMMENT ON COLUMN billing_price.plan_ref IS 'The provider''s reference for the plan.';
COMMENT ON COLUMN billing_price.amount_minor IS 'The charge in the currency''s minor units.';
COMMENT ON COLUMN billing_price.currency IS 'The currency, ISO 4217.';
COMMENT ON COLUMN billing_price.is_enabled IS 'Whether the price is offered. A withdrawn price doesn''t downgrade those already on it.';
COMMENT ON COLUMN billing_price.updated_at IS 'When it last changed.';
COMMENT ON COLUMN billing_price.updated_by IS 'Who changed it.';
CALL shadow('billing_price');

CREATE TABLE billing_customer (
  account_id    uuid NOT NULL CONSTRAINT billing_customer_account_fk REFERENCES account (id),
  provider      text NOT NULL CONSTRAINT billing_customer_provider_ck CHECK (provider ~ '^[a-z][a-z0-9_-]{1,40}$'),
  customer_ref  text NOT NULL CONSTRAINT billing_customer_ref_ck CHECK (length(customer_ref) BETWEEN 1 AND 200),
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT billing_customer_pk PRIMARY KEY (account_id, provider)
);
COMMENT ON TABLE billing_customer IS 'An account''s record at a payment provider.';
COMMENT ON COLUMN billing_customer.account_id IS 'The account.';
COMMENT ON COLUMN billing_customer.provider IS 'The payment provider.';
COMMENT ON COLUMN billing_customer.customer_ref IS 'The provider''s reference for the customer.';
COMMENT ON COLUMN billing_customer.created_at IS 'When it was made.';
CALL shadow('billing_customer');

CREATE TABLE billing_checkout (
  id                uuid CONSTRAINT billing_checkout_pk PRIMARY KEY DEFAULT uuidv7() CONSTRAINT billing_checkout_id_v7_ck CHECK (uuid_extract_version(id) IS NOT DISTINCT FROM 7),
  account_id        uuid NOT NULL CONSTRAINT billing_checkout_account_fk REFERENCES account (id),
  provider          text NOT NULL CONSTRAINT billing_checkout_provider_ck CHECK (provider ~ '^[a-z][a-z0-9_-]{1,40}$'),
  tier_id           uuid NOT NULL CONSTRAINT billing_checkout_tier_fk REFERENCES tier (id),
  interval          text NOT NULL CONSTRAINT billing_checkout_interval_ck CHECK (interval IN ('month', 'year')),
  amount_minor      bigint NOT NULL CONSTRAINT billing_checkout_amount_ck CHECK (amount_minor BETWEEN 0 AND 100000000),
  currency          text NOT NULL CONSTRAINT billing_checkout_currency_ck CHECK (currency ~ '^[A-Z]{3}$'),
  subscription_ref  text CONSTRAINT billing_checkout_ref_ck CHECK (length(subscription_ref) BETWEEN 1 AND 200),
  checkout_url      text CONSTRAINT billing_checkout_url_ck CHECK (length(checkout_url) BETWEEN 1 AND 2000),
  state             text NOT NULL CONSTRAINT billing_checkout_state_ck CHECK (state IN ('pending', 'activated', 'abandoned', 'expired', 'failed')),
  expires_at        timestamptz NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX billing_checkout_ref_uq ON billing_checkout (provider, subscription_ref) WHERE subscription_ref IS NOT NULL;
CREATE INDEX billing_checkout_pending_ix ON billing_checkout (created_at) WHERE state = 'pending';
CREATE INDEX billing_checkout_account_ix ON billing_checkout (account_id);
COMMENT ON TABLE billing_checkout IS 'An attempt to subscribe. The amount is copied when it starts.';
COMMENT ON COLUMN billing_checkout.id IS 'The checkout.';
COMMENT ON COLUMN billing_checkout.account_id IS 'Who is paying.';
COMMENT ON COLUMN billing_checkout.provider IS 'The payment provider.';
COMMENT ON COLUMN billing_checkout.tier_id IS 'The tier being bought.';
COMMENT ON COLUMN billing_checkout.interval IS 'month or year.';
COMMENT ON COLUMN billing_checkout.amount_minor IS 'The price when the attempt started, in minor units.';
COMMENT ON COLUMN billing_checkout.currency IS 'The currency, ISO 4217.';
COMMENT ON COLUMN billing_checkout.subscription_ref IS 'The provider''s pending subscription, or null if the provider never answered.';
COMMENT ON COLUMN billing_checkout.checkout_url IS 'The provider''s hosted page, where the card is entered.';
COMMENT ON COLUMN billing_checkout.state IS 'pending, activated, abandoned, expired or failed.';
COMMENT ON COLUMN billing_checkout.expires_at IS 'When an unfinished attempt is given up.';
COMMENT ON COLUMN billing_checkout.created_at IS 'When it started.';
COMMENT ON COLUMN billing_checkout.updated_at IS 'When it last changed.';
CALL shadow('billing_checkout');

CREATE TABLE billing_subscription (
  id                   uuid CONSTRAINT billing_subscription_pk PRIMARY KEY DEFAULT uuidv7() CONSTRAINT billing_subscription_id_v7_ck CHECK (uuid_extract_version(id) IS NOT DISTINCT FROM 7),
  account_id           uuid NOT NULL CONSTRAINT billing_subscription_account_fk REFERENCES account (id),
  provider             text NOT NULL CONSTRAINT billing_subscription_provider_ck CHECK (provider ~ '^[a-z][a-z0-9_-]{1,40}$'),
  subscription_ref     text NOT NULL CONSTRAINT billing_subscription_ref_ck CHECK (length(subscription_ref) BETWEEN 1 AND 200),
  tier_id              uuid NOT NULL CONSTRAINT billing_subscription_tier_fk REFERENCES tier (id),
  interval             text NOT NULL CONSTRAINT billing_subscription_interval_ck CHECK (interval IN ('month', 'year')),
  state                text NOT NULL CONSTRAINT billing_subscription_state_ck CHECK (state IN ('pending', 'active', 'past_due', 'canceled', 'expired')),
  current_period_end   timestamptz NOT NULL,
  amount_minor         bigint NOT NULL CONSTRAINT billing_subscription_amount_ck CHECK (amount_minor BETWEEN 0 AND 100000000),
  currency             text NOT NULL CONSTRAINT billing_subscription_currency_ck CHECK (currency ~ '^[A-Z]{3}$'),
  cancel_requested_at  timestamptz,
  last_reconciled_at   timestamptz NOT NULL,
  started_at           timestamptz NOT NULL,
  ended_at             timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT billing_subscription_ref_uq UNIQUE (provider, subscription_ref)
);
CREATE INDEX billing_subscription_account_ix ON billing_subscription (account_id) WHERE ended_at IS NULL;
COMMENT ON TABLE billing_subscription IS 'An account''s subscription: the provider''s state as last reconciled, and our intent to cancel.';
COMMENT ON COLUMN billing_subscription.id IS 'The subscription.';
COMMENT ON COLUMN billing_subscription.account_id IS 'Whose it is.';
COMMENT ON COLUMN billing_subscription.provider IS 'The payment provider.';
COMMENT ON COLUMN billing_subscription.subscription_ref IS 'The provider''s reference.';
COMMENT ON COLUMN billing_subscription.tier_id IS 'The tier it pays for.';
COMMENT ON COLUMN billing_subscription.interval IS 'month or year.';
COMMENT ON COLUMN billing_subscription.state IS 'The provider''s state in our vocabulary: pending, active, past_due, canceled or expired.';
COMMENT ON COLUMN billing_subscription.current_period_end IS 'When the paid period ends.';
COMMENT ON COLUMN billing_subscription.amount_minor IS 'What each period costs, in minor units.';
COMMENT ON COLUMN billing_subscription.currency IS 'The currency, ISO 4217.';
COMMENT ON COLUMN billing_subscription.cancel_requested_at IS 'When the customer asked to cancel at the period''s end.';
COMMENT ON COLUMN billing_subscription.last_reconciled_at IS 'When the provider was last asked.';
COMMENT ON COLUMN billing_subscription.started_at IS 'When it became active.';
COMMENT ON COLUMN billing_subscription.ended_at IS 'When it ended, or null.';
COMMENT ON COLUMN billing_subscription.created_at IS 'When the row was made.';
COMMENT ON COLUMN billing_subscription.updated_at IS 'When it last changed.';
CALL shadow('billing_subscription');
