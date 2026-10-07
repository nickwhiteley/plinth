package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/settings"
	"github.com/nickwhiteley/plinth/settings/mem"
	"github.com/nickwhiteley/plinth/settings/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) (settings.Store, fixture.Source) { return mem.New(), fixture.Minted{} })
}
