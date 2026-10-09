// Package provider is the port a payment provider sits behind (spec.md §2). The card is entered
// only on the provider's hosted page; this product never sees it. An adapter maps the provider's
// own state names onto billing's five, and refuses one it can't map.
package provider

import (
	"context"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/ids"
)

// Customer is who a provider is told about.
type Customer struct {
	AccountID ids.UUID
	Email     string
	Name      string
}

// Started is a subscription begun at the provider, and the hosted page to send the customer to.
type Started struct {
	Ref         string
	CheckoutURL string
}

// Provider is a payment provider.
type Provider interface {
	// Name is the provider's key in prices and subscriptions: lower case, e.g. "stub".
	Name() string
	// CreateCustomer makes the provider's record of an account. Asking again for the same account
	// returns the same reference.
	CreateCustomer(ctx context.Context, c Customer) (string, error)
	// StartSubscription begins a pending subscription on a plan and returns where to send the
	// customer. returnURL is where the hosted page sends them back.
	StartSubscription(ctx context.Context, customerRef, planRef, returnURL string) (Started, error)
	// Fetch is the provider's current view of a subscription.
	Fetch(ctx context.Context, ref string) (billing.Fetched, error)
	// Cancel ends a subscription at the end of the paid period.
	Cancel(ctx context.Context, ref string) error
}
