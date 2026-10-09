// Package storetest is the conformance suite every rbac.Store must pass (spec.md §2).
package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nickwhiteley/plinth/fixture"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/rbac"
)

// Run runs the suite. factory returns an empty store for each case, and the fixtures (accounts)
// its grants reference.
func Run(t *testing.T, factory func(t *testing.T) (rbac.Store, fixture.Source)) {
	for name, f := range map[string]func(*testing.T, rbac.Store, fixture.Source){
		"permissions":     testPermissions,
		"roles":           testRoles,
		"holding":         testHolding,
		"the last holder": testLastHolder,
	} {
		t.Run(name, func(t *testing.T) { s, fx := factory(t); f(t, s, fx) })
	}
}

var ctx = context.Background()

func perms(t *testing.T, s rbac.Store, codes ...string) {
	t.Helper()
	for _, c := range codes {
		if err := s.InsertPermission(ctx, c); err != nil {
			t.Fatalf("InsertPermission(%s): %v", c, err)
		}
	}
}

func role(key string, ps ...string) rbac.Role {
	return rbac.Role{ID: ids.New(), Key: key, Name: key + " role", Permissions: ps}
}

func testPermissions(t *testing.T, s rbac.Store, _ fixture.Source) {
	perms(t, s, "accounts.read", "accounts.manage", "accounts.read")
	got, err := s.Permissions(ctx)
	if err != nil || len(got) != 2 || got[0].Code != "accounts.manage" || got[1].Code != "accounts.read" || got[0].Retired {
		t.Fatalf("Permissions = %+v, %v", got, err)
	}
	if err := s.SetPermissionRetired(ctx, "accounts.read", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Permissions(ctx); !got[1].Retired {
		t.Errorf("not retired: %+v", got)
	}
	if err := s.SetPermissionRetired(ctx, "nothing.here", true); !errors.Is(err, rbac.ErrNotFound) {
		t.Errorf("retiring an unknown permission = %v", err)
	}
}

func testRoles(t *testing.T, s rbac.Store, fx fixture.Source) {
	perms(t, s, "accounts.read", "accounts.manage")
	support := role("support", "accounts.read")
	if err := s.CreateRole(ctx, support, nil); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	seeded := role("administrator", "accounts.manage", "accounts.read")
	seeded.System = true
	if err := s.CreateRole(ctx, seeded, nil); err != nil {
		t.Fatalf("CreateRole(system): %v", err)
	}
	got, err := s.Roles(ctx)
	if err != nil || len(got) != 2 || got[0].Key != "administrator" || !got[0].System || got[1].Key != "support" ||
		!slices.Equal(got[0].Permissions, []string{"accounts.manage", "accounts.read"}) || got[1].ID != support.ID {
		t.Fatalf("Roles = %+v, %v", got, err)
	}
	if err := s.CreateRole(ctx, role("support"), nil); !errors.Is(err, rbac.ErrRoleExists) {
		t.Errorf("a duplicate key = %v", err)
	}
	for name, bad := range map[string]rbac.Role{
		"a bad key": {ID: ids.New(), Key: "Support Team", Name: "x"},
		"no name":   {ID: ids.New(), Key: "ok_key"},
	} {
		if err := s.CreateRole(ctx, bad, nil); !errors.Is(err, rbac.ErrRoleInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if err := s.CreateRole(ctx, role("other", "no.such"), nil); !errors.Is(err, rbac.ErrUnknownPermission) {
		t.Errorf("an unknown permission = %v", err)
	}
	if got, _ := s.Roles(ctx); len(got) != 2 {
		t.Errorf("a refused role was stored: %d", len(got))
	}

	by := fx.Account(t)
	if err := s.RenameRole(ctx, support.ID, "Support team", &by); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRolePermissions(ctx, support.ID, []string{"accounts.manage"}, &by); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Roles(ctx)
	if got[1].Name != "Support team" || !slices.Equal(got[1].Permissions, []string{"accounts.manage"}) {
		t.Errorf("after editing: %+v", got[1])
	}
	if err := s.SetRolePermissions(ctx, support.ID, []string{"no.such"}, nil); !errors.Is(err, rbac.ErrUnknownPermission) {
		t.Errorf("setting an unknown permission = %v", err)
	}
	if err := s.RenameRole(ctx, ids.New(), "x", nil); !errors.Is(err, rbac.ErrNotFound) {
		t.Errorf("renaming an unknown role = %v", err)
	}

	// Seeded roles can't be deleted; others can, and then their key is free.
	if err := s.DeleteRole(ctx, seeded.ID, &by); !errors.Is(err, rbac.ErrSystemRole) {
		t.Errorf("deleting a system role = %v", err)
	}
	if err := s.DeleteRole(ctx, support.ID, &by); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Roles(ctx); len(got) != 1 {
		t.Errorf("after deleting: %d roles", len(got))
	}
	if err := s.DeleteRole(ctx, support.ID, nil); !errors.Is(err, rbac.ErrNotFound) {
		t.Errorf("deleting twice = %v", err)
	}
	if err := s.CreateRole(ctx, role("support"), nil); err != nil {
		t.Errorf("reusing a deleted role's key = %v", err)
	}
}

func testHolding(t *testing.T, s rbac.Store, fx fixture.Source) {
	perms(t, s, "accounts.read", "audit.read")
	a, b, by := role("analyst", "audit.read"), role("support", "accounts.read"), fx.Account(t)
	for _, r := range []rbac.Role{a, b} {
		if err := s.CreateRole(ctx, r, nil); err != nil {
			t.Fatal(err)
		}
	}
	x, y := fx.Account(t), fx.Account(t)
	for _, g := range [][2]ids.UUID{{x, a.ID}, {x, b.ID}, {y, b.ID}, {x, a.ID}} {
		if err := s.Grant(ctx, g[0], g[1], &by); err != nil {
			t.Fatalf("Grant: %v", err)
		}
	}
	held, err := s.Held(ctx, x)
	if err != nil || len(held) != 2 || held[0].Key != "analyst" || held[1].Key != "support" {
		t.Fatalf("Held(x) = %+v, %v", held, err)
	}
	if h, _ := s.Held(ctx, fx.Account(t)); len(h) != 0 {
		t.Errorf("a stranger holds %d roles", len(h))
	}
	if hs, _ := s.Holders(ctx, b.ID); len(hs) != 2 {
		t.Errorf("Holders(support) = %v", hs)
	}
	if err := s.Grant(ctx, x, ids.New(), nil); !errors.Is(err, rbac.ErrNotFound) {
		t.Errorf("granting an unknown role = %v", err)
	}
	if err := s.Revoke(ctx, x, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, x, a.ID); err != nil {
		t.Errorf("revoking what isn't held = %v", err)
	}
	if held, _ := s.Held(ctx, x); len(held) != 1 {
		t.Errorf("after revoking: %+v", held)
	}
	// A deleted role is no longer held.
	if err := s.DeleteRole(ctx, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	if held, _ := s.Held(ctx, x); len(held) != 0 {
		t.Errorf("a deleted role is still held: %+v", held)
	}
	if err := s.Grant(ctx, x, b.ID, nil); !errors.Is(err, rbac.ErrNotFound) {
		t.Errorf("granting a deleted role = %v", err)
	}
}

func testLastHolder(t *testing.T, s rbac.Store, fx fixture.Source) {
	perms(t, s, rbac.RolesAssign, "audit.read")
	admin, other := role("administrator", rbac.RolesAssign, "audit.read"), role("deputy", rbac.RolesAssign)
	admin.System = true
	for _, r := range []rbac.Role{admin, other} {
		if err := s.CreateRole(ctx, r, nil); err != nil {
			t.Fatal(err)
		}
	}
	first, second := fx.Account(t), fx.Account(t)

	// With nobody holding it yet there is nothing to protect: roles change freely.
	if err := s.SetRolePermissions(ctx, other.ID, []string{rbac.RolesAssign}, nil); err != nil {
		t.Fatalf("editing before anyone holds it = %v", err)
	}
	if err := s.Grant(ctx, first, admin.ID, nil); err != nil {
		t.Fatal(err)
	}
	// The one holder can't be revoked, the role can't lose the permission or be deleted.
	if err := s.Revoke(ctx, first, admin.ID); !errors.Is(err, rbac.ErrLastHolder) {
		t.Errorf("revoking the last holder = %v", err)
	}
	if err := s.SetRolePermissions(ctx, admin.ID, []string{"audit.read"}, nil); !errors.Is(err, rbac.ErrLastHolder) {
		t.Errorf("taking the permission from the last holder's role = %v", err)
	}
	if err := s.DeleteRole(ctx, other.ID, nil); err != nil {
		t.Errorf("deleting a role nobody holds = %v", err)
	}
	if held, _ := s.Held(ctx, first); len(held) != 1 {
		t.Errorf("a refused change was kept: %+v", held)
	}
	// With a second holder, the first can go, and then the second is the last.
	if err := s.Grant(ctx, second, admin.ID, &first); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, first, admin.ID); err != nil {
		t.Errorf("revoking one of two holders = %v", err)
	}
	if err := s.Revoke(ctx, second, admin.ID); !errors.Is(err, rbac.ErrLastHolder) {
		t.Errorf("revoking the new last holder = %v", err)
	}
}
