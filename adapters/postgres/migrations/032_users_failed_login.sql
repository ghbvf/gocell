-- Migration 032: add failed_login_count + last_failed_at + locked_until for
-- ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 auto-lockout policy.
--
-- Schema:
--   - failed_login_count INTEGER NOT NULL DEFAULT 0 CHECK (>=0)
--     Monotonic counter incremented on each wrong-password attempt; reset to
--     0 on successful login or stale-window expiry; reset to 0 on admin
--     ActivateUser mutation (avoids the "unlock then immediately re-lock"
--     trap if user has one more typo within the window).
--   - last_failed_at TIMESTAMPTZ NULL
--     UTC timestamp of the most recent failure. If the gap between now() and
--     last_failed_at exceeds the stale-window (15 min in v1), the counter is
--     reset to 1 (treated as a fresh attempt sequence).
--   - locked_until TIMESTAMPTZ NULL
--     Lazy-unlock TTL. When status=locked AND locked_until <= now(),
--     sessionlogin transparently unlocks (sets status=active, count=0,
--     locked_until=NULL) inside the login tx, then continues bcrypt.
--     NULL when status != locked or for manual admin lock (which has no TTL).
--
-- Industry alignment:
--   - Keycloak BruteForceProtector: failedLoginNotBefore (= locked_until)
--   - ASP.NET Identity: AccessFailedCount + LockoutEnd
--   - django-axes: failures_since_start + attempt_time
--
-- users was created in migration 017. NOT NULL DEFAULT 0 is safe on existing
-- rows (any pre-deployment row gets count=0 = "no failures yet").
--
-- ref: keycloak BruteForceProtector LoginFailureEntity
-- ref: dotnet/aspnetcore Identity LockoutOptions
--
-- Deploy runbook: ADD COLUMN only; PG 12+ does not rewrite the table (metadata
-- default). Standard rolling deploy — no traffic drain required.

-- +goose Up
ALTER TABLE users
    ADD COLUMN failed_login_count INTEGER NOT NULL DEFAULT 0
        CONSTRAINT users_failed_login_count_positive CHECK (failed_login_count >= 0),
    ADD COLUMN last_failed_at TIMESTAMPTZ,
    ADD COLUMN locked_until TIMESTAMPTZ;

COMMENT ON COLUMN users.failed_login_count IS
    'Monotonic failed-login counter for auto-lockout (ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01). Reset to 0 on success / stale-window expiry / admin unlock.';
COMMENT ON COLUMN users.last_failed_at IS
    'UTC timestamp of the most recent failed login. NULL if no failures recorded. Drives stale-window reset semantics.';
COMMENT ON COLUMN users.locked_until IS
    'Lazy-unlock TTL for auto-locked accounts. When status=locked AND now() >= locked_until, login transparently unlocks. NULL for active accounts or manual admin lock (no TTL).';

-- +goose Down
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

-- Drop the CHECK constraint explicitly before the columns. PG would
-- cascade-drop it with the column, but writing it out keeps the Down path
-- symmetric with the Up path (where the constraint is added explicitly)
-- and matches the established 023/028 convention. IF EXISTS is defensive
-- in case Up was partial; Up runs both the column and the constraint in a
-- single statement so this is normally a no-op.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_failed_login_count_positive;

-- DROP COLUMN IF EXISTS (vs ADD COLUMN in Up) is intentional: it lets a
-- partial Up + Down sequence converge to the migration-031 schema instead
-- of failing on a missing column.
ALTER TABLE users
    DROP COLUMN IF EXISTS locked_until,
    DROP COLUMN IF EXISTS last_failed_at,
    DROP COLUMN IF EXISTS failed_login_count;
