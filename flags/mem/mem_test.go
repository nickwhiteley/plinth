package mem_test

import (
	"testing"

	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/flags/mem"
	"github.com/nickwhiteley/plinth/flags/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) flags.Store { return mem.New() })
}
