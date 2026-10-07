package email_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nickwhiteley/plinth/email"
	"github.com/nickwhiteley/plinth/email/mem"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/settings"
)

// Lifted from Bloomprint's logged and postmark tests, plus the catalogue.

type sender struct {
	sent []email.Message
	id   string
	err  error
}

func (s *sender) SendWithID(_ context.Context, m email.Message) (string, error) {
	s.sent = append(s.sent, m)
	return s.id, s.err
}
func (s *sender) Send(ctx context.Context, m email.Message) error {
	_, err := s.SendWithID(ctx, m)
	return err
}
func (s *sender) Provider() string { return "fake" }

// failing is a store whose writes fail.
type failing struct{ email.Store }

func (failing) Record(context.Context, email.Communication) error {
	return errors.New("table unavailable")
}

func kinds() *email.Registry {
	return email.NewRegistry().Declare(email.Kinds).Declare(map[email.Kind]email.KindSpec{
		"launch":         {Category: email.CategoryMarketing},
		"project_shared": {Category: email.CategoryOperational},
	})
}

func only(t *testing.T, st email.Store) email.Communication {
	t.Helper()
	got, _ := st.Communications(context.Background(), email.Query{})
	if len(got) != 1 {
		t.Fatalf("%d records, want 1", len(got))
	}
	return got[0]
}

func TestLoggedRecordsASend(t *testing.T) {
	st, next := mem.New(), &sender{id: "pm-42"}
	acct := ids.New()
	l := email.Logged{Next: next, Kinds: kinds(), Store: st}
	err := l.Send(context.Background(), email.Message{Kind: "project_shared", To: "sam@example.com", Subject: "A project", TextBody: "Shared with you.",
		AccountID: &acct, RelatedType: "project", RelatedID: "prj_1"})
	if err != nil || len(next.sent) != 1 {
		t.Fatalf("Send = %v, sent %d", err, len(next.sent))
	}
	c := only(t, st)
	if c.Status != email.StatusSent || c.ProviderMessageID != "pm-42" || c.Provider != "fake" || c.Body != "Shared with you." ||
		c.Category != email.CategoryOperational || *c.AccountID != acct || c.SentAt == nil {
		t.Errorf("recorded %+v", c)
	}
}

func TestLoggedRecordsAFailure(t *testing.T) {
	st, next := mem.New(), &sender{err: errors.New("postmark said no")}
	l := email.Logged{Next: next, Kinds: kinds(), Store: st}
	if err := l.Send(context.Background(), email.Message{Kind: "project_shared", To: "sam@example.com", Subject: "x"}); err == nil {
		t.Fatal("the failure was swallowed")
	}
	if c := only(t, st); c.Status != email.StatusFailed || c.Error != "postmark said no" || c.SentAt != nil {
		t.Errorf("recorded %+v", c)
	}
}

func TestLoggedRefusesWhatIsNotDeclared(t *testing.T) {
	next := &sender{}
	l := email.Logged{Next: next, Kinds: kinds(), Store: mem.New()}
	ctx := context.Background()
	if err := l.Send(ctx, email.Message{Kind: "mystery", To: "a@example.com"}); !errors.Is(err, email.ErrUnknownKind) {
		t.Errorf("an undeclared kind = %v", err)
	}
	// Marketing goes on the broadcast stream, and nothing else does.
	if err := l.Send(ctx, email.Message{Kind: "launch", To: "a@example.com"}); !errors.Is(err, email.ErrBroadcastMismatch) {
		t.Errorf("marketing off the broadcast stream = %v", err)
	}
	if err := l.Send(ctx, email.Message{Kind: "project_shared", To: "a@example.com", Broadcast: true}); !errors.Is(err, email.ErrBroadcastMismatch) {
		t.Errorf("operational mail broadcast = %v", err)
	}
	if len(next.sent) != 0 {
		t.Error("a refused message was sent")
	}
	// With no store, kinds are still enforced.
	if err := (email.Logged{Next: next, Kinds: kinds()}).Send(ctx, email.Message{Kind: "mystery", To: "a@example.com"}); !errors.Is(err, email.ErrUnknownKind) {
		t.Errorf("with no store = %v", err)
	}
}

// A reset link in a support screen is a takeover, so a credential's body is never kept.
func TestLoggedWithholdsACredential(t *testing.T) {
	st := mem.New()
	l := email.Logged{Next: &sender{}, Kinds: kinds(), Store: st}
	if err := l.Send(context.Background(), email.Message{Kind: email.KindPasswordReset, To: "sam@example.com", Subject: "Reset", TextBody: "https://app.example/reset?token=SECRET"}); err != nil {
		t.Fatal(err)
	}
	if c := only(t, st); c.Body != "" || !c.BodyWithheld {
		t.Errorf("a credential was recorded: %+v", c)
	}
}

// Recording never stops a send: a reset link that doesn't arrive is worse than a gap in the log.
func TestLoggedStillSendsWhenTheRecordFails(t *testing.T) {
	var logged bytes.Buffer
	next := &sender{}
	l := email.Logged{Next: next, Kinds: kinds(), Store: failing{mem.New()}, Log: slog.New(slog.NewTextHandler(&logged, nil))}
	if err := l.Send(context.Background(), email.Message{Kind: email.KindPasswordReset, To: "sam@example.com", Subject: "Reset"}); err != nil {
		t.Fatalf("Send = %v", err)
	}
	if len(next.sent) != 1 {
		t.Error("the message wasn't sent")
	}
	if !strings.Contains(logged.String(), "level=ERROR") {
		t.Errorf("the gap wasn't logged as an error: %s", logged.String())
	}
	// An operator alert that can't be recorded is a warning, so it can't feed itself.
	logged.Reset()
	_ = l.Send(context.Background(), email.Message{Kind: email.KindOperatorReport, To: "ops@example.com", Subject: "x"})
	if !strings.Contains(logged.String(), "level=WARN") {
		t.Errorf("an alert's gap: %s", logged.String())
	}
}

func TestDiscardsLooksThroughTheWrapper(t *testing.T) {
	if !email.Discards(email.Logged{Next: email.NoOp{}}) || email.Discards(email.Logged{Next: &sender{}}) {
		t.Error("Discards")
	}
}

func TestEveryCategoryHasARetention(t *testing.T) {
	for k, spec := range email.Kinds {
		if email.Retention[spec.Category] < email.MinRetention {
			t.Errorf("%s: category %s keeps less than the minimum", k, spec.Category)
		}
	}
}

func TestPostmark(t *testing.T) {
	var got map[string]string
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("X-Postmark-Server-Token")
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["To"] == "refuse@example.com" {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"ErrorCode":406,"Message":"inactive recipient"}`))
			return
		}
		_, _ = w.Write([]byte(`{"MessageID":"pm-1"}`))
	}))
	defer srv.Close()
	p, err := email.NewPostmark("token", "hello@app.example", "")
	if err != nil {
		t.Fatal(err)
	}
	p.SetURLForTesting(srv.URL)
	ctx := context.Background()
	id, err := p.SendWithID(ctx, email.Message{To: "sam@example.com", Subject: "Hi", TextBody: "Hello"})
	if err != nil || id != "pm-1" || token != "token" || got["From"] != "hello@app.example" || got["MessageStream"] != "" {
		t.Fatalf("SendWithID = %q, %v; posted %v", id, err, got)
	}
	_, err = p.SendWithID(ctx, email.Message{To: "refuse@example.com", Subject: "Hi"})
	if !errors.Is(err, email.ErrProviderFailed) || !strings.Contains(err.Error(), "inactive recipient") {
		t.Errorf("a refusal = %v: want the code, with Postmark's reason for the log", err)
	}
	// Header injection is refused before anything is sent.
	if _, err := p.SendWithID(ctx, email.Message{To: "a@example.com\r\nBcc: x@evil.example", Subject: "x"}); !errors.Is(err, email.ErrInvalidAddress) {
		t.Errorf("an injected address = %v", err)
	}
	if _, err := p.SendWithID(ctx, email.Message{To: "a@example.com", Subject: "x\r\nBcc: x@evil.example"}); !errors.Is(err, email.ErrInvalidSubject) {
		t.Errorf("an injected subject = %v", err)
	}
	// Marketing needs its own stream.
	if _, err := p.SendWithID(ctx, email.Message{To: "a@example.com", Subject: "x", Broadcast: true}); !errors.Is(err, email.ErrNoBroadcastStream) {
		t.Errorf("a broadcast with no stream = %v", err)
	}
	if _, err := email.NewPostmark("", "hello@app.example", ""); !errors.Is(err, email.ErrUnconfigured) {
		t.Errorf("no key = %v", err)
	}
}

func TestSettings(t *testing.T) {
	r := settings.NewRegistry().Declare(email.Settings...).Rule(email.SettingsRule)
	half := r.Snapshot([]settings.State{{Key: email.KeyProviderKey, Value: "token"}})
	if ps := r.Validate(context.Background(), half, half); len(ps) != 1 || ps[0].Fatal {
		t.Errorf("half configured: %+v", ps)
	}
	if s, err := email.SenderFromSnapshot(half); err != nil || !email.Discards(s) {
		t.Errorf("half configured sends nowhere: %T, %v", s, err)
	}
	full := r.Snapshot([]settings.State{{Key: email.KeyProviderKey, Value: "token"}, {Key: email.KeyFrom, Value: "hello@app.example"}})
	if s, err := email.SenderFromSnapshot(full); err != nil || email.Discards(s) {
		t.Errorf("configured: %T, %v", s, err)
	}
}

func TestCatalogue(t *testing.T) {
	product := fstest.MapFS{
		"en-GB.json": {Data: []byte(`{"$schema": "https://inlang.com/schema", "email.password_reset.subject": "Reset your Furniture Magic password",
			"email.project_shared.subject": "{name} shared a project", "email.project_shared.body": "{name} shared {project} with you.\n\nOpen it: {link}"}`)},
		"en-XA.json": {Data: []byte(`{"email.project_shared.subject": "[{name} ŝĥåŕéð å þŕöĵéçţ]"}`)},
		"README.md":  {Data: []byte("not a message file")},
	}
	c, err := email.LoadCatalogue(email.Messages(), product)
	if err != nil {
		t.Fatal(err)
	}
	// The product overrides plinth's text, and plinth's fills the rest.
	r, err := c.Render("en-GB", email.KindPasswordReset, map[string]string{"email": "sam@example.com", "link": "https://app.example/reset?t=1"})
	if err != nil || r.Subject != "Reset your Furniture Magic password" || !strings.Contains(r.TextBody, "sam@example.com") {
		t.Fatalf("Render = %+v, %v", r, err)
	}
	// A locale falls back to the base locale for what it lacks.
	r, err = c.Render("en-XA", "project_shared", map[string]string{"name": "Sam", "project": "Snug", "link": "https://app.example/p/1"})
	if err != nil || r.Subject != "[Sam ŝĥåŕéð å þŕöĵéçţ]" || r.TextBody != "Sam shared Snug with you.\n\nOpen it: https://app.example/p/1" {
		t.Fatalf("en-XA = %+v, %v", r, err)
	}
	if !strings.Contains(r.HTMLBody, `<p>Sam shared Snug with you.</p>`) || !strings.Contains(r.HTMLBody, `<a href="https://app.example/p/1">`) {
		t.Errorf("HTML = %s", r.HTMLBody)
	}
	// A parameter is text, never markup: a name can't inject anything.
	r, _ = c.Render("en-GB", "project_shared", map[string]string{"name": `<script>alert(1)</script>`, "project": `"><img src=x>`, "link": `https://app.example/p/"><b>`})
	if strings.Contains(r.HTMLBody, "<script>") || strings.Contains(r.HTMLBody, "<img") || strings.Contains(r.HTMLBody, "<b>") {
		t.Errorf("unescaped: %s", r.HTMLBody)
	}
	// And a link is a web link: escaping stops it breaking out, but not a javascript: link working.
	for _, link := range []string{`javascript:alert(1)`, `data:text/html,x`, `//evil.example`, ``} {
		if _, err := c.Render("en-GB", "project_shared", map[string]string{"name": "Sam", "project": "Snug", "link": link}); !errors.Is(err, email.ErrLinkInvalid) {
			t.Errorf("link %q = %v", link, err)
		}
	}
	if _, err := c.Render("en-GB", "project_shared", map[string]string{"name": "Sam"}); !errors.Is(err, email.ErrParamMissing) {
		t.Errorf("a missing parameter = %v", err)
	}
	if _, err := c.Render("en-GB", "nothing_declared", nil); !errors.Is(err, email.ErrMessageMissing) {
		t.Errorf("a missing message = %v", err)
	}
	if missing := c.Missing("en-XA"); len(missing) == 0 {
		t.Error("Missing doesn't report the pseudo-locale's gaps")
	}
	if _, err := email.LoadCatalogue(fstest.MapFS{"en-GB.json": {Data: []byte(`{"nested": {"no": "thanks"}}`)}}); !errors.Is(err, email.ErrCatalogue) {
		t.Errorf("a nested file = %v", err)
	}
}

// plinth's own kinds render in en-GB with the parameters their callers pass.
func TestPlinthsOwnMessagesRender(t *testing.T) {
	c, err := email.LoadCatalogue(email.Messages())
	if err != nil {
		t.Fatal(err)
	}
	for k, params := range map[email.Kind]map[string]string{
		email.KindPasswordReset:  {"email": "a@example.com", "link": "https://x"},
		email.KindVerifyAddress:  {"email": "a@example.com", "link": "https://x"},
		email.KindAddressChanged: {"old_email": "a@example.com", "new_email": "b@example.com"},
	} {
		if _, err := c.Render("en-GB", k, params); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
}
