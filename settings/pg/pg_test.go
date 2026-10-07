package pg_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/pgtest"
	"github.com/nickwhiteley/plinth/settings"
	"github.com/nickwhiteley/plinth/settings/pg"
	"github.com/nickwhiteley/plinth/settings/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) (settings.Store, fixture.Source) {
		d := pgtest.New(t)
		return pg.New(d.Pool), pgtest.Fixtures{DB: d}
	})
}
