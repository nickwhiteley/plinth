package quotas_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/flags"
	flagsmem "github.com/nickwhiteley/plinth/flags/mem"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/quotas"
	"github.com/nickwhiteley/plinth/quotas/mem"
)

func n(v int64) *int64 { return &v }

func describe(p *int64) int64 {
	if p == nil {
		return -1 // unlimited
	}
	return *p
}

var (
	ta    = flags.Tier{ID: ids.New(), Key: "a", Order: 1, Enabled: true}
	tb    = flags.Tier{ID: ids.New(), Key: "b", Order: 2, Enabled: true}
	tc    = flags.Tier{ID: ids.New(), Key: "c", Order: 3, Enabled: true}
	three = []flags.Tier{tc, ta, tb} // deliberately out of order
)

func p(id ids.UUID) *ids.UUID { return &id }

// Lifted from Bloomprint's TestLimitResolvesOwnRowThenBelowThenDefault.
func TestLimitResolvesOwnRowThenBelowThenDefault(t *testing.T) {
	d := quotas.Declaration{Key: "projects_max", Unit: quotas.UnitCount}
	gone := ids.New()
	for _, c := range []struct {
		name string
		rows []quotas.TierLimit
		tier *ids.UUID
		want *int64
	}{
		{"no rows is the declared default", nil, p(tb.ID), nil},
		{"own row", []quotas.TierLimit{{tb.ID, "projects_max", n(5)}}, p(tb.ID), n(5)},
		{"inherited from below", []quotas.TierLimit{{ta.ID, "projects_max", n(1)}}, p(tc.ID), n(1)},
		{"the nearest below wins", []quotas.TierLimit{{ta.ID, "projects_max", n(1)}, {tb.ID, "projects_max", n(10)}}, p(tc.ID), n(10)},
		{"never from above", []quotas.TierLimit{{tc.ID, "projects_max", n(3)}}, p(ta.ID), nil},
		{"explicit unlimited beats an inherited number", []quotas.TierLimit{{ta.ID, "projects_max", n(1)}, {tb.ID, "projects_max", nil}}, p(tc.ID), nil},
		{"another quota's rows are ignored", []quotas.TierLimit{{ta.ID, "other", n(1)}}, p(tc.ID), nil},
		{"an unknown tier is the lowest enabled", []quotas.TierLimit{{ta.ID, "projects_max", n(1)}}, p(gone), n(1)},
		{"no tier is the lowest enabled", []quotas.TierLimit{{ta.ID, "projects_max", n(1)}}, nil, n(1)},
	} {
		if got := quotas.Limit(d, c.rows, three, c.tier, nil, time.Now()); describe(got) != describe(c.want) {
			t.Errorf("%s = %d, want %d", c.name, describe(got), describe(c.want))
		}
	}
}

func TestLimitHonoursADeclaredDefault(t *testing.T) {
	d := quotas.Declaration{Key: "reply_days", Unit: quotas.UnitCount, Default: n(3), Minimum: 1}
	if got := quotas.Limit(d, nil, three, p(tc.ID), nil, time.Now()); describe(got) != 3 {
		t.Errorf("= %d, want 3", describe(got))
	}
	if got := quotas.Limit(d, []quotas.TierLimit{{ta.ID, "reply_days", nil}}, three, p(tc.ID), nil, time.Now()); got != nil {
		t.Errorf("a row beats the default, even an unlimited one: %d", describe(got))
	}
	if got := quotas.Limit(d, []quotas.TierLimit{{ta.ID, "reply_days", n(1)}}, nil, p(ta.ID), nil, time.Now()); describe(got) != 3 {
		t.Errorf("with no tiers = %d, want 3", describe(got))
	}
}

// A disabled tier can't be chosen, but still holds its accounts and passes its limits up.
func TestADisabledTierStillPassesItsLimitUp(t *testing.T) {
	d := quotas.Declaration{Key: "projects_max", Unit: quotas.UnitCount}
	off := ta
	off.Enabled = false
	ladder := []flags.Tier{off, tb}
	if got := quotas.Limit(d, []quotas.TierLimit{{ta.ID, "projects_max", n(2)}}, ladder, p(tb.ID), nil, time.Now()); describe(got) != 2 {
		t.Errorf("= %d, want 2", describe(got))
	}
}

func TestAnOverrideWinsUntilItExpires(t *testing.T) {
	d := quotas.Declaration{Key: "tokens_daily", Unit: quotas.UnitTokens}
	rows := []quotas.TierLimit{{ta.ID, "tokens_daily", n(1000)}}
	now := time.Now()
	later := now.Add(time.Hour)
	o := &quotas.Override{Key: "tokens_daily", Limit: n(50000), ExpiresAt: &later}
	if got := quotas.Limit(d, rows, three, p(ta.ID), o, now); describe(got) != 50000 {
		t.Errorf("a live override = %d", describe(got))
	}
	if got := quotas.Limit(d, rows, three, p(ta.ID), o, later); describe(got) != 1000 {
		t.Errorf("at its expiry = %d, want the tier's", describe(got))
	}
	// An override of unlimited, and one of a lower limit, both win.
	if got := quotas.Limit(d, rows, three, p(ta.ID), &quotas.Override{Key: "tokens_daily"}, now); got != nil {
		t.Errorf("an unlimited override = %d", describe(got))
	}
	if got := quotas.Limit(d, rows, three, p(ta.ID), &quotas.Override{Key: "tokens_daily", Limit: n(10)}, now); describe(got) != 10 {
		t.Errorf("a lower override = %d", describe(got))
	}
	// Another quota's override doesn't apply.
	if got := quotas.Limit(d, rows, three, p(ta.ID), &quotas.Override{Key: "other", Limit: n(1)}, now); describe(got) != 1000 {
		t.Errorf("another quota's override = %d", describe(got))
	}
}

func TestAllowsAndAccepts(t *testing.T) {
	if !quotas.Allows(nil, 1<<40) || !quotas.Allows(n(3), 2) || quotas.Allows(n(3), 3) || quotas.Allows(n(0), 0) {
		t.Error("Allows")
	}
	unlimited := quotas.Declaration{Key: "projects_max", Unit: quotas.UnitCount}
	promise := quotas.Declaration{Key: "reply_days", Unit: quotas.UnitCount, Default: n(3), Minimum: 1}
	if !unlimited.Accepts(nil) || !unlimited.Accepts(n(0)) || unlimited.Accepts(n(-1)) {
		t.Error("an unlimited-by-default quota")
	}
	if promise.Accepts(nil) || promise.Accepts(n(0)) || !promise.Accepts(n(1)) {
		t.Error("a quota with a minimum")
	}
}

func TestDeclareRefusesWhatCannotWork(t *testing.T) {
	for name, d := range map[string]quotas.Declaration{
		"a duplicate":       {Key: "projects_max", Unit: quotas.UnitCount},
		"no unit":           {Key: "fine"},
		"a refused default": {Key: "fine", Unit: quotas.UnitCount, Default: n(0), Minimum: 1},
		"a malformed key":   {Key: "Projects", Unit: quotas.UnitCount},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: want a panic", name)
				}
			}()
			quotas.NewRegistry().Declare(quotas.Declaration{Key: "projects_max", Unit: quotas.UnitCount}).Declare(d)
		}()
	}
}

func TestService(t *testing.T) {
	ctx := context.Background()
	fl := flagsmem.New()
	for _, tr := range three {
		if err := fl.CreateTier(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	st := mem.New(fl)
	if err := st.InsertQuotaKey(ctx, quotas.KeyState{Key: "dropped", Unit: quotas.UnitCount}); err != nil {
		t.Fatal(err)
	}
	reg := quotas.NewRegistry().Declare(
		quotas.Declaration{Key: "projects_max", Unit: quotas.UnitCount},
		quotas.Declaration{Key: "tokens_daily", Unit: quotas.UnitTokens},
	)
	now := time.Now()
	svc := quotas.NewService(reg, st, fl).WithClock(func() time.Time { return now })
	added, retired, err := svc.Reconcile(ctx)
	if err != nil || added != 2 || len(retired) != 1 {
		t.Fatalf("Reconcile = %d, %v, %v", added, retired, err)
	}
	if err := st.SetTierLimit(ctx, quotas.TierLimit{TierID: ta.ID, Key: "projects_max", Limit: n(3)}); err != nil {
		t.Fatal(err)
	}
	acct, support := ids.New(), ids.New()
	if l, err := svc.Limit(ctx, acct, p(tb.ID), "projects_max"); err != nil || describe(l) != 3 {
		t.Fatalf("Limit = %d, %v", describe(l), err)
	}
	// Support adjusts the cap, and it must say why and who.
	for _, bad := range []quotas.Override{
		{AccountID: acct, Key: "projects_max", Limit: n(10), GrantedBy: support},
		{AccountID: acct, Key: "projects_max", Limit: n(10), Reason: "growing studio"},
		{AccountID: acct, Key: "projects_max", Limit: n(-1), Reason: "x", GrantedBy: support},
		{AccountID: acct, Key: "nope", Limit: n(1), Reason: "x", GrantedBy: support},
	} {
		if err := svc.Grant(ctx, bad); err == nil {
			t.Errorf("Grant(%+v) was accepted", bad)
		}
	}
	if err := svc.Grant(ctx, quotas.Override{AccountID: acct, Key: "projects_max", Limit: n(10), Reason: "growing studio", GrantedBy: support}); err != nil {
		t.Fatal(err)
	}
	limits, err := svc.Limits(ctx, acct, p(tb.ID))
	if err != nil || describe(limits["projects_max"]) != 10 || limits["tokens_daily"] != nil {
		t.Errorf("Limits = %v, %v", limits, err)
	}
	if _, err := svc.Limit(ctx, acct, nil, "nope"); !errors.Is(err, quotas.ErrUnknownQuota) {
		t.Errorf("an unknown quota = %v", err)
	}
}
