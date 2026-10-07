// Package fixture is what a conformance suite asks for when a store's rows reference another
// package's (spec.md §3): a tier, an account. In memory those are just ids (Minted); in Postgres
// they're real rows (pgtest.Fixtures), so the foreign keys are exercised too.
package fixture

import (
	"testing"

	"github.com/nickwhiteley/plinth/ids"
)

// Source provides the rows other packages own.
type Source interface {
	Tier(t testing.TB) ids.UUID
	Account(t testing.TB) ids.UUID
}

// Minted is the in-memory Source: fresh ids, nothing behind them.
type Minted struct{}

func (Minted) Tier(testing.TB) ids.UUID    { return ids.New() }
func (Minted) Account(testing.TB) ids.UUID { return ids.New() }
