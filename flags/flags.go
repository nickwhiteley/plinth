// Package flags gates features by tier, with per-account overrides (spec.md §2). Lifted from
// Bloomprint's flags package; with quotas and usage it is one mechanism.
//
// A flag is product: what a tier includes. It is declared in code, by the package or product that
// checks it, into a Registry; its state (on, off, on from a minimum tier) is data an administrator
// changes; and an account override beats both. Resolution is pure.
package flags

import (
	"regexp"
	"sort"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/ids"
)

// Tier is a row of tier. Tiers are configuration: nothing in the source names one.
type Tier struct {
	ID    ids.UUID
	Key   string
	Name  string
	Order int
	// Enabled says whether a tier may be chosen. A disabled tier still holds its accounts and
	// still passes its quota limits up the ladder.
	Enabled bool
}

// Declaration is a flag as the source declares it.
type Declaration struct {
	Key string
	// Default is whether the flag is on when it is first reconciled, and never afterwards.
	Default bool
	// Operational marks a flag that is a switch for operators (a kill switch, a feature under
	// construction) rather than something a tier sells. A pricing page lists only the others.
	Operational bool
	// Public marks a flag a pricing page describes, with MessageID("public_name") and
	// MessageID("public_line").
	Public bool
}

// MessageID is the message a product renders for one part of a declaration: "name",
// "description", "public_name" or "public_line".
func (d Declaration) MessageID(part string) string { return "flags." + d.Key + "." + part }

// State is a row of feature_flag.
type State struct {
	Key     string
	Enabled bool
	// MinTierID is the lowest tier the flag is on for, or nil for every tier.
	MinTierID *ids.UUID
	// Retired is a stored flag the source no longer declares. It resolves off.
	Retired bool
}

var keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// ErrUnknownFlag is a key no declaration names.
var ErrUnknownFlag = code.New("flags.unknown_flag")

// Registry is the set of declared flags.
type Registry struct {
	decls []Declaration
	index map[string]int
}

func NewRegistry() *Registry { return &Registry{index: map[string]int{}} }

// Declare adds declarations. A key declared twice, or a malformed one, panics.
func (r *Registry) Declare(ds ...Declaration) *Registry {
	for _, d := range ds {
		if !keyRe.MatchString(d.Key) {
			panic("flags: malformed key " + `"` + d.Key + `"`)
		}
		if _, dup := r.index[d.Key]; dup {
			panic("flags: " + d.Key + " declared twice")
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

// Reconcile returns the declared flags with no stored state, at their defaults, sorted by key.
// Insert-only: an administrator's decision outranks the source forever.
func (r *Registry) Reconcile(stored []State) []State {
	have := map[string]bool{}
	for _, s := range stored {
		have[s.Key] = true
	}
	var missing []State
	for _, d := range r.decls {
		if !have[d.Key] {
			missing = append(missing, State{Key: d.Key, Enabled: d.Default})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Key < missing[j].Key })
	return missing
}

// Stale returns the stored keys the source no longer declares, sorted, which are then marked
// retired. Not deleted: their rows are history, and an account override may name them.
func (r *Registry) Stale(stored []State) []string {
	var out []string
	for _, s := range stored {
		if _, ok := r.index[s.Key]; !ok {
			out = append(out, s.Key)
		}
	}
	sort.Strings(out)
	return out
}

// Resolve decides one flag for an account on a tier (nil: no tier), given the ladder.
//
//  1. An override wins outright, in either direction.
//  2. A disabled flag is off whatever the tier: the kill switch outranks payment.
//  3. With no minimum, it is on.
//  4. Otherwise it is on for a tier at or above the minimum. An account with no tier, on a tier
//     the ladder doesn't hold, or against a minimum the ladder doesn't hold, fails closed.
func Resolve(s State, tiers []Tier, tier *ids.UUID, override *bool) bool {
	if override != nil {
		return *override
	}
	if !s.Enabled || s.Retired {
		return false
	}
	if s.MinTierID == nil {
		return true
	}
	min, ok := orderOf(tiers, *s.MinTierID)
	if !ok || tier == nil {
		return false
	}
	have, ok := orderOf(tiers, *tier)
	return ok && have >= min
}

func orderOf(tiers []Tier, id ids.UUID) (int, bool) {
	for _, t := range tiers {
		if t.ID == id {
			return t.Order, true
		}
	}
	return 0, false
}

// Set is every declared flag resolved for one account.
type Set map[string]bool

func (s Set) Enabled(key string) bool { return s[key] }

// ResolveAll resolves every declared flag. A declared flag with no stored state is off (nobody has
// decided it yet), and a stored flag nothing declares is absent, which is what makes deleting a
// flag from the code safe.
func (r *Registry) ResolveAll(states []State, tiers []Tier, tier *ids.UUID, overrides map[string]bool) Set {
	byKey := make(map[string]State, len(states))
	for _, s := range states {
		byKey[s.Key] = s
	}
	out := make(Set, len(r.decls))
	for _, d := range r.decls {
		s, ok := byKey[d.Key]
		if !ok {
			out[d.Key] = false
			continue
		}
		var override *bool
		if v, ok := overrides[d.Key]; ok {
			override = &v
		}
		out[d.Key] = Resolve(s, tiers, tier, override)
	}
	return out
}
