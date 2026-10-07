// Package shadowlog checks, at boot, that the audit guarantee holds (spec.md §5). The shadow log
// itself (the shadow() helper, the statement-level trigger, the exclusion registry) is SQL, in
// plinth's first migration.
//
// The guarantee is that the runtime role can't write or rewrite the log. Any one of these voids
// it, so the server refuses to start in production, and warns elsewhere, if the runtime role:
//   - is a superuser, or has CREATEROLE, CREATEDB, BYPASSRLS or REPLICATION
//   - is a member of pg_write_all_data, pg_read_all_data or pg_execute_server_program
//   - owns any table
//   - can SET ROLE to the owner
package shadowlog

import (
	"context"
	"strings"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/db"
)

// Report is what the boot check found about the current role.
type Report struct {
	Role               string
	DangerousAttribute bool
	DangerousMember    bool
	OwnsTable          bool
	CanBecomeOwner     bool
}

// OK reports that the role is safe to run as.
func (r Report) OK() bool {
	return !r.DangerousAttribute && !r.DangerousMember && !r.OwnsTable && !r.CanBecomeOwner
}

// ErrUnsafeRole is a runtime role that voids the audit guarantee. It carries "role" and
// "problems", a comma-separated list of what was found.
var ErrUnsafeRole = code.New("shadowlog.unsafe_role")

// Err returns ErrUnsafeRole describing what's wrong, or nil.
func (r Report) Err() error {
	if r.OK() {
		return nil
	}
	var p []string
	if r.DangerousAttribute {
		p = append(p, "attribute")
	}
	if r.DangerousMember {
		p = append(p, "membership")
	}
	if r.OwnsTable {
		p = append(p, "owns_table")
	}
	if r.CanBecomeOwner {
		p = append(p, "can_become_owner")
	}
	return ErrUnsafeRole.With("role", r.Role).With("problems", strings.Join(p, ","))
}

// Check examines the connection's current role. owner is the owner role's name; if it doesn't
// exist, nobody can become it. Proved on Postgres 18 by spike 0.1; pg_has_role(…, 'SET') needs 16.
func Check(ctx context.Context, q db.Querier, owner string) (Report, error) {
	var r Report
	err := q.QueryRow(ctx, `
		SELECT r.rolname,
		       r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication,
		       pg_has_role(current_user, 'pg_write_all_data', 'MEMBER')
		         OR pg_has_role(current_user, 'pg_read_all_data', 'MEMBER')
		         OR pg_has_role(current_user, 'pg_execute_server_program', 'MEMBER'),
		       EXISTS (SELECT 1 FROM pg_class c WHERE c.relowner = r.oid AND c.relkind IN ('r', 'p')),
		       coalesce(to_regrole($1) IS NOT NULL AND pg_has_role(current_user, to_regrole($1), 'SET'), false)
		  FROM pg_roles r WHERE r.rolname = current_user`, owner).
		Scan(&r.Role, &r.DangerousAttribute, &r.DangerousMember, &r.OwnsTable, &r.CanBecomeOwner)
	return r, err
}
