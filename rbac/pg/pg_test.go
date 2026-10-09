package pg_test

import (
	"context"
	"testing"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/pgtest"
	"github.com/nickwhiteley/plinth/rbac"
	"github.com/nickwhiteley/plinth/rbac/pg"
	"github.com/nickwhiteley/plinth/rbac/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) (rbac.Store, fixture.Source) {
		d := pgtest.New(t)
		return pg.New(d.Pool), pgtest.Fixtures{DB: d}
	})
}

// The guard is the database's, so it holds for an account being deactivated or deleted, which the
// store never sees.
func TestDeactivatingTheLastHolderIsRefused(t *testing.T) {
	ctx := context.Background()
	d := pgtest.New(t)
	fx := pgtest.Fixtures{DB: d}
	s := pg.New(d.Pool)
	if err := s.InsertPermission(ctx, rbac.RolesAssign); err != nil {
		t.Fatal(err)
	}
	admin := rbac.Role{ID: ids.New(), Key: "administrator", Name: "Administrator", System: true, Permissions: []string{rbac.RolesAssign}}
	if err := s.CreateRole(ctx, admin, nil); err != nil {
		t.Fatal(err)
	}
	one, two := fx.Account(t), fx.Account(t)
	// Before anyone holds it, deactivating is free.
	if _, err := d.Pool.Exec(ctx, `UPDATE account SET is_active = false, deactivated_at = now() WHERE id = $1`, two); err != nil {
		t.Fatalf("deactivating before anyone holds roles.assign: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE account SET is_active = true, deactivated_at = NULL WHERE id = $1`, two); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(ctx, one, admin.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE account SET is_active = false, deactivated_at = now() WHERE id = $1`, one); db.Constraint(err) != "roles_assign_holder_ck" {
		t.Errorf("deactivating the last holder = %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE account SET deleted_at = now() WHERE id = $1`, one); db.Constraint(err) != "roles_assign_holder_ck" {
		t.Errorf("deleting the last holder = %v", err)
	}
	// With another holder, the first can go.
	if err := s.Grant(ctx, two, admin.ID, &one); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE account SET is_active = false, deactivated_at = now() WHERE id = $1`, one); err != nil {
		t.Errorf("deactivating one of two holders = %v", err)
	}
}
