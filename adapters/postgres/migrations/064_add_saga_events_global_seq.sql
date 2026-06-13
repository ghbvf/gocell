-- Migration 064: add a monotonic global position column `global_seq` to
-- saga_events for the PG-backed GlobalReader + SagaJournalSource replay
-- conformance (EPIC #1630, Batch 1 / PR-PG).
--
-- Why a dedicated IDENTITY column (not derived):
--   journal.GlobalReader requires a stable, monotonic, 1-based global order
--   across ALL saga instances. saga_events has only (instance_id, version)
--   which is per-instance, not global. A GENERATED ALWAYS AS IDENTITY column
--   gives a durable, strictly increasing value assigned at INSERT time and
--   preserved across process restarts. Gaps are allowed (the GlobalReader
--   interface does not mandate contiguity for the PG backend; the conformance
--   suite verifies contiguous serial appends only when using a single
--   connection, which is the PG IDENTITY semantics under txid ordering).
--
-- Writer is unchanged:
--   The existing insertEvent query lists explicit columns and omits global_seq;
--   GENERATED ALWAYS AS IDENTITY means PostgreSQL auto-assigns the value.
--   No writer change is needed.
--
-- Empty-table precondition (machine-enforced below):
--   ADD COLUMN GENERATED ALWAYS AS IDENTITY backfills existing rows in heap
--   order (not guaranteed = event order). Heap-order backfill silently
--   corrupts projection replay: a SagaJournalSource that relies on global_seq
--   for causal ordering will observe out-of-order events for pre-migration
--   rows. This migration therefore hard-aborts if saga_events is non-empty.
--   This is a no-op in CI/e2e (fresh template DBs). For a non-empty DB,
--   archive or empty saga_events first, or implement deterministic backfill
--   (see issue #2070).
--
-- No CONCURRENTLY for the index: this runs as part of the ordered migration
-- set; pre-v1.0 has no live production table to avoid locking. CONCURRENTLY
-- also cannot run inside a transactional DDL block.
--
-- ref: adapters/postgres/migrations/049_outbox_entries_seq.sql
-- ref: adapters/postgres/migrations/040_create_saga_tables.sql

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM saga_events LIMIT 1) THEN
    RAISE EXCEPTION 'migration 064: saga_events must be empty before adding global_seq — IDENTITY backfill assigns heap order, not event order, silently corrupting projection replay. Archive or empty saga_events first, or implement deterministic backfill (see #2070).';
  END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE saga_events
    ADD COLUMN global_seq BIGINT GENERATED ALWAYS AS IDENTITY;

CREATE UNIQUE INDEX IF NOT EXISTS idx_saga_events_global_seq ON saga_events (global_seq);

-- +goose Down
-- WARNING: dropping `global_seq` removes the GlobalReader stream position
-- source; every SagaJournalSource (projection replay) loses its monotonic
-- ordering basis and PGJournal no longer satisfies journal.GlobalReader.
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP INDEX IF EXISTS idx_saga_events_global_seq;

ALTER TABLE saga_events
    DROP COLUMN IF EXISTS global_seq;
