-- Migration 023: enforce users.status and users.creation_source enum values via DB CHECK.
-- Defense-in-depth: domain.ValidUserStatus / ValidUserSource validate writes,
-- scanUser validates reads, this CHECK rejects any path that bypasses both
-- (direct SQL, cross-cell scripts, recovery tooling).
-- Transactional migration (no CONCURRENTLY): SET LOCAL lock_timeout per migrations/README.md rule 4.
-- ref: PostgreSQL CHECK constraint — adapters/postgres/migrations/015_add_outbox_claiming_lease_check.sql.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE users
    ADD CONSTRAINT users_status_chk
    CHECK (status IN ('active', 'suspended', 'locked'));

ALTER TABLE users
    ADD CONSTRAINT users_creation_source_chk
    CHECK (creation_source IN ('identity', 'setup'));

-- +goose Down
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- Set by Migrator.Down's destructiveDownSessionLocker; direct goose CLI / psql bypass
-- without the GUC will RAISE EXCEPTION here.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_creation_source_chk;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_chk;
