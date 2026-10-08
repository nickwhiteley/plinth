package rbac

import (
	"context"
	"slices"
	"sort"

	"github.com/nickwhiteley/plinth/ids"
)

// Store persists permissions, roles and who holds them (spec.md §2). A change that would leave
// nobody holding RolesAssign returns ErrLastHolder, in every implementation.
type Store interface {
	Permissions(ctx context.Context) ([]PermissionState, error)
	// InsertPermission adds a permission that isn't stored; a stored one is a no-op.
	InsertPermission(ctx context.Context, code string) error
	SetPermissionRetired(ctx context.Context, code string, retired bool) error

	// Roles returns the live roles with their permissions, sorted by key.
	Roles(ctx context.Context) ([]Role, error)
	// CreateRole adds a role and what it carries. ErrRoleExists if a live role has its key,
	// ErrRoleInvalid if it is malformed, ErrUnknownPermission if it carries an unstored permission.
	CreateRole(ctx context.Context, r Role, by *ids.UUID) error
	RenameRole(ctx context.Context, id ids.UUID, name string, by *ids.UUID) error
	// SetRolePermissions replaces what a role carries.
	SetRolePermissions(ctx context.Context, id ids.UUID, permissions []string, by *ids.UUID) error
	// DeleteRole soft-deletes a role. ErrSystemRole for a seeded one.
	DeleteRole(ctx context.Context, id ids.UUID, by *ids.UUID) error

	// Held returns the live roles an account holds.
	Held(ctx context.Context, account ids.UUID) ([]Role, error)
	// Holders returns the accounts holding a live role, in id order.
	Holders(ctx context.Context, role ids.UUID) ([]ids.UUID, error)
	// Grant gives an account a role; having it already is a no-op. by is nil for the break-glass
	// grant. ErrNotFound for an unknown role or account.
	Grant(ctx context.Context, account, role ids.UUID, by *ids.UUID) error
	// Revoke takes a role from an account; not holding it is not an error.
	Revoke(ctx context.Context, account, role ids.UUID) error
}

// Service applies the rules to a store.
type Service struct {
	reg   *Registry
	store Store
}

func NewService(reg *Registry, store Store) *Service { return &Service{reg: reg, store: store} }

// Registry is the declarations the service was built with.
func (s *Service) Registry() *Registry { return s.reg }

// Reconcile stores newly declared permissions, marks stored ones the source no longer declares as
// retired (and declared ones as not), creates seeded roles that aren't stored, and gives a role
// declared All any declared permission it lacks. Safe on every boot.
func (s *Service) Reconcile(ctx context.Context) error {
	stored, err := s.store.Permissions(ctx)
	if err != nil {
		return err
	}
	have := map[string]PermissionState{}
	for _, p := range stored {
		have[p.Code] = p
	}
	for _, c := range s.reg.Declared() {
		if _, ok := have[c]; !ok {
			if err := s.store.InsertPermission(ctx, c); err != nil {
				return err
			}
		}
	}
	for _, p := range stored {
		if stale := !s.reg.IsDeclared(p.Code); p.Retired != stale {
			if err := s.store.SetPermissionRetired(ctx, p.Code, stale); err != nil {
				return err
			}
		}
	}
	roles, err := s.store.Roles(ctx)
	if err != nil {
		return err
	}
	byKey := map[string]Role{}
	for _, r := range roles {
		byKey[r.Key] = r
	}
	for _, sd := range s.reg.Seeds() {
		r, ok := byKey[sd.Key]
		if !ok {
			if err := s.store.CreateRole(ctx, Role{ID: ids.New(), Key: sd.Key, Name: sd.Name, System: true, Permissions: sd.Permissions}, nil); err != nil {
				return err
			}
			continue
		}
		if !sd.All {
			continue
		}
		want := sd.Permissions
		if !slices.Equal(want, r.Permissions) {
			if err := s.store.SetRolePermissions(ctx, r.ID, want, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Permissions is the union of what an account's roles carry, sorted, less any retired permission.
func (s *Service) Permissions(ctx context.Context, account ids.UUID) ([]string, error) {
	held, err := s.store.Held(ctx, account)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range held {
		for _, p := range r.Permissions {
			if s.reg.IsDeclared(p) {
				set[p] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// Can reports whether an account holds a permission through any role.
func (s *Service) Can(ctx context.Context, account ids.UUID, permission string) (bool, error) {
	ps, err := s.Permissions(ctx, account)
	return slices.Contains(ps, permission), err
}

func (s *Service) require(ctx context.Context, actor ids.UUID, permission string) ([]string, error) {
	ps, err := s.Permissions(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(ps, permission) {
		return nil, ErrForbidden.With("permission", permission)
	}
	return ps, nil
}

// holdsAll is ErrNotHeld unless held contains every permission in want.
func holdsAll(held, want []string) error {
	for _, p := range want {
		if !slices.Contains(held, p) {
			return ErrNotHeld.With("permission", p)
		}
	}
	return nil
}

func (s *Service) roleByID(ctx context.Context, id ids.UUID) (Role, error) {
	roles, err := s.store.Roles(ctx)
	if err != nil {
		return Role{}, err
	}
	for _, r := range roles {
		if r.ID == id {
			return r, nil
		}
	}
	return Role{}, ErrNotFound
}

// Roles lists the live roles.
func (s *Service) Roles(ctx context.Context) ([]Role, error) { return s.store.Roles(ctx) }

// Held lists an account's live roles.
func (s *Service) Held(ctx context.Context, account ids.UUID) ([]Role, error) {
	return s.store.Held(ctx, account)
}

// Holders lists the accounts holding a role.
func (s *Service) Holders(ctx context.Context, role ids.UUID) ([]ids.UUID, error) {
	return s.store.Holders(ctx, role)
}

// Bootstrap gives an account the role with the key, with no actor: the break-glass grant for the
// first administrator, run with deploy credentials. ErrNotFound if the role isn't stored.
func (s *Service) Bootstrap(ctx context.Context, account ids.UUID, roleKey string) error {
	roles, err := s.store.Roles(ctx)
	if err != nil {
		return err
	}
	for _, r := range roles {
		if r.Key == roleKey {
			return s.store.Grant(ctx, account, r.ID, nil)
		}
	}
	return ErrNotFound
}

// Assign gives an account a role. The actor must hold RolesAssign and every permission the role
// carries: nobody can hand out what they don't have.
func (s *Service) Assign(ctx context.Context, actor, account, role ids.UUID) error {
	held, err := s.require(ctx, actor, RolesAssign)
	if err != nil {
		return err
	}
	r, err := s.roleByID(ctx, role)
	if err != nil {
		return err
	}
	if err := holdsAll(held, r.Permissions); err != nil {
		return err
	}
	return s.store.Grant(ctx, account, role, &actor)
}

// Revoke takes a role from an account. The actor must hold RolesAssign. Taking the last
// RolesAssign away from everyone is ErrLastHolder.
func (s *Service) Revoke(ctx context.Context, actor, account, role ids.UUID) error {
	if _, err := s.require(ctx, actor, RolesAssign); err != nil {
		return err
	}
	return s.store.Revoke(ctx, account, role)
}

// CreateRole makes a role. The actor must hold RolesManage and every permission they put in it.
func (s *Service) CreateRole(ctx context.Context, actor ids.UUID, key, name string, permissions []string) (Role, error) {
	held, err := s.require(ctx, actor, RolesManage)
	if err != nil {
		return Role{}, err
	}
	if !ValidKey(key) || !ValidName(name) {
		return Role{}, ErrRoleInvalid
	}
	perms := sortedCopy(permissions)
	for _, p := range perms {
		if !s.reg.IsDeclared(p) {
			return Role{}, ErrUnknownPermission.With("permission", p)
		}
	}
	if err := holdsAll(held, perms); err != nil {
		return Role{}, err
	}
	r := Role{ID: ids.New(), Key: key, Name: name, Permissions: slices.Compact(perms)}
	return r, s.store.CreateRole(ctx, r, &actor)
}

// UpdateRole renames a role and/or replaces what it carries. The actor must hold RolesManage and
// every permission they add; taking one away needs no more than RolesManage. A seeded role can be
// renamed and its permissions changed, but never deleted or re-keyed.
func (s *Service) UpdateRole(ctx context.Context, actor, role ids.UUID, name *string, permissions []string) error {
	held, err := s.require(ctx, actor, RolesManage)
	if err != nil {
		return err
	}
	r, err := s.roleByID(ctx, role)
	if err != nil {
		return err
	}
	if name != nil {
		if !ValidName(*name) {
			return ErrRoleInvalid
		}
		if err := s.store.RenameRole(ctx, role, *name, &actor); err != nil {
			return err
		}
	}
	if permissions == nil {
		return nil
	}
	perms := slices.Compact(sortedCopy(permissions))
	var added []string
	for _, p := range perms {
		if !s.reg.IsDeclared(p) {
			return ErrUnknownPermission.With("permission", p)
		}
		if !r.Has(p) {
			added = append(added, p)
		}
	}
	if err := holdsAll(held, added); err != nil {
		return err
	}
	return s.store.SetRolePermissions(ctx, role, perms, &actor)
}

// DeleteRole removes a role. The actor must hold RolesManage.
func (s *Service) DeleteRole(ctx context.Context, actor, role ids.UUID) error {
	if _, err := s.require(ctx, actor, RolesManage); err != nil {
		return err
	}
	return s.store.DeleteRole(ctx, role, &actor)
}
