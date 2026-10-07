package settings_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/nickwhiteley/plinth/settings"
	"github.com/nickwhiteley/plinth/settings/mem"
)

// Lifted from Bloomprint's bootstrap settings tests: the shell around the pure package.

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func zeroCipher(t *testing.T) *settings.Cipher {
	t.Helper()
	c, err := settings.NewCipher(make([]byte, settings.EncryptionKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// failing wraps a store and can be told to break, so both halves of the asymmetry can be tested:
// fatal at boot, harmless at refresh.
type failing struct {
	settings.Store
	mu   sync.Mutex
	fail bool
}

func (f *failing) Settings(ctx context.Context) ([]settings.Row, error) {
	f.mu.Lock()
	broken := f.fail
	f.mu.Unlock()
	if broken {
		return nil, errors.New("the database is unreachable")
	}
	return f.Store.Settings(ctx)
}

func (f *failing) breakIt() { f.mu.Lock(); f.fail = true; f.mu.Unlock() }

// seeded reconciles a store and writes the given key, value pairs under the zero key.
func seeded(t *testing.T, pairs ...string) (*failing, *settings.Cipher) {
	t.Helper()
	ctx := context.Background()
	m, c := mem.New(), zeroCipher(t)
	if _, err := settings.NewLive(registry(), m, c, quiet()).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		sealed, _ := c.Encrypt(pairs[i+1])
		if err := m.SetSetting(ctx, pairs[i], sealed, settings.KeyVersion); err != nil {
			t.Fatal(err)
		}
	}
	return &failing{Store: m}, c
}

func set(t *testing.T, st settings.Store, c *settings.Cipher, key, value string) {
	t.Helper()
	sealed, _ := c.Encrypt(value)
	if err := st.SetSetting(context.Background(), key, sealed, 1); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	l := settings.NewLive(registry(), mem.New(), zeroCipher(t), quiet())
	first, err := l.Reconcile(context.Background())
	if err != nil || first != len(registry().Declared()) {
		t.Fatalf("first reconcile added %d, %v", first, err)
	}
	if second, err := l.Reconcile(context.Background()); err != nil || second != 0 {
		t.Fatalf("second reconcile added %d, %v: a default would overwrite on every restart", second, err)
	}
}

func TestLoadInstallsTheStoredConfiguration(t *testing.T) {
	st, c := seeded(t, kFrom, "hello@example.com", kToken, "token")
	l := settings.NewLive(registry(), st, c, quiet())
	if err := l.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.Snapshot().String(kFrom) != "hello@example.com" || l.Snapshot().Source(kFrom) != settings.SourceRow {
		t.Fatalf("installed %q from %s", l.Snapshot().String(kFrom), l.Snapshot().Source(kFrom))
	}
}

// At boot a failure is fatal: starting on defaults would be a deployment quietly missing things.
func TestLoadFailsWhenTheStoreCannotBeRead(t *testing.T) {
	st, c := seeded(t)
	st.breakIt()
	if err := settings.NewLive(registry(), st, c, quiet()).Load(context.Background()); err == nil {
		t.Fatal("Load succeeded against an unreadable store")
	}
}

func TestLoadRefusesAnIncoherentConfiguration(t *testing.T) {
	st, c := seeded(t, kProvider, "real")
	err := settings.NewLive(registry(), st, c, quiet()).Load(context.Background())
	if !errors.Is(err, settings.ErrIncoherent) {
		t.Fatalf("Load = %v", err)
	}
}

// At refresh it's the opposite: a blip must not un-configure a server that's working.
func TestRefreshKeepsTheLastGoodConfiguration(t *testing.T) {
	st, c := seeded(t, kFrom, "hello@example.com")
	l := settings.NewLive(registry(), st, c, quiet())
	if err := l.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.breakIt()
	l.Refresh(context.Background())
	if l.Snapshot().String(kFrom) != "hello@example.com" {
		t.Fatal("a failed refresh changed the configuration")
	}
}

func TestRefreshRejectsAnIncoherentSnapshotWhole(t *testing.T) {
	st, c := seeded(t, kFrom, "hello@example.com")
	l := settings.NewLive(registry(), st, c, quiet())
	if err := l.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	set(t, st, c, kProvider, "real") // without its secret: fatal
	set(t, st, c, kFrom, "someone-else@example.com")
	l.Refresh(context.Background())
	if l.Snapshot().String(kProvider) != "" || l.Snapshot().String(kFrom) != "hello@example.com" {
		t.Errorf("part of a rejected snapshot was applied: %q, %q", l.Snapshot().String(kProvider), l.Snapshot().String(kFrom))
	}
}

func TestOnlyChangedBindingsRebuild(t *testing.T) {
	st, c := seeded(t, kFrom, "one@example.com")
	l := settings.NewLive(registry(), st, c, quiet())
	var mailer, other int
	l.Bind(
		settings.Binding{Name: "mailer", DependsOn: []string{kFrom}, Build: func(settings.Snapshot) (any, error) { mailer++; return nil, nil }, Install: func(any) {}},
		settings.Binding{Name: "other", DependsOn: []string{kContact}, Build: func(settings.Snapshot) (any, error) { other++; return nil, nil }, Install: func(any) {}},
	)
	if err := l.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.Refresh(context.Background())
	if mailer != 1 || other != 1 {
		t.Fatalf("after boot and an idle refresh: %d, %d", mailer, other)
	}
	set(t, st, c, kFrom, "two@example.com")
	l.Refresh(context.Background())
	if mailer != 2 || other != 1 {
		t.Errorf("after a change: mailer %d, other %d", mailer, other)
	}
}

func TestAFailedBuildKeepsThePreviousValue(t *testing.T) {
	st, c := seeded(t, kFrom, "one@example.com")
	l := settings.NewLive(registry(), st, c, quiet())
	installed, fail := "", false
	l.Bind(settings.Binding{Name: "mailer", DependsOn: []string{kFrom},
		Build: func(s settings.Snapshot) (any, error) {
			if fail {
				return nil, errors.New("that key is not valid")
			}
			return s.String(kFrom), nil
		},
		Install: func(v any) { installed, _ = v.(string) }})
	if err := l.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	fail = true
	set(t, st, c, kFrom, "two@example.com")
	l.Refresh(context.Background())
	if installed != "one@example.com" {
		t.Fatalf("a failed build replaced the working value with %q", installed)
	}
}

func TestAStaleKeyIsIgnored(t *testing.T) {
	st, c := seeded(t)
	sealed, _ := c.Encrypt("whatever")
	if err := st.InsertSetting(context.Background(), settings.Row{Key: "retired_long_ago", Value: sealed, KeyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	// Under another key it wouldn't even decrypt, and it still mustn't stop the load.
	if err := st.InsertSetting(context.Background(), settings.Row{Key: "retired_unreadable", Value: []byte("rubbish"), KeyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	l := settings.NewLive(registry(), st, c, quiet())
	if err := l.Load(context.Background()); err != nil {
		t.Fatalf("a stale key stopped the load: %v", err)
	}
}

func TestInstallRebindsWithoutReadingTheStore(t *testing.T) {
	st, c := seeded(t)
	l := settings.NewLive(registry(), st, c, quiet())
	installed := ""
	l.Bind(settings.Binding{Name: "app url", DependsOn: []string{settings.KeyAppURL},
		Build: func(s settings.Snapshot) (any, error) { return s.String(settings.KeyAppURL), nil }, Install: func(v any) { installed, _ = v.(string) }})
	if err := l.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.breakIt()
	l.Install(l.Snapshot().With(settings.KeyAppURL, "https://new.example"))
	if installed != "https://new.example" || l.Snapshot().String(settings.KeyAppURL) != "https://new.example" {
		t.Fatalf("installed %q", installed)
	}
}

func TestSnapshotIsSafeUnderConcurrency(t *testing.T) {
	st, c := seeded(t, kFrom, "one@example.com")
	l := settings.NewLive(registry(), st, c, quiet())
	if err := l.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_ = l.Snapshot().String(kFrom)
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				l.Refresh(context.Background())
			}
		}()
	}
	wg.Wait()
}

func TestSetWritesValidatesAndInstalls(t *testing.T) {
	ctx := context.Background()
	st, c := seeded(t)
	l := settings.NewLive(registry(), st, c, quiet())
	if err := l.Load(ctx); err != nil {
		t.Fatal(err)
	}
	// The half-filled pair is a banner, not a refusal.
	problems, err := l.Set(ctx, kFrom, "hello@example.com")
	if err != nil || len(problems) != 1 || problems[0].Fatal {
		t.Fatalf("Set = %+v, %v", problems, err)
	}
	if l.Snapshot().String(kFrom) != "hello@example.com" {
		t.Error("the write wasn't installed")
	}
	// It reached the store, encrypted.
	rows, _ := st.Settings(ctx)
	for _, r := range rows {
		if r.Key == kFrom {
			if bytes.Contains(r.Value, []byte("hello")) {
				t.Error("the stored value is plaintext")
			}
			if v, _ := c.Decrypt(r.Value); v != "hello@example.com" {
				t.Errorf("stored %q", v)
			}
		}
	}
	// Refused writes change nothing: a bad shape, an unknown key, and the guard on the switch.
	for _, w := range []struct {
		key, value string
		want       error
	}{
		{kSweep, "soon", settings.ErrNotDuration},
		{"no_such_key", "x", settings.ErrUnknownKey},
		{kProvider, "real", settings.ErrIncoherent},
	} {
		if _, err := l.Set(ctx, w.key, w.value); !errors.Is(err, w.want) {
			t.Errorf("Set(%s) = %v, want %v", w.key, err, w.want)
		}
	}
	if l.Snapshot().String(kProvider) != "" || l.Snapshot().String(kSweep) != "0" {
		t.Error("a refused write was installed")
	}
	// With the secret first, the switch is allowed.
	if _, err := l.Set(ctx, kSecret, "sk"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Set(ctx, kProvider, "real"); err != nil {
		t.Errorf("the provider with its secret: %v", err)
	}
}

func TestUnreadableRowsAreReportedAndCanBeReset(t *testing.T) {
	ctx := context.Background()
	m := mem.New()
	if _, err := settings.NewLive(registry(), m, zeroCipher(t), quiet()).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	other, _ := settings.NewCipher(bytes.Repeat([]byte{7}, settings.EncryptionKeyBytes))
	stranded := settings.NewLive(registry(), m, other, quiet())
	if err := stranded.Load(ctx); !errors.Is(err, settings.ErrUndecryptable) {
		t.Fatalf("Load under the wrong key = %v", err)
	}
	unreadable, err := stranded.Unreadable(ctx)
	if err != nil || len(unreadable) != len(registry().Declared()) {
		t.Fatalf("Unreadable = %v, %v", unreadable, err)
	}
	// One row repaired by hand is no longer reported.
	sealed, _ := other.Encrypt("put back")
	if err := m.SetSetting(ctx, kFrom, sealed, 1); err != nil {
		t.Fatal(err)
	}
	if unreadable, _ = stranded.Unreadable(ctx); len(unreadable) != len(registry().Declared())-1 {
		t.Fatalf("after a repair: %v", unreadable)
	}
	// The recovery: every unreadable row back at its default, under the current key.
	reset, err := stranded.ResetUnreadable(ctx)
	if err != nil || len(reset) != len(registry().Declared())-1 {
		t.Fatalf("ResetUnreadable = %v, %v", reset, err)
	}
	if err := stranded.Load(ctx); err != nil {
		t.Fatalf("Load after the reset: %v", err)
	}
	if stranded.Snapshot().String(kFrom) != "put back" || stranded.Snapshot().String(kStub) != "stub-webhook-secret" {
		t.Errorf("after the reset: %q, %q", stranded.Snapshot().String(kFrom), stranded.Snapshot().String(kStub))
	}
}
