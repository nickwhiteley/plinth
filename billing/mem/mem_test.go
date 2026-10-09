package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/billing/mem"
	"github.com/nickwhiteley/plinth/billing/storetest"
	"github.com/nickwhiteley/plinth/fixture"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) (billing.Store, fixture.Source) { return mem.New(), fixture.Minted{} })
}
