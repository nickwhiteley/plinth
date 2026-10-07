-- Identity and accounts (spec.md §6, §12), from Furniture Magic data-model §4, with the account
-- kept to what every product has. Run as the owner role, with search_path set to the app schema.

CREATE TABLE locale (
  code       text CONSTRAINT locale_pk PRIMARY KEY CONSTRAINT locale_code_ck CHECK (code ~ '^[a-z]{2,3}(-[A-Z]{2}|-[A-Z]{2,3})?$'),
  is_shipped boolean NOT NULL DEFAULT false
);
COMMENT ON TABLE locale IS 'The locales a product ships, and the target of every profile''s locale.';
COMMENT ON COLUMN locale.code IS 'BCP 47 tag, e.g. en-GB.';
COMMENT ON COLUMN locale.is_shipped IS 'Offered to people. A pseudo-locale exists in development and test only.';
INSERT INTO locale (code, is_shipped) VALUES ('en-GB', true);
CALL shadow('locale');

CREATE TABLE tier (
  id          uuid CONSTRAINT tier_pk PRIMARY KEY DEFAULT uuidv7() CONSTRAINT tier_id_v7_ck CHECK (uuid_extract_version(id) IS NOT DISTINCT FROM 7),
  key         text NOT NULL CONSTRAINT tier_key_ck CHECK (key ~ '^[a-z][a-z0-9_]{1,63}$'),
  name        text NOT NULL CONSTRAINT tier_name_ck CHECK (length(name) BETWEEN 1 AND 120),
  sort_order  integer NOT NULL CONSTRAINT tier_sort_order_ck CHECK (sort_order BETWEEN 0 AND 1000000),
  is_enabled  boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT tier_key_uq UNIQUE (key),
  CONSTRAINT tier_sort_order_uq UNIQUE (sort_order) DEFERRABLE INITIALLY DEFERRED
);
COMMENT ON TABLE tier IS 'Usage tiers, in order. Configuration: nothing in the source names one.';
COMMENT ON COLUMN tier.id IS 'The tier (UUIDv7; tie_ at the API).';
COMMENT ON COLUMN tier.key IS 'A stable machine name.';
COMMENT ON COLUMN tier.name IS 'The display name.';
COMMENT ON COLUMN tier.sort_order IS 'Position on the ladder, lowest first. Flags and quota inheritance follow it.';
COMMENT ON COLUMN tier.is_enabled IS 'Whether the tier may be chosen. A disabled tier keeps its accounts and still passes its limits up.';
COMMENT ON COLUMN tier.created_at IS 'When the tier was created.';
COMMENT ON COLUMN tier.updated_at IS 'When the tier last changed.';
CALL shadow('tier');

CREATE TABLE local_identity (
  id                uuid CONSTRAINT local_identity_pk PRIMARY KEY CONSTRAINT local_identity_id_v7_ck CHECK (uuid_extract_version(id) IS NOT DISTINCT FROM 7),
  email             text NOT NULL CONSTRAINT local_identity_email_ck CHECK (email = lower(btrim(email)) AND length(email) BETWEEN 3 AND 320),
  email_verified_at timestamptz,
  password_hash     text CONSTRAINT local_identity_password_ck CHECK (password_hash LIKE '$argon2id$%' AND length(password_hash) <= 500),
  google_subject    text CONSTRAINT local_identity_google_ck CHECK (length(google_subject) BETWEEN 1 AND 255),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz
);
CREATE UNIQUE INDEX local_identity_email_uq ON local_identity (email) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX local_identity_google_uq ON local_identity (google_subject) WHERE google_subject IS NOT NULL AND deleted_at IS NULL;
COMMENT ON TABLE local_identity IS 'The local identity provider (issuer local): an email address and the credentials that prove it.';
COMMENT ON COLUMN local_identity.id IS 'The identity (UUIDv7; idn_ at the API, and the account''s identity_subject).';
COMMENT ON COLUMN local_identity.email IS 'The address, trimmed and lower-cased. Unique among live identities.';
COMMENT ON COLUMN local_identity.email_verified_at IS 'When the current address was proved, or null.';
COMMENT ON COLUMN local_identity.password_hash IS 'Argon2id PHC string, or null for an identity created through Google. Secret: never logged or exported.';
COMMENT ON COLUMN local_identity.google_subject IS 'Google''s stable sub for a linked account. Secret: never logged or exported.';
COMMENT ON COLUMN local_identity.created_at IS 'When the identity was created.';
COMMENT ON COLUMN local_identity.updated_at IS 'When the identity last changed.';
COMMENT ON COLUMN local_identity.deleted_at IS 'When the identity was deleted (soft delete).';
CALL exclude_from_log('local_identity', 'password_hash', 'a secret');
CALL exclude_from_log('local_identity', 'google_subject', 'a secret');
CALL shadow('local_identity');

-- Ephemeral: hard-deleted and not logged.
CREATE TABLE identity_token (
  token_hash   bytea CONSTRAINT identity_token_pk PRIMARY KEY CONSTRAINT identity_token_hash_ck CHECK (length(token_hash) = 32),
  identity_id  uuid NOT NULL CONSTRAINT identity_token_identity_fk REFERENCES local_identity (id),
  purpose      text NOT NULL CONSTRAINT identity_token_purpose_ck CHECK (purpose IN ('reset', 'verify')),
  email        text NOT NULL CONSTRAINT identity_token_email_ck CHECK (email = lower(btrim(email)) AND length(email) BETWEEN 3 AND 320),
  expires_at   timestamptz NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT identity_token_expiry_ck CHECK (expires_at > created_at AND expires_at <= created_at + interval '7 days')
);
CREATE INDEX identity_token_identity_ix ON identity_token (identity_id);
CREATE INDEX identity_token_expiry_ix ON identity_token (expires_at);
COMMENT ON TABLE identity_token IS 'Reset and verification links: single use, hashed, expiring. Ephemeral, not logged.';
COMMENT ON COLUMN identity_token.token_hash IS 'SHA-256 of the token (bound to the address for verification). Secret.';
COMMENT ON COLUMN identity_token.identity_id IS 'The identity the link is for.';
COMMENT ON COLUMN identity_token.purpose IS 'reset or verify; a token is found only under its own purpose.';
COMMENT ON COLUMN identity_token.email IS 'The address the link was sent to.';
COMMENT ON COLUMN identity_token.expires_at IS 'When the link stops working.';
COMMENT ON COLUMN identity_token.created_at IS 'When the link was issued.';

CREATE TABLE account (
  id                uuid CONSTRAINT account_pk PRIMARY KEY CONSTRAINT account_id_v7_ck CHECK (uuid_extract_version(id) IS NOT DISTINCT FROM 7),
  identity_issuer   text NOT NULL CONSTRAINT account_issuer_ck CHECK (identity_issuer ~ '^[a-z][a-z0-9_]{1,31}$'),
  identity_subject  text NOT NULL CONSTRAINT account_subject_ck CHECK (length(identity_subject) BETWEEN 1 AND 255),
  tier_id           uuid NOT NULL CONSTRAINT account_tier_fk REFERENCES tier (id),
  tier_override_id  uuid CONSTRAINT account_tier_override_fk REFERENCES tier (id),
  is_active         boolean NOT NULL DEFAULT true,
  deactivated_at    timestamptz,
  quota_time_zone   text NOT NULL CONSTRAINT account_quota_tz_ck CHECK (valid_time_zone(quota_time_zone)),
  is_tombstone      boolean NOT NULL DEFAULT false,
  last_signed_in_at timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid CONSTRAINT account_created_by_fk REFERENCES account (id) DEFAULT nullif(current_setting('app.modified_by', true), '')::uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid CONSTRAINT account_updated_by_fk REFERENCES account (id),
  deleted_at        timestamptz,
  deleted_by        uuid CONSTRAINT account_deleted_by_fk REFERENCES account (id),
  CONSTRAINT account_active_ck CHECK (is_active = (deactivated_at IS NULL)),
  CONSTRAINT account_tombstone_ck CHECK (NOT is_tombstone OR (deleted_at IS NOT NULL AND NOT is_active)),
  CONSTRAINT account_deleted_ck CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL)
);
CREATE UNIQUE INDEX account_identity_uq ON account (identity_issuer, identity_subject) WHERE deleted_at IS NULL;
CREATE INDEX account_tier_ix ON account (tier_id);
COMMENT ON TABLE account IS 'Who uses the products, as distinct from how they prove it. Products extend it one-to-one, never alter it.';
COMMENT ON COLUMN account.id IS 'The account (UUIDv7; acc_ at the API).';
COMMENT ON COLUMN account.identity_issuer IS 'The identity provider: local, or another.';
COMMENT ON COLUMN account.identity_subject IS 'The provider''s identifier for the person; for local, the idn_ id.';
COMMENT ON COLUMN account.tier_id IS 'The account''s tier.';
COMMENT ON COLUMN account.tier_override_id IS 'A tier support has put the account on instead, or null. The effective tier is this if set.';
COMMENT ON COLUMN account.is_active IS 'False for a deactivated account, which can''t sign in.';
COMMENT ON COLUMN account.deactivated_at IS 'When the account was deactivated, or null.';
COMMENT ON COLUMN account.quota_time_zone IS 'Where the quota day runs midnight to midnight. Fixed at creation; only support changes it.';
COMMENT ON COLUMN account.is_tombstone IS 'Erased: the row is kept for foreign keys, its personal data scrubbed.';
COMMENT ON COLUMN account.last_signed_in_at IS 'The last sign-in, or null.';
COMMENT ON COLUMN account.created_at IS 'When the account was created.';
COMMENT ON COLUMN account.created_by IS 'Who created it, or null for sign-up.';
COMMENT ON COLUMN account.updated_at IS 'When the account last changed.';
COMMENT ON COLUMN account.updated_by IS 'Who last changed it, or null.';
COMMENT ON COLUMN account.deleted_at IS 'When the account was deleted (soft delete).';
COMMENT ON COLUMN account.deleted_by IS 'Who deleted it.';
CALL shadow('account');

CREATE TABLE account_profile (
  account_id    uuid CONSTRAINT account_profile_pk PRIMARY KEY CONSTRAINT account_profile_account_fk REFERENCES account (id),
  display_name  text NOT NULL CONSTRAINT account_profile_name_ck CHECK (length(display_name) BETWEEN 1 AND 120),
  time_zone     text NOT NULL DEFAULT 'Europe/London' CONSTRAINT account_profile_tz_ck CHECK (valid_time_zone(time_zone)),
  locale        text NOT NULL DEFAULT 'en-GB' CONSTRAINT account_profile_locale_fk REFERENCES locale (code),
  email         text NOT NULL CONSTRAINT account_profile_email_ck CHECK (email = lower(btrim(email)) AND length(email) BETWEEN 3 AND 320),
  refreshed_at  timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE account_profile IS 'A cache of identity-owned facts, plus the person''s time zone and locale. Scrubbed on erasure.';
COMMENT ON COLUMN account_profile.account_id IS 'The account.';
COMMENT ON COLUMN account_profile.display_name IS 'The name shown to others.';
COMMENT ON COLUMN account_profile.time_zone IS 'The person''s time zone, for display. Not the quota day''s.';
COMMENT ON COLUMN account_profile.locale IS 'The person''s locale, for messages and email.';
COMMENT ON COLUMN account_profile.email IS 'The identity''s address, cached at sign-in.';
COMMENT ON COLUMN account_profile.refreshed_at IS 'When the cache was last refreshed from the identity.';
COMMENT ON COLUMN account_profile.updated_at IS 'When the profile last changed.';
CALL shadow('account_profile');

-- Ephemeral: hard-deleted, not logged, and unlogged at the storage level. A crash signs everyone out.
CREATE UNLOGGED TABLE session (
  token_hash    bytea CONSTRAINT session_pk PRIMARY KEY CONSTRAINT session_hash_ck CHECK (length(token_hash) = 32),
  account_id    uuid NOT NULL CONSTRAINT session_account_fk REFERENCES account (id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  CONSTRAINT session_expiry_ck CHECK (expires_at > created_at AND expires_at <= last_seen_at + interval '31 days')
) WITH (fillfactor = 70);
CREATE INDEX session_account_ix ON session (account_id);
CREATE INDEX session_expiry_ix ON session (expires_at);
COMMENT ON TABLE session IS 'Signed-in sessions. Ephemeral, not logged.';
COMMENT ON COLUMN session.token_hash IS 'SHA-256 of the session token. Secret.';
COMMENT ON COLUMN session.account_id IS 'The signed-in account.';
COMMENT ON COLUMN session.created_at IS 'When the session was issued.';
COMMENT ON COLUMN session.last_seen_at IS 'The last request, rewritten at most once a minute.';
COMMENT ON COLUMN session.expires_at IS 'When the session ends; it slides with use.';
