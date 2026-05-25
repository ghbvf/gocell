-- Migration 033: enforce users.password_version >= 0 (defense-in-depth).
--
-- Rationale (issue #940 P2-1):
--   NewUser initialises password_version = 0; BumpPasswordVersion only
--   increments. The application layer now expresses "nil user" as a
--   valid=false pin rather than expected=-1, eliminating the implicit
--   assumption that password_version >= 0. This CHECK makes the invariant
--   explicit at the DB level so any regression in the application layer
--   is caught at write time rather than silently persisted.
--
-- Industry alignment:
--   - ASP.NET Identity AccessFailedCount is also constrained non-negative at
--     the ORM model level; DB constraints are the last line of defence.
--   - PostgreSQL convention: constrain all monotonic counters at the DB
--     level (cf. migration 032 users_failed_login_count_positive).
--
-- Deploy runbook:
--   ADD CONSTRAINT only; ALTER TABLE validates existing rows before adding.
--   All current rows have password_version >= 0 (set by migration 022
--   DEFAULT 0 + application-only increments), so the constraint is
--   satisfied immediately. Standard rolling deploy — no traffic drain
--   required.
--
-- ref: dotnet/aspnetcore Identity ConcurrencyStamp / SecurityStamp semantics
-- ref: migration 032 users_failed_login_count_positive (same pattern)

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE users
    ADD CONSTRAINT users_password_version_non_negative CHECK (password_version >= 0);

COMMENT ON CONSTRAINT users_password_version_non_negative ON users IS
    'Defense-in-depth: password_version is a monotonic counter (starts at 0, only incremented). issue #940 P2-1.';

-- +goose Down
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

SET LOCAL lock_timeout = '5s';

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_password_version_non_negative;
