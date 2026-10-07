package pg_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/identity"
	"github.com/nickwhiteley/plinth/identity/pg"
	"github.com/nickwhiteley/plinth/identity/storetest"
	"github.com/nickwhiteley/plinth/pgtest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) identity.Store { return pg.New(pgtest.New(t).Pool) })
}
