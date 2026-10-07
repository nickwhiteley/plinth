package db_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/migrations"
	"github.com/nickwhiteley/plinth/pgtest"
	"github.com/nickwhiteley/plinth/shadowlog"
)

var roles = pgtest.Roles

// as runs fn logged in as role.
func as(t *testing.T, d *pgtest.DB, role string, fn func(c *pgx.Conn)) {
	t.Helper()
	fn(pgtest.ConnectAs(t, d, role))
}

func denied(t *testing.T, c *pgx.Conn, what, q string, args ...any) {
	t.Helper()
	_, err := c.Exec(ctx, q, args...)
	if s := db.SQLState(err); s != "42501" {
		t.Errorf("%s: %v, want permission denied (42501)", what, err)
	}
}

func allowed(t *testing.T, c *pgx.Conn, what, q string, args ...any) {
	t.Helper()
	if _, err := c.Exec(ctx, q, args...); err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

func TestManifestMatchesTheCatalogue(t *testing.T) {
	d := pgtest.New(t)
	problems, err := db.CheckManifest(ctx, d.Pool, migrations.Tables)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	// And it notices a table nobody listed.
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE stray (id int CONSTRAINT stray_pk PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if problems, _ := db.CheckManifest(ctx, d.Pool, migrations.Tables); len(problems) != 1 {
		t.Errorf("an unlisted table: %v", problems)
	}
}

func TestRoleSeparation(t *testing.T) {
	d := pgtest.New(t)
	fx := pgtest.Fixtures{DB: d}
	acct := fx.Account(t)
	setup := conn(t, d)
	pgtest.EnsureRoles(t, d, migrations.Tables)
	logs := pgx.Identifier{d.Schema + "_log"}.Sanitize()

	as(t, d, roles.App, func(c *pgx.Conn) {
		allowed(t, c, "the app reads and writes an account", `UPDATE account SET last_signed_in_at = now() WHERE id = $1`, acct)
		allowed(t, c, "the app reads the log", `SELECT count(*) FROM `+logs+`.account_log`)
		allowed(t, c, "the app deletes a session", `DELETE FROM session WHERE account_id = $1`, acct)
		denied(t, c, "the app deletes an entity", `DELETE FROM account WHERE id = $1`, acct)
		denied(t, c, "the app writes the log", `INSERT INTO `+logs+`.account_log (op) VALUES ('I')`)
		denied(t, c, "the app rewrites the log", `UPDATE `+logs+`.account_log SET modified_by = NULL`)
		denied(t, c, "the app deletes from the log", `DELETE FROM `+logs+`.account_log`)
		denied(t, c, "the app edits the exclusions", `DELETE FROM `+logs+`.log_exclusion`)
		denied(t, c, "the app truncates", `TRUNCATE session`)
		denied(t, c, "the app creates a table", `CREATE TABLE sneaky (id int)`)
		denied(t, c, "the app disables a trigger", `ALTER TABLE account DISABLE TRIGGER account_20_log_u`)
		denied(t, c, "the app touches the version table", `SELECT * FROM plinth_schema_version`)
		denied(t, c, "the app becomes the owner", `SET ROLE plinth_t_owner`)
		r, err := shadowlog.Check(ctx, c, "plinth_t_owner")
		if err != nil || !r.OK() || r.Err() != nil {
			t.Errorf("the app's boot check: %+v, %v", r, err)
		}
	})
	// The app's write through the trigger is still logged, attributed.
	var n int
	if err := setup.QueryRow(ctx, `SELECT count(*) FROM `+logs+`.account_log WHERE op = 'U'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the app's update logged %d rows, %v", n, err)
	}

	as(t, d, roles.Extract, func(c *pgx.Conn) {
		allowed(t, c, "extract reads the log", `SELECT count(*) FROM `+logs+`.account_log`)
		denied(t, c, "extract reads the app schema", `SELECT count(*) FROM `+pgx.Identifier{d.Schema}.Sanitize()+`.account`)
		denied(t, c, "extract reads the settings log", `SELECT count(*) FROM `+logs+`.app_setting_log`)
	})

	as(t, d, roles.Readonly, func(c *pgx.Conn) {
		allowed(t, c, "readonly reads an address", `SELECT email FROM local_identity`)
		denied(t, c, "readonly reads a password hash", `SELECT password_hash FROM local_identity`)
		denied(t, c, "readonly reads a token hash", `SELECT token_hash FROM session`)
		denied(t, c, "readonly writes", `UPDATE account SET is_tombstone = false`)
	})

	// The boot check catches the roles that void the guarantee.
	as(t, d, "plinth_t_migrate", func(c *pgx.Conn) {
		r, err := shadowlog.Check(ctx, c, "plinth_t_owner")
		if err != nil || !r.CanBecomeOwner || !errors.Is(r.Err(), shadowlog.ErrUnsafeRole) {
			t.Errorf("a role that can become the owner: %+v, %v", r, err)
		}
	})
	if r, err := shadowlog.Check(ctx, setup, "plinth_t_owner"); err != nil || !r.DangerousAttribute || r.OK() {
		t.Errorf("a superuser: %+v, %v", r, err)
	}
}
