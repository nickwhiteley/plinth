// Package code is how plinth reports an error a person may see: a stable code plus parameters,
// never prose. A product renders the text in the reader's locale (spec.md §9).
package code

import (
	"maps"
	"regexp"
)

// codeRe is a code's form: lower-case words joined by underscores, optionally namespaced by
// package with dots, e.g. "token_expired" or "identity.token_expired".
var codeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// Error is an error with a stable code. Code is part of the contract with every product and is
// never renamed or reused. Params carry what a message needs, as language-neutral strings.
type Error struct {
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
}

func (e *Error) Error() string { return e.Code }

// New returns an Error with the given code and parameters, given as key, value pairs. A malformed
// code, an odd number of parameter arguments or an empty key is a programming error, and panics.
func New(c string, kv ...string) *Error {
	if !codeRe.MatchString(c) {
		panic("code: malformed code " + `"` + c + `"`)
	}
	if len(kv)%2 != 0 {
		panic("code: " + c + ": parameters must be key, value pairs")
	}
	e := &Error{Code: c}
	if len(kv) > 0 {
		e.Params = make(map[string]string, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			if kv[i] == "" {
				panic("code: " + c + ": empty parameter key")
			}
			e.Params[kv[i]] = kv[i+1]
		}
	}
	return e
}

// Is matches on the code alone, so errors.Is(err, code.New("x")) works.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// With returns a copy with one more parameter.
func (e *Error) With(k, v string) *Error {
	c := &Error{Code: e.Code, Params: maps.Clone(e.Params)}
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	c.Params[k] = v
	return c
}
