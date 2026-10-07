-- The communication log (spec.md §9): every email, recorded before it is sent. Not shadow-logged:
-- it is its own record, kept by category (six years for regulatory mail, 400 days for operational
-- and marketing, 90 for internal) and pruned when that expires.

CREATE TABLE communication_log (
  id                   uuid CONSTRAINT communication_log_pk PRIMARY KEY CONSTRAINT communication_log_id_v7_ck CHECK (uuid_extract_version(id) IS NOT DISTINCT FROM 7),
  kind                 text NOT NULL CONSTRAINT communication_log_kind_ck CHECK (kind ~ '^[a-z][a-z0-9_]{1,63}$'),
  category             text NOT NULL CONSTRAINT communication_log_category_ck CHECK (category IN ('regulatory', 'operational', 'marketing', 'internal')),
  account_id           uuid CONSTRAINT communication_log_account_fk REFERENCES account (id),
  recipient            text CONSTRAINT communication_log_recipient_ck CHECK (length(recipient) BETWEEN 3 AND 320),
  subject              text NOT NULL CONSTRAINT communication_log_subject_ck CHECK (length(subject) <= 998),
  body                 text CONSTRAINT communication_log_body_ck CHECK (length(body) <= 200000),
  is_body_withheld     boolean NOT NULL DEFAULT false,
  related_type         text CONSTRAINT communication_log_related_type_ck CHECK (related_type ~ '^[a-z][a-z0-9_]{1,63}$'),
  related_id           text CONSTRAINT communication_log_related_id_ck CHECK (length(related_id) BETWEEN 1 AND 100),
  status               text NOT NULL CONSTRAINT communication_log_status_ck CHECK (status IN ('queued', 'sent', 'failed')),
  provider             text NOT NULL CONSTRAINT communication_log_provider_ck CHECK (length(provider) BETWEEN 1 AND 40),
  provider_message_id  text CONSTRAINT communication_log_provider_id_ck CHECK (length(provider_message_id) <= 200),
  error                text CONSTRAINT communication_log_error_ck CHECK (length(error) <= 4000),
  created_at           timestamptz NOT NULL DEFAULT now(),
  sent_at              timestamptz,
  recipient_erased_at  timestamptz,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT communication_log_withheld_ck CHECK (NOT is_body_withheld OR body IS NULL),
  CONSTRAINT communication_log_sent_ck CHECK ((status = 'sent') = (sent_at IS NOT NULL)),
  CONSTRAINT communication_log_related_ck CHECK ((related_type IS NULL) = (related_id IS NULL))
);
CREATE INDEX communication_log_account_ix ON communication_log (account_id, id DESC);
CREATE INDEX communication_log_category_ix ON communication_log (category, created_at);
COMMENT ON TABLE communication_log IS 'Every email sent, recorded before sending. Kept by category, then pruned. Not shadow-logged.';
COMMENT ON COLUMN communication_log.id IS 'The record (UUIDv7, so newest sorts last).';
COMMENT ON COLUMN communication_log.kind IS 'What the message is: a declared kind.';
COMMENT ON COLUMN communication_log.category IS 'regulatory, operational, marketing or internal; decides retention.';
COMMENT ON COLUMN communication_log.account_id IS 'The recipient''s account, when there is one.';
COMMENT ON COLUMN communication_log.recipient IS 'The address sent to. Personal: scrubbed on erasure.';
COMMENT ON COLUMN communication_log.subject IS 'The subject sent. Personal: scrubbed on erasure.';
COMMENT ON COLUMN communication_log.body IS 'The text sent, or null when withheld. Personal: scrubbed on erasure.';
COMMENT ON COLUMN communication_log.is_body_withheld IS 'The body carried a credential (a reset or verification link) and was not kept.';
COMMENT ON COLUMN communication_log.related_type IS 'What the message is about (project, subscription), or null.';
COMMENT ON COLUMN communication_log.related_id IS 'The id of what it is about, or null.';
COMMENT ON COLUMN communication_log.status IS 'queued, then sent or failed.';
COMMENT ON COLUMN communication_log.provider IS 'The sender: postmark, noop, or another.';
COMMENT ON COLUMN communication_log.provider_message_id IS 'The provider''s id for the message, for tracing delivery.';
COMMENT ON COLUMN communication_log.error IS 'Why a send failed.';
COMMENT ON COLUMN communication_log.created_at IS 'When the message was recorded.';
COMMENT ON COLUMN communication_log.sent_at IS 'When the provider accepted it.';
COMMENT ON COLUMN communication_log.recipient_erased_at IS 'When erasure scrubbed the personal columns.';
COMMENT ON COLUMN communication_log.updated_at IS 'When the record last changed.';
CREATE TRIGGER communication_log_10_touch BEFORE UPDATE ON communication_log FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
