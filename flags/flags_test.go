package flags_test

import (
	"context"
	"testing"

	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/flags/mem"
	"github.com/nickwhiteley/plinth/ids"
)

// A three-step ladder: free < plus < pro.
var (
	free  = flags.Tier{ID: ids.New(), Key: "free", Name: "Free", Order: 10, Enabled: true}
	plus  = flags.Tier{ID: ids.New(), Key: "plus", Name: "Plus", Order: 20, Enabled: true}
	pro   = flags.Tier{ID: ids.New(), Key: "pro", Name: "Pro", Order: 30, Enabled: true}
	tiers = []flags.Tier{free, plus, pro}
)

func p(id ids.UUID) *ids.UUID { return &id }
func b(v bool) *bool          { return &v }

// Lifted from Bloomprint's TestResolve, with minimum tiers named by id.
func TestResolve(t *testing.T) {
	gone := ids.New()
	for _, c := range []struct {
		name     string
		state    flags.State
		tier     *ids.UUID
		override *bool
		want     bool
	}{
		{"enabled, no minimum", flags.State{Enabled: true}, p(free.ID), nil, true},
		{"disabled, no minimum", flags.State{}, p(free.ID), nil, false},
		{"enabled, no minimum, no tier", flags.State{Enabled: true}, nil, nil, true},
		{"tier above the minimum", flags.State{Enabled: true, MinTierID: p(plus.ID)}, p(pro.ID), nil, true},
		{"tier at the minimum", flags.State{Enabled: true, MinTierID: p(plus.ID)}, p(plus.ID), nil, true},
		{"tier below the minimum", flags.State{Enabled: true, MinTierID: p(plus.ID)}, p(free.ID), nil, false},
		// The kill switch outranks payment.
		{"disabled beats a sufficient tier", flags.State{MinTierID: p(free.ID)}, p(pro.ID), nil, false},
		// No tier fails a tier requirement rather than passing it, and so does what the ladder lacks.
		{"no tier against a minimum", flags.State{Enabled: true, MinTierID: p(free.ID)}, nil, nil, false},
		{"an unknown account tier", flags.State{Enabled: true, MinTierID: p(free.ID)}, p(gone), nil, false},
		{"a minimum that has gone", flags.State{Enabled: true, MinTierID: p(gone)}, p(pro.ID), nil, false},
		{"retired", flags.State{Enabled: true, Retired: true}, p(pro.ID), nil, false},
		// The override wins outright, in both directions.
		{"override on beats disabled", flags.State{}, p(free.ID), b(true), true},
		{"override on beats an insufficient tier", flags.State{Enabled: true, MinTierID: p(pro.ID)}, p(free.ID), b(true), true},
		{"override off beats enabled", flags.State{Enabled: true}, p(free.ID), b(false), false},
		{"override on with no tier", flags.State{}, nil, b(true), true},
	} {
		if got := flags.Resolve(c.state, tiers, c.tier, c.override); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}

func registry() *flags.Registry {
	return flags.NewRegistry().Declare(
		flags.Declaration{Key: "assistant", Default: false, Public: true},
		flags.Declaration{Key: "export_pdf", Default: true, Public: true},
		flags.Declaration{Key: "checkout", Operational: true},
	)
}

func TestResolveAll(t *testing.T) {
	r := registry()
	states := []flags.State{
		{Key: "assistant", Enabled: true, MinTierID: p(pro.ID)},
		{Key: "export_pdf", Enabled: true},
		// checkout deliberately absent: declared, not yet reconciled.
		{Key: "retired_flag", Enabled: true},
	}
	set := r.ResolveAll(states, tiers, p(plus.ID), map[string]bool{"assistant": true, "checkout": true})
	if !set.Enabled("assistant") || !set.Enabled("export_pdf") {
		t.Errorf("set = %v", set)
	}
	// Declared but undecided is off, even with an override: nobody has decided it.
	if set.Enabled("checkout") {
		t.Error("an unreconciled flag resolved on")
	}
	if _, present := set["retired_flag"]; present {
		t.Error("a flag the source no longer declares is in the set")
	}
}

func TestReconcileAndStale(t *testing.T) {
	r := registry()
	missing := r.Reconcile([]flags.State{{Key: "assistant", Enabled: true}})
	if len(missing) != 2 || missing[0].Key != "checkout" || missing[1].Key != "export_pdf" || !missing[1].Enabled {
		t.Errorf("Reconcile = %+v", missing)
	}
	if stale := r.Stale([]flags.State{{Key: "assistant"}, {Key: "old_b"}, {Key: "old_a"}}); len(stale) != 2 || stale[0] != "old_a" {
		t.Errorf("Stale = %v", stale)
	}
}

func TestDeclareRefusesDuplicatesAndBadKeys(t *testing.T) {
	for _, d := range []flags.Declaration{{Key: "assistant"}, {Key: "Bad Key"}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Declare(%q): want a panic", d.Key)
				}
			}()
			registry().Declare(d)
		}()
	}
	d, _ := registry().Lookup("assistant")
	if d.MessageID("public_line") != "flags.assistant.public_line" {
		t.Error(d.MessageID("public_line"))
	}
}

func TestServiceReconcilesAndResolves(t *testing.T) {
	ctx := context.Background()
	st := mem.New()
	for _, tr := range tiers {
		if err := st.CreateTier(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertFlag(ctx, flags.State{Key: "dropped_from_code", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	svc := flags.NewService(registry(), st)
	added, retired, err := svc.Reconcile(ctx)
	if err != nil || added != 3 || len(retired) != 1 || retired[0] != "dropped_from_code" {
		t.Fatalf("Reconcile = %d, %v, %v", added, retired, err)
	}
	if again, _, _ := svc.Reconcile(ctx); again != 0 {
		t.Errorf("a second reconcile added %d", again)
	}
	if err := st.SetFlag(ctx, "assistant", true, &plus.ID); err != nil {
		t.Fatal(err)
	}
	acct := ids.New()
	set, err := svc.For(ctx, acct, &free.ID)
	if err != nil || set.Enabled("assistant") || !set.Enabled("export_pdf") || set.Enabled("checkout") {
		t.Fatalf("For(free) = %v, %v", set, err)
	}
	// Support switches it on for one account.
	if err := st.SetAccountFlag(ctx, acct, "assistant", true); err != nil {
		t.Fatal(err)
	}
	if set, _ := svc.For(ctx, acct, &free.ID); !set.Enabled("assistant") {
		t.Error("the account override didn't apply")
	}
	if set, _ := svc.For(ctx, ids.New(), &free.ID); set.Enabled("assistant") {
		t.Error("one account's override reached another")
	}
}
