package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/account"
	"github.com/nickwhiteley/plinth/account/mem"
	"github.com/nickwhiteley/plinth/account/storetest"
	"github.com/nickwhiteley/plinth/fixture"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) (account.Store, fixture.Source) { return mem.New(), fixture.Minted{} })
}
