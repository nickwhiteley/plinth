package billing

import (
	"context"

	"github.com/nickwhiteley/plinth/ids"
)

// Store persists prices, customers, checkouts and subscriptions (spec.md §3).
type Store interface {
	// Prices returns every stored price, by tier then interval.
	Prices(ctx context.Context) ([]Price, error)
	// SetPrice creates or replaces the price for (tier, interval, provider). ErrPriceInvalid for a
	// malformed one, ErrNotFound for an unknown tier.
	SetPrice(ctx context.Context, p Price) error

	// Customer returns an account's reference at a provider, and whether there is one.
	Customer(ctx context.Context, account ids.UUID, provider string) (string, bool, error)
	PutCustomer(ctx context.Context, account ids.UUID, provider, ref string) error

	CreateCheckout(ctx context.Context, c Checkout) error
	Checkout(ctx context.Context, id ids.UUID) (Checkout, error)
	// CheckoutByRef finds the checkout that started the provider's subscription.
	CheckoutByRef(ctx context.Context, provider, subscriptionRef string) (Checkout, error)
	// PendingCheckouts returns the checkouts still pending, oldest first.
	PendingCheckouts(ctx context.Context) ([]Checkout, error)
	SetCheckoutState(ctx context.Context, id ids.UUID, state CheckoutState) error

	// Subscription returns the account's current subscription: the latest that hasn't ended.
	// ErrNotFound if it has none.
	Subscription(ctx context.Context, account ids.UUID) (Subscription, error)
	SubscriptionByRef(ctx context.Context, provider, ref string) (Subscription, error)
	// PutSubscription creates or replaces a subscription by (provider, ref).
	PutSubscription(ctx context.Context, s Subscription) error
	// Subscriptions returns every subscription not ended, by account, for the sweep and the admin.
	Subscriptions(ctx context.Context) ([]Subscription, error)
}
