-- System roles and permissions (spec.md §2 rbac; Furniture Magic data-model §4.1). Permissions are
-- declared in code and reconciled in; roles are data; an account holds any number of roles. The
-- last holder of roles.assign can never be removed.

CREATE TABLE system_permission (
  code        text CONSTRAINT system_permission_pk PRIMARY KEY CONSTRAINT system_permission_code_ck CHECK (code ~ '^[a-z]+(\.[a-z_]+)+$'),
  is_retired  boolean NOT NULL DEFAULT false
);
COMMENT ON TABLE system_permission IS 'What the system lets someone do, declared in code and reconciled in. Rows are never deleted.';
COMMENT ON COLUMN system_permission.code IS 'The permission, e.g. accounts.read.';
COMMENT ON COLUMN system_permission.is_retired IS 'Stored but no longer declared by the source. Grants nothing.';
CALL shadow('system_permission');

-- A system role's key and standing are fixed, and it can't be deleted: a CHECK alone is bypassed by
-- first clearing is_system.
CREATE FUNCTION system_role_guard() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND OLD.is_system THEN
    IF NEW.key IS DISTINCT FROM OLD.key OR NOT NEW.is_system THEN
      RAISE EXCEPTION 'a system role''s key and standing are fixed' USING ERRCODE = '23514', CONSTRAINT = 'system_role_fixed_ck';
    END IF;
    IF NEW.deleted_at IS NOT NULL THEN
      RAISE EXCEPTION 'a system role cannot be deleted' USING ERRCODE = '23514', CONSTRAINT = 'system_role_undeletable_ck';
    END IF;
  END IF;
  RETURN NEW;
END $$;

CREATE TABLE system_role (
  id          uuid CONSTRAINT system_role_pk PRIMARY KEY DEFAULT uuidv7() CONSTRAINT system_role_id_v7_ck CHECK (uuid_extract_version(id) IS NOT DISTINCT FROM 7),
  key         text NOT NULL CONSTRAINT system_role_key_ck CHECK (key ~ '^[a-z][a-z0-9_]{1,63}$'),
  name        text NOT NULL CONSTRAINT system_role_name_ck CHECK (length(name) BETWEEN 1 AND 120),
  is_system   boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  created_by  uuid CONSTRAINT system_role_created_by_fk REFERENCES account (id) DEFAULT nullif(current_setting('app.modified_by', true), '')::uuid,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid CONSTRAINT system_role_updated_by_fk REFERENCES account (id),
  deleted_at  timestamptz,
  deleted_by  uuid CONSTRAINT system_role_deleted_by_fk REFERENCES account (id),
  CONSTRAINT system_role_deleted_ck CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL)
);
CREATE UNIQUE INDEX system_role_key_uq ON system_role (key) WHERE deleted_at IS NULL;
COMMENT ON TABLE system_role IS 'A named bundle of permissions. Seeded system roles can''t be deleted or change key; administrators make others.';
COMMENT ON COLUMN system_role.id IS 'The role (rol_ at the API).';
COMMENT ON COLUMN system_role.key IS 'Its stable name, unique among live roles.';
COMMENT ON COLUMN system_role.name IS 'Its display name.';
COMMENT ON COLUMN system_role.is_system IS 'Seeded by the source: its key is fixed and it can''t be deleted.';
COMMENT ON COLUMN system_role.created_at IS 'When it was made.';
COMMENT ON COLUMN system_role.created_by IS 'Who made it, or null for the seed.';
COMMENT ON COLUMN system_role.updated_at IS 'When it last changed.';
COMMENT ON COLUMN system_role.updated_by IS 'Who last changed it.';
COMMENT ON COLUMN system_role.deleted_at IS 'When it was deleted (soft delete).';
COMMENT ON COLUMN system_role.deleted_by IS 'Who deleted it.';
CALL shadow('system_role', false, 'system_role_guard');

CREATE TABLE system_role_permission (
  role_id          uuid NOT NULL CONSTRAINT system_role_permission_role_fk REFERENCES system_role (id),
  permission_code  text NOT NULL CONSTRAINT system_role_permission_code_fk REFERENCES system_permission (code),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid CONSTRAINT system_role_permission_created_by_fk REFERENCES account (id) DEFAULT nullif(current_setting('app.modified_by', true), '')::uuid,
  CONSTRAINT system_role_permission_pk PRIMARY KEY (role_id, permission_code)
);
COMMENT ON TABLE system_role_permission IS 'The permissions a role carries.';
COMMENT ON COLUMN system_role_permission.role_id IS 'The role.';
COMMENT ON COLUMN system_role_permission.permission_code IS 'The permission.';
COMMENT ON COLUMN system_role_permission.created_at IS 'When the role was given it.';
COMMENT ON COLUMN system_role_permission.created_by IS 'Who gave it.';
CALL shadow('system_role_permission');

CREATE TABLE account_system_role (
  account_id  uuid NOT NULL CONSTRAINT account_system_role_account_fk REFERENCES account (id),
  role_id     uuid NOT NULL CONSTRAINT account_system_role_role_fk REFERENCES system_role (id),
  granted_by  uuid CONSTRAINT account_system_role_granted_by_fk REFERENCES account (id) DEFAULT nullif(current_setting('app.modified_by', true), '')::uuid,
  granted_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT account_system_role_pk PRIMARY KEY (account_id, role_id)
);
CREATE INDEX account_system_role_role_ix ON account_system_role (role_id);
COMMENT ON TABLE account_system_role IS 'The system roles an account holds. Its rights are the union of their permissions.';
COMMENT ON COLUMN account_system_role.account_id IS 'The account.';
COMMENT ON COLUMN account_system_role.role_id IS 'The role.';
COMMENT ON COLUMN account_system_role.granted_by IS 'Who granted it, or null for the break-glass grant made with deploy credentials.';
COMMENT ON COLUMN account_system_role.granted_at IS 'When it was granted.';
CALL shadow('account_system_role');

-- The last holder of roles.assign can't be removed. One function checks it after any change that
-- could have removed a holder; it locks the permission row, so competing changes are serialised.
-- It only objects when the row that changed was one a holder rested on (a role someone holds, an
-- account that holds it), so changing roles and accounts before anyone holds the permission (a
-- fresh install) is not blocked.
CREATE FUNCTION assert_roles_assign_holder() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE relevant boolean;
BEGIN
  IF TG_TABLE_NAME = 'account_system_role' THEN
    relevant := EXISTS (SELECT 1 FROM system_role_permission p WHERE p.role_id = OLD.role_id AND p.permission_code = 'roles.assign');
  ELSIF TG_TABLE_NAME = 'system_role_permission' THEN
    relevant := OLD.permission_code = 'roles.assign' AND EXISTS (SELECT 1 FROM account_system_role a WHERE a.role_id = OLD.role_id);
  ELSIF TG_TABLE_NAME = 'system_role' THEN
    relevant := EXISTS (SELECT 1 FROM system_role_permission p WHERE p.role_id = OLD.id AND p.permission_code = 'roles.assign')
            AND EXISTS (SELECT 1 FROM account_system_role a WHERE a.role_id = OLD.id);
  ELSE
    relevant := EXISTS (SELECT 1 FROM account_system_role a JOIN system_role_permission p ON p.role_id = a.role_id AND p.permission_code = 'roles.assign'
                         WHERE a.account_id = OLD.id);
  END IF;
  IF NOT relevant THEN
    RETURN NULL;
  END IF;
  PERFORM 1 FROM system_permission WHERE code = 'roles.assign' FOR NO KEY UPDATE;
  IF NOT EXISTS (SELECT 1 FROM account_system_role a
                   JOIN system_role r ON r.id = a.role_id AND r.deleted_at IS NULL
                   JOIN system_role_permission p ON p.role_id = r.id AND p.permission_code = 'roles.assign'
                   JOIN account ac ON ac.id = a.account_id AND ac.is_active AND ac.deleted_at IS NULL) THEN
    RAISE EXCEPTION 'the last holder of roles.assign cannot be removed' USING ERRCODE = '23514', CONSTRAINT = 'roles_assign_holder_ck';
  END IF;
  RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER account_system_role_holder_ck AFTER UPDATE OR DELETE ON account_system_role
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_roles_assign_holder();
CREATE CONSTRAINT TRIGGER system_role_permission_holder_ck AFTER UPDATE OR DELETE ON system_role_permission
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_roles_assign_holder();
CREATE CONSTRAINT TRIGGER system_role_holder_ck AFTER UPDATE OF deleted_at ON system_role
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_roles_assign_holder();
CREATE CONSTRAINT TRIGGER account_holder_ck AFTER UPDATE OF is_active, deleted_at ON account
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_roles_assign_holder();
