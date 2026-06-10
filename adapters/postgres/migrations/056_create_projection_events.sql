-- Migration 056: create projection_events — the durable, append-only,
-- never-deleted projection event journal (model-a retained event store; EPIC #1504).
--
-- Why a dedicated table (not outbox_entries):
--   The projection ReplaySource/Cursor previously read their stream position from
--   the transient outbox_entries.seq. The relay's CleanupPublished/CleanupDead
--   physically DELETE published/dead rows, so a later Cursor.Position
--   (SELECT seq WHERE id=$1) hit ErrNoRows → permanent error → live events
--   dead-lettered and rebuild-from-0 aborted (the #1504 root bug). This dedicated
--   journal is never cleaned, and the source reads global_seq from the carrier it
--   already delivered (no deleted-row lookup), so both gaps close structurally.
--
-- Columns = outbox_entries minus the relay-internal delivery-state columns
-- (status/attempts/next_retry_at/claimed_at/last_error/dead_at/lease_id/published_at):
-- a journal never publishes or retries, so only the EntryScan-rebuild columns are kept.
-- principal is NOT NULL DEFAULT '{}' (background/system ctx with no auth principal
-- serializes as {} not null, matching outbox_entries; at-rest permanent per ADR §5).
--
-- No CONCURRENTLY for the index: this runs in the ordered migration set against a
-- fresh table (no live rows to lock); CONCURRENTLY also cannot run inside a
-- transactional migration.
--
-- ref: AxonFramework JdbcEventStore + TrackingToken — retained event store as projection source.
-- ref: adapters/postgres/migrations/049_outbox_entries_seq.sql — GENERATED ALWAYS AS IDENTITY position.
-- ref: docs/architecture/202606071600-1504-adr-projection-event-journal.md §4.1 (D3).

-- +goose Up
CREATE TABLE IF NOT EXISTS projection_events (
    global_seq     BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id             TEXT        NOT NULL,
    aggregate_id   TEXT        NOT NULL DEFAULT '',
    aggregate_type TEXT        NOT NULL DEFAULT '',
    event_type     TEXT        NOT NULL,
    topic          TEXT        NOT NULL DEFAULT '',
    payload        JSONB       NOT NULL,
    metadata       JSONB       DEFAULT '{}',
    observability  JSONB,
    principal      JSONB       NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL,
    occurred_at    TIMESTAMPTZ NOT NULL
);

-- Idempotency + cursor key. global_seq (the PK) serves the replay range scan
-- `WHERE global_seq > $1 ORDER BY global_seq` via its btree, so no extra index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_projection_events_id ON projection_events (id);

-- +goose Down
-- WARNING: dropping projection_events PERMANENTLY DELETES the durable projection
-- journal; every projection loses its replay source and cannot rebuild from 0.
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP TABLE IF EXISTS projection_events;
