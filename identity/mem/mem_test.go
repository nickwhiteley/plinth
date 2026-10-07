package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/identity"
	"github.com/nickwhiteley/plinth/identity/mem"
	"github.com/nickwhiteley/plinth/identity/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) identity.Store { return mem.New() })
}
