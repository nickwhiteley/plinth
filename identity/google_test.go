package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ------------------------------------------------------------ a fake Google

// fakeGoogle stands in for Google's token endpoint.
//
// It mints unsigned identity tokens, which is exactly what the real endpoint's
// response looks like to this code: the signature is not checked (see google.go
// for why), so a test double does not need a key pair to be faithful.
type fakeGoogle struct {
	t *testing.T
	// claims is what the next exchange returns, before the nonce is filled in
	// from the request. Tests mutate it to describe the case under test.
	claims map[string]any
	// status and body override the response entirely, for the refusal cases.
	status int
	body   string
	// lastForm is what the exchange posted, so the PKCE and redirect
	// parameters can be asserted.
	lastForm url.Values
	server   *httptest.Server
}

const testClientID = "test-client.apps.googleusercontent.com"

func newFakeGoogle(t *testing.T) (*Google, *fakeGoogle) {
	t.Helper()
	f := &fakeGoogle{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(f.token))
	t.Cleanup(f.server.Close)

	g, err := NewGoogle(testClientID, "test-secret", "https://app.example/auth/google/callback")
	if err != nil {
		t.Fatalf("NewGoogle: %v", err)
	}
	g.tokenEndpoint = f.server.URL
	return g, f
}

// defaults returns a claim set that passes every check.
func (f *fakeGoogle) defaults() map[string]any {
	return map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            testClientID,
		"sub":            "108000000000000000001",
		"exp":            time.Now().Add(time.Hour).Unix(),
		"email":          "sam@gmail.com",
		"email_verified": true,
		"name":           "Sam Maker",
	}
}

func (f *fakeGoogle) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		f.t.Errorf("unreadable token request: %v", err)
	}
	f.lastForm = r.PostForm

	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
		return
	}

	claims := f.claims
	if claims == nil {
		claims = f.defaults()
	}
	// Echo the nonce the flow started with, as Google does, unless the test
	// has deliberately set one.
	if _, ok := claims["nonce"]; !ok {
		claims["nonce"] = r.PostFormValue("nonce_for_test")
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		f.t.Fatalf("Marshal: %v", err)
	}
	token := "eyJhbGciOiJSUzI1NiJ9." +
		base64.RawURLEncoding.EncodeToString(payload) + ".not-a-real-signature"
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id_token":"` + token + `"}`))
}

// exchange runs a whole flow: Begin, then Exchange with the nonce Begin made.
func exchange(t *testing.T, g *Google, f *fakeGoogle) (ExternalIdentity, error) {
	t.Helper()
	p := g.Begin()
	if f.claims == nil {
		f.claims = f.defaults()
	}
	if _, ok := f.claims["nonce"]; !ok {
		f.claims["nonce"] = p.Nonce
	}
	return g.Exchange(context.Background(), "auth-code", p.Verifier, p.Nonce)
}

// ---------------------------------------------------------------- the client

func TestGoogleBeginBuildsAnAuthorisationURL(t *testing.T) {
	g, _ := newFakeGoogle(t)
	p := g.Begin()

	parsed, err := url.Parse(p.URL)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	q := parsed.Query()
	for field, want := range map[string]string{
		"client_id":             testClientID,
		"redirect_uri":          "https://app.example/auth/google/callback",
		"response_type":         "code",
		"scope":                 "openid email profile",
		"state":                 p.State,
		"nonce":                 p.Nonce,
		"code_challenge_method": "S256",
	} {
		if got := q.Get(field); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}

	// The verifier itself must never appear in the URL — the whole point of
	// PKCE is that the front channel carries only its hash.
	if strings.Contains(p.URL, p.Verifier) {
		t.Error("the PKCE verifier is in the authorisation URL")
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge") == p.Verifier {
		t.Errorf("code_challenge = %q, want the hash of the verifier", q.Get("code_challenge"))
	}

	// Two sign-ins must not share secrets, or one browser's callback could be
	// completed with another's.
	other := g.Begin()
	if other.State == p.State || other.Nonce == p.Nonce || other.Verifier == p.Verifier {
		t.Error("Begin reused a secret between sign-ins")
	}
}

func TestGoogleExchangeReturnsTheIdentity(t *testing.T) {
	g, f := newFakeGoogle(t)

	id, err := exchange(t, g, f)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if id.Subject != "108000000000000000001" {
		t.Errorf("Subject = %q", id.Subject)
	}
	if id.Email != "sam@gmail.com" || !id.EmailVerified {
		t.Errorf("email = %q verified = %v", id.Email, id.EmailVerified)
	}
	if id.Name != "Sam Maker" {
		t.Errorf("Name = %q", id.Name)
	}

	// The back channel carries the verifier, the secret and the redirect URI.
	// Dropping any of them would still work against a lenient provider and
	// would remove a protection nobody would notice was gone.
	for _, field := range []string{"code", "code_verifier", "client_secret", "redirect_uri"} {
		if f.lastForm.Get(field) == "" {
			t.Errorf("the exchange did not send %s", field)
		}
	}
	if f.lastForm.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", f.lastForm.Get("grant_type"))
	}
}

// The claim checks, one case each. Each of these is a token that is *real* —
// Google minted it — but not for this application, this sign-in, or this
// moment, which is the distinction the checks exist to draw.
func TestGoogleExchangeRefusesBadClaims(t *testing.T) {
	cases := []struct {
		name  string
		mutex func(claims map[string]any)
	}{
		{"another issuer", func(c map[string]any) { c["iss"] = "https://accounts.evil.example" }},
		{"another application", func(c map[string]any) { c["aud"] = "someone-else.apps.googleusercontent.com" }},
		{"another sign-in", func(c map[string]any) { c["nonce"] = "a nonce from an earlier flow" }},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }},
		{"no expiry at all", func(c map[string]any) { delete(c, "exp") }},
		{"no subject", func(c map[string]any) { delete(c, "sub") }},
		{"no email", func(c map[string]any) { delete(c, "email") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, f := newFakeGoogle(t)
			f.claims = f.defaults()
			tc.mutex(f.claims)

			if _, err := exchange(t, g, f); !errors.Is(err, ErrGoogleExchange) {
				t.Errorf("Exchange = %v, want ErrGoogleExchange", err)
			}
		})
	}
}

func TestGoogleExchangeRefusesARefusedCode(t *testing.T) {
	g, f := newFakeGoogle(t)
	f.status = http.StatusBadRequest
	f.body = `{"error":"invalid_grant","error_description":"Code was already redeemed."}`

	_, err := exchange(t, g, f)
	if !errors.Is(err, ErrGoogleExchange) {
		t.Fatalf("Exchange = %v, want ErrGoogleExchange", err)
	}
	// Google's own words belong in the log — they name the fault — and the
	// handler is what keeps them out of the response.
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("the error dropped Google's reason: %v", err)
	}
}

func TestGoogleExchangeNeedsEverySecret(t *testing.T) {
	g, _ := newFakeGoogle(t)
	ctx := context.Background()
	for _, tc := range []struct{ code, verifier, nonce string }{
		{"", "v", "n"}, {"c", "", "n"}, {"c", "v", ""},
	} {
		if _, err := g.Exchange(ctx, tc.code, tc.verifier, tc.nonce); !errors.Is(err, ErrGoogleExchange) {
			t.Errorf("Exchange(%q, %q, %q) = %v, want ErrGoogleExchange",
				tc.code, tc.verifier, tc.nonce, err)
		}
	}
}

// email_verified is a bool in Google's tokens and a string in the OpenID
// Connect specification. Anything else has to read as false: this one claim
// decides whether an address counts as proved.
func TestGoogleReadsEmailVerifiedInBothForms(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string false", "false", false},
		{"a number", 1, false},
		{"null", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, f := newFakeGoogle(t)
			f.claims = f.defaults()
			f.claims["email_verified"] = tc.value

			id, err := exchange(t, g, f)
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if id.EmailVerified != tc.want {
				t.Errorf("EmailVerified = %v, want %v", id.EmailVerified, tc.want)
			}
		})
	}
}

func TestNewGoogleNeedsEveryCredential(t *testing.T) {
	for _, tc := range []struct{ id, secret, redirect string }{
		{"", "s", "https://x/y"}, {"i", "", "https://x/y"}, {"i", "s", ""},
	} {
		if _, err := NewGoogle(tc.id, tc.secret, tc.redirect); !errors.Is(err, ErrGoogleUnconfigured) {
			t.Errorf("NewGoogle(%q, %q, %q) = %v, want ErrGoogleUnconfigured", tc.id, tc.secret, tc.redirect, err)
		}
	}
}
