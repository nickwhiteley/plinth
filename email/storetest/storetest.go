// Package storetest is the conformance suite every email.Store must pass (spec.md §3).
package storetest

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/email"
	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/ids"
)

// Run runs the suite. factory returns an empty store, and the fixtures (accounts) its records
// reference.
func Run(t *testing.T, factory func(t *testing.T) (email.Store, fixture.Source)) {
	for name, f := range map[string]func(*testing.T, email.Store, fixture.Source){
		"records": testRecords,
		"query":   testQuery,
		"prune":   testPrune,
	} {
		t.Run(name, func(t *testing.T) { s, fx := factory(t); f(t, s, fx) })
	}
}

func rec(at time.Time, kind email.Kind, cat email.Category, acct *ids.UUID) email.Communication {
	return email.Communication{ID: ids.NewAt(at, rand.Reader), Kind: kind, Category: cat, AccountID: acct,
		Recipient: "sam@example.com", Subject: "Hello", Body: "Hello, Sam.", Status: email.StatusQueued, Provider: "noop", CreatedAt: at}
}

func testRecords(t *testing.T, s email.Store, fx fixture.Source) {
	ctx := context.Background()
	acct := fx.Account(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := rec(now, "password_reset", email.CategoryOperational, &acct)
	c.Body, c.BodyWithheld = "", true
	c.RelatedType, c.RelatedID = "project", "prj_01M483M2YGE1CTRQSGT39SE6KV"
	if err := s.Record(ctx, c); err != nil {
		t.Fatalf("Record: %v", err)
	}
	sent := now.Add(time.Second)
	if err := s.Finish(ctx, c.ID, email.Outcome{Status: email.StatusSent, ProviderMessageID: "pm-1", At: sent}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Communications(ctx, email.Query{})
	if err != nil || len(got) != 1 {
		t.Fatalf("Communications = %+v, %v", got, err)
	}
	g := got[0]
	if g.ID != c.ID || g.Kind != "password_reset" || g.Category != email.CategoryOperational || g.AccountID == nil || *g.AccountID != acct ||
		g.Recipient != "sam@example.com" || g.Subject != "Hello" || g.Body != "" || !g.BodyWithheld || g.RelatedType != "project" ||
		g.Status != email.StatusSent || g.ProviderMessageID != "pm-1" || g.SentAt == nil || !g.SentAt.Equal(sent) || !g.CreatedAt.Equal(now) {
		t.Errorf("round trip: %+v", g)
	}
	// A failure carries its reason and no sent time.
	f := rec(now, "operator_report", email.CategoryInternal, nil)
	if err := s.Record(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, f.ID, email.Outcome{Status: email.StatusFailed, Error: "provider refused", At: now}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Communications(ctx, email.Query{Status: email.StatusFailed})
	if len(got) != 1 || got[0].Error != "provider refused" || got[0].SentAt != nil || got[0].AccountID != nil {
		t.Errorf("a failure: %+v", got)
	}
	if err := s.Finish(ctx, ids.New(), email.Outcome{Status: email.StatusSent, At: now}); !errors.Is(err, email.ErrNotFound) {
		t.Errorf("Finish(unknown) = %v", err)
	}
}

func testQuery(t *testing.T, s email.Store, fx fixture.Source) {
	ctx := context.Background()
	a, b := fx.Account(t), fx.Account(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	var all []email.Communication
	for i := range 5 {
		acct := &a
		if i%2 == 1 {
			acct = &b
		}
		c := rec(base.Add(time.Duration(i)*time.Minute), "address_changed", email.CategoryOperational, acct)
		if i == 4 {
			c.Kind, c.Category = "launch", email.CategoryMarketing
		}
		if err := s.Record(ctx, c); err != nil {
			t.Fatal(err)
		}
		all = append(all, c)
	}
	got, _ := s.Communications(ctx, email.Query{})
	if len(got) != 5 || got[0].ID != all[4].ID || got[4].ID != all[0].ID {
		t.Fatalf("newest first: %d records", len(got))
	}
	if got, _ := s.Communications(ctx, email.Query{AccountID: &b}); len(got) != 2 {
		t.Errorf("by account: %d", len(got))
	}
	if got, _ := s.Communications(ctx, email.Query{Category: email.CategoryMarketing}); len(got) != 1 || got[0].Kind != "launch" {
		t.Errorf("by category: %+v", got)
	}
	if got, _ := s.Communications(ctx, email.Query{Kind: "address_changed"}); len(got) != 4 {
		t.Errorf("by kind: %d", len(got))
	}
	// Paging: the last id of a page is the next page's cursor.
	page1, _ := s.Communications(ctx, email.Query{Limit: 2})
	page2, _ := s.Communications(ctx, email.Query{Limit: 2, Before: &page1[1].ID})
	if len(page1) != 2 || len(page2) != 2 || page2[0].ID != all[2].ID {
		t.Errorf("paging: %d then %d", len(page1), len(page2))
	}
}

func testPrune(t *testing.T, s email.Store, _ fixture.Source) {
	ctx := context.Background()
	old := time.Now().UTC().Add(-200 * 24 * time.Hour)
	for _, c := range []email.Communication{
		rec(old, "operator_report", email.CategoryInternal, nil),
		rec(old, "address_changed", email.CategoryOperational, nil),
		rec(time.Now().UTC(), "operator_report", email.CategoryInternal, nil),
	} {
		if err := s.Record(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// One category at a time, and only what's older than the cutoff.
	n, err := s.Prune(ctx, email.CategoryInternal, time.Now().Add(-email.MinRetention))
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if got, _ := s.Communications(ctx, email.Query{}); len(got) != 2 {
		t.Errorf("after pruning: %d records", len(got))
	}
	// Never what's recent.
	if _, err := s.Prune(ctx, email.CategoryOperational, time.Now().Add(-time.Hour)); !errors.Is(err, email.ErrTooRecent) {
		t.Errorf("a recent cutoff = %v", err)
	}
}
