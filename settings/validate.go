package settings

import (
	"context"
	"strings"

	"github.com/nickwhiteley/plinth/code"
)

// Problem is one thing wrong with a configuration.
//
// Fatal means the snapshot must not be installed and a write producing it must be refused. A
// non-fatal problem is a banner on the admin screen: an incomplete configuration rather than an
// incoherent one, which is the normal state halfway through typing two secrets in.
type Problem struct {
	Key   string
	Err   *code.Error
	Fatal bool
}

func (p Problem) Error() string { return p.Key + ": " + p.Err.Error() }

// Rule checks the coherence of a configuration. A package registers the rules for its own
// settings, and the product the rules for its own; a rule that needs a fact from outside the
// snapshot (how many subscriptions are live) closes over whatever supplies it.
//
// current is what is installed now, and may be the zero Snapshot at boot; next is what is
// proposed. Passing the same value for both validates a snapshot on its own terms, which is what
// loading and refreshing do; the transition guards belong to the write path.
//
// **The guard sits on the switch, not on the pieces.** A half-configured provider is the
// necessary state between two saves, so the pieces are written freely and turning the feature on
// is what's refused until they're there.
type Rule func(ctx context.Context, current, next Snapshot) []Problem

// Rule registers rules.
func (r *Registry) Rule(rules ...Rule) *Registry {
	r.rules = append(r.rules, rules...)
	return r
}

// Validate runs every rule.
func (r *Registry) Validate(ctx context.Context, current, next Snapshot) []Problem {
	var problems []Problem
	for _, rule := range r.rules {
		problems = append(problems, rule(ctx, current, next)...)
	}
	return problems
}

// FirstFatal returns the problem to show a caller whose write was refused.
func FirstFatal(problems []Problem) (Problem, bool) {
	for _, p := range problems {
		if p.Fatal {
			return p, true
		}
	}
	return Problem{}, false
}

// ValidAddress is the shape check for an address setting. Not RFC 5322: what it must catch is
// what makes an address dangerous rather than merely wrong, whitespace and separators, which are
// how a header injection arrives. Empty is allowed: it turns the feature off.
func ValidAddress(key string) func(string) error {
	return func(v string) error {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		local, domain, ok := strings.Cut(v, "@")
		if strings.ContainsAny(v, " \t\r\n,;<>") || !ok || local == "" || domain == "" || !strings.Contains(domain, ".") {
			return ErrNotAddress.With("key", key).With("value", v)
		}
		return nil
	}
}
