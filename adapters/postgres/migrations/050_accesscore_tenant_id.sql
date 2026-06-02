-- Migration 050: rebuild accesscore tables (users / roles / role_assignments)
-- with tenant_id for Model-A multi-tenancy isolation (EPIC #1337 PR-2a).
--
-- Rationale (per CLAUDE.md "Review 和重构时不考虑向后兼容——当前只有 gocell 自身"):
--   No production deployment exists; dev data loss is acceptable. The
--   drop+recreate pattern mirrors migration 043 (audit_entries v2). All
--   application-layer invariants and trigger semantics are preserved and
--   ported to be per-tenant-scoped.
--
-- Schema changes (users):
--   - Add tenant_id TEXT NOT NULL (no DEFAULT — tables start empty after rebuild).
--   - PK stays (id UUID) — users are globally unique by UUID.
--   - REPLACE global UNIQUE indexes on username/email with composite
--     UNIQUE (tenant_id, username) and UNIQUE (tenant_id, email) so the same
--     username/email can exist in different tenants.
--   - Keep all 017+022+023+028+032+033 columns and their CHECK constraints.
--   - Add UNIQUE (tenant_id, id) on users so role_assignments FK can reference
--     the composite (tenant_id, user_id) pair without changing the PK shape.
--
-- Schema changes (roles):
--   - Add tenant_id TEXT NOT NULL.
--   - PK becomes composite (tenant_id, id) — roles are per-tenant scoped;
--     the same role id (e.g. "admin") exists independently per tenant.
--
-- Schema changes (role_assignments):
--   - Add tenant_id TEXT NOT NULL.
--   - PK becomes (tenant_id, user_id, role_id).
--   - FK shape: two FKs enforce tenant boundary at the DB layer:
--       (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE —
--         enforces same-tenant user membership; cross-tenant authorization grants
--         (tenant_id=A, user_id=<user in tenant B>) are FK violations, rejected
--         by the DB. Uses the UNIQUE(tenant_id, id) support index on users.
--         ON DELETE CASCADE: removing a user removes all their role grants.
--       (tenant_id, role_id) REFERENCES roles(tenant_id, id) ON DELETE RESTRICT —
--         role must exist in the same tenant; prevents cross-tenant role grant.
--
-- effective_admin_invariant trigger family (ported from migration 024):
--   - Advisory lock key incorporates the tenant:
--       pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin',
--                              hashtext(<row's tenant_id>)))
--     Different tenants do not serialize against each other; the invariant
--     "at least one effective admin" is enforced independently per tenant.
--   - The remaining COUNT adds AND <table>.tenant_id = <target tenant_id> so
--     the count is scoped to the same tenant as the mutated row.
--   - Trigger names are UNCHANGED: effective_admin_invariant_on_role_assignments
--     and effective_admin_invariant_on_users. The schema_guard.go expectedTriggers
--     and expectedFunctions entries do not need to change.
--   - EXCEPTION message prefix 'effective_admin_invariant:' and ERRCODE 'P0001'
--     are UNCHANGED — cells/accesscore/internal/adapters/postgres/errcode.go
--     isLastAdminProtected matches on that prefix. Do NOT change it.
--   - sessions table is NOT touched in this migration (deferred to PR-3).
--
-- ref: adapters/postgres/migrations/043_audit_entries_v2.sql (drop+recreate pattern)
-- ref: adapters/postgres/migrations/024_effective_admin_invariant.sql (trigger family)

-- +goose Up
-- Forward-rebuild gate is enforced in Go (Migrator.ForwardRebuild + ForwardRebuildPermit); see issue #1248.
-- One annotation line per rebuilt table (multi-target forward-rebuild, see ADR-1122 amendment).
-- +gocell forward-rebuild target=users
-- +gocell forward-rebuild target=roles
-- +gocell forward-rebuild target=role_assignments

-- -----------------------------------------------------------------------
-- 1. Drop dependent objects in reverse dependency order
-- -----------------------------------------------------------------------

DROP TRIGGER IF EXISTS effective_admin_invariant_on_users ON users;
DROP TRIGGER IF EXISTS effective_admin_invariant_on_role_assignments ON role_assignments;
DROP FUNCTION IF EXISTS effective_admin_invariant_fn();

-- role_assignments references both users and roles via FK; drop it first.
DROP INDEX IF EXISTS idx_role_assignments_role;
DROP TABLE IF EXISTS role_assignments;

-- roles has no dependents beyond role_assignments (now dropped).
DROP TABLE IF EXISTS roles;

-- users has sessions FK (sessions_subject_id_fkey) — use CASCADE to drop that
-- FK while keeping the sessions table itself intact.
DROP TABLE IF EXISTS users CASCADE;

-- -----------------------------------------------------------------------
-- 2. Recreate users with tenant_id
-- -----------------------------------------------------------------------

CREATE TABLE users (
    id                       UUID        PRIMARY KEY,
    tenant_id                TEXT        NOT NULL,
    username                 TEXT        NOT NULL,
    email                    TEXT        NOT NULL,
    password_hash            TEXT        NOT NULL,
    password_reset_required  BOOLEAN     NOT NULL DEFAULT FALSE,
    status                   TEXT        NOT NULL,
    creation_source          TEXT        NOT NULL,
    -- ADR-credential D2 + §A8: monotonic epoch bumped on every credential
    -- state change (role-revoke, password reset, lock, delete). Row is the
    -- credential-provenance source of truth. Migration 028 enforces > 0.
    authz_epoch              BIGINT      NOT NULL,
    -- Migration 022: narrow-scope CAS for password updates (S6).
    -- DEFAULT 0 mirrors migration 022 and matches insertUserSQL (no explicit column).
    password_version         BIGINT      NOT NULL DEFAULT 0,
    created_at               TIMESTAMPTZ NOT NULL,
    updated_at               TIMESTAMPTZ NOT NULL,
    -- Migration 032: auto-lockout bookkeeping (ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01).
    failed_login_count       INTEGER     NOT NULL DEFAULT 0
                                         CONSTRAINT users_failed_login_count_positive CHECK (failed_login_count >= 0),
    last_failed_at           TIMESTAMPTZ,
    locked_until             TIMESTAMPTZ,
    -- Migration 028: hard DB invariant — authz_epoch must be > 0.
    CONSTRAINT users_authz_epoch_positive CHECK (authz_epoch > 0),
    -- Migration 033: defense-in-depth — password_version >= 0.
    CONSTRAINT users_password_version_non_negative CHECK (password_version >= 0),
    -- Migration 023: status and creation_source value-set constraints.
    CONSTRAINT users_status_chk CHECK (status IN ('active', 'suspended', 'locked')),
    CONSTRAINT users_creation_source_chk CHECK (creation_source IN ('identity', 'setup'))
);

-- Composite unique per-tenant: same username/email can exist in different tenants.
CREATE UNIQUE INDEX idx_users_username
    ON users (tenant_id, username);

CREATE UNIQUE INDEX idx_users_email
    ON users (tenant_id, email);

-- Status filter for admin lifecycle (count active admins, suspend sweeps).
CREATE INDEX idx_users_status
    ON users (status);

-- Support index for role_assignments FK (tenant_id, user_id) reference.
-- Allows the role FK to verify tenant boundary without changing the users PK shape.
CREATE UNIQUE INDEX idx_users_tenant_id_id
    ON users (tenant_id, id);

-- -----------------------------------------------------------------------
-- 3. Recreate roles with tenant_id and composite PK
-- -----------------------------------------------------------------------

CREATE TABLE roles (
    tenant_id    TEXT        NOT NULL,
    id           TEXT        NOT NULL,
    name         TEXT        NOT NULL,
    permissions  JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, id)
);

-- -----------------------------------------------------------------------
-- 4. Recreate role_assignments with tenant_id
-- -----------------------------------------------------------------------

CREATE TABLE role_assignments (
    tenant_id   TEXT        NOT NULL,
    user_id     UUID        NOT NULL,
    role_id     TEXT        NOT NULL,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id, role_id),
    -- (tenant_id, user_id) references users(tenant_id, id) via the UNIQUE(tenant_id, id)
    -- support index, enforcing same-tenant user membership at the DB layer.
    -- This prevents cross-tenant authorization grants: a row (tenant_id=A, user_id=<user in tenant B>)
    -- is now a FK violation and is rejected by the DB.
    -- ON DELETE CASCADE: removing a user removes all their role grants.
    CONSTRAINT role_assignments_user_id_fkey
        FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE,
    -- (tenant_id, role_id) references roles composite PK: role must exist in same tenant.
    -- ON DELETE RESTRICT: cannot delete a role that has active assignments.
    CONSTRAINT role_assignments_role_id_fkey
        FOREIGN KEY (tenant_id, role_id) REFERENCES roles(tenant_id, id) ON DELETE RESTRICT
);

-- Composite index for queries that filter by (tenant_id, role_id), e.g. role lookup.
CREATE INDEX idx_role_assignments_role
    ON role_assignments (tenant_id, role_id);

-- -----------------------------------------------------------------------
-- 5. Recreate effective_admin_invariant trigger function (per-tenant)
-- -----------------------------------------------------------------------

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION effective_admin_invariant_fn() RETURNS trigger AS $$
DECLARE
    remaining INT;
    user_was_active_admin BOOL := FALSE;
    target_id UUID;
    target_tenant TEXT;
BEGIN
    -- Determine the user and tenant whose effective-admin status this row
    -- mutation would demote, then decide whether the user was previously
    -- an effective admin.
    IF TG_TABLE_NAME = 'role_assignments' THEN
        IF OLD.role_id <> 'admin' THEN
            RETURN OLD; -- non-admin role removals never affect the invariant
        END IF;
        target_id := OLD.user_id;
        target_tenant := OLD.tenant_id;
        user_was_active_admin := EXISTS (
            SELECT 1 FROM users WHERE id = target_id AND tenant_id = target_tenant AND status = 'active'
        );
    ELSIF TG_TABLE_NAME = 'users' THEN
        target_id := OLD.id;
        target_tenant := OLD.tenant_id;
        IF TG_OP = 'UPDATE' THEN
            -- Re-activation (suspended/locked -> active) adds capacity; skip.
            -- Same-status updates don't change the effective-admin set.
            IF OLD.status = NEW.status OR OLD.status <> 'active' THEN
                RETURN NEW;
            END IF;
        END IF;
        user_was_active_admin := (OLD.status = 'active') AND EXISTS (
            SELECT 1 FROM role_assignments
             WHERE user_id = target_id AND tenant_id = target_tenant AND role_id = 'admin'
        );
    END IF;

    -- If the user was not an effective admin, the mutation cannot reduce the
    -- effective-admin count below the invariant. Allow without lock.
    IF NOT user_was_active_admin THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    -- Serialize all concurrent guard paths for this tenant through one
    -- xact-scoped advisory lock keyed by tenant. Different tenants do not
    -- block each other; the same tenant serializes correctly.
    PERFORM pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin', hashtext(target_tenant)));

    SELECT count(*) INTO remaining
    FROM role_assignments ra
    JOIN users u ON u.id = ra.user_id AND u.tenant_id = ra.tenant_id
    WHERE ra.role_id = 'admin'
      AND ra.tenant_id = target_tenant
      AND u.status = 'active'
      AND u.id <> target_id;

    IF remaining = 0 THEN
        -- Application layer translates SQLSTATE P0001 with this message
        -- prefix into errcode.ErrAuthLastAdminProtected (HTTP 403) via
        -- adapters/postgres/errcode.go::isLastAdminProtected.
        -- DO NOT change this prefix — the Go errcode.go match depends on it.
        RAISE EXCEPTION 'effective_admin_invariant: would leave the system with no effective admin'
            USING ERRCODE = 'P0001';
    END IF;

    -- Allow the mutation to proceed.
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER effective_admin_invariant_on_role_assignments
    BEFORE DELETE ON role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION effective_admin_invariant_fn();

CREATE TRIGGER effective_admin_invariant_on_users
    BEFORE UPDATE OR DELETE ON users
    FOR EACH ROW
    EXECUTE FUNCTION effective_admin_invariant_fn();

-- -----------------------------------------------------------------------
-- 6. Restore sessions FK (dropped by CASCADE above)
-- -----------------------------------------------------------------------

-- sessions.subject_id references users(id); the CASCADE drop in step 1
-- removed the FK constraint from sessions. Re-add it now that users is
-- recreated. The sessions table itself is unchanged (PR-3 scope).
ALTER TABLE sessions
    ADD CONSTRAINT sessions_subject_id_fkey
        FOREIGN KEY (subject_id) REFERENCES users(id) ON DELETE CASCADE;

-- +goose Down
-- FORWARD-ONLY MIGRATION — NO ROLLBACK SUPPORTED.
--
-- This migration is irreversible / destructive. Rolling back to the pre-050
-- single-tenant schema is NOT supported via goose Down:
--   - The down section drops all v2 tables (users / roles / role_assignments)
--     and ALL multi-tenant accesscore data is permanently deleted.
--   - Restoring the pre-050 schema in production requires a backup restore.
--   - Dev environments must re-run from migration 001 forward (drop + migrate up).
--
-- Per CLAUDE.md "不考虑向后兼容": GoCell is pre-v1.0 with no external consumers;
-- destructive-rebuild is the accepted pattern (mirrors migration 043 audit_entries_v2).
--
-- WARNING: Running goose Down on this migration in any environment with data
-- WILL PERMANENTLY DELETE all users, roles, and role_assignments.
--
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP TRIGGER IF EXISTS effective_admin_invariant_on_users ON users;
DROP TRIGGER IF EXISTS effective_admin_invariant_on_role_assignments ON role_assignments;
DROP FUNCTION IF EXISTS effective_admin_invariant_fn();

DROP INDEX IF EXISTS idx_role_assignments_role;
DROP TABLE IF EXISTS role_assignments;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS users CASCADE;
