package settings_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/settings"
)

// Lifted from Bloomprint's settings tests, against a registry rather than one global list.

func TestReconcileInsertsOnlyWhatIsMissing(t *testing.T) {
	r := registry()
	// An administrator's value outranks the default forever, however odd it is.
	missing := r.Reconcile([]settings.State{{Key: kProvider, Value: ""}})
	for _, m := range missing {
		if m.Key == kProvider {
			t.Fatal("Reconcile returned a stored key; its default would overwrite an administrator's value")
		}
		d, ok := r.Lookup(m.Key)
		if !ok || m.Value != d.Default {
			t.Errorf("%s = %q, want its declared default", m.Key, m.Value)
		}
	}
	if len(missing) != len(r.Declared())-1 {
		t.Fatalf("missing = %d, want %d", len(missing), len(r.Declared())-1)
	}
	if got := len(r.Reconcile(nil)); got != len(r.Declared()) {
		t.Fatalf("Reconcile(nil) = %d", got)
	}
}

// A variable is not a setting: with a same-named variable set, the key still takes its default.
func TestTheEnvironmentIsNotRead(t *testing.T) {
	t.Setenv("PROVIDER", "real")
	t.Setenv("APP_URL", "https://from-env.example")
	snap := registry().Snapshot(nil)
	if snap.String(kProvider) != "" || snap.String(settings.KeyAppURL) != "" {
		t.Error("a value came from the environment")
	}
}

func TestSnapshotResolution(t *testing.T) {
	r := registry()
	snap := r.Snapshot([]settings.State{{Key: kProvider, Value: "real"}, {Key: kStub, Value: ""}})
	if snap.String(kProvider) != "real" || snap.Source(kProvider) != settings.SourceRow {
		t.Errorf("a row: %q from %s", snap.String(kProvider), snap.Source(kProvider))
	}
	if snap.String(kSweep) != "0" || snap.Source(kSweep) != settings.SourceDefault {
		t.Errorf("a key with no row takes its default: %q from %s", snap.String(kSweep), snap.Source(kSweep))
	}
	// An empty row beats the default: an administrator who cleared a value meant to.
	if snap.String(kStub) != "" || snap.Source(kStub) != settings.SourceRow {
		t.Errorf("a cleared row resolved to %q from %s", snap.String(kStub), snap.Source(kStub))
	}
	if snap.String("no_such_key") != "" {
		t.Error("an undeclared key resolved")
	}
	// A stored key nothing declares isn't in the snapshot.
	if r.Snapshot([]settings.State{{Key: "retired", Value: "x"}}).String("retired") != "" {
		t.Error("a stale key resolved")
	}
}

func TestSnapshotAccessors(t *testing.T) {
	r := registry()
	snap := r.Snapshot([]settings.State{{Key: kSweep, Value: "90s"}})
	if snap.Duration(kSweep) != 90*time.Second {
		t.Errorf("Duration = %v", snap.Duration(kSweep))
	}
	// Rubbish fails closed: ParseValue refuses it at the write, so anything reaching here is a bug.
	if r.Snapshot([]settings.State{{Key: kSweep, Value: "soon"}}).Duration(kSweep) != 0 {
		t.Error("rubbish read as a duration")
	}
}

func TestWithDoesNotMutateTheReceiver(t *testing.T) {
	snap := registry().Snapshot([]settings.State{{Key: settings.KeyAppURL, Value: "https://a.example"}})
	next := snap.With(settings.KeyAppURL, "https://b.example")
	if snap.String(settings.KeyAppURL) != "https://a.example" || next.String(settings.KeyAppURL) != "https://b.example" {
		t.Fatal("With mutated the receiver")
	}
}

func TestFingerprintTracksOnlyItsOwnKeys(t *testing.T) {
	base := registry().Snapshot([]settings.State{{Key: kFrom, Value: "a@example.com"}, {Key: settings.KeyAppURL, Value: "https://a.example"}})
	mailer := []string{kToken, kFrom}
	if base.Fingerprint(mailer...) != base.With(settings.KeyAppURL, "https://b.example").Fingerprint(mailer...) {
		t.Error("an unrelated change moved the fingerprint")
	}
	if base.Fingerprint(mailer...) == base.With(kFrom, "b@example.com").Fingerprint(mailer...) {
		t.Error("its own change didn't move the fingerprint")
	}
	if base.Fingerprint(mailer[0], mailer[1]) != base.Fingerprint(mailer[1], mailer[0]) {
		t.Error("the fingerprint depends on argument order")
	}
}

func TestRedactedNeverReturnsASensitiveValue(t *testing.T) {
	r := registry()
	secret, _ := r.Lookup(kSecret)
	if shown, isSet := settings.Redacted(secret, "sk_live_abcdef"); shown != "" || !isSet {
		t.Fatalf("a sensitive value: %q, set %v", shown, isSet)
	}
	plain, _ := r.Lookup(kFrom)
	if shown, _ := settings.Redacted(plain, "a@example.com"); shown != "a@example.com" {
		t.Errorf("a readable value was hidden: %q", shown)
	}
}

func TestParseValue(t *testing.T) {
	r := registry()
	url, _ := r.Lookup(settings.KeyAppURL)
	dur, _ := r.Lookup(kSweep)
	contact, _ := r.Lookup(kContact)
	boolean := settings.Declaration{Key: "flag", Kind: settings.KindBool}
	integer := settings.Declaration{Key: "count", Kind: settings.KindInt}
	for _, c := range []struct {
		d     settings.Declaration
		value string
		want  error
	}{
		{url, "https://example.com", nil},
		{url, "example.com", settings.ErrNotURL},
		{url, "/projects", settings.ErrNotURL},
		{url, "", nil}, // empty is always allowed
		{dur, "5m", nil},
		{dur, "5", settings.ErrNotDuration},
		{boolean, "yes", settings.ErrNotBool},
		{integer, "2.5", settings.ErrNotInt},
		{contact, "ops@example.com", nil},
		{contact, "ops@example.com\r\nBcc: x@evil.example", settings.ErrNotAddress},
		{contact, "nobody", settings.ErrNotAddress},
	} {
		if err := settings.ParseValue(c.d, c.value); !errors.Is(err, c.want) && !(err == nil && c.want == nil) {
			t.Errorf("%s %q = %v, want %v", c.d.Key, c.value, err, c.want)
		}
	}
}

func TestDeclareRefusesWhatCannotWork(t *testing.T) {
	for name, d := range map[string]settings.Declaration{
		"a duplicate":       settings.Core[0],
		"a malformed key":   {Key: "App URL", Scope: settings.ScopeAPI, Kind: settings.KindString},
		"no scope":          {Key: "fine", Kind: settings.KindString},
		"no kind":           {Key: "fine", Scope: settings.ScopeAPI},
		"a refused default": {Key: "fine", Scope: settings.ScopeAPI, Kind: settings.KindInt, Default: "many"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: want a panic", name)
				}
			}()
			registry().Declare(d)
		}()
	}
}

func TestMessageIDs(t *testing.T) {
	d, _ := registry().Lookup(settings.KeyAppURL)
	if d.MessageID("name") != "settings.app_url.name" || !d.HasConsequence {
		t.Errorf("%s, consequence %v", d.MessageID("name"), d.HasConsequence)
	}
}

func TestValidateRunsEveryRule(t *testing.T) {
	r := registry()
	ctx := context.Background()
	next := r.Snapshot([]settings.State{{Key: kProvider, Value: "real"}, {Key: kFrom, Value: "a@example.com"}})
	ps := r.Validate(ctx, next, next)
	p, fatal := settings.FirstFatal(ps)
	if len(ps) != 2 || !fatal || p.Key != kProvider || p.Err.Code != "test.provider_needs_secret" {
		t.Fatalf("problems %+v", ps)
	}
	if _, fatal := settings.FirstFatal(r.Validate(ctx, r.Snapshot(nil), r.Snapshot(nil))); fatal {
		t.Error("the defaults are refused")
	}
}
