// Package billing is tiers paid for (spec.md §2): prices per tier and interval, the attempts to
// pay, and the subscriptions they become. This package decides everything that can be decided
// from data alone and imports no provider: the provider is the authority on what is charged and
// what state a subscription is in, and a decision made here can't reach it by accident.
//
// A checkout is an attempt; a subscription is a relationship. The provider's state is copied in by
// a reconcile, never written by a route, and the account's tier follows the subscription.
//
// Lifted from Bloomprint's billing, and smaller: no proration, pause or dunning yet. Those are
// additions to the Subscription's own intent fields, not changes to it.
package billing

import (
	"time"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/ids"
)

var (
	// ErrNotFound is a price, checkout or subscription that isn't stored.
	ErrNotFound = code.New("billing.not_found")
	// ErrNotOffered is a tier and interval with no enabled price.
	ErrNotOffered = code.New("billing.not_offered")
	// ErrNoProvider is a price whose provider isn't configured.
	ErrNoProvider = code.New("billing.no_provider")
	// ErrAlreadySubscribed is starting a subscription for an account that has a live one.
	ErrAlreadySubscribed = code.New("billing.already_subscribed")
	// ErrPriceInvalid is a price with no plan, a negative amount, or a malformed currency.
	ErrPriceInvalid = code.New("billing.price_invalid")
	// ErrProvider is the provider failing or answering something unintelligible.
	ErrProvider = code.New("billing.provider")
)

// Interval is how often a subscription renews.
type Interval string

const (
	IntervalMonth Interval = "month"
	IntervalYear  Interval = "year"
)

// Valid reports whether i is month or year.
func (i Interval) Valid() bool { return i == IntervalMonth || i == IntervalYear }

// ProviderState is a subscription's state in our vocabulary, not the provider's. An adapter maps
// the provider's names onto these five, and a name it can't map is an error there, because once a
// state drives a tier, one we don't understand must stop and be seen.
type ProviderState string

const (
	StatePending  ProviderState = "pending"
	StateActive   ProviderState = "active"
	StatePastDue  ProviderState = "past_due"
	StateCanceled ProviderState = "canceled"
	StateExpired  ProviderState = "expired"
)

// Valid reports whether s is one of the five.
func (s ProviderState) Valid() bool {
	switch s {
	case StatePending, StateActive, StatePastDue, StateCanceled, StateExpired:
		return true
	}
	return false
}

// EffectiveState is what a customer and an administrator are shown: the provider's truth with our
// intent laid over it. StateOf is the only thing that makes one, so everything agrees.
type EffectiveState string

const (
	EffectivePending EffectiveState = "pending"
	EffectiveActive  EffectiveState = "active"
	EffectivePastDue EffectiveState = "past_due"
	// EffectiveCancelling is a subscription the customer has asked to end, still active at the
	// provider until its period does.
	EffectiveCancelling EffectiveState = "cancelling"
	// EffectiveCancelled is the provider's canceled while the paid period still runs.
	EffectiveCancelled EffectiveState = "cancelled"
	EffectiveExpired   EffectiveState = "expired"
)

// CheckoutState is where one attempt to pay got to.
type CheckoutState string

const (
	CheckoutPending   CheckoutState = "pending"
	CheckoutActivated CheckoutState = "activated"
	// CheckoutAbandoned is the provider reporting the subscription cancelled or expired before it
	// ever became active.
	CheckoutAbandoned CheckoutState = "abandoned"
	// CheckoutExpired is an attempt nobody finished, reaped by the sweep.
	CheckoutExpired CheckoutState = "expired"
	// CheckoutFailed is an attempt the provider never answered: it corresponds to nothing there.
	CheckoutFailed CheckoutState = "failed"
)

// Price is what a tier costs at an interval. AmountMinor and Currency are what the provider's plan
// charges, so the figure shown can't disagree with the card; PlanRef is opaque to us.
type Price struct {
	TierID      ids.UUID
	Interval    Interval
	Provider    string
	PlanRef     string
	AmountMinor int64
	Currency    string
	// Enabled decides whether the price is offered. A tier withdrawn from sale must not downgrade
	// the people on it, so this is separate from the tier's own switch.
	Enabled   bool
	UpdatedAt time.Time
}

// Checkout is an attempt to subscribe. Its amount and currency are copied at the moment it
// starts: a price changed tomorrow must not rewrite what someone agreed to today.
type Checkout struct {
	ID              ids.UUID
	AccountID       ids.UUID
	Provider        string
	TierID          ids.UUID
	Interval        Interval
	AmountMinor     int64
	Currency        string
	SubscriptionRef string // empty until the provider has answered
	URL             string // the provider's hosted page, where the card is entered
	State           CheckoutState
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

// Subscription is the relationship, as we store it, in three groups: the provider's truth, which a
// reconcile overwrites and no route touches; our intent, which only our routes write; and
// bookkeeping.
type Subscription struct {
	ID        ids.UUID
	AccountID ids.UUID
	Provider  string
	Ref       string
	TierID    ids.UUID
	Interval  Interval

	State            ProviderState
	CurrentPeriodEnd time.Time
	AmountMinor      int64
	Currency         string

	CancelRequestedAt *time.Time

	LastReconciledAt time.Time
	StartedAt        time.Time
	EndedAt          *time.Time
}

// Fetched is the subscription as the provider reports it now: the second half of every reconcile.
type Fetched struct {
	Ref              string
	State            ProviderState
	CurrentPeriodEnd time.Time
	PlanRef          string
	AmountMinor      int64
	Currency         string
}

// StateOf is the state to show.
func StateOf(s Subscription, now time.Time) EffectiveState {
	paid := now.Before(s.CurrentPeriodEnd)
	switch s.State {
	case StatePending:
		return EffectivePending
	case StateActive:
		if s.CancelRequestedAt != nil {
			return EffectiveCancelling
		}
		return EffectiveActive
	case StatePastDue:
		return EffectivePastDue
	case StateCanceled:
		if paid {
			return EffectiveCancelled
		}
		return EffectiveExpired
	}
	return EffectiveExpired
}

// Grace is how long past its period's end a subscription that failed to renew keeps its tier.
const Grace = 7 * 24 * time.Hour

// Entitled reports whether a subscription still earns its tier: active (even cancelling), past due
// within the grace period, or cancelled with the paid period still running. A customer who has paid
// for a month keeps the month.
func Entitled(s Subscription, now time.Time) bool {
	switch s.State {
	case StateActive:
		return true
	case StatePastDue:
		return now.Before(s.CurrentPeriodEnd.Add(Grace))
	case StateCanceled:
		return now.Before(s.CurrentPeriodEnd)
	}
	return false
}

// TierFor is the tier a subscription puts its account on: its own while entitled, else the
// fallback (the lowest enabled tier).
func TierFor(s Subscription, now time.Time, fallback ids.UUID) ids.UUID {
	if Entitled(s, now) {
		return s.TierID
	}
	return fallback
}

// Live reports whether a subscription is one the account still has: not ended, and not a pending
// attempt that never activated.
func (s Subscription) Live(now time.Time) bool {
	return s.EndedAt == nil && (s.State == StateActive || s.State == StatePastDue || (s.State == StateCanceled && now.Before(s.CurrentPeriodEnd)))
}
