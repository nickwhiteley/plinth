package settings

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"time"
)

// Snapshot is the whole configuration as one immutable value.
//
// Immutable is what makes late binding safe: the shell installs a snapshot
// whole behind an atomic pointer, so a reader either sees all of the previous
// configuration or all of the next one, never a mixture. Nothing here is
// concurrent — concurrency belongs to the pointer that holds it.
type Snapshot struct {
	values  map[string]string
	sources map[string]Source
}

// Snapshot resolves every declared key from the rows, then the declared
// defaults. Nothing reads the environment: a value that could come from a
// variable is a setting that hasn't actually moved.
func (r *Registry) Snapshot(rows []State) Snapshot {
	stored := make(map[string]string, len(rows))
	present := make(map[string]bool, len(rows))
	for _, row := range rows {
		stored[row.Key] = row.Value
		present[row.Key] = true
	}
	s := Snapshot{
		values:  make(map[string]string, len(r.decls)),
		sources: make(map[string]Source, len(r.decls)),
	}
	for _, d := range r.decls {
		switch {
		// A row wins even when it is empty. An administrator who cleared a
		// value meant to clear it, and falling through to the default would
		// make that edit silently do nothing.
		case present[d.Key]:
			s.values[d.Key], s.sources[d.Key] = stored[d.Key], SourceRow
		default:
			s.values[d.Key], s.sources[d.Key] = d.Default, SourceDefault
		}
	}
	return s
}

// String returns a value, or "" for a key nothing declares.
func (s Snapshot) String(key string) string { return s.values[key] }

// Bool reports a value as a boolean; anything unparseable is false, because
// ParseValue has already refused it at the write and a snapshot that got here
// with rubbish in it should fail closed.
func (s Snapshot) Bool(key string) bool {
	v, err := strconv.ParseBool(s.values[key])
	return err == nil && v
}

// Int returns a value as a whole number, or zero.
func (s Snapshot) Int(key string) int {
	n, err := strconv.Atoi(s.values[key])
	if err != nil {
		return 0
	}
	return n
}

// Duration returns a value as a duration, or zero. Zero is a meaningful answer
// for every duration declared here — it disables the thing it paces.
func (s Snapshot) Duration(key string) time.Duration {
	d, err := time.ParseDuration(s.values[key])
	if err != nil {
		return 0
	}
	return d
}

// Source says where a value came from.
func (s Snapshot) Source(key string) Source { return s.sources[key] }

// With returns a copy carrying one changed value, for validating a prospective
// write without applying it. The receiver is untouched.
func (s Snapshot) With(key, value string) Snapshot {
	next := Snapshot{
		values:  make(map[string]string, len(s.values)),
		sources: make(map[string]Source, len(s.sources)),
	}
	for k, v := range s.values {
		next.values[k], next.sources[k] = v, s.sources[k]
	}
	next.values[key], next.sources[key] = value, SourceRow
	return next
}

// Fingerprint summarises the values of the named keys.
//
// What bindings compare to decide whether to rebuild. A hash rather than
// the values themselves so that nothing holds a secret longer than it has to,
// and so that a binding's identity can be logged without leaking one.
func (s Snapshot) Fingerprint(keys ...string) string {
	ordered := append([]string(nil), keys...)
	sort.Strings(ordered)
	h := sha256.New()
	for _, k := range ordered {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(s.values[k]))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Redacted returns what an administrator may see back for a key.
//
// A sensitive value is never returned, not even masked: a masked value in a
// form is a value that gets submitted back. The screen shows whether it is set
// and takes a new one.
func Redacted(d Declaration, value string) (shown string, isSet bool) {
	if d.Sensitive {
		return "", value != ""
	}
	return value, value != ""
}
