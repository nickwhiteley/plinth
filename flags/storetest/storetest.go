// Package storetest is the conformance suite every flags.Store must pass (spec.md §3).
package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
)

// Run runs the suite. factory returns an empty store for each case, and the fixtures (accounts)
// its overrides reference.
func Run(t *testing.T, factory func(t *testing.T) (flags.Store, fixture.Source)) {
	for name, f := range map[string]func(*testing.T, flags.Store, fixture.Source){
		"tiers":     testTiers,
		"flags":     testFlags,
		"overrides": testOverrides,
	} {
		t.Run(name, func(t *testing.T) { s, fx := factory(t); f(t, s, fx) })
	}
}

func tier(key string, order int) flags.Tier {
	return flags.Tier{ID: ids.New(), Key: key, Name: key, Order: order, Enabled: true}
}

func testTiers(t *testing.T, s flags.Store, _ fixture.Source) {
	ctx := context.Background()
	pro, free := tier("pro", 20), tier("free", 10)
	for _, tr := range []flags.Tier{pro, free} {
		if err := s.CreateTier(ctx, tr); err != nil {
			t.Fatalf("CreateTier(%s): %v", tr.Key, err)
		}
	}
	got, err := s.Tiers(ctx)
	if err != nil || len(got) != 2 || got[0].Key != "free" || got[1].Key != "pro" || got[0].ID != free.ID || !got[0].Enabled {
		t.Fatalf("Tiers = %+v, %v: want lowest first", got, err)
	}
	if err := s.CreateTier(ctx, tier("free", 30)); !errors.Is(err, flags.ErrTierExists) {
		t.Errorf("a duplicate key = %v", err)
	}
	if err := s.CreateTier(ctx, tier("plus", 20)); !errors.Is(err, flags.ErrTierExists) {
		t.Errorf("a duplicate order = %v", err)
	}
	for name, bad := range map[string]flags.Tier{
		"no name":      {ID: ids.New(), Key: "plus", Order: 15},
		"a bad key":    {ID: ids.New(), Key: "Plus Tier", Name: "Plus", Order: 15},
		"a huge order": {ID: ids.New(), Key: "plus", Name: "Plus", Order: 2000000},
	} {
		if err := s.CreateTier(ctx, bad); !errors.Is(err, flags.ErrTierInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if err := s.SetTierEnabled(ctx, pro.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Tiers(ctx); got[1].Enabled {
		t.Error("SetTierEnabled didn't take")
	}
	if err := s.SetTierEnabled(ctx, ids.New(), true); !errors.Is(err, flags.ErrNotFound) {
		t.Errorf("SetTierEnabled(unknown) = %v", err)
	}
}

func testFlags(t *testing.T, s flags.Store, _ fixture.Source) {
	ctx := context.Background()
	pro := tier("pro", 20)
	if err := s.CreateTier(ctx, pro); err != nil {
		t.Fatal(err)
	}
	for _, st := range []flags.State{{Key: "zeta", Enabled: true}, {Key: "alpha"}} {
		if err := s.InsertFlag(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	// Insert never overwrites an administrator's decision.
	if err := s.SetFlag(ctx, "alpha", true, &pro.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertFlag(ctx, flags.State{Key: "alpha"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Flags(ctx)
	if err != nil || len(got) != 2 || got[0].Key != "alpha" || got[1].Key != "zeta" {
		t.Fatalf("Flags = %+v, %v", got, err)
	}
	if !got[0].Enabled || got[0].MinTierID == nil || *got[0].MinTierID != pro.ID || got[0].Retired {
		t.Errorf("alpha = %+v", got[0])
	}
	if err := s.SetFlag(ctx, "alpha", true, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Flags(ctx); got[0].MinTierID != nil {
		t.Error("clearing the minimum tier didn't take")
	}
	if err := s.SetRetired(ctx, "zeta", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Flags(ctx); !got[1].Retired {
		t.Error("SetRetired didn't take")
	}
	if err := s.SetFlag(ctx, "nope", true, nil); !errors.Is(err, flags.ErrNotFound) {
		t.Errorf("SetFlag(unknown) = %v", err)
	}
	unknownTier := ids.New()
	if err := s.SetFlag(ctx, "alpha", true, &unknownTier); !errors.Is(err, flags.ErrNotFound) {
		t.Errorf("SetFlag(unknown tier) = %v", err)
	}
	if err := s.SetRetired(ctx, "nope", true); !errors.Is(err, flags.ErrNotFound) {
		t.Errorf("SetRetired(unknown) = %v", err)
	}
}

func testOverrides(t *testing.T, s flags.Store, fx fixture.Source) {
	ctx := context.Background()
	for _, k := range []string{"one", "two"} {
		if err := s.InsertFlag(ctx, flags.State{Key: k}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := fx.Account(t), fx.Account(t)
	if err := s.SetAccountFlag(ctx, a, "one", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountFlag(ctx, a, "two", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountFlag(ctx, b, "one", false); err != nil {
		t.Fatal(err)
	}
	got, err := s.AccountFlags(ctx, a)
	if err != nil || len(got) != 2 || !got["one"] || got["two"] {
		t.Fatalf("AccountFlags(a) = %v, %v", got, err)
	}
	// An override can be flipped in place.
	if err := s.SetAccountFlag(ctx, a, "one", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AccountFlags(ctx, a); got["one"] {
		t.Error("re-setting an override didn't take")
	}
	if err := s.ClearAccountFlag(ctx, a, "one"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AccountFlags(ctx, a); len(got) != 1 {
		t.Errorf("after clearing: %v", got)
	}
	if got, _ := s.AccountFlags(ctx, b); len(got) != 1 || got["one"] {
		t.Errorf("another account's overrides moved: %v", got)
	}
	if err := s.ClearAccountFlag(ctx, a, "one"); err != nil {
		t.Errorf("clearing twice = %v", err)
	}
	if err := s.SetAccountFlag(ctx, a, "nope", true); !errors.Is(err, flags.ErrNotFound) {
		t.Errorf("an override for an unknown flag = %v", err)
	}
}
