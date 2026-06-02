-- Migration 049: add a monotonic position column `seq` to outbox_entries for the
-- production journal-backed projection ReplaySource + Cursor (epic #1100, PR-04c
-- / #1368).
--
-- Why a dedicated column (not derived):
--   The projection harness Cursor/ReplaySource require a 1-based, monotonic,
--   gap-allowed stream position per cursor.go's invariants. outbox_entries has
--   only a UUID `id` (not insertion-ordered) and `created_at` (TIMESTAMPTZ, can
--   collide under concurrent INSERT). A derived position via
--   ROW_NUMBER() OVER (ORDER BY created_at, id) is NOT stable — when
--   CleanupPublished/CleanupDead delete earlier rows, every surviving row's
--   number shifts down, violating the monotonic/stable contract. A
--   GENERATED ALWAYS AS IDENTITY column gives a durable, strictly increasing
--   value assigned at INSERT time; deletions create gaps, which the Cursor
--   explicitly tolerates (cursor.go invariant #3). Mirrors the saga journal's
--   monotonic `version` column.
--
-- Additive & non-destructive: ADD COLUMN ... GENERATED ALWAYS AS IDENTITY
-- back-fills existing rows with sequential values (no TRUNCATE, no data loss).
-- The outbox writer's INSERT does not list `seq`, so PostgreSQL assigns it
-- automatically (GENERATED ALWAYS forbids client-supplied values — the writer
-- already omits it, so no writer change is needed).
--
-- Retention boundary (documented v1 limitation, tracked in backlog): the outbox
-- is a transient relay — CleanupPublished/CleanupDead delete published/dead rows
-- after retention. A projection's full rebuild-from-0 therefore replays only
-- un-cleaned history; live/catch-up are unaffected (gaps are tolerated). Faithful
-- retention of projection-consumed events is a follow-up. See ADR
-- docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md
-- §Amendment 2026-06-03.
--
-- No CONCURRENTLY for the index: this runs as part of the ordered migration set;
-- pre-v1.0 has no live production table to avoid locking.
--
-- ref: AxonFramework EventStore — ordered replay by monotonic token/position.
-- ref: adapters/postgres/migrations/040_create_saga_tables.sql — saga_events.version.

-- +goose Up
ALTER TABLE outbox_entries
    ADD COLUMN seq BIGINT GENERATED ALWAYS AS IDENTITY;

CREATE UNIQUE INDEX IF NOT EXISTS idx_outbox_seq ON outbox_entries (seq);

-- +goose Down
-- WARNING: dropping `seq` removes the projection stream position source; every
-- projection's ReplaySource/Cursor loses its monotonic ordering basis.
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP INDEX IF EXISTS idx_outbox_seq;

ALTER TABLE outbox_entries
    DROP COLUMN IF EXISTS seq;
