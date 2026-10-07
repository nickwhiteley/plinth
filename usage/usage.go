// Package usage decides metering against quotas (spec.md §2). It never counts: what has been
// used is the product's to sum (assistant turns, projects owned), and this package says what the
// window is and whether the sum is allowed.
//
// With flags and quotas it is one mechanism. Bloomprint's usage package was product analytics
// (sign-ups by source, prints) and stays there.
package usage

import (
	"strconv"
	"time"

	"github.com/nickwhiteley/plinth/code"
)

var (
	// ErrQuotaExceeded is a quota at its limit. It carries "quota", "limit" and, for a quota that
	// resets, "resets_at" (RFC 3339, UTC).
	ErrQuotaExceeded = code.New("quota_exceeded")
	// ErrTimeZoneInvalid is a quota time zone Go can't load.
	ErrTimeZoneInvalid = code.New("usage.time_zone_invalid")
)

// Day is the quota day containing now: midnight to midnight in the account's quota time zone,
// which is fixed when the account is created so changing the profile's zone can't reset a quota
// early. A day the clocks change on is 23 or 25 hours long.
func Day(now time.Time, zone string) (start, end time.Time, err error) {
	if zone == "Local" || zone == "" {
		return time.Time{}, time.Time{}, ErrTimeZoneInvalid.With("time_zone", zone)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, time.Time{}, ErrTimeZoneInvalid.With("time_zone", zone)
	}
	local := now.In(loc)
	y, m, d := local.Date()
	start = time.Date(y, m, d, 0, 0, 0, 0, loc)
	end = time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	return start, end, nil
}

// Status is a quota's state for one account.
type Status struct {
	Used int64
	// Limit is nil for unlimited.
	Limit *int64
	// ResetsAt is when the window ends, zero for a quota that doesn't reset (a count of things
	// owned).
	ResetsAt time.Time
}

// Exceeded reports that the limit has been reached: nothing more may start.
func (s Status) Exceeded() bool { return s.Limit != nil && s.Used >= *s.Limit }

// Warning reports that use has reached the given share of the limit (0.8 for the 80% bar).
func (s Status) Warning(ratio float64) bool {
	return s.Limit != nil && float64(s.Used) >= ratio*float64(*s.Limit)
}

// Check refuses with ErrQuotaExceeded at the limit. Called before something starts: a turn already
// running may finish within its own ceilings.
func Check(quota string, s Status) error {
	if !s.Exceeded() {
		return nil
	}
	e := ErrQuotaExceeded.With("quota", quota).With("limit", strconv.FormatInt(*s.Limit, 10))
	if !s.ResetsAt.IsZero() {
		e = e.With("resets_at", s.ResetsAt.UTC().Format(time.RFC3339))
	}
	return e
}
