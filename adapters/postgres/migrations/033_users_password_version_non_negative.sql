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
--   In-place ADD CONSTRAINT (NOT VALID is intentionally NOT used — same
--   decision as migration 028). PostgreSQL's NOT VALID + VALIDATE CONSTRAINT
--   pattern (https://www.postgresql.org/docs/17/sql-altertable.html) reduces
--   the ACCESS EXCLUSIVE lock window of the full-table validation scan on
--   LARGE / non-empty tables. It does not apply here:
--     1. Project invariant (cf. 028): no deployed environment, no historical
--        data — the users table is provably empty at deploy outside CI, so the
--        validation scan is instant and the lock window is negligible.
--     2. goose runs each migration in a single transaction; ADD CONSTRAINT
--        NOT VALID + VALIDATE CONSTRAINT in the same file/tx would not release
--        the ACCESS EXCLUSIVE lock between the two steps, so it would buy no
--        concurrency benefit without also splitting into separate NO
--        TRANSACTION migrations — overkill for a provably-empty table.
--   All current rows satisfy password_version >= 0 (migration 022 DEFAULT 0 +
--   application-only increments). Standard rolling deploy — no traffic drain.
--   Non-empty-DB upgrade concerns are tracked by backlog
--   MIGRATION-NONEMPTY-DB-UPGRADE-GUIDE-01 (shared with the 026/027/028 chain).
--
-- ref: dotnet/aspnetcore Identity ConcurrencyStamp / SecurityStamp semantics
-- ref: migration 032 users_failed_login_count_positive (same pattern)

-- +goose Up
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

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_password_version_non_negative;
