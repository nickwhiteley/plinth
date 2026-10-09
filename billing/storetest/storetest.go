// Package storetest is the conformance suite every billing.Store must pass (spec.md §3).
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/ids"
)

// Run runs the suite. factory returns an empty store for each case, and the fixtures (tiers and
// accounts) its rows reference.
func Run(t *testing.T, factory func(t *testing.T) (billing.Store, fixture.Source)) {
	for name, f := range map[string]func(*testing.T, billing.Store, fixture.Source){
		"prices":        testPrices,
		"customers":     testCustomers,
		"checkouts":     testCheckouts,
		"subscriptions": testSubscriptions,
	} {
		t.Run(name, func(t *testing.T) { s, fx := factory(t); f(t, s, fx) })
	}
}

var ctx = context.Background()

func testPrices(t *testing.T, s billing.Store, fx fixture.Source) {
	tier := fx.Tier(t)
	p := billing.Price{TierID: tier, Interval: billing.IntervalMonth, Provider: "stub", PlanRef: "plan_m", AmountMinor: 900, Currency: "GBP", Enabled: true}
	if err := s.SetPrice(ctx, p); err != nil {
		t.Fatal(err)
	}
	year := p
	year.Interval, year.PlanRef, year.AmountMinor = billing.IntervalYear, "plan_y", 9000
	if err := s.SetPrice(ctx, year); err != nil {
		t.Fatal(err)
	}
	// Setting again replaces, and a price can be switched off.
	p.AmountMinor, p.Enabled = 1000, false
	if err := s.SetPrice(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Prices(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("Prices = %+v, %v", got, err)
	}
	for _, g := range got {
		if g.Interval == billing.IntervalMonth && (g.AmountMinor != 1000 || g.Enabled) {
			t.Errorf("the month price = %+v", g)
		}
		if g.Interval == billing.IntervalYear && (g.AmountMinor != 9000 || !g.Enabled) {
			t.Errorf("the year price = %+v", g)
		}
	}
	for name, bad := range map[string]billing.Price{
		"no plan":      {TierID: tier, Interval: billing.IntervalMonth, Provider: "stub", AmountMinor: 1, Currency: "GBP"},
		"a negative":   {TierID: tier, Interval: billing.IntervalMonth, Provider: "stub", PlanRef: "x", AmountMinor: -1, Currency: "GBP"},
		"a bad money":  {TierID: tier, Interval: billing.IntervalMonth, Provider: "stub", PlanRef: "x", AmountMinor: 1, Currency: "pounds"},
		"a bad period": {TierID: tier, Interval: "week", Provider: "stub", PlanRef: "x", AmountMinor: 1, Currency: "GBP"},
	} {
		if err := s.SetPrice(ctx, bad); !errors.Is(err, billing.ErrPriceInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func testCustomers(t *testing.T, s billing.Store, fx fixture.Source) {
	a := fx.Account(t)
	if _, ok, err := s.Customer(ctx, a, "stub"); ok || err != nil {
		t.Fatalf("a customer before it exists: %v %v", ok, err)
	}
	if err := s.PutCustomer(ctx, a, "stub", "cus_1"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutCustomer(ctx, a, "stub", "cus_2"); err != nil {
		t.Fatal(err)
	}
	if r, ok, _ := s.Customer(ctx, a, "stub"); !ok || r != "cus_2" {
		t.Errorf("customer = %q %v", r, ok)
	}
	if _, ok, _ := s.Customer(ctx, a, "other"); ok {
		t.Error("another provider's customer")
	}
}

func checkout(fx fixture.Source, t *testing.T) billing.Checkout {
	return billing.Checkout{ID: ids.New(), AccountID: fx.Account(t), Provider: "stub", TierID: fx.Tier(t), Interval: billing.IntervalMonth,
		AmountMinor: 900, Currency: "GBP", SubscriptionRef: "sub_1", URL: "https://pay.example/1", State: billing.CheckoutPending, ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)}
}

func testCheckouts(t *testing.T, s billing.Store, fx fixture.Source) {
	c := checkout(fx, t)
	if err := s.CreateCheckout(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Checkout(ctx, c.ID)
	if err != nil || got.SubscriptionRef != "sub_1" || got.AmountMinor != 900 || got.State != billing.CheckoutPending || got.URL != c.URL {
		t.Fatalf("Checkout = %+v, %v", got, err)
	}
	if by, err := s.CheckoutByRef(ctx, "stub", "sub_1"); err != nil || by.ID != c.ID {
		t.Errorf("CheckoutByRef = %+v, %v", by, err)
	}
	if _, err := s.CheckoutByRef(ctx, "stub", "nope"); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("an unknown ref = %v", err)
	}
	// An attempt the provider never answered has no ref, and several can.
	for i := 0; i < 2; i++ {
		f := checkout(fx, t)
		f.SubscriptionRef, f.URL, f.State = "", "", billing.CheckoutFailed
		if err := s.CreateCheckout(ctx, f); err != nil {
			t.Fatalf("a ref-less checkout: %v", err)
		}
	}
	if pend, _ := s.PendingCheckouts(ctx); len(pend) != 1 {
		t.Errorf("pending = %d", len(pend))
	}
	if err := s.SetCheckoutState(ctx, c.ID, billing.CheckoutActivated); err != nil {
		t.Fatal(err)
	}
	if pend, _ := s.PendingCheckouts(ctx); len(pend) != 0 {
		t.Errorf("pending after activating = %d", len(pend))
	}
	if err := s.SetCheckoutState(ctx, ids.New(), billing.CheckoutExpired); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("an unknown checkout = %v", err)
	}
}

func testSubscriptions(t *testing.T, s billing.Store, fx fixture.Source) {
	acct, tier := fx.Account(t), fx.Tier(t)
	if _, err := s.Subscription(ctx, acct); !errors.Is(err, billing.ErrNotFound) {
		t.Fatalf("a subscription before one exists = %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	x := billing.Subscription{ID: ids.New(), AccountID: acct, Provider: "stub", Ref: "sub_1", TierID: tier, Interval: billing.IntervalMonth,
		State: billing.StateActive, CurrentPeriodEnd: now.Add(24 * time.Hour), AmountMinor: 900, Currency: "GBP", LastReconciledAt: now, StartedAt: now}
	if err := s.PutSubscription(ctx, x); err != nil {
		t.Fatal(err)
	}
	cancel := now.Add(time.Minute)
	x.State, x.CancelRequestedAt, x.CurrentPeriodEnd = billing.StatePastDue, &cancel, now.Add(48*time.Hour)
	x.ID = ids.New() // a replace is by (provider, ref): the first id stays
	if err := s.PutSubscription(ctx, x); err != nil {
		t.Fatal(err)
	}
	got, err := s.Subscription(ctx, acct)
	if err != nil || got.State != billing.StatePastDue || got.CancelRequestedAt == nil || !got.CancelRequestedAt.Equal(cancel) || !got.CurrentPeriodEnd.Equal(x.CurrentPeriodEnd) {
		t.Fatalf("Subscription = %+v, %v", got, err)
	}
	if by, err := s.SubscriptionByRef(ctx, "stub", "sub_1"); err != nil || by.ID != got.ID {
		t.Errorf("SubscriptionByRef = %+v, %v", by, err)
	}
	if all, _ := s.Subscriptions(ctx); len(all) != 1 {
		t.Errorf("Subscriptions = %d", len(all))
	}
	// Ending it takes it from the account's current one, and from the sweep's list.
	ended := now.Add(time.Hour)
	x.State, x.EndedAt = billing.StateExpired, &ended
	if err := s.PutSubscription(ctx, x); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Subscription(ctx, acct); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("an ended subscription is still current: %v", err)
	}
	if all, _ := s.Subscriptions(ctx); len(all) != 0 {
		t.Errorf("an ended subscription is still swept: %d", len(all))
	}
}
