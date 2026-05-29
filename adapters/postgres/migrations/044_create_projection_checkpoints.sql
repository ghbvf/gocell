-- Migration 044: create projection_checkpoints table for the CQRS projection
-- lifecycle harness PG CheckpointStore backend (epic #1100, PR-02).
--
-- Design decisions:
--   - (cell_id, projection_id) composite PRIMARY KEY identifies one projection's
--     consumed-offset row. cell_id is the observability owner; projection_id is
--     the per-cell projection name (no-dash convention, caller/cellgen owned).
--   - offset_seq BIGINT is the opaque, monotonically increasing cursor over the
--     projection's input stream. The harness Coordinator advances it inside the
--     same transaction as the Apply mutation (exactly-once); cold start is the
--     absence of a row (LoadOffset returns 0). DEFAULT 0 makes an INSERT that
--     omits offset_seq well-defined, though the adapter always writes it.
--   - owner TEXT NOT NULL DEFAULT '' is RESERVED for v1.1 multi-pod pessimistic
--     claim (ADR §Q5, ref AxonFramework token_entry.owner). v1 is single-pod and
--     NEVER reads or writes this column — the INSERT/UPDATE write path omits it
--     entirely, statically guarded by archtest
--     PROJECTION-CHECKPOINT-OWNER-COLUMN-V1-RESERVED-01. Pre-provisioning the
--     column means v1.1 claim needs no migration. Multi-pod safety in v1 is the
--     responsibility of upper-layer leader election (ADR threat matrix row 7).
--   - updated_at TIMESTAMPTZ is set by the adapter via SQL NOW() on every write;
--     it is an ops-visibility column (last advance time), not a correctness input.
--     It has no dedicated index by design: the table cardinality is O(projections)
--     (one row per cell/projection), so a staleness query
--     (updated_at < NOW() - interval) is a cheap full scan — no index needed.
--
-- No CONCURRENTLY here; this is a new table with no concurrent reads at migration
-- time, so regular CREATE statements are safe and avoid the no-transaction requirement.
--
-- ref: AxonFramework JdbcTokenStore — same-transaction checkpoint commit; token_entry.owner reserved column.
-- ref: ThreeDotsLabs/watermill-sql offset_adapter_postgresql.go — transactional offset upsert.
-- ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §3 (Q1/Q2) + §Q5.

-- +goose Up
CREATE TABLE IF NOT EXISTS projection_checkpoints (
    cell_id        TEXT        NOT NULL,
    projection_id  TEXT        NOT NULL,
    offset_seq     BIGINT      NOT NULL DEFAULT 0,
    owner          TEXT        NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cell_id, projection_id)
);

-- +goose Down
-- WARNING: This down migration drops projection_checkpoints and PERMANENTLY
-- DELETES all projection offset state. After a rollback every projection
-- restarts from offset 0 and replays its entire input stream on next startup.
-- The down path is intended for development/test environments.
--
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

DROP TABLE IF EXISTS projection_checkpoints;
