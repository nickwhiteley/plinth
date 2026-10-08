package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/billing/mem"
	"github.com/nickwhiteley/plinth/billing/provider/stub"
	"github.com/nickwhiteley/plinth/billing/service"
	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
)

var ctx = context.Background()

type accounts map[ids.UUID]ids.UUID

func (a accounts) Tier(_ context.Context, id ids.UUID) (ids.UUID, error) { return a[id], nil }
func (a accounts) SetTier(_ context.Context, id, tier ids.UUID) error    { a[id] = tier; return nil }

type ladder []flags.Tier

func (l ladder) Tiers(context.Context) ([]flags.Tier, error) { return l, nil }

type env struct {
	svc     *service.Service
	prov    *stub.Provider
	store   billing.Store
	accts   accounts
	free    flags.Tier
	pro     flags.Tier
	account ids.UUID
	clock   *time.Time
}

func setup(t *testing.T, enabled ...billing.Interval) env {
	t.Helper()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	e := env{free: flags.Tier{ID: ids.New(), Key: "free", Name: "Free", Order: 10, Enabled: true},
		pro: flags.Tier{ID: ids.New(), Key: "pro", Name: "Pro", Order: 20, Enabled: true}, account: ids.New(), clock: &now}
	e.accts = accounts{e.account: e.free.ID}
	e.prov = stub.New("http://app.test").WithClock(func() time.Time { return *e.clock })
	e.store = mem.New()
	e.svc = service.New(e.store, e.accts, ladder{e.free, e.pro}, e.prov).WithClock(func() time.Time { return *e.clock })
	for _, iv := range enabled {
		amount := int64(900)
		if iv == billing.IntervalYear {
			amount = 9000
		}
		if err := e.store.SetPrice(ctx, billing.Price{TierID: e.pro.ID, Interval: iv, Provider: stub.Name, PlanRef: stub.PlanRef(iv, amount, "GBP"), AmountMinor: amount, Currency: "GBP", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e env) start(t *testing.T, iv billing.Interval) billing.Checkout {
	t.Helper()
	c, err := e.svc.Start(ctx, service.StartInput{AccountID: e.account, Email: "a@example.com", Name: "A", TierID: e.pro.ID, Interval: iv, ReturnURL: "http://app.test/back"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return c
}

func TestBillingIsOffMeansNoOffers(t *testing.T) {
	e := setup(t)
	if offers, _ := e.svc.Offers(ctx); len(offers) != 0 {
		t.Errorf("offers without prices: %+v", offers)
	}
	none := service.New(e.store, e.accts, ladder{e.free, e.pro})
	if none.Enabled() {
		t.Error("enabled with no provider")
	}
	if !e.svc.Enabled() {
		t.Error("not enabled with a provider")
	}
}

func TestOffersAreTheEnabledTiersWithEnabledPrices(t *testing.T) {
	e := setup(t, billing.IntervalMonth, billing.IntervalYear)
	offers, err := e.svc.Offers(ctx)
	if err != nil || len(offers) != 1 || offers[0].Tier.ID != e.pro.ID || len(offers[0].Prices) != 2 {
		t.Fatalf("Offers = %+v, %v", offers, err)
	}
	// A price switched off, and a price at a provider that isn't configured, aren't offered.
	p := offers[0].Prices[1]
	p.Enabled = false
	_ = e.store.SetPrice(ctx, p)
	_ = e.store.SetPrice(ctx, billing.Price{TierID: e.pro.ID, Interval: billing.IntervalMonth, Provider: "elsewhere", PlanRef: "x", AmountMinor: 1, Currency: "GBP", Enabled: true})
	offers, _ = e.svc.Offers(ctx)
	if len(offers[0].Prices) != 1 {
		t.Errorf("after switching one off: %+v", offers[0].Prices)
	}
	if _, err := e.svc.Start(ctx, service.StartInput{AccountID: e.account, TierID: e.pro.ID, Interval: p.Interval}); !errors.Is(err, billing.ErrNotOffered) {
		t.Errorf("starting a price that isn't offered = %v", err)
	}
	if _, err := e.svc.Start(ctx, service.StartInput{AccountID: e.account, TierID: e.free.ID, Interval: billing.IntervalMonth}); !errors.Is(err, billing.ErrNotOffered) {
		t.Errorf("starting a tier with no price = %v", err)
	}
}

func TestSubscribingMovesTheTierWhenTheProviderSaysPaid(t *testing.T) {
	e := setup(t, billing.IntervalMonth)
	c := e.start(t, billing.IntervalMonth)
	if c.State != billing.CheckoutPending || c.AmountMinor != 900 || c.Currency != "GBP" || c.URL == "" || c.SubscriptionRef == "" {
		t.Fatalf("checkout = %+v", c)
	}
	// Still on the hosted page: nothing changes, however often it is asked.
	if _, err := e.svc.Reconcile(ctx, stub.Name, c.SubscriptionRef); err != nil || e.accts[e.account] != e.free.ID {
		t.Fatalf("while pending: %v, tier %v", err, e.accts[e.account])
	}
	if _, err := e.svc.Current(ctx, e.account); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("a subscription before paying = %v", err)
	}
	// The customer pays; the next reconcile (a redirect, a webhook, the sweep) moves the tier.
	if err := e.prov.Complete(c.SubscriptionRef); err != nil {
		t.Fatal(err)
	}
	x, err := e.svc.Reconcile(ctx, stub.Name, c.SubscriptionRef)
	if err != nil || x.State != billing.StateActive || x.TierID != e.pro.ID || e.accts[e.account] != e.pro.ID {
		t.Fatalf("after paying: %+v, %v, tier %v", x, err, e.accts[e.account])
	}
	if got, _ := e.store.Checkout(ctx, c.ID); got.State != billing.CheckoutActivated {
		t.Errorf("checkout = %s", got.State)
	}
	if _, err := e.svc.Start(ctx, service.StartInput{AccountID: e.account, TierID: e.pro.ID, Interval: billing.IntervalMonth}); !errors.Is(err, billing.ErrAlreadySubscribed) {
		t.Errorf("subscribing twice = %v", err)
	}
}

func TestAnAbandonedCheckoutLeavesTheAccountAlone(t *testing.T) {
	e := setup(t, billing.IntervalMonth)
	c := e.start(t, billing.IntervalMonth)
	if err := e.prov.Abandon(c.SubscriptionRef); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reconcile(ctx, stub.Name, c.SubscriptionRef); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Checkout(ctx, c.ID); got.State != billing.CheckoutAbandoned || e.accts[e.account] != e.free.ID {
		t.Errorf("checkout %s, tier %v", got.State, e.accts[e.account])
	}
	if _, err := e.svc.Current(ctx, e.account); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("an abandoned checkout made a subscription: %v", err)
	}
	// And the customer can try again.
	e.start(t, billing.IntervalMonth)
}

func TestRenewalFailureGraceCancellationAndExpiry(t *testing.T) {
	e := setup(t, billing.IntervalMonth)
	c := e.start(t, billing.IntervalMonth)
	_ = e.prov.Complete(c.SubscriptionRef)
	if _, err := e.svc.Reconcile(ctx, stub.Name, c.SubscriptionRef); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour

	// A renewal is refused: the tier is kept for the grace period, then goes.
	_ = e.prov.FailRenewal(c.SubscriptionRef)
	*e.clock = e.clock.Add(31 * day) // the period is over
	if _, err := e.svc.Reconcile(ctx, stub.Name, c.SubscriptionRef); err != nil || e.accts[e.account] != e.pro.ID {
		t.Fatalf("past due inside the grace: %v, tier %v", err, e.accts[e.account])
	}
	*e.clock = e.clock.Add(billing.Grace + day)
	x, err := e.svc.Reconcile(ctx, stub.Name, c.SubscriptionRef)
	if err != nil || e.accts[e.account] != e.free.ID || x.State != billing.StatePastDue {
		t.Fatalf("past due beyond the grace: %+v, %v, tier %v", x, err, e.accts[e.account])
	}
}

func TestCancellingKeepsThePaidPeriodThenEnds(t *testing.T) {
	e := setup(t, billing.IntervalMonth)
	c := e.start(t, billing.IntervalMonth)
	_ = e.prov.Complete(c.SubscriptionRef)
	_, _ = e.svc.Reconcile(ctx, stub.Name, c.SubscriptionRef)
	if err := e.svc.Cancel(ctx, e.account); err != nil {
		t.Fatal(err)
	}
	x, _ := e.svc.Current(ctx, e.account)
	if x.CancelRequestedAt == nil || e.accts[e.account] != e.pro.ID || billing.StateOf(x, *e.clock) != billing.EffectiveCancelled {
		t.Fatalf("after cancelling: %+v, tier %v, state %s", x, e.accts[e.account], billing.StateOf(x, *e.clock))
	}
	// The period runs out: the sweep takes the tier back and ends the subscription.
	*e.clock = e.clock.Add(32 * 24 * time.Hour)
	if err := e.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if e.accts[e.account] != e.free.ID {
		t.Errorf("tier after the period: %v", e.accts[e.account])
	}
	if _, err := e.svc.Current(ctx, e.account); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("an expired subscription is still current: %v", err)
	}
	if err := e.svc.Cancel(ctx, e.account); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("cancelling nothing = %v", err)
	}
}

func TestTheSweepGivesUpUnfinishedCheckouts(t *testing.T) {
	e := setup(t, billing.IntervalMonth)
	c := e.start(t, billing.IntervalMonth)
	if err := e.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Checkout(ctx, c.ID); got.State != billing.CheckoutPending {
		t.Errorf("a fresh checkout was reaped: %s", got.State)
	}
	*e.clock = e.clock.Add(service.CheckoutTTL + time.Minute)
	if err := e.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Checkout(ctx, c.ID); got.State != billing.CheckoutExpired {
		t.Errorf("an old checkout = %s", got.State)
	}
	// A sweep also finds a payment made on the hosted page that nothing else heard about.
	d := e.start(t, billing.IntervalMonth)
	_ = e.prov.Complete(d.SubscriptionRef)
	if err := e.svc.Sweep(ctx); err != nil || e.accts[e.account] != e.pro.ID {
		t.Errorf("sweeping a paid checkout: %v, tier %v", err, e.accts[e.account])
	}
}

func TestReconcilingWhatIsUnknownIsRefused(t *testing.T) {
	e := setup(t, billing.IntervalMonth)
	if _, err := e.svc.Reconcile(ctx, stub.Name, "sub_nope"); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("an unknown ref = %v", err)
	}
	if _, err := e.svc.Reconcile(ctx, "elsewhere", "x"); !errors.Is(err, billing.ErrNoProvider) {
		t.Errorf("an unknown provider = %v", err)
	}
}
