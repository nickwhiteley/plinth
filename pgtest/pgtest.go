// Package pgtest gives each test its own Postgres schema pair with plinth's migrations applied
// (spec.md §11). Isolation is per schema, which is why nothing in plinth uses an extension.
//
// It reads PLINTH_TEST_DATABASE_URL (a role that can create schemas and roles, such as the
// container's superuser) and skips the test when it is unset, so `make test` runs anywhere and
// `make test-db` runs everything.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/migrations"
)

// EnvURL names the test database. It is test configuration, never read by plinth itself.
const EnvURL = "PLINTH_TEST_DATABASE_URL"

// DB is one test's database: a pool whose search_path is the test's schema.
type DB struct {
	Pool   *pgxpool.Pool
	Schema string
	URL    string
}

// URL returns the test database's URL, or skips the test.
func URL(t testing.TB) string {
	t.Helper()
	u := os.Getenv(EnvURL)
	if u == "" {
		t.Skip(EnvURL + " is not set: run make test-db")
	}
	return u
}

// New creates a schema pair (s and s_log), applies the given streams (plinth's first), and drops
// both when the test ends.
func New(t testing.TB, streams ...db.Stream) *DB {
	t.Helper()
	url := URL(t)
	ctx := context.Background()
	var b [6]byte
	_, _ = rand.Read(b[:])
	schema := "t_" + hex.EncodeToString(b[:])

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("pgtest: connecting: %v", err)
	}
	defer admin.Close(ctx)
	for _, s := range []string{schema, schema + "_log"} {
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{s}.Sanitize()); err != nil {
			t.Fatalf("pgtest: %v", err)
		}
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), url)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+", "+pgx.Identifier{schema + "_log"}.Sanitize()+" CASCADE")
	})

	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	mig, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mig.Close(ctx)
	if _, err := db.Migrate(ctx, mig, "", append([]db.Stream{migrations.Plinth}, streams...)...); err != nil {
		t.Fatalf("pgtest: migrating: %v", err)
	}

	pcfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pcfg.ConnConfig.RuntimeParams["search_path"] = schema
	pcfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &DB{Pool: pool, Schema: schema, URL: url}
}

// Fixtures inserts the rows other tables' foreign keys need, so a conformance suite can ask for
// "an account" or "a tier" without depending on another package's store.
type Fixtures struct{ DB *DB }

// Tier inserts a tier and returns its id.
func (f Fixtures) Tier(t testing.TB) ids.UUID {
	t.Helper()
	id := ids.New()
	var order int
	if err := f.DB.Pool.QueryRow(context.Background(),
		`INSERT INTO tier (id, key, name, sort_order) VALUES ($1::uuid, $2::text, $2::text, (SELECT coalesce(max(sort_order), 0) + 1 FROM tier)) RETURNING sort_order`,
		id.String(), "t"+hex.EncodeToString(id[10:])).Scan(&order); err != nil {
		t.Fatalf("pgtest: a tier: %v", err)
	}
	return id
}

// Account inserts an account on a new tier, with its profile, and returns its id.
func (f Fixtures) Account(t testing.TB) ids.UUID {
	t.Helper()
	tier := f.Tier(t)
	id := ids.New()
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO account (id, identity_issuer, identity_subject, tier_id, quota_time_zone) VALUES ($1::uuid, 'fixture', $1::text, $2::uuid, 'UTC')`,
		id.String(), tier.String()); err != nil {
		t.Fatalf("pgtest: an account: %v", err)
	}
	if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO account_profile (account_id, display_name, email) VALUES ($1, 'Fixture', 'fixture@example.com')`, id.String()); err != nil {
		t.Fatalf("pgtest: a profile: %v", err)
	}
	return id
}

// Roles are the test roles: real logins, so a test runs with exactly a role's privileges.
// SET ROLE from a superuser session isn't enough: SET ROLE is checked against the session user.
var Roles = db.Roles{App: "plinth_t_app", Extract: "plinth_t_extract", Readonly: "plinth_t_readonly"}

// Owner is the test owner role, which plinth_t_migrate may SET ROLE to.
const Owner = "plinth_t_owner"

// Migrate is a login that can become the owner, which the boot check must flag.
const Migrate = "plinth_t_migrate"

const rolePassword = "plinth_t"

// EnsureRoles creates the cluster-wide test roles once, idempotently, under a lock, and grants
// them this test's schemas per the manifest.
func EnsureRoles(t testing.TB, d *DB, tables []db.Table) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, d.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, `SELECT pg_advisory_lock(42)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = c.Exec(ctx, `SELECT pg_advisory_unlock(42)`) }()
	for _, q := range []string{
		`DO $$ BEGIN
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'plinth_t_owner') THEN CREATE ROLE plinth_t_owner NOLOGIN; END IF;
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'plinth_t_app') THEN CREATE ROLE plinth_t_app NOINHERIT; END IF;
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'plinth_t_extract') THEN CREATE ROLE plinth_t_extract NOINHERIT; END IF;
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'plinth_t_readonly') THEN CREATE ROLE plinth_t_readonly NOINHERIT; END IF;
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'plinth_t_migrate') THEN
		     CREATE ROLE plinth_t_migrate;
		     GRANT plinth_t_owner TO plinth_t_migrate WITH INHERIT FALSE, SET TRUE;
		   END IF;
		 END $$`,
		`ALTER ROLE plinth_t_app LOGIN PASSWORD '` + rolePassword + `'`,
		`ALTER ROLE plinth_t_extract LOGIN PASSWORD '` + rolePassword + `'`,
		`ALTER ROLE plinth_t_readonly LOGIN PASSWORD '` + rolePassword + `'`,
		`ALTER ROLE plinth_t_migrate LOGIN PASSWORD '` + rolePassword + `'`,
		// A product's setup revokes CONNECT from PUBLIC, so grant it to the test logins.
		`DO $$ BEGIN EXECUTE format('GRANT CONNECT ON DATABASE %I TO plinth_t_app, plinth_t_extract, plinth_t_readonly, plinth_t_migrate', current_database()); END $$`,
	} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := pgx.ParseConfig(d.URL)
	cfg.RuntimeParams["search_path"] = d.Schema
	g, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close(ctx)
	tx, err := g.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// As a product's one-time setup does (ALTER DEFAULT PRIVILEGES … REVOKE EXECUTE … FROM PUBLIC),
	// so a function a constraint calls must be granted, here as in a deployment.
	if _, err := tx.Exec(ctx, `REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA `+pgx.Identifier{d.Schema}.Sanitize()+`, `+pgx.Identifier{d.Schema + "_log"}.Sanitize()+` FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	if err := db.Grant(ctx, tx, Roles, tables); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// URLAs returns the test database's URL logged in as role.
func URLAs(t testing.TB, d *DB, role string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(d.URL)
	if err != nil {
		t.Fatal(err)
	}
	return "postgres://" + role + ":" + rolePassword + "@" + cfg.Host + ":" + strconv.Itoa(int(cfg.Port)) + "/" + cfg.Database + "?sslmode=disable&search_path=" + d.Schema
}

// ConnectAs logs in as role, with the test schema on the search_path.
func ConnectAs(t testing.TB, d *DB, role string) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(d.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = role, rolePassword
	cfg.RuntimeParams["search_path"] = d.Schema
	c, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}
