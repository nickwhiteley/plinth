package alert

import (
	"testing"

	"github.com/nickwhiteley/plinth/settings"
)

func TestSettings(t *testing.T) {
	r := settings.NewRegistry().Declare(Settings...)
	if b := BudgetFromSnapshot(r.Snapshot(nil)); b.Daily != 20 {
		t.Errorf("the default ceiling = %d", b.Daily)
	}
	if b := BudgetFromSnapshot(r.Snapshot([]settings.State{{Key: KeyAlertDailyCap, Value: "5"}})); b.Daily != 5 {
		t.Errorf("a set ceiling = %d", b.Daily)
	}
	d, _ := r.Lookup(KeyOpsEmail)
	if settings.ParseValue(d, "ops@example.com\nBcc: x@evil.example") == nil {
		t.Error("an address carrying a header injection was accepted")
	}
}
