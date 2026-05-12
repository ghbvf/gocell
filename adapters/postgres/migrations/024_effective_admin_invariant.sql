-- Migration 024: effective last-admin invariant (S4.0 / B-route plan).
--
-- Upgrades the at-least-one-admin invariant from "any holder of the admin
-- role" (migration 019) to "at least one EFFECTIVE admin", defined as a user
-- with users.status='active' AND a row in role_assignments with role_id='admin'.
-- Before this migration, a locked/suspended admin still counted toward the
-- invariant, which allowed `DELETE FROM users WHERE id = <active admin>` to
-- succeed and leave the system with only an unusable admin.
--
-- Layered protection (DB layer is the SQL-level safety net for direct-SQL
-- bypass; precise errcode reporting stays in the application layer):
--
--   1. role_assignments BEFORE DELETE — blocks raw DELETE that would remove
--      the sole effective admin's admin row. Replaces migration 019's
--      `last_admin_protected` trigger; the old trigger and its function are
--      dropped at the top of this migration's Up.
--
--   2. users BEFORE UPDATE OR DELETE — blocks status transitions out of
--      'active' (UPDATE) and outright DELETE for users that are the sole
--      effective admin. The old role_assignments-only trigger could not see
--      this path (UPDATE users SET status='locked' …) at all.
--
-- Both triggers share a single PL/pgSQL function `effective_admin_invariant_fn`
-- and the same `pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin', 0))`
-- key — concurrent application-layer guards in
-- cells/accesscore/internal/adapters/postgres/role_repo.go
-- (countEffectiveAdminsSQL + removeIfNotLastSQL CTEs) take the same key, so
-- the entire system serializes through one xact-scoped lock.
--
-- ref: ory/kratos persistence/sql/migrations — role table triggers
-- ref: dexidp/dex storage/sql — identity status guard composition

-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Pre-flight invariant check (S4.0 sanity gate): refuse to install the
-- effective-admin trigger family on a database whose pre-existing state
-- already violates the post-S4.0 invariant. Specifically: at least one
-- admin role assignment exists but ZERO of them belong to an active user.
-- Such a database, post-migration, would be locked out of every HTTP
-- mutation guarded by the trigger (admin role / status changes), and the
-- application-layer setup-retirement check would still fast-path 410 if
-- it ran the pre-S4.0 logic. The application layer now also routes setup
-- retirement through ports.RoleRepository.EffectiveAdminExists (S4.0
-- follow-up), so post-migration recovery via /api/v1/access/setup/admin
-- IS available — but failing fast at migration time gives the operator
-- an unambiguous signal to plan the cutover (e.g., temporarily reactivate
-- one admin row, deploy 024, then re-lock). Fresh installs (zero admin
-- assignments) are not affected by this gate.
-- +goose StatementBegin
DO $$
DECLARE
    admin_assignments BIGINT;
    effective_admins BIGINT;
BEGIN
    SELECT count(*) INTO admin_assignments
      FROM role_assignments WHERE role_id = 'admin';
    SELECT count(*) INTO effective_admins
      FROM role_assignments ra
      JOIN users u ON u.id = ra.user_id
      WHERE ra.role_id = 'admin' AND u.status = 'active';
    IF admin_assignments > 0 AND effective_admins = 0 THEN
        RAISE EXCEPTION 'migration 024 pre-flight: % admin role assignment(s) exist but zero effective admins (status=active AND admin role); reactivate one admin before deploying S4.0 to keep an HTTP recovery path open', admin_assignments
            USING ERRCODE = 'P0001';
    END IF;
END $$;
-- +goose StatementEnd

-- Drop the obsolete migration-019 trigger and its function before installing
-- the new shared function. CREATE OR REPLACE on the new function name is
-- safe; the old name is fully retired (no compatibility shim — S4.0 "彻底
-- 不向后兼容" principle).
DROP TRIGGER IF EXISTS last_admin_protected ON role_assignments;
DROP FUNCTION IF EXISTS last_admin_protected_fn();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION effective_admin_invariant_fn() RETURNS trigger AS $$
DECLARE
    remaining INT;
    user_was_active_admin BOOL := FALSE;
    target_id UUID;
BEGIN
    -- Determine the user whose effective-admin status this row mutation would
    -- demote, then decide whether the user was previously an effective admin.
    IF TG_TABLE_NAME = 'role_assignments' THEN
        IF OLD.role_id <> 'admin' THEN
            RETURN OLD; -- non-admin role removals never affect the invariant
        END IF;
        target_id := OLD.user_id;
        user_was_active_admin := EXISTS (
            SELECT 1 FROM users WHERE id = target_id AND status = 'active'
        );
    ELSIF TG_TABLE_NAME = 'users' THEN
        target_id := OLD.id;
        IF TG_OP = 'UPDATE' THEN
            -- Re-activation (suspended/locked -> active) adds capacity; skip.
            -- Same-status updates don't change the effective-admin set.
            IF OLD.status = NEW.status OR OLD.status <> 'active' THEN
                RETURN NEW;
            END IF;
        END IF;
        user_was_active_admin := (OLD.status = 'active') AND EXISTS (
            SELECT 1 FROM role_assignments WHERE user_id = target_id AND role_id = 'admin'
        );
    END IF;

    -- If the user was not an effective admin, the mutation cannot reduce the
    -- effective-admin count below the invariant. Allow without lock.
    -- BEFORE-trigger return convention: returning NULL cancels the mutation,
    -- returning OLD or NEW allows it. On a DELETE row trigger the NEW pseudo-
    -- record is undefined; on an UPDATE trigger both OLD and NEW are present.
    -- Using explicit TG_OP branches below makes the "allow" intent obvious
    -- without relying on the COALESCE idiom over an undefined NEW.
    IF NOT user_was_active_admin THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    -- Serialize all concurrent guard paths (this trigger on either table, the
    -- application-layer CTEs in role_repo.go) through one xact-scoped key.
    PERFORM pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin', 0));

    SELECT count(*) INTO remaining
    FROM role_assignments ra
    JOIN users u ON u.id = ra.user_id
    WHERE ra.role_id = 'admin' AND u.status = 'active' AND u.id <> target_id;

    IF remaining = 0 THEN
        -- Application layer translates SQLSTATE P0001 with this message
        -- prefix into errcode.ErrAuthLastAdminProtected (HTTP 403) via
        -- adapters/postgres/errcode.go::isLastAdminProtected.
        RAISE EXCEPTION 'effective_admin_invariant: would leave the system with no effective admin'
            USING ERRCODE = 'P0001';
    END IF;

    -- Allow the mutation to proceed; explicit TG_OP branches preserve the
    -- BEFORE-trigger return contract without the COALESCE-of-NULL-NEW idiom.
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

-- +goose Down
-- WARNING: rolling back weakens the at-least-one-admin invariant — a locked
-- admin would once again count as a usable holder, re-opening the S4.0 hazard.
-- Fail-closed: refuse rollback unless gocell.allow_destructive_down is set.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS effective_admin_invariant_on_users ON users;
DROP TRIGGER IF EXISTS effective_admin_invariant_on_role_assignments ON role_assignments;
DROP FUNCTION IF EXISTS effective_admin_invariant_fn();

-- Restore the migration-019 trigger / function so schema_guard's pre-024
-- expected shape is reachable after Down. The DROP IF EXISTS pair below
-- makes this Down idempotent: the recreated function/trigger override any
-- pre-existing remnants without depending on migration-019's run state
-- (e.g., partial restore, replay after a manual restore, or an out-of-band
-- patch to the 019 function). CREATE TRIGGER itself has no IF NOT EXISTS
-- form in PG, so we DROP IF EXISTS first.
DROP TRIGGER IF EXISTS last_admin_protected ON role_assignments;
DROP FUNCTION IF EXISTS last_admin_protected_fn();

-- +goose StatementBegin
CREATE FUNCTION last_admin_protected_fn() RETURNS trigger AS $$
DECLARE
    remaining_admins BIGINT;
BEGIN
    IF OLD.role_id <> 'admin' THEN
        RETURN OLD;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin', 0));
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

CREATE TRIGGER last_admin_protected
    BEFORE DELETE ON role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION last_admin_protected_fn();
