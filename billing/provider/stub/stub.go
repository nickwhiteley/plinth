// Package stub is a payment provider that takes no money (spec.md §2): a subscription it starts is
// pending until a test, or a development page, completes it. It is the v1 provider until a vendor
// adapter is written, and the one every other is held against.
package stub

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/billing/provider"
)

// Name is the stub's provider key.
const Name = "stub"

// PlanRef is a plan reference this provider understands: "stub_<interval>_<amount>_<currency>".
func PlanRef(i billing.Interval, amountMinor int64, currency string) string {
	return fmt.Sprintf("stub_%s_%d_%s", i, amountMinor, currency)
}

type sub struct {
	plan     string
	state    billing.ProviderState
	end      time.Time
	canceled bool
}

// Provider is safe for concurrent use.
type Provider struct {
	mu        sync.Mutex
	now       func() time.Time
	base      string
	customers map[string]string
	subs      map[string]*sub
	n         int
}

// New returns a provider whose hosted page is at base+"/billing/stub?ref=…".
func New(base string) *Provider {
	return &Provider{now: time.Now, base: base, customers: map[string]string{}, subs: map[string]*sub{}}
}

// WithClock sets the clock. For tests.
func (p *Provider) WithClock(now func() time.Time) *Provider { p.now = now; return p }

var _ provider.Provider = (*Provider)(nil)

func (p *Provider) Name() string { return Name }

func (p *Provider) CreateCustomer(_ context.Context, c provider.Customer) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.customers[c.AccountID.String()]; ok {
		return r, nil
	}
	r := "cus_" + c.AccountID.String()[:8]
	p.customers[c.AccountID.String()] = r
	return r, nil
}

func (p *Provider) StartSubscription(_ context.Context, customerRef, planRef, returnURL string) (provider.Started, error) {
	if customerRef == "" || planRef == "" {
		return provider.Started{}, billing.ErrProvider
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	ref := fmt.Sprintf("sub_%s_%d", customerRef, p.n)
	p.subs[ref] = &sub{plan: planRef, state: billing.StatePending}
	q := url.Values{"ref": {ref}, "return": {returnURL}}
	return provider.Started{Ref: ref, CheckoutURL: p.base + "/billing/stub?" + q.Encode()}, nil
}

func (p *Provider) Fetch(_ context.Context, ref string) (billing.Fetched, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.subs[ref]
	if !ok {
		return billing.Fetched{}, billing.ErrNotFound
	}
	// An active subscription whose period has gone by renews, unless it was cancelled, when it ends.
	for s.state == billing.StateActive && !p.now().Before(s.end) {
		if s.canceled {
			s.state = billing.StateExpired
			break
		}
		s.end = s.end.AddDate(0, interval(s.plan), 0)
	}
	if s.state == billing.StateCanceled && !p.now().Before(s.end) {
		s.state = billing.StateExpired
	}
	amount, cur := parse(s.plan)
	return billing.Fetched{Ref: ref, State: s.state, CurrentPeriodEnd: s.end, PlanRef: s.plan, AmountMinor: amount, Currency: cur}, nil
}

func (p *Provider) Cancel(_ context.Context, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.subs[ref]
	if !ok {
		return billing.ErrNotFound
	}
	s.canceled = true
	if s.state == billing.StateActive {
		s.state = billing.StateCanceled
	}
	return nil
}

// Complete is the customer paying on the hosted page: the subscription becomes active for one
// interval.
func (p *Provider) Complete(ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.subs[ref]
	if !ok {
		return billing.ErrNotFound
	}
	s.state = billing.StateActive
	s.end = p.now().AddDate(0, interval(s.plan), 0)
	return nil
}

// Abandon is the customer leaving the hosted page without paying.
func (p *Provider) Abandon(ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.subs[ref]
	if !ok {
		return billing.ErrNotFound
	}
	s.state = billing.StateCanceled
	s.end = p.now()
	return nil
}

// FailRenewal is a renewal payment being refused: the subscription goes past due.
func (p *Provider) FailRenewal(ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.subs[ref]
	if !ok {
		return billing.ErrNotFound
	}
	s.state = billing.StatePastDue
	return nil
}

// interval is the months a plan runs, from "stub_<interval>_<amount>_<currency>".
func interval(plan string) int {
	if parts := strings.Split(plan, "_"); len(parts) == 4 && parts[1] == string(billing.IntervalYear) {
		return 12
	}
	return 1
}

func parse(plan string) (int64, string) {
	parts := strings.Split(plan, "_")
	if len(parts) != 4 {
		return 0, ""
	}
	amount, _ := strconv.ParseInt(parts[2], 10, 64)
	return amount, parts[3]
}
