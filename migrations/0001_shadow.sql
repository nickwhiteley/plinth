-- The shadow-log machinery (spec.md §4; Furniture Magic data-model §10.2), from spike 0.1.
-- Schema-free: the log schema is always derived as <table's schema>_log, and the product's
-- one-time setup creates both schemas. Run as the owner role.

DO $$
BEGIN
  EXECUTE pg_catalog.format($t$
    CREATE TABLE %I.log_exclusion (
      table_name  text NOT NULL,
      column_name text NOT NULL,
      reason      text NOT NULL CONSTRAINT log_exclusion_reason_ck CHECK (length(reason) BETWEEN 1 AND 200),
      CONSTRAINT log_exclusion_pk PRIMARY KEY (table_name, column_name)
    )$t$, current_schema() || '_log');
  EXECUTE pg_catalog.format($c$
    COMMENT ON TABLE %1$I.log_exclusion IS 'Columns the shadow log deliberately does not keep: secrets and payloads.';
    COMMENT ON COLUMN %1$I.log_exclusion.table_name IS 'The base table.';
    COMMENT ON COLUMN %1$I.log_exclusion.column_name IS 'The base column that is never copied into the log.';
    COMMENT ON COLUMN %1$I.log_exclusion.reason IS 'Why: a secret, or a payload too large to log.'
  $c$, current_schema() || '_log');
END $$;

-- exclude_from_log registers a column the log twin will not have. Call it before shadow().
CREATE PROCEDURE exclude_from_log(t text, col text, reason text)
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
  EXECUTE pg_catalog.format('INSERT INTO %I.log_exclusion (table_name, column_name, reason) VALUES ($1, $2, $3)',
                            current_schema() || '_log') USING t, col, reason;
END $$;

-- One INSERT … SELECT per statement, from the transition table, into <schema>_log.<table>_log.
-- SECURITY DEFINER, owned by the owner role, so the runtime role (which can't write the log
-- schema) can fire it. The column intersection is loud: a base column that is neither in the log
-- twin nor a registered exclusion fails the write (42703) rather than silently going unlogged.
CREATE FUNCTION shadow_log_stmt() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $$
DECLARE
  _logschem text := TG_TABLE_SCHEMA || '_log';
  _logtable text := TG_TABLE_NAME || '_log';
  _src      text := CASE TG_ARGV[0] WHEN 'D' THEN 'old_rows' ELSE 'new_rows' END;
  _cols     text;
  _missing  text;
  _excl     text[];
  _by       uuid := nullif(pg_catalog.current_setting('app.modified_by', true), '')::uuid;
BEGIN
  EXECUTE pg_catalog.format('SELECT coalesce(pg_catalog.array_agg(column_name::text), ''{}'') FROM %I.log_exclusion WHERE table_name = $1', _logschem)
    INTO _excl USING TG_TABLE_NAME;
  WITH base AS (
    SELECT a.attname, a.attnum FROM pg_catalog.pg_attribute a
     WHERE a.attrelid = TG_RELID AND a.attnum > 0 AND NOT a.attisdropped
  ), logc AS (
    SELECT a.attname FROM pg_catalog.pg_attribute a
     WHERE a.attrelid = pg_catalog.format('%I.%I', _logschem, _logtable)::pg_catalog.regclass
       AND a.attnum > 0 AND NOT a.attisdropped
  )
  SELECT pg_catalog.string_agg(pg_catalog.quote_ident(b.attname), ', ' ORDER BY b.attnum)
           FILTER (WHERE b.attname IN (SELECT attname FROM logc) AND b.attname <> ALL (_excl)),
         pg_catalog.string_agg(b.attname, ', ')
           FILTER (WHERE b.attname NOT IN (SELECT attname FROM logc) AND b.attname <> ALL (_excl))
    INTO _cols, _missing
    FROM base b;
  IF _missing IS NOT NULL THEN
    RAISE EXCEPTION 'shadow log %.% lacks column(s) %, and they are not registered exclusions',
      _logschem, _logtable, _missing USING ERRCODE = '42703';
  END IF;
  EXECUTE pg_catalog.format('INSERT INTO %I.%I (%s, op, modified_by) SELECT %s, $1, $2 FROM %I',
                            _logschem, _logtable, _cols, _cols, _src)
    USING TG_ARGV[0], _by;
  RETURN NULL;
END $$;
REVOKE EXECUTE ON FUNCTION shadow_log_stmt() FROM PUBLIC;

-- Primary-key columns (the trigger's arguments) and created_at never change.
CREATE FUNCTION pk_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE c text; o jsonb := to_jsonb(OLD); n jsonb := to_jsonb(NEW);
BEGIN
  FOREACH c IN ARRAY TG_ARGV LOOP
    IF o -> c IS DISTINCT FROM n -> c THEN
      RAISE EXCEPTION '%.% is immutable', TG_TABLE_NAME, c USING ERRCODE = '55006';
    END IF;
  END LOOP;
  RETURN NEW;
END $$;

CREATE FUNCTION touch_updated_at() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END $$;

-- For tables with updated_by: the actor comes from app.modified_by, as the log's does.
CREATE FUNCTION touch_updated_at_by() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
  NEW.updated_at := now();
  NEW.updated_by := nullif(current_setting('app.modified_by', true), '')::uuid;
  RETURN NEW;
END $$;

-- shadow(t, partitioned, guard, noop): the log twin, its cursor index, its column comments, and
-- every trigger a table needs, in firing order (_00, _05, _10, _20). Call it after the table's
-- CREATE and COMMENTs, and after registering its exclusions.
--   partitioned  the log twin is range-partitioned by logged_at, with a default partition
--   guard        a trigger function for _00_guard (a product's frozen-content guard), or NULL
--   noop         adds suppress_redundant_updates_trigger() as _05_noop
CREATE PROCEDURE shadow(t regclass, partitioned boolean DEFAULT false, guard text DEFAULT NULL, noop boolean DEFAULT false)
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
  _schema text;
  _name   text;
  _log    text;
  _cols   text;
  _pk     text;
  _excl   text[];
  _c      record;
BEGIN
  SELECT n.nspname, c.relname INTO _schema, _name
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = t;
  _log := _schema || '_log';
  EXECUTE format('SELECT coalesce(array_agg(column_name::text), ''{}'') FROM %I.log_exclusion WHERE table_name = $1', _log)
    INTO _excl USING _name;

  SELECT string_agg(format('%I %s', a.attname, format_type(a.atttypid, a.atttypmod)), ', ' ORDER BY a.attnum)
    INTO _cols
    FROM pg_attribute a
   WHERE a.attrelid = t AND a.attnum > 0 AND NOT a.attisdropped AND a.attname <> ALL (_excl);

  EXECUTE format('CREATE TABLE %I.%I (%s, log_id uuid NOT NULL DEFAULT uuidv7(), '
                 'op text NOT NULL CONSTRAINT %I CHECK (op IN (''I'',''U'',''D'',''X'')), '
                 'txid xid8 NOT NULL DEFAULT pg_current_xact_id(), '
                 'logged_at timestamptz NOT NULL DEFAULT clock_timestamp(), modified_by uuid)%s',
                 _log, _name || '_log', _cols, _name || '_log_op_ck',
                 CASE WHEN partitioned THEN ' PARTITION BY RANGE (logged_at)' ELSE '' END);
  IF partitioned THEN
    EXECUTE format('CREATE TABLE %I.%I PARTITION OF %I.%I DEFAULT', _log, _name || '_log_default', _log, _name || '_log');
  END IF;
  EXECUTE format('CREATE INDEX %I ON %I.%I (txid, log_id)', _name || '_log_cursor_ix', _log, _name || '_log');

  -- The warehouse reads the log twin under the same contract as the table: every column commented.
  EXECUTE format('COMMENT ON TABLE %I.%I IS %L', _log, _name || '_log',
                 'Shadow log of ' || _name || ': one row per row written, by statement.');
  FOR _c IN SELECT a.attname, col_description(t, a.attnum) AS d FROM pg_attribute a
             WHERE a.attrelid = t AND a.attnum > 0 AND NOT a.attisdropped AND a.attname <> ALL (_excl) LOOP
    EXECUTE format('COMMENT ON COLUMN %I.%I.%I IS %L', _log, _name || '_log', _c.attname,
                   coalesce(_c.d, 'As ' || _name || '.' || _c.attname || '.'));
  END LOOP;
  EXECUTE format($cm$
    COMMENT ON COLUMN %1$I.%2$I.log_id IS 'This log row (UUIDv7).';
    COMMENT ON COLUMN %1$I.%2$I.op IS 'I insert, U update (the new row), D delete (the old row), X an erasure scrub.';
    COMMENT ON COLUMN %1$I.%2$I.txid IS 'The writing transaction; extraction reads by (txid, log_id) below the oldest open one.';
    COMMENT ON COLUMN %1$I.%2$I.logged_at IS 'When the row was logged.';
    COMMENT ON COLUMN %1$I.%2$I.modified_by IS 'The account that made the change, or null for a system write.'
  $cm$, _log, _name || '_log');

  SELECT string_agg(quote_literal(a.attname), ', ') INTO _pk
    FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
   WHERE i.indrelid = t AND i.indisprimary;
  IF EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = t AND attname = 'created_at' AND NOT attisdropped) THEN
    _pk := concat_ws(', ', _pk, quote_literal('created_at'));
  END IF;
  EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION pk_immutable(%s)',
                 _name || '_00_pk_immutable', t, _pk);
  IF guard IS NOT NULL THEN
    EXECUTE format('CREATE TRIGGER %I BEFORE INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()',
                   _name || '_00_guard', t, guard::regproc);
  END IF;
  IF noop THEN
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION suppress_redundant_updates_trigger()',
                   _name || '_05_noop', t);
  END IF;
  IF EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = t AND attname = 'updated_by' AND NOT attisdropped) THEN
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION touch_updated_at_by()', _name || '_10_touch', t);
  ELSIF EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = t AND attname = 'updated_at' AND NOT attisdropped) THEN
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION touch_updated_at()', _name || '_10_touch', t);
  END IF;
  EXECUTE format('CREATE TRIGGER %I AFTER INSERT ON %s REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION shadow_log_stmt(%L)',
                 _name || '_20_log_i', t, 'I');
  EXECUTE format('CREATE TRIGGER %I AFTER UPDATE ON %s REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION shadow_log_stmt(%L)',
                 _name || '_20_log_u', t, 'U');
  EXECUTE format('CREATE TRIGGER %I AFTER DELETE ON %s REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION shadow_log_stmt(%L)',
                 _name || '_20_log_d', t, 'D');
END $$;

-- A time zone a person or support chose must be one Go can load and Postgres knows by name:
-- AT TIME ZONE alone also accepts abbreviations and offsets (Furniture Magic data-model §4).
CREATE FUNCTION valid_time_zone(z text) RETURNS boolean
LANGUAGE sql STABLE SET search_path FROM CURRENT AS $$
  SELECT z ~ '^[A-Za-z]+(/[A-Za-z0-9_+-]+){0,2}$' AND EXISTS (SELECT 1 FROM pg_timezone_names WHERE name = z)
$$;
