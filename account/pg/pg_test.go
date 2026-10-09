package pg_test

import (
	"context"
	"testing"

	"github.com/nickwhiteley/plinth/account"
	"github.com/nickwhiteley/plinth/account/pg"
	"github.com/nickwhiteley/plinth/account/storetest"
	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/pgtest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) (account.Store, fixture.Source) {
		d := pgtest.New(t)
		return pg.New(d.Pool), pgtest.Fixtures{DB: d}
	})
}

// A session must survive the database restarting: managed Postgres suspends an idle database, and
// an unlogged table comes back empty, which signed everyone out after a short break.
func TestSessionTableIsLogged(t *testing.T) {
	d := pgtest.New(t)
	var persistence string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT relpersistence::text FROM pg_class WHERE oid = to_regclass('session')`).Scan(&persistence); err != nil {
		t.Fatal(err)
	}
	if persistence != "p" {
		t.Errorf("session relpersistence = %q, want p (permanent, crash-safe)", persistence)
	}
}
