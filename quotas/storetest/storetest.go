// Package storetest is the conformance suite every quotas.Store must pass (spec.md §3).
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/quotas"
)

// Run runs the suite. factory returns an empty quota store over an empty tier store, and the
// fixtures (accounts) its overrides reference.
func Run(t *testing.T, factory func(t *testing.T) (quotas.Store, flags.Store, fixture.Source)) {
	for name, f := range map[string]func(*testing.T, quotas.Store, flags.Store, fixture.Source){
		"keys":      testKeys,
		"limits":    testLimits,
		"overrides": testOverrides,
	} {
		t.Run(name, func(t *testing.T) { q, fl, fx := factory(t); f(t, q, fl, fx) })
	}
}

func n(v int64) *int64 { return &v }

func testKeys(t *testing.T, s quotas.Store, _ flags.Store, _ fixture.Source) {
	ctx := context.Background()
	for _, k := range []quotas.KeyState{{Key: "zeta", Unit: quotas.UnitTokens}, {Key: "alpha", Unit: quotas.UnitCount}} {
		if err := s.InsertQuotaKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertQuotaKey(ctx, quotas.KeyState{Key: "alpha", Unit: quotas.UnitTokens}); err != nil {
		t.Fatal(err)
	}
	got, err := s.QuotaKeys(ctx)
	if err != nil || len(got) != 2 || got[0].Key != "alpha" || got[0].Unit != quotas.UnitCount || got[1].Unit != quotas.UnitTokens {
		t.Fatalf("QuotaKeys = %+v, %v: insert must not overwrite", got, err)
	}
	if err := s.SetRetired(ctx, "zeta", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.QuotaKeys(ctx); !got[1].Retired {
		t.Error("SetRetired didn't take")
	}
	if err := s.SetRetired(ctx, "nope", true); !errors.Is(err, quotas.ErrNotFound) {
		t.Errorf("SetRetired(unknown) = %v", err)
	}
}

func testLimits(t *testing.T, s quotas.Store, fl flags.Store, _ fixture.Source) {
	ctx := context.Background()
	free := flags.Tier{ID: ids.New(), Key: "free", Name: "Free", Order: 1, Enabled: true}
	pro := flags.Tier{ID: ids.New(), Key: "pro", Name: "Pro", Order: 2, Enabled: true}
	for _, tr := range []flags.Tier{free, pro} {
		if err := fl.CreateTier(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertQuotaKey(ctx, quotas.KeyState{Key: "projects_max", Unit: quotas.UnitCount}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTierLimit(ctx, quotas.TierLimit{TierID: free.ID, Key: "projects_max", Limit: n(3)}); err != nil {
		t.Fatal(err)
	}
	// An explicit unlimited is a row, distinct from no row.
	if err := s.SetTierLimit(ctx, quotas.TierLimit{TierID: pro.ID, Key: "projects_max", Limit: nil}); err != nil {
		t.Fatal(err)
	}
	got, err := s.TierLimits(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("TierLimits = %+v, %v", got, err)
	}
	byTier := map[ids.UUID]quotas.TierLimit{}
	for _, l := range got {
		byTier[l.TierID] = l
	}
	if l := byTier[free.ID]; l.Limit == nil || *l.Limit != 3 {
		t.Errorf("free = %+v", l)
	}
	if l, ok := byTier[pro.ID]; !ok || l.Limit != nil {
		t.Errorf("pro's explicit unlimited = %+v, %v", l, ok)
	}
	if err := s.SetTierLimit(ctx, quotas.TierLimit{TierID: free.ID, Key: "projects_max", Limit: n(5)}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearTierLimit(ctx, pro.ID, "projects_max"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TierLimits(ctx); len(got) != 1 || *got[0].Limit != 5 {
		t.Errorf("after replacing one and clearing the other: %+v", got)
	}
	if err := s.SetTierLimit(ctx, quotas.TierLimit{TierID: ids.New(), Key: "projects_max", Limit: n(1)}); !errors.Is(err, quotas.ErrNotFound) {
		t.Errorf("an unknown tier = %v", err)
	}
	if err := s.SetTierLimit(ctx, quotas.TierLimit{TierID: free.ID, Key: "nope", Limit: n(1)}); !errors.Is(err, quotas.ErrNotFound) {
		t.Errorf("an unknown key = %v", err)
	}
}

func testOverrides(t *testing.T, s quotas.Store, _ flags.Store, fx fixture.Source) {
	ctx := context.Background()
	for _, k := range []string{"projects_max", "tokens_daily"} {
		if err := s.InsertQuotaKey(ctx, quotas.KeyState{Key: k, Unit: quotas.UnitCount}); err != nil {
			t.Fatal(err)
		}
	}
	a, b, support := fx.Account(t), fx.Account(t), fx.Account(t)
	at := time.Now().UTC().Truncate(time.Microsecond)
	expires := at.Add(24 * time.Hour)
	o := quotas.Override{AccountID: a, Key: "tokens_daily", Limit: n(500000), Reason: "a demo for a client", ExpiresAt: &expires, GrantedBy: support, GrantedAt: at}
	if err := s.SetOverride(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOverride(ctx, quotas.Override{AccountID: a, Key: "projects_max", Limit: nil, Reason: "unlimited for a partner", GrantedBy: support, GrantedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOverride(ctx, quotas.Override{AccountID: b, Key: "projects_max", Limit: n(1), Reason: "x", GrantedBy: support, GrantedAt: at}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Overrides(ctx, a)
	if err != nil || len(got) != 2 || got[0].Key != "projects_max" || got[0].Limit != nil || got[0].ExpiresAt != nil {
		t.Fatalf("Overrides = %+v, %v", got, err)
	}
	g := got[1]
	if *g.Limit != 500000 || g.Reason != "a demo for a client" || !g.ExpiresAt.Equal(expires) || g.GrantedBy != support || !g.GrantedAt.Equal(at) {
		t.Errorf("round trip: %+v", g)
	}
	if err := s.ClearOverride(ctx, a, "projects_max"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Overrides(ctx, a); len(got) != 1 {
		t.Errorf("after clearing: %+v", got)
	}
	if got, _ := s.Overrides(ctx, b); len(got) != 1 {
		t.Errorf("another account's overrides moved: %+v", got)
	}
	if err := s.ClearOverride(ctx, a, "projects_max"); err != nil {
		t.Errorf("clearing twice = %v", err)
	}
	if err := s.SetOverride(ctx, quotas.Override{AccountID: a, Key: "nope", Reason: "x", GrantedBy: support, GrantedAt: at}); !errors.Is(err, quotas.ErrNotFound) {
		t.Errorf("an override of an unknown quota = %v", err)
	}
}
