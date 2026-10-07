-- Extraction cursors (spec.md §14; Furniture Magic data-model §10.3). One per log table, in the
-- log schema, written only by the extract role. Retention reads them: a log row the warehouse
-- hasn't extracted is never dropped.

DO $$
BEGIN
  EXECUTE pg_catalog.format($t$
    CREATE TABLE %1$I.extract_cursor (
      table_name   text CONSTRAINT extract_cursor_pk PRIMARY KEY CONSTRAINT extract_cursor_table_ck CHECK (table_name ~ '^[a-z][a-z0-9_]{0,62}$'),
      last_txid    xid8 NOT NULL,
      last_log_id  uuid NOT NULL,
      rows_total   bigint NOT NULL DEFAULT 0 CONSTRAINT extract_cursor_rows_ck CHECK (rows_total BETWEEN 0 AND 1000000000000000),
      updated_at   timestamptz NOT NULL DEFAULT now()
    );
    COMMENT ON TABLE %1$I.extract_cursor IS 'How far the warehouse has extracted each log table, by (txid, log_id).';
    COMMENT ON COLUMN %1$I.extract_cursor.table_name IS 'The base table whose log is extracted.';
    COMMENT ON COLUMN %1$I.extract_cursor.last_txid IS 'The transaction of the last row acknowledged.';
    COMMENT ON COLUMN %1$I.extract_cursor.last_log_id IS 'The last row acknowledged, within its transaction.';
    COMMENT ON COLUMN %1$I.extract_cursor.rows_total IS 'Rows acknowledged since the cursor was last reset.';
    COMMENT ON COLUMN %1$I.extract_cursor.updated_at IS 'When the cursor last moved.'
  $t$, current_schema() || '_log');
END $$;
