// Package mem is the in-memory rbac.Store: a conformance reference and a test double.
package mem

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/rbac"
)

// Store is safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	perms  map[string]bool // code -> retired
	roles  map[ids.UUID]*rbac.Role
	deaths map[ids.UUID]bool
	held   map[ids.UUID]map[ids.UUID]bool // account -> roles
}

func New() *Store {
	return &Store{perms: map[string]bool{}, roles: map[ids.UUID]*rbac.Role{}, deaths: map[ids.UUID]bool{}, held: map[ids.UUID]map[ids.UUID]bool{}}
}

var _ rbac.Store = (*Store)(nil)

func (s *Store) Permissions(context.Context) ([]rbac.PermissionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rbac.PermissionState
	for c, r := range s.perms {
		out = append(out, rbac.PermissionState{Code: c, Retired: r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (s *Store) InsertPermission(_ context.Context, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.perms[code]; !ok {
		s.perms[code] = false
	}
	return nil
}

func (s *Store) SetPermissionRetired(_ context.Context, code string, retired bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.perms[code]; !ok {
		return rbac.ErrNotFound
	}
	s.perms[code] = retired
	return nil
}

func (s *Store) live() []*rbac.Role {
	var out []*rbac.Role
	for id, r := range s.roles {
		if !s.deaths[id] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func clone(r *rbac.Role) rbac.Role {
	c := *r
	c.Permissions = append([]string{}, r.Permissions...)
	return c
}

func (s *Store) Roles(context.Context) ([]rbac.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rbac.Role
	for _, r := range s.live() {
		out = append(out, clone(r))
	}
	return out, nil
}

// holdersLocked is whether anyone holds RolesAssign through a live role.
func (s *Store) holdersLocked() bool {
	for _, roles := range s.held {
		for id := range roles {
			if r := s.roles[id]; r != nil && !s.deaths[id] && r.Has(rbac.RolesAssign) {
				return true
			}
		}
	}
	return false
}

// guard runs change, and undoes it if it leaves nobody holding RolesAssign who held it before.
func (s *Store) guard(change func() error, undo func()) error {
	had := s.holdersLocked()
	if err := change(); err != nil {
		return err
	}
	if had && !s.holdersLocked() {
		undo()
		return rbac.ErrLastHolder
	}
	return nil
}

func (s *Store) CreateRole(_ context.Context, r rbac.Role, _ *ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !rbac.ValidKey(r.Key) || !rbac.ValidName(r.Name) {
		return rbac.ErrRoleInvalid
	}
	for _, o := range s.live() {
		if o.Key == r.Key {
			return rbac.ErrRoleExists
		}
	}
	for _, p := range r.Permissions {
		if _, ok := s.perms[p]; !ok {
			return rbac.ErrUnknownPermission.With("permission", p)
		}
	}
	c := clone(&r)
	sort.Strings(c.Permissions)
	s.roles[r.ID] = &c
	return nil
}

func (s *Store) RenameRole(_ context.Context, id ids.UUID, name string, _ *ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok || s.deaths[id] {
		return rbac.ErrNotFound
	}
	if !rbac.ValidName(name) {
		return rbac.ErrRoleInvalid
	}
	r.Name = name
	return nil
}

func (s *Store) SetRolePermissions(_ context.Context, id ids.UUID, permissions []string, _ *ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok || s.deaths[id] {
		return rbac.ErrNotFound
	}
	for _, p := range permissions {
		if _, ok := s.perms[p]; !ok {
			return rbac.ErrUnknownPermission.With("permission", p)
		}
	}
	old := r.Permissions
	return s.guard(func() error {
		r.Permissions = slices.Compact(append([]string{}, permissions...))
		sort.Strings(r.Permissions)
		return nil
	}, func() { r.Permissions = old })
}

func (s *Store) DeleteRole(_ context.Context, id ids.UUID, _ *ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok || s.deaths[id] {
		return rbac.ErrNotFound
	}
	if r.System {
		return rbac.ErrSystemRole
	}
	return s.guard(func() error { s.deaths[id] = true; return nil }, func() { delete(s.deaths, id) })
}

func (s *Store) Held(_ context.Context, account ids.UUID) ([]rbac.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rbac.Role
	for _, r := range s.live() {
		if s.held[account][r.ID] {
			out = append(out, clone(r))
		}
	}
	return out, nil
}

func (s *Store) Holders(_ context.Context, role ids.UUID) ([]ids.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.roles[role]; !ok || s.deaths[role] || r == nil {
		return nil, nil
	}
	var out []ids.UUID
	for a, roles := range s.held {
		if roles[role] {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func (s *Store) Grant(_ context.Context, account, role ids.UUID, _ *ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.roles[role]; !ok || s.deaths[role] {
		return rbac.ErrNotFound
	}
	if s.held[account] == nil {
		s.held[account] = map[ids.UUID]bool{}
	}
	s.held[account][role] = true
	return nil
}

func (s *Store) Revoke(_ context.Context, account, role ids.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.held[account][role] {
		return nil
	}
	return s.guard(func() error { delete(s.held[account], role); return nil }, func() { s.held[account][role] = true })
}
