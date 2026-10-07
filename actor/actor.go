// Package actor carries who is acting on a context (spec.md §3). The transaction helper reads it
// into app.modified_by, so the shadow log attributes every write; an in-memory store reads it to
// keep the same history. A write with no actor is honestly authorless: seeding, or a system job.
package actor

import (
	"context"

	"github.com/nickwhiteley/plinth/ids"
)

type key struct{}

// With returns a context acting as the given account.
func With(ctx context.Context, account ids.UUID) context.Context {
	return context.WithValue(ctx, key{}, account)
}

// From returns the acting account, if there is one.
func From(ctx context.Context) (ids.UUID, bool) {
	a, ok := ctx.Value(key{}).(ids.UUID)
	return a, ok && !a.IsZero()
}
