package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/flags"
	flagsmem "github.com/nickwhiteley/plinth/flags/mem"
	"github.com/nickwhiteley/plinth/quotas"
	"github.com/nickwhiteley/plinth/quotas/mem"
	"github.com/nickwhiteley/plinth/quotas/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) (quotas.Store, flags.Store, fixture.Source) {
		fl := flagsmem.New()
		return mem.New(fl), fl, fixture.Minted{}
	})
}
