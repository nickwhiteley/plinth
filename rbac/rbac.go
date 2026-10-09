// Package rbac is system roles and permissions (spec.md §2): what an account may do to the system,
// as distinct from what it may do to a thing it has been given. Permissions are declared in code
// and reconciled into the database, so the code is the authority on what exists. Roles are data:
// named bundles of permissions, some seeded and undeletable, others made by administrators. An
// account holds any number of roles, and its rights are the union of their permissions.
//
// Two rules are enforced here and, where the database can, there too:
//   - nobody can give a role, or give someone a role, containing a permission they don't hold;
//   - the last holder of RolesAssign can't be removed, however it is attempted.
package rbac

import (
	"regexp"
	"sort"

	"github.com/nickwhiteley/plinth/code"
	"github.com/nickwhiteley/plinth/ids"
)

// The permissions plinth itself declares. A registry always holds them: the second is what makes
// the first possible to grant, and the first is the one that can never be left with no holder.
const (
	RolesAssign = "roles.assign" // grant and revoke roles on accounts
	RolesManage = "roles.manage" // make roles, change what they carry, delete them
)

var (
	// ErrNotFound is a role or a permission that isn't stored.
	ErrNotFound = code.New("rbac.not_found")
	// ErrRoleExists is a role key a live role already has.
	ErrRoleExists = code.New("rbac.role_exists")
	// ErrRoleInvalid is a role with a malformed key, or no name, or a name over 120 characters.
	ErrRoleInvalid = code.New("rbac.role_invalid")
	// ErrUnknownPermission is a permission that isn't declared. It carries "permission".
	ErrUnknownPermission = code.New("rbac.unknown_permission")
	// ErrSystemRole is a change a seeded role doesn't allow: deleting it, or changing its key.
	ErrSystemRole = code.New("rbac.system_role")
	// ErrLastHolder is a change that would leave nobody holding RolesAssign.
	ErrLastHolder = code.New("rbac.last_holder")
	// ErrForbidden is an actor who lacks the permission the action needs. It carries "permission".
	ErrForbidden = code.New("rbac.forbidden")
	// ErrNotHeld is an actor giving away a permission they don't hold. It carries "permission".
	ErrNotHeld = code.New("rbac.not_held")
)

var (
	codeRe = regexp.MustCompile(`^[a-z]+(\.[a-z_]+)+$`)
	keyRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
)

// Role is a role and the permissions it carries, sorted.
type Role struct {
	ID          ids.UUID
	Key         string
	Name        string
	System      bool
	Permissions []string
}

// Has reports whether the role carries a permission.
func (r Role) Has(permission string) bool {
	for _, p := range r.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

// PermissionState is a stored permission.
type PermissionState struct {
	Code    string
	Retired bool
}

// Seed is a role the source declares. It is created, as a system role, where its key isn't stored.
// An administrator's later changes to it stay, except that a role with All always carries every
// declared permission, so a new permission reaches the administrator without a migration.
type Seed struct {
	Key         string
	Name        string
	Permissions []string
	All         bool
}

// Registry is the declared permissions and seeded roles.
type Registry struct {
	perms map[string]bool
	seeds []Seed
}

// NewRegistry has plinth's own permissions declared.
func NewRegistry() *Registry {
	return (&Registry{perms: map[string]bool{}}).Declare(RolesAssign, RolesManage)
}

// Declare adds permissions. A malformed code panics, as a declaration is a programming error.
func (r *Registry) Declare(codes ...string) *Registry {
	for _, c := range codes {
		if !codeRe.MatchString(c) {
			panic("rbac: " + c + " is not a permission code")
		}
		r.perms[c] = true
	}
	return r
}

// Seed adds seeded roles. A role carrying a permission nobody declared, or a bad key, panics.
func (r *Registry) Seed(seeds ...Seed) *Registry {
	for _, s := range seeds {
		if !keyRe.MatchString(s.Key) || s.Name == "" {
			panic("rbac: seeded role " + s.Key + " is malformed")
		}
		for _, p := range s.Permissions {
			if !r.perms[p] {
				panic("rbac: seeded role " + s.Key + " carries " + p + ", which is not declared")
			}
		}
		r.seeds = append(r.seeds, s)
	}
	return r
}

// Declared lists the declared permission codes, sorted.
func (r *Registry) Declared() []string {
	out := make([]string, 0, len(r.perms))
	for p := range r.perms {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// IsDeclared reports whether a permission is declared.
func (r *Registry) IsDeclared(permission string) bool { return r.perms[permission] }

// Seeds lists the seeded roles, with All resolved to every declared permission.
func (r *Registry) Seeds() []Seed {
	out := make([]Seed, len(r.seeds))
	for i, s := range r.seeds {
		if s.All {
			s.Permissions = r.Declared()
		}
		s.Permissions = sortedCopy(s.Permissions)
		out[i] = s
	}
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// ValidKey reports whether a role key is well formed.
func ValidKey(k string) bool { return keyRe.MatchString(k) }

// ValidName reports whether a role name is acceptable.
func ValidName(n string) bool {
	l := len([]rune(n))
	return l >= 1 && l <= 120
}
