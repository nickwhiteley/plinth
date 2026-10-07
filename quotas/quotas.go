// Package quotas limits what an account may have or use, by tier, with per-account overrides
// (spec.md §2). Lifted from Bloomprint's quotas package; with flags and usage it is one mechanism.
//
// A quota is declared in code by the package or product that enforces it. Its limit resolves:
//  1. a live per-account override, set by support, audited, and able to expire
//  2. the account's tier, or the nearest tier below it that sets one (never one above)
//  3. the declared default
//  4. unlimited
//
// Downgrades take nothing away: going over a limit by moving down a tier deletes and locks
// nothing; only making another is refused.
package quotas

import (
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/flags"
	"github.com/nickwhiteley/plinth/ids"
)

// Unit is what a quota counts.
type Unit string

const (
	UnitCount  Unit = "count"
	UnitTokens Unit = "tokens"
)

// Declaration is a quota as the source declares it.
type Declaration struct {
	Key  string
	Unit Unit
	// Default is the limit when no tier sets one, nil for unlimited.
	Default *int64
	// Minimum is the lowest limit an administrator may set: a limit of zero on a promise (a reply
	// time in days, say) would be nonsense.
	Minimum int64
}

// MessageID is the message a product renders for one part of a declaration: "name",
// "description", or "public", which takes the parameter "limit" (empty for unlimited).
func (d Declaration) MessageID(part string) string { return "quotas." + d.Key + "." + part }

// Accepts reports whether an administrator may set this limit. Unlimited is acceptable only for a
// quota whose default is unlimited.
func (d Declaration) Accepts(limit *int64) bool {
	if limit == nil {
		return d.Default == nil
	}
	return *limit >= d.Minimum && *limit >= 0
}

// KeyState is a row of quota_key.
type KeyState struct {
	Key     string
	Unit    Unit
	Retired bool
}

// TierLimit is a row of tier_quota. A nil Limit is an explicit "unlimited", which beats a number
// inherited from below.
type TierLimit struct {
	TierID ids.UUID
	Key    string
	Limit  *int64
}

// Override is a row of account_quota: support's adjustment for one account.
type Override struct {
	AccountID ids.UUID
	Key       string
	// Limit is nil for unlimited.
	Limit  *int64
	Reason string
	// ExpiresAt is nil for "until removed".
	ExpiresAt *time.Time
	GrantedBy ids.UUID
	GrantedAt time.Time
}

// Live reports whether the override still applies.
func (o Override) Live(now time.Time) bool { return o.ExpiresAt == nil || now.Before(*o.ExpiresAt) }

var (
	ErrUnknownQuota = code.New("quotas.unknown_quota")
	// ErrNotFound is a quota, tier or override that isn't stored.
	ErrNotFound = code.New("quotas.not_found")
	// ErrLimitInvalid is a limit the declaration doesn't accept. It carries "key" and "minimum".
	ErrLimitInvalid = code.New("quotas.limit_invalid")
	// ErrReasonInvalid is an override without a reason, or one over 500 characters.
	ErrReasonInvalid = code.New("quotas.reason_invalid")
	// ErrGrantorRequired is an override that doesn't say who granted it.
	ErrGrantorRequired = code.New("quotas.grantor_required")
)

var keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// Registry is the set of declared quotas.
type Registry struct {
	decls []Declaration
	index map[string]int
}

func NewRegistry() *Registry { return &Registry{index: map[string]int{}} }

// Declare adds declarations. A duplicate, a malformed key, an unknown unit, or a default the
// declaration refuses panics.
func (r *Registry) Declare(ds ...Declaration) *Registry {
	for _, d := range ds {
		if _, dup := r.index[d.Key]; dup || !keyRe.MatchString(d.Key) {
			panic("quotas: " + d.Key + " is declared twice or malformed")
		}
		if d.Unit != UnitCount && d.Unit != UnitTokens {
			panic("quotas: " + d.Key + " has an unknown unit")
		}
		if d.Default != nil && !d.Accepts(d.Default) {
			panic("quotas: " + d.Key + " refuses its own default")
		}
		r.index[d.Key] = len(r.decls)
		r.decls = append(r.decls, d)
	}
	return r
}

func (r *Registry) Declared() []Declaration { return append([]Declaration(nil), r.decls...) }

func (r *Registry) Lookup(key string) (Declaration, bool) {
	i, ok := r.index[key]
	if !ok {
		return Declaration{}, false
	}
	return r.decls[i], true
}

// Reconcile returns the declared quotas with no key row, sorted by key.
func (r *Registry) Reconcile(stored []KeyState) []KeyState {
	have := map[string]bool{}
	for _, s := range stored {
		have[s.Key] = true
	}
	var missing []KeyState
	for _, d := range r.decls {
		if !have[d.Key] {
			missing = append(missing, KeyState{Key: d.Key, Unit: d.Unit})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Key < missing[j].Key })
	return missing
}

// Stale returns the stored keys the source no longer declares, sorted.
func (r *Registry) Stale(stored []KeyState) []string {
	var out []string
	for _, s := range stored {
		if _, ok := r.index[s.Key]; !ok {
			out = append(out, s.Key)
		}
	}
	sort.Strings(out)
	return out
}

// Check validates a limit an administrator is setting, for a tier or an override.
func (d Declaration) Check(limit *int64) error {
	if !d.Accepts(limit) {
		return ErrLimitInvalid.With("key", d.Key).With("minimum", strconv.FormatInt(d.Minimum, 10))
	}
	return nil
}

// Limit resolves one quota for an account. tiers is the ladder in any order; tier is the account's
// effective tier, nil for none. An unknown or missing tier resolves as the lowest enabled one.
func Limit(d Declaration, rows []TierLimit, tiers []flags.Tier, tier *ids.UUID, override *Override, now time.Time) *int64 {
	if override != nil && override.Key == d.Key && override.Live(now) {
		return override.Limit
	}
	ordered := append([]flags.Tier(nil), tiers...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	at := -1
	if tier != nil {
		for i, t := range ordered {
			if t.ID == *tier {
				at = i
				break
			}
		}
	}
	if at < 0 {
		for i, t := range ordered {
			if t.Enabled {
				at = i
				break
			}
		}
	}
	if at < 0 {
		return d.Default
	}
	byTier := map[ids.UUID]TierLimit{}
	for _, r := range rows {
		if r.Key == d.Key {
			byTier[r.TierID] = r
		}
	}
	for i := at; i >= 0; i-- {
		if r, ok := byTier[ordered[i].ID]; ok {
			return r.Limit
		}
	}
	return d.Default
}

// Allows reports whether one more may be had under a limit, with used already had.
func Allows(limit *int64, used int64) bool { return limit == nil || used < *limit }
