package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/email"
	"github.com/nickwhiteley/plinth/email/mem"
	"github.com/nickwhiteley/plinth/email/storetest"
	"github.com/nickwhiteley/plinth/fixture"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) (email.Store, fixture.Source) { return mem.New(), fixture.Minted{} })
}
