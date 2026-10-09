package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/rbac"
	"github.com/nickwhiteley/plinth/rbac/mem"
	"github.com/nickwhiteley/plinth/rbac/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) (rbac.Store, fixture.Source) { return mem.New(), fixture.Minted{} })
}
