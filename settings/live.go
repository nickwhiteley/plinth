package settings

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickwhiteley/plinth/code"
)

// RefreshInterval bounds how stale a setting can be. A constant, and deliberately not itself a
// setting: a value governing how settings refresh could be set to never and lock the mechanism
// out of its own correction.
const RefreshInterval = 3 * time.Minute

// ErrIncoherent is a write, or a stored configuration, that a rule refuses. It carries "key" and
// "problem", the refusing problem's code; the problem itself is returned alongside.
var ErrIncoherent = code.New("settings.incoherent")

// Binding rebuilds one dependency when the settings it reads change, which is what makes "takes
// effect within three minutes" true: the wiring is rebuilt, not just the values. Only a binding
// whose own inputs moved is rebuilt, so a change to one setting doesn't reconnect the mailer.
type Binding struct {
	// Name appears in the log and nowhere else.
	Name string
	// DependsOn are the keys whose values decide whether Build runs again.
	DependsOn []string
	// Build turns a snapshot into the dependency, or nil for "unavailable". A failed build leaves
	// the previous value installed: a mistyped key must not remove a mailer that's working.
	Build   func(Snapshot) (any, error)
	Install func(any)

	fingerprint string
	built       bool
}

// Live is the configuration in use: a snapshot behind an atomic pointer, the bindings that follow
// it, and the loop that keeps both current. It reads, decrypts and installs; it decides nothing.
type Live struct {
	reg    *Registry
	store  Store
	cipher *Cipher
	log    *slog.Logger

	snap atomic.Pointer[Snapshot]

	mu       sync.Mutex
	bindings []*Binding
}

// NewLive builds the live configuration. A nil store is legitimate: every key then resolves to its
// declared default.
func NewLive(reg *Registry, store Store, cipher *Cipher, log *slog.Logger) *Live {
	l := &Live{reg: reg, store: store, cipher: cipher, log: log}
	empty := reg.Snapshot(nil)
	l.snap.Store(&empty)
	return l
}

// Snapshot returns the installed configuration.
func (l *Live) Snapshot() Snapshot { return *l.snap.Load() }

// Bind registers dependencies to rebuild when their settings change. Register before Load, so
// boot wires everything once.
func (l *Live) Bind(bs ...Binding) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range bs {
		b := bs[i]
		l.bindings = append(l.bindings, &b)
	}
}

// Reconcile inserts the declared keys that aren't stored yet, at their defaults, and reports how
// many. Insert-only, so it's safe on every boot.
func (l *Live) Reconcile(ctx context.Context) (int, error) {
	if l.store == nil {
		return 0, nil
	}
	rows, err := l.store.Settings(ctx)
	if err != nil {
		return 0, err
	}
	stored := make([]State, len(rows))
	for i, r := range rows {
		stored[i] = State{Key: r.Key}
	}
	missing := l.reg.Reconcile(stored)
	for _, m := range missing {
		sealed, err := l.cipher.Encrypt(m.Value)
		if err != nil {
			return 0, err
		}
		if err := l.store.InsertSetting(ctx, Row{Key: m.Key, Value: sealed, KeyVersion: KeyVersion}); err != nil {
			return 0, err
		}
	}
	return len(missing), nil
}

// Load reads, validates and installs the configuration, then rebinds. Called once at boot, where
// any failure is fatal: starting on defaults would be a deployment that is up, answering, and
// quietly missing email and sign-in.
//
// Validated on its own terms (current and next the same), so only incoherence fires and not the
// transition guards, which belong to the write path.
func (l *Live) Load(ctx context.Context) error {
	rows, _, err := l.read(ctx, false)
	if err != nil {
		return err
	}
	next := l.reg.Snapshot(rows)
	problems := l.reg.Validate(ctx, next, next)
	if p, fatal := FirstFatal(problems); fatal {
		return ErrIncoherent.With("key", p.Key).With("problem", p.Err.Code)
	}
	for _, p := range problems {
		l.log.Warn("configuration incomplete", "setting", p.Key, "problem", p.Err.Code)
	}
	l.install(next)
	return nil
}

// Refresh re-reads and re-installs, keeping the last good configuration on any failure: at boot a
// failure means the deployment isn't what it claims to be, and at refresh a database blip must
// not un-configure a server that's working. An incoherent snapshot is rejected whole, so one bad
// row can't take a feature down by way of a coherent-looking neighbour.
func (l *Live) Refresh(ctx context.Context) {
	rows, _, err := l.read(ctx, false)
	if err != nil {
		l.log.Warn("settings refresh failed; keeping the last good configuration", "err", err)
		return
	}
	next := l.reg.Snapshot(rows)
	if p, fatal := FirstFatal(l.reg.Validate(ctx, next, next)); fatal {
		l.log.Warn("the stored configuration is not coherent; keeping the last good one", "setting", p.Key, "problem", p.Err.Code)
		return
	}
	l.install(next)
}

// Start refreshes every RefreshInterval until the returned stop is called or ctx ends. It's the
// one goroutine this package starts, and only when the product asks.
func (l *Live) Start(ctx context.Context) (stop func()) {
	if l.store == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(RefreshInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.Refresh(ctx)
			}
		}
	}()
	return cancel
}

// Install replaces the snapshot and rebinds without reading the store. Set calls it, so whoever
// just changed a setting sees it at once; other instances converge on the timer.
func (l *Live) Install(next Snapshot) { l.install(next) }

func (l *Live) install(next Snapshot) {
	l.snap.Store(&next)
	l.rebind(next)
}

func (l *Live) rebind(snap Snapshot) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.bindings {
		fp := snap.Fingerprint(b.DependsOn...)
		if b.built && fp == b.fingerprint {
			continue
		}
		built, err := b.Build(snap)
		if err != nil {
			l.log.Warn("keeping the previous configuration for this dependency", "binding", b.Name, "err", err)
			continue
		}
		b.Install(built)
		b.fingerprint, b.built = fp, true
	}
}

// Set validates and writes one value, then installs the result. It returns the non-fatal problems
// the new configuration still has, for a banner. A refused write changes nothing.
func (l *Live) Set(ctx context.Context, key, value string) ([]Problem, error) {
	d, ok := l.reg.Lookup(key)
	if !ok {
		return nil, ErrUnknownKey.With("key", key)
	}
	if err := ParseValue(d, value); err != nil {
		return nil, err
	}
	current := l.Snapshot()
	next := current.With(key, value)
	problems := l.reg.Validate(ctx, current, next)
	if p, fatal := FirstFatal(problems); fatal {
		return problems, ErrIncoherent.With("key", p.Key).With("problem", p.Err.Code)
	}
	sealed, err := l.cipher.Encrypt(value)
	if err != nil {
		return nil, err
	}
	if err := l.store.SetSetting(ctx, key, sealed, KeyVersion); err != nil {
		return nil, err
	}
	l.install(next)
	return problems, nil
}

// Unreadable returns the declared keys whose stored value won't decrypt. For recovery only:
// everywhere else an undecryptable row is fatal, because carrying on would run on a default nobody
// chose while the screen showed something else.
func (l *Live) Unreadable(ctx context.Context) ([]string, error) {
	_, unreadable, err := l.read(ctx, true)
	return unreadable, err
}

// ResetUnreadable rewrites every unreadable row at its declared default under the current key, and
// returns the keys it reset. The recovery for a lost or changed encryption key: the values then
// have to be entered again.
func (l *Live) ResetUnreadable(ctx context.Context) ([]string, error) {
	keys, err := l.Unreadable(ctx)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		d, _ := l.reg.Lookup(k)
		sealed, err := l.cipher.Encrypt(d.Default)
		if err != nil {
			return nil, err
		}
		if err := l.store.SetSetting(ctx, k, sealed, KeyVersion); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// read decrypts the stored rows of declared keys. A stale key (stored, no longer declared) is
// skipped rather than decrypted: a value nothing reads shouldn't be in memory. With tolerate
// false, every path the server takes, an undecryptable row is fatal; with tolerate true it's
// reported.
func (l *Live) read(ctx context.Context, tolerate bool) ([]State, []string, error) {
	if l.store == nil {
		return nil, nil, nil
	}
	rows, err := l.store.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out []State
	var unreadable []string
	for _, r := range rows {
		if _, declared := l.reg.Lookup(r.Key); !declared {
			continue
		}
		v, err := l.cipher.Decrypt(r.Value)
		if err != nil {
			if !tolerate {
				return nil, nil, ErrUndecryptable.With("key", r.Key).With("fingerprint", l.cipher.Fingerprint())
			}
			unreadable = append(unreadable, r.Key)
			continue
		}
		out = append(out, State{Key: r.Key, Value: v})
	}
	return out, unreadable, nil
}
