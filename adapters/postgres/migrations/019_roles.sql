-- Migration 019: create roles and role_assignments tables (absorbs B3).
--
-- domain.Role fields: id, name, permissions ([]Permission — stored as JSONB).
-- created_at is a storage-layer timestamp not mirrored on domain.Role but
-- required for GC / audit queries without a separate audit table.
--
-- role_assignments models the userRoles map in mem.RoleRepository:
--   userID -> set of roleIDs, with assigned_at for audit.
-- PK (user_id, role_id) enforces the set semantics (no duplicate assignments).
--
-- Partial UNIQUE index on role_assignments (role_id) WHERE role_id = 'admin'
-- enforces single-admin invariant at the DB layer across multi-pod POST races:
-- first INSERT wins, concurrent INSERT on same role_id='admin' gets a
-- unique_violation (23505) mapped to ErrAuthRoleDuplicate / 409.
--
-- ref: B3 ACCESSCORE-PG-USERS-MIGRATION-01
-- ref: PostgreSQL partial indexes (docs/indexes-partial.html)
-- ref: jackc/pgx v5 pgconn PgError 23505 unique_violation
-- ref: cells/accesscore/internal/mem/role_repo.go (RoleRepository.userRoles map)

-- +goose Up
-- +goose StatementBegin
CREATE TABLE roles (
    id          TEXT        PRIMARY KEY,
    name        TEXT        NOT NULL UNIQUE,
    permissions JSONB       NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE role_assignments (
    user_id     TEXT        NOT NULL,
    role_id     TEXT        NOT NULL,
    assigned_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, role_id)
);

CREATE UNIQUE INDEX idx_role_assignments_single_admin
    ON role_assignments (role_id) WHERE role_id = 'admin';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX  IF EXISTS idx_role_assignments_single_admin;
DROP TABLE  IF EXISTS role_assignments;
DROP TABLE  IF EXISTS roles;
-- +goose StatementEnd
