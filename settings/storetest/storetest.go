// Package storetest is the conformance suite every settings.Store must pass (spec.md §3).
package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/nickwhiteley/plinth/actor"
	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/settings"
)

// Run runs the suite. factory returns an empty store for each case, and the fixtures (an account
// to act as) its writes may reference.
func Run(t *testing.T, factory func(t *testing.T) (settings.Store, fixture.Source)) {
	for name, f := range map[string]func(*testing.T, settings.Store, fixture.Source){
		"rows":    testRows,
		"history": testHistory,
	} {
		t.Run(name, func(t *testing.T) { s, fx := factory(t); f(t, s, fx) })
	}
}

func testRows(t *testing.T, s settings.Store, _ fixture.Source) {
	ctx := context.Background()
	if rows, err := s.Settings(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("an empty store: %v, %v", rows, err)
	}
	for _, k := range []string{"zeta", "alpha"} {
		if err := s.InsertSetting(ctx, settings.Row{Key: k, Value: []byte("sealed-" + k), KeyVersion: 1}); err != nil {
			t.Fatalf("InsertSetting(%s): %v", k, err)
		}
	}
	// Insert never overwrites: the declared default applies once, and an administrator's value
	// outranks it on every later boot.
	if err := s.SetSetting(ctx, "alpha", []byte("chosen"), 1); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSetting(ctx, settings.Row{Key: "alpha", Value: []byte("default again"), KeyVersion: 1}); err != nil {
		t.Errorf("inserting a stored key = %v, want a no-op", err)
	}
	rows, err := s.Settings(ctx)
	if err != nil || len(rows) != 2 || rows[0].Key != "alpha" || rows[1].Key != "zeta" {
		t.Fatalf("Settings = %+v, %v: want two rows sorted by key", rows, err)
	}
	if string(rows[0].Value) != "chosen" || rows[0].KeyVersion != 1 || rows[0].UpdatedAt.IsZero() {
		t.Errorf("alpha = %+v", rows[0])
	}
	// Ciphertext is bytes, stored exactly.
	raw := []byte{0, 1, 2, 255, 'A', 0}
	if err := s.SetSetting(ctx, "zeta", raw, 2); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.Settings(ctx)
	if string(rows[1].Value) != string(raw) || rows[1].KeyVersion != 2 {
		t.Errorf("zeta = %v v%d", rows[1].Value, rows[1].KeyVersion)
	}
	if err := s.SetSetting(ctx, "never_inserted", []byte("x"), 1); !errors.Is(err, settings.ErrNotFound) {
		t.Errorf("SetSetting(unknown) = %v", err)
	}
}

func testHistory(t *testing.T, s settings.Store, fx fixture.Source) {
	ctx := context.Background()
	if err := s.InsertSetting(ctx, settings.Row{Key: "app_url", Value: []byte("v0"), KeyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	who := fx.Account(t)
	for _, v := range []string{"v1", "v2"} {
		if err := s.SetSetting(actor.With(ctx, who), "app_url", []byte(v), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.SettingHistory(ctx, 10)
	if err != nil || len(h) != 3 {
		t.Fatalf("SettingHistory = %+v, %v", h, err)
	}
	// Newest first; the instructed writes are attributed, and the reconciling insert honestly isn't.
	if string(h[0].Value) != "v2" || string(h[2].Value) != "v0" || h[0].Key != "app_url" {
		t.Errorf("order: %s %s %s", h[0].Value, h[1].Value, h[2].Value)
	}
	if h[0].ModifiedBy == nil || *h[0].ModifiedBy != who || h[2].ModifiedBy != nil {
		t.Errorf("attribution: %v, %v", h[0].ModifiedBy, h[2].ModifiedBy)
	}
	if h[0].ModifiedAt.Before(h[2].ModifiedAt) {
		t.Error("history times run backwards")
	}
	if h, _ := s.SettingHistory(ctx, 1); len(h) != 1 || string(h[0].Value) != "v2" {
		t.Errorf("a limit of one: %+v", h)
	}
}
