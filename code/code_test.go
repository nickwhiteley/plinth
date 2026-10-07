package code

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorsMatchOnCodeAlone(t *testing.T) {
	err := fmt.Errorf("signing in: %w", New("password_wrong", "attempts", "3"))
	if !errors.Is(err, New("password_wrong")) {
		t.Fatal("a wrapped error should match its code")
	}
	if errors.Is(err, New("email_unverified")) {
		t.Fatal("a different code matched")
	}
	if err.Error() != "signing in: password_wrong" {
		t.Fatalf("the message is the code, never prose: %q", err.Error())
	}
}

func TestWithDoesNotMutateTheOriginal(t *testing.T) {
	base := New("quota_exceeded")
	a := base.With("quota", "assistant_tokens")
	if base.Params != nil || a.Params["quota"] != "assistant_tokens" {
		t.Fatalf("base %v, a %v", base.Params, a.Params)
	}
	b := a.With("limit", "100000")
	if len(a.Params) != 1 || len(b.Params) != 2 {
		t.Fatalf("a %v, b %v", a.Params, b.Params)
	}
}

// A shared module's codes are a contract with every product, so a malformed code or a dangling
// parameter is a programming error, caught at the call rather than shipped.
func TestMisuseIsAProgrammingError(t *testing.T) {
	for name, f := range map[string]func(){
		"odd parameters": func() { New("token_expired", "purpose") },
		"empty code":     func() { New("") },
		"prose as code":  func() { New("Token expired") },
		"leading digit":  func() { New("2fa_required") },
		"empty key":      func() { New("token_expired", "", "reset") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: want a panic", name)
				}
			}()
			f()
		}()
	}
	New("identity.token_expired", "purpose", "reset") // a package may namespace its codes
}
