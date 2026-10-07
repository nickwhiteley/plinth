-- Flags and quotas (spec.md §13), from Furniture Magic data-model §4.2. One mechanism, by tier.

CREATE TABLE feature_flag (
  key          text CONSTRAINT feature_flag_pk PRIMARY KEY CONSTRAINT feature_flag_key_ck CHECK (key ~ '^[a-z][a-z0-9_]{1,63}$'),
  is_enabled   boolean NOT NULL DEFAULT false,
  min_tier_id  uuid CONSTRAINT feature_flag_min_tier_fk REFERENCES tier (id),
  is_retired   boolean NOT NULL DEFAULT false,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid CONSTRAINT feature_flag_updated_by_fk REFERENCES account (id)
);
COMMENT ON TABLE feature_flag IS 'Feature flags: declared in code, reconciled in, switched by administrators.';
COMMENT ON COLUMN feature_flag.key IS 'The declared key.';
COMMENT ON COLUMN feature_flag.is_enabled IS 'The switch. Off is off for every tier.';
COMMENT ON COLUMN feature_flag.min_tier_id IS 'The lowest tier the flag is on for, or null for every tier.';
COMMENT ON COLUMN feature_flag.is_retired IS 'Stored but no longer declared by the source. Resolves off; never deleted.';
COMMENT ON COLUMN feature_flag.updated_at IS 'When the flag last changed.';
COMMENT ON COLUMN feature_flag.updated_by IS 'Who last changed it.';
CALL shadow('feature_flag');

CREATE TABLE account_flag (
  account_id  uuid NOT NULL CONSTRAINT account_flag_account_fk REFERENCES account (id),
  flag_key    text NOT NULL CONSTRAINT account_flag_flag_fk REFERENCES feature_flag (key),
  is_enabled  boolean NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  created_by  uuid CONSTRAINT account_flag_created_by_fk REFERENCES account (id) DEFAULT nullif(current_setting('app.modified_by', true), '')::uuid,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid CONSTRAINT account_flag_updated_by_fk REFERENCES account (id),
  CONSTRAINT account_flag_pk PRIMARY KEY (account_id, flag_key)
);
COMMENT ON TABLE account_flag IS 'Per-account flag overrides, set by support. They win over the flag''s state in either direction.';
COMMENT ON COLUMN account_flag.account_id IS 'The account.';
COMMENT ON COLUMN account_flag.flag_key IS 'The flag.';
COMMENT ON COLUMN account_flag.is_enabled IS 'On or off for this account, whatever its tier.';
COMMENT ON COLUMN account_flag.created_at IS 'When the override was set.';
COMMENT ON COLUMN account_flag.created_by IS 'Who set it.';
COMMENT ON COLUMN account_flag.updated_at IS 'When it last changed.';
COMMENT ON COLUMN account_flag.updated_by IS 'Who last changed it.';
CALL shadow('account_flag');

CREATE TABLE quota_key (
  key         text CONSTRAINT quota_key_pk PRIMARY KEY CONSTRAINT quota_key_key_ck CHECK (key ~ '^[a-z][a-z0-9_]{1,63}$'),
  unit        text NOT NULL CONSTRAINT quota_key_unit_ck CHECK (unit IN ('count', 'tokens')),
  is_retired  boolean NOT NULL DEFAULT false
);
COMMENT ON TABLE quota_key IS 'Declared quotas, reconciled in from code.';
COMMENT ON COLUMN quota_key.key IS 'The declared key.';
COMMENT ON COLUMN quota_key.unit IS 'What the quota counts: count or tokens.';
COMMENT ON COLUMN quota_key.is_retired IS 'Stored but no longer declared by the source.';
CALL shadow('quota_key');

CREATE TABLE tier_quota (
  tier_id      uuid NOT NULL CONSTRAINT tier_quota_tier_fk REFERENCES tier (id),
  quota_key    text NOT NULL CONSTRAINT tier_quota_quota_fk REFERENCES quota_key (key),
  limit_value  bigint CONSTRAINT tier_quota_limit_ck CHECK (limit_value BETWEEN 0 AND 1000000000000000),
  CONSTRAINT tier_quota_pk PRIMARY KEY (tier_id, quota_key)
);
COMMENT ON TABLE tier_quota IS 'A tier''s limit for a quota. A tier without a row inherits from the nearest tier below with one.';
COMMENT ON COLUMN tier_quota.tier_id IS 'The tier.';
COMMENT ON COLUMN tier_quota.quota_key IS 'The quota.';
COMMENT ON COLUMN tier_quota.limit_value IS 'The limit, in the quota''s unit; null is an explicit unlimited, which beats an inherited number.';
CALL shadow('tier_quota');

CREATE TABLE account_quota (
  account_id   uuid NOT NULL CONSTRAINT account_quota_account_fk REFERENCES account (id),
  quota_key    text NOT NULL CONSTRAINT account_quota_quota_fk REFERENCES quota_key (key),
  limit_value  bigint CONSTRAINT account_quota_limit_ck CHECK (limit_value BETWEEN 0 AND 1000000000000000),
  reason       text NOT NULL CONSTRAINT account_quota_reason_ck CHECK (length(btrim(reason)) BETWEEN 1 AND 500),
  expires_at   timestamptz,
  granted_by   uuid NOT NULL CONSTRAINT account_quota_granted_by_fk REFERENCES account (id),
  granted_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT account_quota_pk PRIMARY KEY (account_id, quota_key),
  CONSTRAINT account_quota_expiry_ck CHECK (expires_at IS NULL OR expires_at > granted_at)
);
COMMENT ON TABLE account_quota IS 'Per-account quota overrides, set by support with a reason. They win over the tier while live.';
COMMENT ON COLUMN account_quota.account_id IS 'The account.';
COMMENT ON COLUMN account_quota.quota_key IS 'The quota.';
COMMENT ON COLUMN account_quota.limit_value IS 'The limit for this account; null is unlimited.';
COMMENT ON COLUMN account_quota.reason IS 'Why support changed it.';
COMMENT ON COLUMN account_quota.expires_at IS 'When the override stops applying, or null until removed.';
COMMENT ON COLUMN account_quota.granted_by IS 'Who granted it.';
COMMENT ON COLUMN account_quota.granted_at IS 'When it was granted.';
CALL shadow('account_quota');
