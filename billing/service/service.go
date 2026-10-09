// Package service is billing's shell (spec.md §2): it fetches, calls billing's decisions, and
// writes. A checkout starts a subscription at a provider; a reconcile copies the provider's state
// in and moves the account's tier to follow it. Nothing here trusts a redirect or a webhook as
// anything but a signal to reconcile: the provider is asked.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/billing/provider"
	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
)

// Accounts is how an account's tier is read and moved. A support override of the tier is the
// product's concern and isn't touched: this moves the tier the subscription decides.
type Accounts interface {
	Tier(ctx context.Context, account ids.UUID) (ids.UUID, error)
	SetTier(ctx context.Context, account, tier ids.UUID) error
}

// Ladder is the tiers.
type Ladder interface {
	Tiers(ctx context.Context) ([]flags.Tier, error)
}

// CheckoutTTL is how long an unfinished checkout waits before the sweep gives it up.
const CheckoutTTL = 2 * time.Hour

// Service is safe for concurrent use if its store and providers are.
type Service struct {
	store     billing.Store
	accounts  Accounts
	ladder    Ladder
	providers map[string]provider.Provider
	now       func() time.Time
}

// New returns a service over the given providers. With none, billing is not enabled.
func New(store billing.Store, accounts Accounts, ladder Ladder, providers ...provider.Provider) *Service {
	s := &Service{store: store, accounts: accounts, ladder: ladder, providers: map[string]provider.Provider{}, now: time.Now}
	for _, p := range providers {
		s.providers[p.Name()] = p
	}
	return s
}

// WithClock sets the clock. For tests.
func (s *Service) WithClock(now func() time.Time) *Service { s.now = now; return s }

// Enabled reports whether any provider is configured. Without one, the Upgrade link is hidden and
// support sets tiers by hand.
func (s *Service) Enabled() bool { return len(s.providers) > 0 }

// Offer is a tier that can be bought, with the prices it can be bought at.
type Offer struct {
	Tier   flags.Tier
	Prices []billing.Price
}

// Offers lists the enabled tiers with at least one enabled price at a configured provider, lowest
// first.
func (s *Service) Offers(ctx context.Context) ([]Offer, error) {
	tiers, err := s.ladder.Tiers(ctx)
	if err != nil {
		return nil, err
	}
	prices, err := s.store.Prices(ctx)
	if err != nil {
		return nil, err
	}
	var out []Offer
	for _, t := range tiers {
		if !t.Enabled {
			continue
		}
		o := Offer{Tier: t}
		for _, p := range prices {
			if p.TierID == t.ID && p.Enabled && s.providers[p.Provider] != nil {
				o.Prices = append(o.Prices, p)
			}
		}
		if len(o.Prices) > 0 {
			out = append(out, o)
		}
	}
	return out, nil
}

// StartInput is a customer choosing a tier.
type StartInput struct {
	AccountID ids.UUID
	Email     string
	Name      string
	TierID    ids.UUID
	Interval  billing.Interval
	// ReturnURL is where the provider's hosted page sends the customer back.
	ReturnURL string
}

// Start begins a subscription at the provider and records the attempt. The customer is sent to the
// checkout's URL, where the card is entered.
func (s *Service) Start(ctx context.Context, in StartInput) (billing.Checkout, error) {
	if cur, err := s.store.Subscription(ctx, in.AccountID); err == nil && cur.Live(s.now()) {
		return billing.Checkout{}, billing.ErrAlreadySubscribed
	} else if err != nil && !errors.Is(err, billing.ErrNotFound) {
		return billing.Checkout{}, err
	}
	offers, err := s.Offers(ctx)
	if err != nil {
		return billing.Checkout{}, err
	}
	var price *billing.Price
	for _, o := range offers {
		if o.Tier.ID != in.TierID {
			continue
		}
		for i := range o.Prices {
			if o.Prices[i].Interval == in.Interval {
				price = &o.Prices[i]
			}
		}
	}
	if price == nil {
		return billing.Checkout{}, billing.ErrNotOffered
	}
	p := s.providers[price.Provider]
	cust, ok, err := s.store.Customer(ctx, in.AccountID, p.Name())
	if err != nil {
		return billing.Checkout{}, err
	}
	if !ok {
		if cust, err = p.CreateCustomer(ctx, provider.Customer{AccountID: in.AccountID, Email: in.Email, Name: in.Name}); err != nil {
			return billing.Checkout{}, fmt.Errorf("%w: %v", billing.ErrProvider, err)
		}
		if err := s.store.PutCustomer(ctx, in.AccountID, p.Name(), cust); err != nil {
			return billing.Checkout{}, err
		}
	}
	started, err := p.StartSubscription(ctx, cust, price.PlanRef, in.ReturnURL)
	if err != nil {
		return billing.Checkout{}, fmt.Errorf("%w: %v", billing.ErrProvider, err)
	}
	now := s.now()
	c := billing.Checkout{ID: ids.New(), AccountID: in.AccountID, Provider: p.Name(), TierID: in.TierID, Interval: in.Interval,
		AmountMinor: price.AmountMinor, Currency: price.Currency, SubscriptionRef: started.Ref, URL: started.CheckoutURL,
		State: billing.CheckoutPending, ExpiresAt: now.Add(CheckoutTTL), CreatedAt: now}
	return c, s.store.CreateCheckout(ctx, c)
}

// lowest is the tier an account falls back to: the lowest enabled.
func (s *Service) lowest(ctx context.Context) (ids.UUID, error) {
	tiers, err := s.ladder.Tiers(ctx)
	if err != nil {
		return ids.UUID{}, err
	}
	for _, t := range tiers { // lowest first
		if t.Enabled {
			return t.ID, nil
		}
	}
	return ids.UUID{}, billing.ErrNotFound
}

// Reconcile asks the provider about a subscription, copies its state in, and moves the account's
// tier to follow it. It is what a redirect back from the hosted page, a webhook and the sweep all
// call: each is only a reason to ask.
func (s *Service) Reconcile(ctx context.Context, providerName, ref string) (billing.Subscription, error) {
	p := s.providers[providerName]
	if p == nil {
		return billing.Subscription{}, billing.ErrNoProvider
	}
	existing, subErr := s.store.SubscriptionByRef(ctx, providerName, ref)
	if subErr != nil && !errors.Is(subErr, billing.ErrNotFound) {
		return billing.Subscription{}, subErr
	}
	co, coErr := s.store.CheckoutByRef(ctx, providerName, ref)
	if coErr != nil && !errors.Is(coErr, billing.ErrNotFound) {
		return billing.Subscription{}, coErr
	}
	if subErr != nil && coErr != nil {
		return billing.Subscription{}, billing.ErrNotFound
	}
	f, err := p.Fetch(ctx, ref)
	if err != nil {
		return billing.Subscription{}, fmt.Errorf("%w: %v", billing.ErrProvider, err)
	}
	if !f.State.Valid() {
		return billing.Subscription{}, fmt.Errorf("%w: unmapped state %q", billing.ErrProvider, f.State)
	}
	now := s.now()
	if f.State == billing.StatePending {
		return existing, nil // the customer is still on the hosted page
	}
	x := existing
	if subErr != nil { // the first time it is seen beyond pending
		x = billing.Subscription{ID: ids.New(), AccountID: co.AccountID, Provider: providerName, Ref: ref, TierID: co.TierID, Interval: co.Interval, StartedAt: now}
	}
	x.State, x.CurrentPeriodEnd, x.AmountMinor, x.Currency, x.LastReconciledAt = f.State, f.CurrentPeriodEnd, f.AmountMinor, f.Currency, now
	if f.State == billing.StateExpired && x.EndedAt == nil {
		x.EndedAt = &now
	}
	// A subscription that never became active (the customer left the hosted page) leaves no trace
	// on the account: it is abandoned, not ended.
	neverActive := subErr != nil && (f.State == billing.StateCanceled || f.State == billing.StateExpired) && !now.Before(f.CurrentPeriodEnd)
	if coErr == nil && co.State == billing.CheckoutPending {
		switch {
		case neverActive:
			if err := s.store.SetCheckoutState(ctx, co.ID, billing.CheckoutAbandoned); err != nil {
				return x, err
			}
			return billing.Subscription{}, nil
		case f.State == billing.StateActive || f.State == billing.StatePastDue || f.State == billing.StateCanceled:
			if err := s.store.SetCheckoutState(ctx, co.ID, billing.CheckoutActivated); err != nil {
				return x, err
			}
		}
	}
	if err := s.store.PutSubscription(ctx, x); err != nil {
		return x, err
	}
	return x, s.applyTier(ctx, x)
}

// applyTier moves the account to the tier its subscription decides, if it isn't there.
func (s *Service) applyTier(ctx context.Context, x billing.Subscription) error {
	fallback, err := s.lowest(ctx)
	if err != nil {
		return err
	}
	want := billing.TierFor(x, s.now(), fallback)
	cur, err := s.accounts.Tier(ctx, x.AccountID)
	if err != nil {
		return err
	}
	if cur == want {
		return nil
	}
	return s.accounts.SetTier(ctx, x.AccountID, want)
}

// Current is the account's subscription, or ErrNotFound.
func (s *Service) Current(ctx context.Context, account ids.UUID) (billing.Subscription, error) {
	return s.store.Subscription(ctx, account)
}

// Cancel records the customer's wish to cancel and asks the provider to end the subscription at the
// period's end. The account keeps its tier until then.
func (s *Service) Cancel(ctx context.Context, account ids.UUID) error {
	x, err := s.store.Subscription(ctx, account)
	if err != nil {
		return err
	}
	p := s.providers[x.Provider]
	if p == nil {
		return billing.ErrNoProvider
	}
	if err := p.Cancel(ctx, x.Ref); err != nil {
		return fmt.Errorf("%w: %v", billing.ErrProvider, err)
	}
	now := s.now()
	x.CancelRequestedAt = &now
	if err := s.store.PutSubscription(ctx, x); err != nil {
		return err
	}
	_, err = s.Reconcile(ctx, x.Provider, x.Ref)
	return err
}

// Sweep reconciles every live subscription and every pending checkout, and gives up the checkouts
// nobody finished. The maintenance job calls it; one failure doesn't stop the rest, and the first
// is returned.
func (s *Service) Sweep(ctx context.Context) error {
	var first error
	note := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	subs, err := s.store.Subscriptions(ctx)
	if err != nil {
		return err
	}
	for _, x := range subs {
		_, err := s.Reconcile(ctx, x.Provider, x.Ref)
		note(err)
	}
	pending, err := s.store.PendingCheckouts(ctx)
	if err != nil {
		return err
	}
	for _, c := range pending {
		if c.SubscriptionRef != "" {
			if _, err := s.Reconcile(ctx, c.Provider, c.SubscriptionRef); err != nil {
				note(err)
			}
		}
		if now := s.now(); now.After(c.ExpiresAt) {
			if cur, err := s.store.Checkout(ctx, c.ID); err == nil && cur.State == billing.CheckoutPending {
				note(s.store.SetCheckoutState(ctx, c.ID, billing.CheckoutExpired))
			}
		}
	}
	return first
}
