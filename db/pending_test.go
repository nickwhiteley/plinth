package db_test

import (
	"errors"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/migrations"
	"github.com/nickwhiteley/plinth/pgtest"
)

func productStream(files map[string]string) db.Stream {
	fs := fstest.MapFS{}
	for k, v := range files {
		fs[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return db.Stream{Name: "product", FS: fs, Table: "product_schema_version"}
}

var productFiles = map[string]string{
	"0001_widgets.sql": "CREATE TABLE widget (id int PRIMARY KEY);",
	"0002_gadgets.sql": "CREATE TABLE gadget (id int PRIMARY KEY);",
}

// Everything migrated: nothing is pending, for the owner and for the runtime role.
func TestPendingIsEmptyAfterMigrate(t *testing.T) {
	product := productStream(productFiles)
	d := pgtest.New(t, product)
	st, err := db.Pending(ctx, d.Pool, migrations.Plinth, product)
	if err != nil || !st.OK() || st.Err() != nil || len(st.Pending()) != 0 {
		t.Fatalf("Pending = %+v, %v", st, err)
	}
	if len(st.Streams) != 2 || st.Streams[0].Stream != "plinth" || st.Streams[1].Stream != "product" {
		t.Errorf("streams = %+v", st.Streams)
	}
}

// The runtime role, logged in as itself, reads the pending state, and cannot write a version row.
func TestPendingAsTheRuntimeRole(t *testing.T) {
	product := productStream(productFiles)
	d := pgtest.New(t, product)
	tables := append(append([]db.Table{}, migrations.Tables...), db.Table{Name: "widget", Class: db.Reference},
		db.Table{Name: "gadget", Class: db.Reference}, db.Table{Name: "product_schema_version", Class: db.Internal})
	pgtest.EnsureRoles(t, d, tables)

	as(t, d, roles.App, func(c *pgx.Conn) {
		st, err := db.Pending(ctx, c, migrations.Plinth, product)
		if err != nil || !st.OK() {
			t.Fatalf("the runtime role: Pending = %+v, %v", st, err)
		}
		denied(t, c, "the app inserts a version", `INSERT INTO product_schema_version (version, name, checksum) VALUES (99, 'x', '\x00')`)
		denied(t, c, "the app updates a version", `UPDATE product_schema_version SET name = 'x'`)
		denied(t, c, "the app deletes a version", `DELETE FROM product_schema_version`)
		denied(t, c, "the app truncates a version table", `TRUNCATE plinth_schema_version`)

		// A migration removed from the record is pending, as the runtime role sees it.
		if _, err := d.Pool.Exec(ctx, `DELETE FROM product_schema_version WHERE version = 2`); err != nil {
			t.Fatal(err)
		}
		st, err = db.Pending(ctx, c, migrations.Plinth, product)
		if err != nil || st.OK() {
			t.Fatalf("after a deleted row: %+v, %v", st, err)
		}
		if got := st.Pending(); !reflect.DeepEqual(got, []string{"product 0002_gadgets"}) {
			t.Errorf("pending = %v", got)
		}
		if !errors.Is(st.Err(), db.ErrMigrationPending) {
			t.Errorf("Err = %v", st.Err())
		}
		var n int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM product_schema_version`).Scan(&n); err != nil || n != 1 {
			t.Errorf("Pending wrote: %d rows, %v", n, err)
		}
	})

	// Internal still means no access to everyone else.
	as(t, d, roles.Readonly, func(c *pgx.Conn) {
		denied(t, c, "readonly reads a version table", `SELECT * FROM plinth_schema_version`)
	})
	as(t, d, roles.Extract, func(c *pgx.Conn) {
		// It has no use of the app schema at all, so the table isn't even visible to it.
		if _, err := c.Exec(ctx, `SELECT * FROM `+pgx.Identifier{d.Schema, "plinth_schema_version"}.Sanitize()); db.SQLState(err) != "42501" {
			t.Errorf("extract reads a version table: %v, want permission denied", err)
		}
	})
}

// Pending lists migrations in order, across streams, and names them as Migrate does.
func TestPendingListsInOrder(t *testing.T) {
	d := pgtest.New(t)
	product := productStream(productFiles)
	st, err := db.Pending(ctx, d.Pool, migrations.Plinth, product)
	if err != nil {
		t.Fatal(err)
	}
	// The product's table doesn't exist yet: nothing has been migrated for it.
	ps := st.Streams[1]
	if !ps.Missing || ps.Stream != "product" || !reflect.DeepEqual(ps.Pending, []string{"product 0001_widgets", "product 0002_gadgets"}) {
		t.Errorf("a stream never migrated: %+v", ps)
	}
	if st.Streams[0].Missing || len(st.Streams[0].Pending) != 0 {
		t.Errorf("plinth: %+v", st.Streams[0])
	}
	if st.OK() || !errors.Is(st.Err(), db.ErrMigrationTableMissing) {
		t.Errorf("Err = %v", st.Err())
	}
	if errors.Is(st.Err(), db.ErrMigrationPending) {
		t.Errorf("a missing table is not reported as pending: %v", st.Err())
	}

	// Migrate only the first file, then ask again: pending, not missing.
	if _, err := db.Migrate(ctx, conn(t, d), "", productStream(map[string]string{"0001_widgets.sql": productFiles["0001_widgets.sql"]})); err != nil {
		t.Fatal(err)
	}
	st, err = db.Pending(ctx, d.Pool, product)
	if err != nil || st.Streams[0].Missing || !reflect.DeepEqual(st.Pending(), []string{"product 0002_gadgets"}) {
		t.Errorf("some pending: %+v, %v", st, err)
	}
	if !errors.Is(st.Err(), db.ErrMigrationPending) || errors.Is(st.Err(), db.ErrMigrationTableMissing) {
		t.Errorf("Err = %v", st.Err())
	}
}

// The code older than the database, and a migration edited after it was applied.
func TestPendingReportsUnknownAndChanged(t *testing.T) {
	product := productStream(productFiles)
	d := pgtest.New(t, product)

	older := productStream(map[string]string{"0001_widgets.sql": productFiles["0001_widgets.sql"]})
	st, err := db.Pending(ctx, d.Pool, older)
	if err != nil || st.OK() {
		t.Fatalf("an unknown version: %+v, %v", st, err)
	}
	if got := st.Streams[0].Unknown; !reflect.DeepEqual(got, []string{"product 0002_gadgets"}) {
		t.Errorf("unknown = %v", got)
	}
	if !errors.Is(st.Err(), db.ErrMigrationUnknown) {
		t.Errorf("Err = %v", st.Err())
	}

	edited := productStream(map[string]string{"0001_widgets.sql": "CREATE TABLE widget (id bigint PRIMARY KEY);", "0002_gadgets.sql": productFiles["0002_gadgets.sql"]})
	st, err = db.Pending(ctx, d.Pool, edited)
	if err != nil || st.OK() {
		t.Fatalf("a changed checksum: %+v, %v", st, err)
	}
	if got := st.Streams[0].Changed; !reflect.DeepEqual(got, []string{"product 0001_widgets"}) {
		t.Errorf("changed = %v", got)
	}
	if !errors.Is(st.Err(), db.ErrMigrationChanged) {
		t.Errorf("Err = %v", st.Err())
	}
	if len(st.Pending()) != 0 {
		t.Errorf("a changed migration is not pending: %v", st.Pending())
	}
}

// Pending reads only: it neither writes nor moves the search_path, and takes no lock.
func TestPendingChangesNothing(t *testing.T) {
	d := pgtest.New(t)
	c := conn(t, d)
	var before string
	if err := c.QueryRow(ctx, `SHOW search_path`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET TRANSACTION READ ONLY`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pending(ctx, tx, migrations.Plinth, productStream(productFiles)); err != nil {
		t.Fatalf("Pending in a read-only transaction: %v", err)
	}
	var locks int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()`).Scan(&locks); err != nil || locks != 0 {
		t.Errorf("advisory locks held: %d, %v", locks, err)
	}
	var after string
	if err := tx.QueryRow(ctx, `SHOW search_path`).Scan(&after); err != nil || after != before {
		t.Errorf("search_path %q became %q, %v", before, after, err)
	}
}

// A malformed stream is the caller's bug, reported as Migrate reports it.
func TestPendingRefusesABadStream(t *testing.T) {
	d := pgtest.New(t)
	bad := db.Stream{Name: "x", FS: fstest.MapFS{}, Table: "Bad;Table"}
	if _, err := db.Pending(ctx, d.Pool, bad); !errors.Is(err, db.ErrMigrationName) {
		t.Errorf("a bad table name = %v", err)
	}
}
