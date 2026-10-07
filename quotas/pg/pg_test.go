package pg_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/flags"
	flagspg "github.com/nickwhiteley/plinth/flags/pg"
	"github.com/nickwhiteley/plinth/pgtest"
	"github.com/nickwhiteley/plinth/quotas"
	"github.com/nickwhiteley/plinth/quotas/pg"
	"github.com/nickwhiteley/plinth/quotas/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) (quotas.Store, flags.Store, fixture.Source) {
		d := pgtest.New(t)
		return pg.New(d.Pool), flagspg.New(d.Pool), pgtest.Fixtures{DB: d}
	})
}
