-- Migration 023: enforce users.status and users.creation_source enum values via DB CHECK.
-- Defense-in-depth: domain.ValidUserStatus / ValidUserSource validate writes,
-- scanUser validates reads, this CHECK rejects any path that bypasses both
-- (direct SQL, cross-cell scripts, recovery tooling).
-- ref: PostgreSQL CHECK constraint — adapters/postgres/migrations/015_add_outbox_claiming_lease_check.sql.

-- +goose Up
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
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_creation_source_chk;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_chk;
