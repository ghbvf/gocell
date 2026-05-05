-- +goose NO TRANSACTION
-- +goose Up

-- N8 PR-V1-OUTBOX-FU-CLOSURE (a): promote the outbox lease invariant from a
-- one-shot startup probe to a DB-level CHECK constraint. After this migration,
-- any INSERT/UPDATE that combines status='claiming' with NULL lease_id is
-- rejected by Postgres directly (SQLSTATE 23514), which means a stale pre-014
-- binary writing through the post-014 schema during a rolling deploy hits a
-- DB error rather than producing a row a startup probe would later refuse.
--
-- This collapses the previous "two-layer defense" (startup probe + future DB
-- CHECK) into a single source of truth and reclaims the
-- `lease_id IS NULL` three-valued-logic edge case in reclaimStale CAS — the
-- offending state simply cannot exist in the table.
--
-- DO block + pg_constraint guard makes the migration rerun-safe under
-- NO TRANSACTION (PostgreSQL ALTER TABLE ADD CONSTRAINT does not support
-- IF NOT EXISTS until version 16; the explicit guard is portable).
--
-- Operational pre-requisite (see ADR cutover §):
--   This migration MUST be applied AFTER every consumer/relay binary in
--   the deployment has been rolled to a post-014 image. A pre-014
--   binary writing through the post-015 schema will hit SQLSTATE 23514
--   (`outbox_claiming_requires_lease` violation) on every ClaimPending
--   and consumption stops until the stale binary drains. This is by
--   design — the constraint is the rolling-deploy fence — but it means
--   the migration is one-way: drain stale workers BEFORE applying.
--
-- ref: riverqueue/river riverdriver/riverpgxv5/migration/main/004_pending_and_more.up.sql
-- ref: docs/architecture/202605051600-adr-pg-outbox-fencing.md (cutover §)

-- +goose StatementBegin
DO $migration_015$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'outbox_claiming_requires_lease'
          AND conrelid = 'outbox_entries'::regclass
    ) THEN
        ALTER TABLE outbox_entries
            ADD CONSTRAINT outbox_claiming_requires_lease
            CHECK (status <> 'claiming' OR lease_id IS NOT NULL);
    END IF;
END
$migration_015$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $migration_015_down$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'outbox_claiming_requires_lease'
          AND conrelid = 'outbox_entries'::regclass
    ) THEN
        ALTER TABLE outbox_entries
            DROP CONSTRAINT outbox_claiming_requires_lease;
    END IF;
END
$migration_015_down$;
-- +goose StatementEnd
