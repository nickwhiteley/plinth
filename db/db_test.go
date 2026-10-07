package db_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/nickwhiteley/plinth/actor"
	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/migrations"
	"github.com/nickwhiteley/plinth/pgtest"
)

var ctx = context.Background()

func conn(t *testing.T, d *pgtest.DB) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(d.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = d.Schema
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(ctx) })
	return c
}

func TestMigrateIsIdempotent(t *testing.T) {
	d := pgtest.New(t)
	applied, err := db.Migrate(ctx, conn(t, d), "", migrations.Plinth)
	if err != nil || len(applied) != 0 {
		t.Fatalf("a second run applied %v, %v", applied, err)
	}
}

func TestMigrateRefusesWhatItMustNot(t *testing.T) {
	d := pgtest.New(t)
	c := conn(t, d)
	stream := func(files map[string]string) db.Stream {
		fs := fstest.MapFS{}
		for k, v := range files {
			fs[k] = &fstest.MapFile{Data: []byte(v)}
		}
		return db.Stream{Name: "product", FS: fs, Table: "product_schema_version"}
	}
	first := stream(map[string]string{"0001_widgets.sql": "CREATE TABLE widget (id int PRIMARY KEY);"})
	if applied, err := db.Migrate(ctx, c, "", first); err != nil || len(applied) != 1 {
		t.Fatalf("Migrate = %v, %v", applied, err)
	}
	// An applied migration edited afterwards.
	edited := stream(map[string]string{"0001_widgets.sql": "CREATE TABLE widget (id bigint PRIMARY KEY);"})
	if _, err := db.Migrate(ctx, c, "", edited); !errors.Is(err, db.ErrMigrationChanged) {
		t.Errorf("an edited migration = %v", err)
	}
	// Code older than the database.
	if _, err := db.Migrate(ctx, c, "", stream(map[string]string{})); !errors.Is(err, db.ErrMigrationUnknown) {
		t.Errorf("an applied version this code lacks = %v", err)
	}
	// Names: forward-only, and well-formed.
	for _, name := range []string{"0002_undo.down.sql", "2_short.sql", "0002_Bad.sql"} {
		s := stream(map[string]string{"0001_widgets.sql": "CREATE TABLE widget (id int PRIMARY KEY);", name: "SELECT 1;"})
		if _, err := db.Migrate(ctx, c, "", s); !errors.Is(err, db.ErrMigrationName) {
			t.Errorf("%s = %v", name, err)
		}
	}
	// A failing migration leaves nothing behind: its transaction rolls back, and it isn't recorded.
	bad := stream(map[string]string{"0001_widgets.sql": "CREATE TABLE widget (id int PRIMARY KEY);",
		"0002_half.sql": "CREATE TABLE half (id int); SELECT 1/0;"})
	if _, err := db.Migrate(ctx, c, "", bad); err == nil {
		t.Fatal("a failing migration succeeded")
	}
	var exists bool
	if err := c.QueryRow(ctx, "SELECT to_regclass('half') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Errorf("a failed migration left its table: %v", err)
	}
}

// A table logged through shadow(): writes are logged by statement with their actor, secrets are
// excluded, keys are immutable, and a column the log lacks fails loudly.
func TestShadowLog(t *testing.T) {
	d := pgtest.New(t)
	f := pgtest.Fixtures{DB: d}
	who := f.Account(t)
	c := conn(t, d)
	for _, q := range []string{
		`CREATE TABLE widget (id uuid PRIMARY KEY, name text NOT NULL, secret text, created_at timestamptz NOT NULL DEFAULT now(),
		   updated_at timestamptz NOT NULL DEFAULT now(), updated_by uuid)`,
		`COMMENT ON COLUMN widget.name IS 'The widget''s name.'`,
		`CALL exclude_from_log('widget', 'secret', 'a secret')`,
		`CALL shadow('widget')`,
	} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	id := ids.New()
	err := db.Run(actor.With(ctx, who), d.Pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO widget (id, name, secret) VALUES ($1, 'one', 'hunter2'), ($2, 'two', 'x')", id.String(), ids.New().String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE widget SET name = 'uno' WHERE id = $1", id.String())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var n, distinctTx int
	var by string
	if err := c.QueryRow(ctx, `SELECT count(*), count(DISTINCT txid), min(modified_by::text) FROM `+d.Schema+`_log.widget_log`).Scan(&n, &distinctTx, &by); err != nil {
		t.Fatal(err)
	}
	if n != 3 || distinctTx != 1 || by != who.String() {
		t.Errorf("log: %d rows in %d transactions by %s; want 3 in 1 by %s", n, distinctTx, by, who)
	}
	var hasSecret bool
	if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'widget_log' AND column_name = 'secret')`, d.Schema+"_log").Scan(&hasSecret); err != nil || hasSecret {
		t.Errorf("the excluded column is in the log twin: %v", err)
	}
	// updated_by follows the actor; the touch trigger sets it.
	var updatedBy *string
	if err := c.QueryRow(ctx, "SELECT updated_by::text FROM widget WHERE id = $1", id.String()).Scan(&updatedBy); err != nil {
		t.Fatal(err)
	}
	if updatedBy == nil || *updatedBy != who.String() {
		t.Errorf("updated_by = %v", updatedBy)
	}
	// The primary key and created_at are immutable.
	for _, q := range []string{"UPDATE widget SET id = gen_random_uuid() WHERE name = 'two'", "UPDATE widget SET created_at = now() - interval '1 day'"} {
		if _, err := c.Exec(ctx, q); db.SQLState(err) != "55006" {
			t.Errorf("%s = %v, want 55006", q, err)
		}
	}
	// A column added to the table and not to its log twin fails the next write.
	if _, err := c.Exec(ctx, "ALTER TABLE widget ADD COLUMN colour text"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "UPDATE widget SET name = 'dos' WHERE name = 'two'"); db.SQLState(err) != "42703" {
		t.Errorf("a write with an unlogged column = %v, want 42703", err)
	}
	// The log twin's columns are commented, from the table's where it has them.
	var comment string
	if err := c.QueryRow(ctx, `SELECT col_description((quote_ident($1) || '.widget_log')::regclass, a.attnum) FROM pg_attribute a
		WHERE a.attrelid = (quote_ident($1) || '.widget_log')::regclass AND a.attname = 'name'`, d.Schema+"_log").Scan(&comment); err != nil || comment != "The widget's name." {
		t.Errorf("the log twin's comment = %q, %v", comment, err)
	}
}

// The warehouse reads plinth's tables under a contract: every column of every table, and of every
// log twin, carries a comment.
func TestEveryColumnIsCommented(t *testing.T) {
	d := pgtest.New(t)
	rows, err := d.Pool.Query(ctx, `
		SELECT n.nspname || '.' || c.relname || '.' || a.attname
		  FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname IN ($1::text, $1::text || '_log') AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped
		   AND c.relname <> 'plinth_schema_version' AND NOT c.relispartition
		   AND col_description(c.oid, a.attnum) IS NULL
		 ORDER BY 1`, d.Schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		_ = rows.Scan(&col)
		t.Errorf("uncommented: %s", col)
	}
}

// Every table's constraints are named <table>_<what>_<pk|fk|uq|ck|ix>, because the API maps
// constraint names to codes and a generated name would change under it.
func TestConstraintsAreNamed(t *testing.T) {
	d := pgtest.New(t)
	rows, err := d.Pool.Query(ctx, `
		SELECT c.conrelid::regclass::text || ': ' || c.conname FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
		 WHERE n.nspname = $1 AND c.conrelid <> 0 AND c.contype <> 'n'
		   AND c.conname !~ ('^' || (SELECT relname FROM pg_class WHERE oid = c.conrelid) || '(_[a-z0-9_]+)?_(pk|fk|uq|ck)$')
		   AND (SELECT relname FROM pg_class WHERE oid = c.conrelid) <> 'plinth_schema_version'`, d.Schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		t.Errorf("unconventional constraint name: %s", c)
	}
}
