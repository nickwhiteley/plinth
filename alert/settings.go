package alert

import "github.com/nickwhiteley/plinth/settings"

// The settings alert reads (spec.md §8).
const (
	// KeyOpsEmail is where alerts go. **Empty turns reporting off**, which is how development
	// runs; on a deployment, empty means the first person to know the product is broken is a
	// customer. Not sensitive: it's an address mail is sent to, and an administrator needs to read
	// it back to check it.
	KeyOpsEmail = "ops_email"
	// KeyAlertDailyCap is how many alerts may be sent in a day before the rest are counted. A
	// mailbox with 900 alerts is indistinguishable from no alerting; the ceiling makes that loud.
	KeyAlertDailyCap = "alert_daily_cap"
)

// Settings declares alert's settings. A product declares them with its own.
var Settings = []settings.Declaration{
	{Key: KeyOpsEmail, Scope: settings.ScopeAPI, Kind: settings.KindString, Validate: settings.ValidAddress(KeyOpsEmail)},
	{Key: KeyAlertDailyCap, Scope: settings.ScopeAPI, Kind: settings.KindInt, Default: "20"},
}

// BudgetFromSnapshot is the budget the settings describe.
func BudgetFromSnapshot(s settings.Snapshot) Budget { return DefaultBudget(s.Int(KeyAlertDailyCap)) }
