package pg_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/billing/pg"
	"github.com/nickwhiteley/plinth/billing/storetest"
	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/pgtest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) (billing.Store, fixture.Source) {
		d := pgtest.New(t)
		return pg.New(d.Pool), pgtest.Fixtures{DB: d}
	})
}
