package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/flags/mem"
	"github.com/nickwhiteley/plinth/flags/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) (flags.Store, fixture.Source) { return mem.New(), fixture.Minted{} })
}
