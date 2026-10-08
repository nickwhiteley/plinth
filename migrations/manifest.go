package migrations

import "github.com/nickwhiteley/plinth/db"

// Tables is plinth's table manifest (spec.md §4). A product's grants step reads it with its own,
// so plinth's tables get exactly the treatment the product's get.
var Tables = []db.Table{
	{Name: "plinth_schema_version", Class: db.Internal},
	{Name: "locale", Class: db.Reference},
	{Name: "tier", Class: db.Reference},
	{Name: "local_identity", Class: db.Entity, Secrets: []string{"password_hash", "google_subject"}},
	{Name: "identity_token", Class: db.Ephemeral, Secrets: []string{"token_hash"}},
	{Name: "account", Class: db.Entity},
	{Name: "account_profile", Class: db.Entity},
	{Name: "session", Class: db.Ephemeral, Secrets: []string{"token_hash"}},
	// value_enc is ciphertext, which the log keeps for history: the encryption protects it, and
	// the warehouse doesn't see it.
	{Name: "app_setting", Class: db.Reference, NoExtract: true},
	{Name: "feature_flag", Class: db.Reference},
	{Name: "account_flag", Class: db.Link},
	{Name: "quota_key", Class: db.Reference},
	{Name: "tier_quota", Class: db.Link},
	{Name: "account_quota", Class: db.Link},
	{Name: "system_permission", Class: db.Reference},
	{Name: "system_role", Class: db.Entity},
	{Name: "system_role_permission", Class: db.Link},
	{Name: "account_system_role", Class: db.Link},
	{Name: "billing_price", Class: db.Reference},
	{Name: "billing_customer", Class: db.Entity},
	{Name: "billing_checkout", Class: db.Entity},
	{Name: "billing_subscription", Class: db.Entity},
	{Name: "communication_log", Class: db.Record, Secrets: []string{"recipient", "subject", "body"}},
}
