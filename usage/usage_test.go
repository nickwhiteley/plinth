package usage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/usage"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTheQuotaDayIsMidnightToMidnightInTheAccountsZone(t *testing.T) {
	for _, c := range []struct {
		zone, now, start, end string
		hours                 float64
	}{
		// 23:30 in London in summer is still the 1st; it's 22:30 UTC.
		{"Europe/London", "2026-07-01T22:30:00Z", "2026-06-30T23:00:00Z", "2026-07-01T23:00:00Z", 24},
		// New York is a day behind at the same instant.
		{"America/New_York", "2026-07-02T01:00:00Z", "2026-07-01T04:00:00Z", "2026-07-02T04:00:00Z", 24},
		// The clocks go forward on 29 March 2026: a 23-hour day.
		{"Europe/London", "2026-03-29T12:00:00Z", "2026-03-29T00:00:00Z", "2026-03-29T23:00:00Z", 23},
		// And back on 25 October 2026: a 25-hour day.
		{"Europe/London", "2026-10-25T12:00:00Z", "2026-10-24T23:00:00Z", "2026-10-26T00:00:00Z", 25},
		{"UTC", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", 24},
	} {
		start, end, err := usage.Day(at(t, c.now), c.zone)
		if err != nil {
			t.Fatalf("%s %s: %v", c.zone, c.now, err)
		}
		if !start.Equal(at(t, c.start)) || !end.Equal(at(t, c.end)) || end.Sub(start).Hours() != c.hours {
			t.Errorf("%s at %s = %s – %s (%vh), want %s – %s", c.zone, c.now, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), end.Sub(start).Hours(), c.start, c.end)
		}
	}
	if _, _, err := usage.Day(time.Now(), "Mars/Olympus"); !errors.Is(err, usage.ErrTimeZoneInvalid) {
		t.Errorf("an unknown zone = %v", err)
	}
	if _, _, err := usage.Day(time.Now(), "Local"); !errors.Is(err, usage.ErrTimeZoneInvalid) {
		t.Errorf("the server's own zone = %v: it isn't the account's", err)
	}
}

func n(v int64) *int64 { return &v }

func TestCheck(t *testing.T) {
	resets := time.Now().Add(time.Hour)
	if err := usage.Check("tokens_daily", usage.Status{Used: 999_999, Limit: nil}); err != nil {
		t.Errorf("unlimited = %v", err)
	}
	if err := usage.Check("tokens_daily", usage.Status{Used: 99, Limit: n(100)}); err != nil {
		t.Errorf("under = %v", err)
	}
	err := usage.Check("tokens_daily", usage.Status{Used: 100, Limit: n(100), ResetsAt: resets})
	var ce *code.Error
	if !errors.Is(err, usage.ErrQuotaExceeded) || !errors.As(err, &ce) || ce.Params["quota"] != "tokens_daily" || ce.Params["limit"] != "100" || ce.Params["resets_at"] != resets.UTC().Format(time.RFC3339) {
		t.Errorf("at the cap = %v %v", err, ce)
	}
	// A count quota with no reset (projects owned) carries no reset time.
	if usage.Check("projects_max", usage.Status{Used: 3, Limit: n(3)}).(*code.Error).Params["resets_at"] != "" {
		t.Error("a count quota carried a reset time")
	}
}

func TestWarning(t *testing.T) {
	for _, c := range []struct {
		used  int64
		limit *int64
		want  bool
	}{
		{79, n(100), false},
		{80, n(100), true},
		{100, n(100), true},
		{1 << 40, nil, false}, // unlimited never warns
		{0, n(0), true},       // a cap of nothing is at its cap
	} {
		if got := (usage.Status{Used: c.used, Limit: c.limit}).Warning(0.8); got != c.want {
			t.Errorf("%d of %v = %v", c.used, c.limit, got)
		}
	}
}
