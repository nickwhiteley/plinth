package billing_test

import (
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/billing"
	"github.com/nickwhiteley/plinth/ids"
)

var (
	now      = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	paid, fb = ids.New(), ids.New()
)

func sub(state billing.ProviderState, end time.Time) billing.Subscription {
	return billing.Subscription{TierID: paid, State: state, CurrentPeriodEnd: end}
}

func TestEntitlement(t *testing.T) {
	day := 24 * time.Hour
	for name, c := range map[string]struct {
		s    billing.Subscription
		want bool
	}{
		"active":                         {sub(billing.StateActive, now.Add(day)), true},
		"active, asked to cancel":        {sub(billing.StateActive, now.Add(day)), true},
		"past due, within the grace":     {sub(billing.StatePastDue, now.Add(-3*day)), true},
		"past due, past the grace":       {sub(billing.StatePastDue, now.Add(-8*day)), false},
		"cancelled, the period paid for": {sub(billing.StateCanceled, now.Add(day)), true},
		"cancelled, the period over":     {sub(billing.StateCanceled, now.Add(-day)), false},
		"pending":                        {sub(billing.StatePending, now.Add(day)), false},
		"expired":                        {sub(billing.StateExpired, now.Add(day)), false},
	} {
		if got := billing.Entitled(c.s, now); got != c.want {
			t.Errorf("%s: Entitled = %v, want %v", name, got, c.want)
		}
		want := fb
		if c.want {
			want = paid
		}
		if got := billing.TierFor(c.s, now, fb); got != want {
			t.Errorf("%s: TierFor = %v", name, got)
		}
	}
}

func TestStateOfFoldsIntentOverTruth(t *testing.T) {
	cancel := now
	active := sub(billing.StateActive, now.Add(time.Hour))
	cancelling := active
	cancelling.CancelRequestedAt = &cancel
	for name, c := range map[string]struct {
		s    billing.Subscription
		want billing.EffectiveState
	}{
		"active":                {active, billing.EffectiveActive},
		"asked to cancel":       {cancelling, billing.EffectiveCancelling},
		"past due":              {sub(billing.StatePastDue, now), billing.EffectivePastDue},
		"cancelled, still paid": {sub(billing.StateCanceled, now.Add(time.Hour)), billing.EffectiveCancelled},
		"cancelled, over":       {sub(billing.StateCanceled, now.Add(-time.Hour)), billing.EffectiveExpired},
		"pending":               {sub(billing.StatePending, now), billing.EffectivePending},
		"expired":               {sub(billing.StateExpired, now), billing.EffectiveExpired},
	} {
		if got := billing.StateOf(c.s, now); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
	if !billing.IntervalMonth.Valid() || !billing.IntervalYear.Valid() || billing.Interval("week").Valid() {
		t.Error("intervals")
	}
	if billing.ProviderState("paused").Valid() {
		t.Error("an unmapped state is valid")
	}
}
