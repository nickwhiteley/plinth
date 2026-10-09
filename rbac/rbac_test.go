package rbac_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/rbac"
	"github.com/nickwhiteley/plinth/rbac/mem"
)

var ctx = context.Background()

func registry() *rbac.Registry {
	return rbac.NewRegistry().Declare("accounts.read", "accounts.manage", "audit.read").Seed(
		rbac.Seed{Key: "administrator", Name: "Administrator", All: true},
		rbac.Seed{Key: "analyst", Name: "Analyst", Permissions: []string{"audit.read", "accounts.read"}},
	)
}

func setup(t *testing.T) (*rbac.Service, rbac.Store, ids.UUID) {
	t.Helper()
	st := mem.New()
	svc := rbac.NewService(registry(), st)
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	root := ids.New()
	if err := svc.Bootstrap(ctx, root, "administrator"); err != nil {
		t.Fatal(err)
	}
	return svc, st, root
}

func keyed(t *testing.T, svc *rbac.Service) map[string]rbac.Role {
	t.Helper()
	rs, err := svc.Roles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]rbac.Role{}
	for _, r := range rs {
		m[r.Key] = r
	}
	return m
}

func TestReconcileDeclaresAndSeeds(t *testing.T) {
	svc, st, _ := setup(t)
	ps, _ := st.Permissions(ctx)
	var codes []string
	for _, p := range ps {
		codes = append(codes, p.Code)
	}
	want := []string{"accounts.manage", "accounts.read", "audit.read", "roles.assign", "roles.manage"}
	if !slices.Equal(codes, want) {
		t.Errorf("permissions %v, want %v", codes, want)
	}
	rs := keyed(t, svc)
	if !rs["administrator"].System || !slices.Equal(rs["administrator"].Permissions, want) || !rs["analyst"].System {
		t.Errorf("roles: %+v", rs)
	}
	// Running again changes nothing, an administrator's edit to a role stays, and a role declared
	// All gets a permission declared later.
	if err := svc.UpdateRole(ctx, mustAdmin(t, svc, st), rs["analyst"].ID, nil, []string{"audit.read"}); err != nil {
		t.Fatal(err)
	}
	later := rbac.NewService(registry().Declare("catalogue.write"), st)
	if err := later.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	rs = keyed(t, later)
	if !slices.Equal(rs["analyst"].Permissions, []string{"audit.read"}) || !rs["administrator"].Has("catalogue.write") || len(rs) != 2 {
		t.Errorf("after a later declaration: %+v", rs)
	}
	// A permission no longer declared is retired, and grants nothing.
	fewer := rbac.NewService(rbac.NewRegistry().Declare("accounts.read").Seed(rbac.Seed{Key: "administrator", Name: "Administrator", All: true}), st)
	if err := fewer.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ps, _ = st.Permissions(ctx)
	for _, p := range ps {
		if p.Retired == (p.Code == "accounts.read" || p.Code == rbac.RolesAssign || p.Code == rbac.RolesManage) {
			t.Errorf("%s retired = %v", p.Code, p.Retired)
		}
	}
}

func mustAdmin(t *testing.T, svc *rbac.Service, st rbac.Store) ids.UUID {
	t.Helper()
	rs := keyed(t, svc)
	hs, _ := st.Holders(ctx, rs["administrator"].ID)
	return hs[0]
}

func TestCanIsTheUnionOfRoles(t *testing.T) {
	svc, _, root := setup(t)
	rs := keyed(t, svc)
	someone := ids.New()
	if ok, _ := svc.Can(ctx, someone, "audit.read"); ok {
		t.Error("a stranger can")
	}
	if err := svc.Assign(ctx, root, someone, rs["analyst"].ID); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"audit.read": true, "accounts.read": true, "accounts.manage": false, rbac.RolesAssign: false} {
		if got, _ := svc.Can(ctx, someone, p); got != want {
			t.Errorf("Can(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestNobodyGivesWhatTheyDoNotHold(t *testing.T) {
	svc, _, root := setup(t)
	rs := keyed(t, svc)
	// deputy may assign roles, and holds audit.read only.
	deputy, err := svc.CreateRole(ctx, root, "deputy", "Deputy", []string{rbac.RolesAssign, rbac.RolesManage, "audit.read"})
	if err != nil {
		t.Fatal(err)
	}
	d, target := ids.New(), ids.New()
	if err := svc.Assign(ctx, root, d, deputy.ID); err != nil {
		t.Fatal(err)
	}
	// They can give analyst (accounts.read they lack): refused, naming the permission.
	err = svc.Assign(ctx, d, target, rs["analyst"].ID)
	var ce interface{ Error() string }
	if !errors.Is(err, rbac.ErrNotHeld) || !errors.As(err, &ce) {
		t.Fatalf("assigning a role with a permission they lack = %v", err)
	}
	// They can't make a role with it, nor add it to one; they can take one away.
	if _, err := svc.CreateRole(ctx, d, "sneaky", "Sneaky", []string{"accounts.manage"}); !errors.Is(err, rbac.ErrNotHeld) {
		t.Errorf("creating = %v", err)
	}
	mine, err := svc.CreateRole(ctx, d, "mine", "Mine", []string{"audit.read"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateRole(ctx, d, mine.ID, nil, []string{"audit.read", "accounts.manage"}); !errors.Is(err, rbac.ErrNotHeld) {
		t.Errorf("adding = %v", err)
	}
	if err := svc.UpdateRole(ctx, d, rs["analyst"].ID, nil, []string{"audit.read"}); err != nil {
		t.Errorf("taking away needs no more than roles.manage: %v", err)
	}
	// Someone without roles.assign can't assign at all.
	if err := svc.Assign(ctx, target, target, mine.ID); !errors.Is(err, rbac.ErrForbidden) {
		t.Errorf("assigning without roles.assign = %v", err)
	}
	if err := svc.DeleteRole(ctx, target, mine.ID); !errors.Is(err, rbac.ErrForbidden) {
		t.Errorf("deleting without roles.manage = %v", err)
	}
}

func TestRoleRulesAndTheLastHolder(t *testing.T) {
	svc, _, root := setup(t)
	rs := keyed(t, svc)
	if _, err := svc.CreateRole(ctx, root, "Bad Key", "x", nil); !errors.Is(err, rbac.ErrRoleInvalid) {
		t.Errorf("a bad key = %v", err)
	}
	if _, err := svc.CreateRole(ctx, root, "ok_key", "x", []string{"made.up"}); !errors.Is(err, rbac.ErrUnknownPermission) {
		t.Errorf("an undeclared permission = %v", err)
	}
	if err := svc.DeleteRole(ctx, root, rs["administrator"].ID); !errors.Is(err, rbac.ErrSystemRole) {
		t.Errorf("deleting a seeded role = %v", err)
	}
	if err := svc.Revoke(ctx, root, root, rs["administrator"].ID); !errors.Is(err, rbac.ErrLastHolder) {
		t.Errorf("revoking the last administrator = %v", err)
	}
	if err := svc.UpdateRole(ctx, root, rs["administrator"].ID, nil, []string{"audit.read"}); !errors.Is(err, rbac.ErrLastHolder) {
		t.Errorf("stripping roles.assign from the last holder's role = %v", err)
	}
	if err := svc.Bootstrap(ctx, ids.New(), "no_such_role"); !errors.Is(err, rbac.ErrNotFound) {
		t.Errorf("bootstrapping an unknown role = %v", err)
	}
}

func TestRegistryPanicsOnMistakes(t *testing.T) {
	for name, f := range map[string]func(){
		"a bad code":         func() { rbac.NewRegistry().Declare("Bad") },
		"an undeclared seed": func() { rbac.NewRegistry().Seed(rbac.Seed{Key: "r1", Name: "R", Permissions: []string{"x.y"}}) },
		"a bad seed key":     func() { rbac.NewRegistry().Seed(rbac.Seed{Key: "R 1", Name: "R"}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s didn't panic", name)
				}
			}()
			f()
		}()
	}
}
