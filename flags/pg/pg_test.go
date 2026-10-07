package pg_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/flags/pg"
	"github.com/nickwhiteley/plinth/flags/storetest"
	"github.com/nickwhiteley/plinth/pgtest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) (flags.Store, fixture.Source) {
		d := pgtest.New(t)
		return pg.New(d.Pool), pgtest.Fixtures{DB: d}
	})
}
