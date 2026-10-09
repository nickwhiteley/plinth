// Package mem is the in-memory billing.Store: a conformance reference and a test double.
package mem

import (
	"context"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/ids"
)

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

// Store is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	now       func() time.Time
	prices    map[string]billing.Price
	customers map[string]string
	checkouts map[ids.UUID]*billing.Checkout
	subs      map[string]*billing.Subscription // provider|ref
}

func New() *Store {
	return &Store{now: time.Now, prices: map[string]billing.Price{}, customers: map[string]string{},
		checkouts: map[ids.UUID]*billing.Checkout{}, subs: map[string]*billing.Subscription{}}
}

var _ billing.Store = (*Store)(nil)

func priceKey(p billing.Price) string {
	return p.TierID.String() + "|" + string(p.Interval) + "|" + p.Provider
}

func (s *Store) Prices(context.Context) ([]billing.Price, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []billing.Price
	for _, p := range s.prices {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return priceKey(out[i]) < priceKey(out[j]) })
	return out, nil
}

func (s *Store) SetPrice(_ context.Context, p billing.Price) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.PlanRef == "" || p.AmountMinor < 0 || !currencyRe.MatchString(p.Currency) || !p.Interval.Valid() || p.Provider == "" {
		return billing.ErrPriceInvalid
	}
	p.UpdatedAt = s.now()
	s.prices[priceKey(p)] = p
	return nil
}

func (s *Store) Customer(_ context.Context, account ids.UUID, provider string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.customers[account.String()+"|"+provider]
	return r, ok, nil
}

func (s *Store) PutCustomer(_ context.Context, account ids.UUID, provider, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customers[account.String()+"|"+provider] = ref
	return nil
}

func (s *Store) CreateCheckout(_ context.Context, c billing.Checkout) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = s.now()
	}
	s.checkouts[c.ID] = &c
	return nil
}

func (s *Store) Checkout(_ context.Context, id ids.UUID) (billing.Checkout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.checkouts[id]; ok {
		return *c, nil
	}
	return billing.Checkout{}, billing.ErrNotFound
}

func (s *Store) CheckoutByRef(_ context.Context, provider, ref string) (billing.Checkout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.checkouts {
		if c.Provider == provider && c.SubscriptionRef == ref && ref != "" {
			return *c, nil
		}
	}
	return billing.Checkout{}, billing.ErrNotFound
}

func (s *Store) PendingCheckouts(context.Context) ([]billing.Checkout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []billing.Checkout
	for _, c := range s.checkouts {
		if c.State == billing.CheckoutPending {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) SetCheckoutState(_ context.Context, id ids.UUID, state billing.CheckoutState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.checkouts[id]
	if !ok {
		return billing.ErrNotFound
	}
	c.State = state
	return nil
}

func (s *Store) Subscription(_ context.Context, account ids.UUID) (billing.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *billing.Subscription
	for _, x := range s.subs {
		if x.AccountID == account && x.EndedAt == nil && (best == nil || x.StartedAt.After(best.StartedAt)) {
			best = x
		}
	}
	if best == nil {
		return billing.Subscription{}, billing.ErrNotFound
	}
	return *best, nil
}

func (s *Store) SubscriptionByRef(_ context.Context, provider, ref string) (billing.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x, ok := s.subs[provider+"|"+ref]; ok {
		return *x, nil
	}
	return billing.Subscription{}, billing.ErrNotFound
}

func (s *Store) PutSubscription(_ context.Context, x billing.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.subs[x.Provider+"|"+x.Ref]; ok {
		x.ID = old.ID
	}
	s.subs[x.Provider+"|"+x.Ref] = &x
	return nil
}

func (s *Store) Subscriptions(context.Context) ([]billing.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []billing.Subscription
	for _, x := range s.subs {
		if x.EndedAt == nil {
			out = append(out, *x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID.String() < out[j].AccountID.String() })
	return out, nil
}
