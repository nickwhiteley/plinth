// Package settings holds a deployment's configuration as declared, encrypted rows (spec.md §8).
//
// Lifted from Bloomprint's settings package and its bootstrap shell. Three rules govern what
// belongs here:
//
//   - A new setting is a declared row. It is never an environment variable.
//   - A flag is product, a setting is platform. What a tier includes is a flag; how this
//     deployment reaches the outside world is a setting.
//   - A setting is global to the deployment. Anything varying per account or per tier is not a
//     setting, which is why a quota can't be one.
//
// **Declarations are registered, not global.** Each package declares the settings it reads
// (identity its Google keys, alert its address and ceiling), the product adds its own, and the
// product builds one Registry at start-up. Everything about a value (parsing, snapshots,
// validation, redaction) is pure; Live is the shell that reads, decrypts and installs.
//
// **No prose.** A setting's name, description and consequence are message ids the product renders
// (MessageID), and every problem is a code.
package settings

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/nickwhiteley/plinth/code"
)

// Scope says which process reads a key. Not which may see it: that's Sensitive.
type Scope string

const (
	ScopeAPI Scope = "api"
	ScopeWeb Scope = "web"
)

// Kind is a value's shape, which parsing and validation are derived from.
type Kind string

const (
	KindString   Kind = "string"
	KindBool     Kind = "bool"
	KindInt      Kind = "int"
	KindDuration Kind = "duration"
	KindURL      Kind = "url"
)

// Declaration is a setting as the source declares it.
//
// Declared in code rather than created through an API: a key is referenced by code, so one that
// exists only in the database configures nothing. The admin screen changes a setting's value,
// never its existence.
type Declaration struct {
	Key   string
	Scope Scope
	Kind  Kind
	// Sensitive is about reading back, not storage: every value is encrypted. A sensitive value is
	// never returned, logged or exported; the screen shows only whether it is set.
	Sensitive bool
	// Default is the value on first insert, and never afterwards.
	Default string
	// HasConsequence marks a setting whose change breaks something outside this system, which
	// validation can't see. The product shows MessageID("consequence") as a confirmation.
	HasConsequence bool
	// Validate covers shapes Kind can't express. Optional, and pure. It returns a *code.Error.
	Validate func(string) error
}

// MessageID is the message a product renders for one part of a declaration: "name",
// "description" or "consequence". A non-sensitive consequence message may use the parameter
// "value".
func (d Declaration) MessageID(part string) string { return "settings." + d.Key + "." + part }

// State is a setting as the store holds it, decrypted: a key and a value.
type State struct {
	Key   string
	Value string
}

// Source says where a resolved value came from: a stored row, or the declared default.
type Source string

const (
	SourceRow     Source = "row"
	SourceDefault Source = "default"
)

var (
	// ErrUnknownKey is a key no declaration names.
	ErrUnknownKey = code.New("settings.unknown_key")
	// The value-shape codes carry "key" and "value".
	ErrNotBool     = code.New("settings.not_bool")
	ErrNotInt      = code.New("settings.not_int")
	ErrNotDuration = code.New("settings.not_duration")
	ErrNotURL      = code.New("settings.not_url")
	// ErrNotAddress is an address with whitespace, separators or no domain.
	ErrNotAddress = code.New("settings.not_address")
)

var keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// Registry is the set of declared settings and the rules between them.
type Registry struct {
	decls []Declaration
	index map[string]int
	rules []Rule
}

func NewRegistry() *Registry { return &Registry{index: map[string]int{}} }

// Declare adds declarations. A key declared twice, a malformed key, an unknown scope or kind, or a
// default the declaration itself refuses is a programming error and panics.
func (r *Registry) Declare(ds ...Declaration) *Registry {
	for _, d := range ds {
		_, dup := r.index[d.Key]
		switch {
		case !keyRe.MatchString(d.Key):
			panic("settings: malformed key " + `"` + d.Key + `"`)
		case dup:
			panic("settings: " + d.Key + " declared twice")
		case d.Scope != ScopeAPI && d.Scope != ScopeWeb:
			panic("settings: " + d.Key + " has no scope")
		}
		switch d.Kind {
		case KindString, KindBool, KindInt, KindDuration, KindURL:
		default:
			panic("settings: " + d.Key + " has an unknown kind")
		}
		if err := ParseValue(d, d.Default); err != nil {
			panic(fmt.Sprintf("settings: %s refuses its own default: %v", d.Key, err))
		}
		r.index[d.Key] = len(r.decls)
		r.decls = append(r.decls, d)
	}
	return r
}

// Declared lists the declarations in the order they were declared, which an admin screen groups by.
func (r *Registry) Declared() []Declaration { return append([]Declaration(nil), r.decls...) }

// Lookup returns the declaration for a key.
func (r *Registry) Lookup(key string) (Declaration, bool) {
	i, ok := r.index[key]
	if !ok {
		return Declaration{}, false
	}
	return r.decls[i], true
}

// Reconcile returns the declarations that need a row, at their declared default, sorted by key.
//
// Insert-only, and that is the point: a default applies when a key first appears and never again,
// so an administrator's value outranks the source and can't be undone by the next deploy.
func (r *Registry) Reconcile(stored []State) []State {
	have := make(map[string]bool, len(stored))
	for _, s := range stored {
		have[s.Key] = true
	}
	var missing []State
	for _, d := range r.decls {
		if !have[d.Key] {
			missing = append(missing, State{Key: d.Key, Value: d.Default})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Key < missing[j].Key })
	return missing
}

// ParseValue checks one value against its declaration's shape. An empty value is always allowed:
// absent is how nearly every setting turns its feature off. Coherence between settings is
// Validate's job.
func ParseValue(d Declaration, value string) error {
	bad := func(e *code.Error) error { return e.With("key", d.Key).With("value", value) }
	if value != "" {
		switch d.Kind {
		case KindBool:
			if _, err := strconv.ParseBool(value); err != nil {
				return bad(ErrNotBool)
			}
		case KindInt:
			if _, err := strconv.Atoi(value); err != nil {
				return bad(ErrNotInt)
			}
		case KindDuration:
			if _, err := time.ParseDuration(value); err != nil {
				return bad(ErrNotDuration)
			}
		case KindURL:
			u, err := url.Parse(value)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return bad(ErrNotURL)
			}
		}
	}
	if d.Validate != nil {
		return d.Validate(value)
	}
	return nil
}

// The settings every product has: where it lives on the web.
const (
	KeyAppURL     = "app_url"
	KeyAPIBaseURL = "api_base_url"
)

// Core declares app_url and api_base_url. A product declares them with its own.
var Core = []Declaration{
	// app_url: links in mail are built from it, and so is the Google redirect URI, which has to
	// match what is registered with Google byte for byte.
	{Key: KeyAppURL, Scope: ScopeAPI, Kind: KindURL, HasConsequence: true},
	// api_base_url: this API's own public base. Empty means the address it listens on.
	{Key: KeyAPIBaseURL, Scope: ScopeAPI, Kind: KindURL},
}
