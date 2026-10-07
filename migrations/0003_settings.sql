-- Settings (spec.md §8): declared in code, reconciled in, every value encrypted.

CREATE TABLE app_setting (
  setting_key  text CONSTRAINT app_setting_pk PRIMARY KEY CONSTRAINT app_setting_key_ck CHECK (setting_key ~ '^[a-z][a-z0-9_]{1,63}$'),
  value_enc    bytea NOT NULL CONSTRAINT app_setting_value_ck CHECK (length(value_enc) BETWEEN 1 AND 65536),
  key_version  smallint NOT NULL DEFAULT 1 CONSTRAINT app_setting_key_version_ck CHECK (key_version BETWEEN 1 AND 1000),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid CONSTRAINT app_setting_created_by_fk REFERENCES account (id) DEFAULT nullif(current_setting('app.modified_by', true), '')::uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid CONSTRAINT app_setting_updated_by_fk REFERENCES account (id) DEFAULT nullif(current_setting('app.modified_by', true), '')::uuid
);
COMMENT ON TABLE app_setting IS 'The deployment''s configuration: declared keys, encrypted values. Platform, never per account.';
COMMENT ON COLUMN app_setting.setting_key IS 'The declared key.';
COMMENT ON COLUMN app_setting.value_enc IS 'Nonce and AES-256-GCM ciphertext under the encryption key, base64. Opaque to SQL by design.';
COMMENT ON COLUMN app_setting.key_version IS 'Which encryption key sealed the value.';
COMMENT ON COLUMN app_setting.created_at IS 'When the key was reconciled in.';
COMMENT ON COLUMN app_setting.created_by IS 'Who created it, or null for a reconciling insert.';
COMMENT ON COLUMN app_setting.updated_at IS 'When the value last changed.';
COMMENT ON COLUMN app_setting.updated_by IS 'Who last changed it, or null for a system write.';
CALL shadow('app_setting');
