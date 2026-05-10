-- Migration 019: roles + role_assignments tables for accesscore PG repos.
--
-- Backs cells/accesscore/internal/adapters/postgres/role_repo.go (S3+S5).
--
-- ADR-admin §3.2 ("at least one admin"): the last-admin invariant is enforced
-- in two layers:
--
--   1. Application layer (S4): cells/accesscore/internal/domain/admin.go
--      LastAdminGuard.CheckRemove returns ERR_AUTH_LAST_ADMIN_PROTECTED
--      when DeleteUser / Lock / RevokeRole would remove the only admin.
--
--   2. DB layer (this migration): a BEFORE DELETE row trigger
--      `last_admin_protected` raises EXCEPTION when the row being removed is
--      the sole admin holder. This is the "direct-SQL safety net" — never
--      replaces the application check (which produces a precise errcode);
--      it just prevents accidental DELETE bypassing the service path.
--
-- We DO NOT use a partial unique index `WHERE role_id='admin'` — that would
-- enforce "only one admin" semantics, explicitly rejected in ADR-admin §2.1
-- in favour of "at least one".
--
-- ref: ory/kratos persistence/sql/migrations role tables
-- ref: dexidp/dex storage/sql identity-role join schema

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS roles (
    id           TEXT        PRIMARY KEY,
    name         TEXT        NOT NULL,
    permissions  JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS role_assignments (
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id     TEXT        NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX IF NOT EXISTS idx_role_assignments_role
    ON role_assignments (role_id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION last_admin_protected_fn() RETURNS trigger AS $$
DECLARE
    remaining_admins BIGINT;
BEGIN
    IF OLD.role_id <> 'admin' THEN
        RETURN OLD;
    END IF;
    SELECT count(*) INTO remaining_admins
      FROM role_assignments
     WHERE role_id = 'admin'
       AND user_id <> OLD.user_id;
    IF remaining_admins = 0 THEN
        RAISE EXCEPTION 'last_admin_protected: cannot remove the last admin'
            USING ERRCODE = 'P0001';
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger WHERE tgname = 'last_admin_protected'
    ) THEN
        CREATE TRIGGER last_admin_protected
            BEFORE DELETE ON role_assignments
            FOR EACH ROW
            EXECUTE FUNCTION last_admin_protected_fn();
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- WARNING: Irreversible — dropping role_assignments destroys every grant.
DROP TRIGGER IF EXISTS last_admin_protected ON role_assignments;
DROP FUNCTION IF EXISTS last_admin_protected_fn();
DROP INDEX IF EXISTS idx_role_assignments_role;
DROP TABLE IF EXISTS role_assignments;
DROP TABLE IF EXISTS roles;
