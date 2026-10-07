package pg_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/email"
	"github.com/nickwhiteley/plinth/email/pg"
	"github.com/nickwhiteley/plinth/email/storetest"
	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/pgtest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) (email.Store, fixture.Source) {
		d := pgtest.New(t)
		return pg.New(d.Pool), pgtest.Fixtures{DB: d}
	})
}
