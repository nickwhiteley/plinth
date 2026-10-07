package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/ids"
)

// Google sign-in, as a relying party for Google's OpenID Connect endpoints.
//
// Written here rather than pulled in as a dependency, and the whole protocol is
// the hundred lines below: an authorisation URL, a form POST to exchange the
// code, and the claims out of the identity token. golang.org/x/oauth2 plus
// go-oidc would bring a JWKS cache, key rotation and a discovery document to
// reach the same place, for one provider whose endpoints are stable and
// documented.
//
// **The identity token's signature is not verified, deliberately.** It is not
// read out of the browser's hands: it comes back over TLS from Google's token
// endpoint, in the response to a request this server made, carrying a client
// secret. Google documents that as the case where signature validation may be
// skipped, because the channel already proves the issuer. What is *not* skipped
// is the claims — issuer, audience, expiry and nonce are all checked below,
// because those say the token was minted for this application and for this
// sign-in rather than merely by Google for somebody.

// ErrGoogleUnconfigured reports that no Google client is wired. A product answers 503 rather
// than offering a button that can't work. ErrGoogleExchange and ErrGoogleUnverifiedEmail are in
// identity.go with the other codes.
var ErrGoogleUnconfigured = code.New("identity.google_unconfigured")

// Google endpoints. Constants rather than a discovery document: one fetch,
// cached and refreshed, to learn two URLs that have not moved in a decade.
const (
	googleAuthEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint = "https://oauth2.googleapis.com/token"
	googleIssuer        = "https://accounts.google.com"
)

// Google exchanges authorisation codes for identities.
type Google struct {
	clientID     string
	clientSecret string
	redirectURI  string
	// Overridable so the tests can point at a fake Google. Not configuration:
	// nothing outside a test sets them, so a deployment cannot be talked into
	// trusting somebody else's issuer.
	authEndpoint, tokenEndpoint string
	http                        *http.Client
	now                         func() time.Time
}

// NewGoogle wires a relying party.
//
// Every argument is required. A half-configured client is an error rather than
// a disabled feature, for the reason a half-configured Postmark is: falling
// back silently would mean a deployment that believes it offers Google sign-in
// and does not, which nobody notices until somebody cannot get in.
func NewGoogle(clientID, clientSecret, redirectURI string) (*Google, error) {
	switch {
	case clientID == "":
		return nil, ErrGoogleUnconfigured.With("missing", "client_id")
	case clientSecret == "":
		return nil, ErrGoogleUnconfigured.With("missing", "client_secret")
	case redirectURI == "":
		return nil, ErrGoogleUnconfigured.With("missing", "redirect_uri")
	}
	return &Google{
		clientID:      clientID,
		clientSecret:  clientSecret,
		redirectURI:   redirectURI,
		authEndpoint:  googleAuthEndpoint,
		tokenEndpoint: googleTokenEndpoint,
		// Bounded, because this call sits inside a request the user is waiting
		// on: without a timeout a slow Google holds a connection open until
		// the server's own WriteTimeout fires, and reports nothing useful.
		http: &http.Client{Timeout: 10 * time.Second},
		now:  time.Now,
	}, nil
}

// SetEndpointsForTesting points this client at a stand-in for Google.
//
// Named to be unmistakable at every call site, and nothing outside a test calls
// it: the endpoints are not configuration, so no deployment can be talked into
// trusting somebody else's issuer by setting an environment variable. The
// alternative — a test that reaches accounts.google.com — is a test that is
// skipped in CI and lying everywhere else.
func (g *Google) SetEndpointsForTesting(authURL, tokenURL string) {
	g.authEndpoint, g.tokenEndpoint = authURL, tokenURL
}

// RedirectURI is where Google is told to send the browser back to. Exposed so
// the boot log can state it: a mismatch with the console's registered URI is
// the single most common way this feature fails, and it fails at Google with a
// message the application never sees.
func (g *Google) RedirectURI() string { return g.redirectURI }

// Pending is what the browser must carry between the two legs of the flow.
//
// The three secrets go to the caller rather than into a table here, and that is
// the design: the product's frontend already owns a cookie jar, so it holds
// them in a short-lived cookie and hands them back. The alternative is
// server-side state keyed by a browser-held id, which is the same cookie with a
// database behind it.
type Pending struct {
	// URL is where to send the browser.
	URL string
	// State is echoed by Google and must match on return. It is what stops a
	// third party feeding somebody else's authorisation code to this callback.
	State string
	// Nonce is carried inside the identity token and must match on return,
	// which binds the token to this sign-in rather than to any earlier one.
	Nonce string
	// Verifier is the PKCE secret, sent only on the back channel. It proves
	// the code being redeemed is the one this flow started.
	Verifier string
}

// Begin generates a sign-in and the URL to send the browser to.
func (g *Google) Begin() Pending {
	p := Pending{State: ids.Token(), Nonce: ids.Token(), Verifier: ids.Token()}

	challenge := sha256.Sum256([]byte(p.Verifier))
	q := url.Values{
		"client_id":     {g.clientID},
		"redirect_uri":  {g.redirectURI},
		"response_type": {"code"},
		// openid for the identity token, email because matching accounts is
		// the entire point, profile for a display name. Nothing else: a scope
		// asked for is a permission the consent screen has to justify.
		"scope":                 {"openid email profile"},
		"state":                 {p.State},
		"nonce":                 {p.Nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		// Google returns a refresh token without this and we have no use for
		// one — this is sign-in, not delegated access to a mailbox.
		"access_type": {"online"},
		// Ask the account chooser to appear rather than silently reusing
		// whichever Google account the browser last used. Somebody signing in
		// on a shared machine should see whose account they are about to use.
		"prompt": {"select_account"},
	}
	p.URL = g.authEndpoint + "?" + q.Encode()
	return p
}

// ExternalIdentity is what an identity provider asserts about a person.
type ExternalIdentity struct {
	// Subject is the provider's stable identifier — Google's `sub`. Never the
	// address: see LocalIdentity.GoogleSubject.
	Subject string
	Email   string
	// EmailVerified is the provider's claim that the address is proved. The
	// account resolution refuses to run without it.
	EmailVerified bool
	// Name is a display name, and may be empty.
	Name string
}

// idToken is the subset of Google's claims this application reads.
type idToken struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Subject  string `json:"sub"`
	Expiry   int64  `json:"exp"`
	Nonce    string `json:"nonce"`

	Email string `json:"email"`
	// A bool in Google's tokens today, but OpenID Connect allows the string
	// "true", and some providers send it. json.RawMessage plus one small
	// decoder here is cheaper than discovering the difference in production,
	// where it would present as every sign-in being refused.
	EmailVerified json.RawMessage `json:"email_verified"`
	Name          string          `json:"name"`
}

// Exchange redeems an authorisation code for the identity behind it.
//
// nonce is what Begin generated for this sign-in; a token carrying anything
// else is refused, which is what stops one replayed from an earlier flow.
func (g *Google) Exchange(ctx context.Context, code, verifier, nonce string) (ExternalIdentity, error) {
	if code == "" || verifier == "" || nonce == "" {
		return ExternalIdentity{}, fmt.Errorf("%w: an incomplete callback", ErrGoogleExchange)
	}

	form := url.Values{
		"code":          {code},
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		// Sent again, and Google checks it matches the one the code was issued
		// against. It is why a code stolen in transit cannot be redeemed
		// anywhere else.
		"redirect_uri":  {g.redirectURI},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return ExternalIdentity{}, fmt.Errorf("%w: %v", ErrGoogleExchange, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := g.http.Do(req)
	if err != nil {
		return ExternalIdentity{}, fmt.Errorf("%w: %v", ErrGoogleExchange, err)
	}
	defer func() { _ = res.Body.Close() }()

	// Capped: this is a response from a remote host, and an unbounded read of
	// one is an unbounded allocation somebody else controls.
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return ExternalIdentity{}, fmt.Errorf("%w: %v", ErrGoogleExchange, err)
	}
	if res.StatusCode != http.StatusOK {
		// Google's error body names the fault — a spent code, a mismatched
		// redirect URI — and is worth carrying into the log. It never reaches
		// the user: handlers report ErrGoogleExchange's own message.
		return ExternalIdentity{}, fmt.Errorf("%w: google returned %d: %s",
			ErrGoogleExchange, res.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ExternalIdentity{}, fmt.Errorf("%w: unreadable token response: %v", ErrGoogleExchange, err)
	}
	if payload.IDToken == "" {
		return ExternalIdentity{}, fmt.Errorf("%w: no identity token in the response", ErrGoogleExchange)
	}

	claims, err := decodeIDToken(payload.IDToken)
	if err != nil {
		return ExternalIdentity{}, err
	}
	return g.identityFrom(claims, nonce)
}

// decodeIDToken reads the claims out of a JWT without verifying its signature.
// See the package comment above for why that is sound here, and only here.
func decodeIDToken(token string) (idToken, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return idToken{}, fmt.Errorf("%w: malformed identity token", ErrGoogleExchange)
	}
	// Raw encoding: JWT segments are unpadded base64url. TrimRight tolerates a
	// padded one rather than rejecting a token over a formatting detail.
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return idToken{}, fmt.Errorf("%w: unreadable identity token: %v", ErrGoogleExchange, err)
	}
	var claims idToken
	if err := json.Unmarshal(raw, &claims); err != nil {
		return idToken{}, fmt.Errorf("%w: unreadable identity claims: %v", ErrGoogleExchange, err)
	}
	return claims, nil
}

// clockSkew is how far the identity token's expiry may already be in the past.
// Small, because the token is seconds old: this covers a clock that disagrees
// with Google's, not a token being kept.
const clockSkew = 2 * time.Minute

func (g *Google) identityFrom(claims idToken, nonce string) (ExternalIdentity, error) {
	// The issuer, with and without the scheme: Google has minted both forms
	// and its own libraries accept both.
	if claims.Issuer != googleIssuer && claims.Issuer != "accounts.google.com" {
		return ExternalIdentity{}, fmt.Errorf("%w: issued by %q", ErrGoogleExchange, claims.Issuer)
	}
	// The audience is what makes this *this application's* token. Without the
	// check, a token minted for any other Google client would be accepted, and
	// those are handed out to anybody who asks for one.
	if claims.Audience != g.clientID {
		return ExternalIdentity{}, fmt.Errorf("%w: minted for another application", ErrGoogleExchange)
	}
	if claims.Nonce != nonce {
		return ExternalIdentity{}, fmt.Errorf("%w: the token belongs to a different sign-in", ErrGoogleExchange)
	}
	if claims.Expiry == 0 || time.Unix(claims.Expiry, 0).Add(clockSkew).Before(g.now()) {
		return ExternalIdentity{}, fmt.Errorf("%w: the token has expired", ErrGoogleExchange)
	}
	if claims.Subject == "" || claims.Email == "" {
		return ExternalIdentity{}, fmt.Errorf("%w: the token names no account", ErrGoogleExchange)
	}

	return ExternalIdentity{
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: truthy(claims.EmailVerified),
		Name:          strings.TrimSpace(claims.Name),
	}, nil
}

// truthy reads a claim that may be a bool or a quoted bool.
//
// Anything else — absent, null, a number — is false. Failing closed is the
// whole point: this claim decides whether an address counts as proved.
func truthy(raw json.RawMessage) bool {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == "true"
	}
	return false
}
