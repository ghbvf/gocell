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
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_creation_source_chk;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_chk;
