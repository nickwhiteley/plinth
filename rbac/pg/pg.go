// Package pg is the Postgres rbac.Store, on plinth's system_permission, system_role,
// system_role_permission and account_system_role tables. The last-holder guard is the database's:
// a deferred constraint trigger, so it holds whatever path a change takes.
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickwhiteley/plinth/db"
	"github.com/nickwhiteley/plinth/ids"
	"github.com/nickwhiteley/plinth/rbac"
)

// Store is safe for concurrent use.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ rbac.Store = (*Store)(nil)

func mapErr(err error) error {
	switch db.Constraint(err) {
	case "":
		return err
	case "system_role_key_uq":
		return rbac.ErrRoleExists
	case "system_role_key_ck", "system_role_name_ck":
		return rbac.ErrRoleInvalid
	case "system_role_permission_code_fk":
		return rbac.ErrUnknownPermission
	case "account_system_role_account_fk", "account_system_role_role_fk", "system_role_permission_role_fk":
		return rbac.ErrNotFound
	case "system_role_fixed_ck", "system_role_undeletable_ck":
		return rbac.ErrSystemRole
	case "roles_assign_holder_ck":
		return rbac.ErrLastHolder
	}
	return err
}

func (s *Store) Permissions(ctx context.Context) ([]rbac.PermissionState, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT code, is_retired FROM system_permission ORDER BY code`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (p rbac.PermissionState, err error) {
		err = r.Scan(&p.Code, &p.Retired)
		return
	})
}

func (s *Store) InsertPermission(ctx context.Context, code string) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO system_permission (code) VALUES ($1) ON CONFLICT (code) DO NOTHING`, code)
		return err
	}))
}

func (s *Store) SetPermissionRetired(ctx context.Context, code string, retired bool) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE system_permission SET is_retired = $2 WHERE code = $1`, code, retired)
		if err == nil && tag.RowsAffected() == 0 {
			return rbac.ErrNotFound
		}
		return err
	}))
}

const rolesSQL = `SELECT r.id, r.key, r.name, r.is_system,
		coalesce(array_agg(p.permission_code ORDER BY p.permission_code) FILTER (WHERE p.permission_code IS NOT NULL), '{}')
	FROM system_role r LEFT JOIN system_role_permission p ON p.role_id = r.id
	WHERE r.deleted_at IS NULL %s GROUP BY r.id ORDER BY r.key`

func scanRoles(rows pgx.Rows) ([]rbac.Role, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (x rbac.Role, err error) {
		err = r.Scan(&x.ID, &x.Key, &x.Name, &x.System, &x.Permissions)
		return
	})
}

func (s *Store) Roles(ctx context.Context) ([]rbac.Role, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, fmt.Sprintf(rolesSQL, ""))
	if err != nil {
		return nil, err
	}
	return scanRoles(rows)
}

func setPermissions(ctx context.Context, tx pgx.Tx, role ids.UUID, permissions []string, by *ids.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM system_role_permission WHERE role_id = $1`, role); err != nil {
		return err
	}
	for _, p := range permissions {
		if _, err := tx.Exec(ctx, `INSERT INTO system_role_permission (role_id, permission_code, created_by) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, role, p, by); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateRole(ctx context.Context, r rbac.Role, by *ids.UUID) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO system_role (id, key, name, is_system, created_by) VALUES ($1, $2, $3, $4, $5)`, r.ID, r.Key, r.Name, r.System, by); err != nil {
			return err
		}
		return setPermissions(ctx, tx, r.ID, r.Permissions, by)
	}))
}

func (s *Store) one(ctx context.Context, sql string, args ...any) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err == nil && tag.RowsAffected() == 0 {
			return rbac.ErrNotFound
		}
		return err
	}))
}

func (s *Store) RenameRole(ctx context.Context, id ids.UUID, name string, by *ids.UUID) error {
	return s.one(ctx, `UPDATE system_role SET name = $2, updated_by = $3 WHERE id = $1 AND deleted_at IS NULL`, id, name, by)
}

func (s *Store) SetRolePermissions(ctx context.Context, id ids.UUID, permissions []string, by *ids.UUID) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var live bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM system_role WHERE id = $1 AND deleted_at IS NULL)`, id).Scan(&live); err != nil {
			return err
		}
		if !live {
			return rbac.ErrNotFound
		}
		return setPermissions(ctx, tx, id, permissions, by)
	}))
}

func (s *Store) DeleteRole(ctx context.Context, id ids.UUID, by *ids.UUID) error {
	return s.one(ctx, `UPDATE system_role SET deleted_at = now(), deleted_by = $2 WHERE id = $1 AND deleted_at IS NULL`, id, by)
}

func (s *Store) Held(ctx context.Context, account ids.UUID) ([]rbac.Role, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, fmt.Sprintf(rolesSQL, `AND r.id IN (SELECT role_id FROM account_system_role WHERE account_id = $1)`), account)
	if err != nil {
		return nil, err
	}
	return scanRoles(rows)
}

func (s *Store) Holders(ctx context.Context, role ids.UUID) ([]ids.UUID, error) {
	rows, err := db.Q(ctx, s.pool).Query(ctx, `SELECT a.account_id FROM account_system_role a JOIN system_role r ON r.id = a.role_id AND r.deleted_at IS NULL
		WHERE a.role_id = $1 ORDER BY a.account_id`, role)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
}

func (s *Store) Grant(ctx context.Context, account, role ids.UUID, by *ids.UUID) error {
	return mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var live bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM system_role WHERE id = $1 AND deleted_at IS NULL)`, role).Scan(&live); err != nil {
			return err
		}
		if !live {
			return rbac.ErrNotFound
		}
		_, err := tx.Exec(ctx, `INSERT INTO account_system_role (account_id, role_id, granted_by) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, account, role, by)
		return err
	}))
}

func (s *Store) Revoke(ctx context.Context, account, role ids.UUID) error {
	err := mapErr(db.Run(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM account_system_role WHERE account_id = $1 AND role_id = $2`, account, role)
		return err
	}))
	if errors.Is(err, rbac.ErrNotFound) {
		return nil
	}
	return err
}
