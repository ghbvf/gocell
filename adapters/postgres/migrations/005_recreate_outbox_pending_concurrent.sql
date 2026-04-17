-- +goose no transaction
-- RL-MIG-01: Recreate idx_outbox_pending as a CONCURRENTLY-built index.
-- Migration 003 created it with CREATE INDEX (blocking); this migration
-- drops and recreates it with CONCURRENTLY to avoid long table locks
-- during future deployments on large outbox_entries tables.
-- Cannot modify migration 003 (immutable per CLAUDE.md convention).
-- Runs outside an explicit transaction; CONCURRENTLY requires this.
-- ref: pressly/goose StatementModifiers; PostgreSQL CREATE INDEX CONCURRENTLY.

-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS idx_outbox_pending;

CREATE INDEX CONCURRENTLY idx_outbox_pending
    ON outbox_entries (next_retry_at NULLS FIRST, created_at)
    WHERE status = 'pending';

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS idx_outbox_pending;

CREATE INDEX CONCURRENTLY idx_outbox_pending
    ON outbox_entries (next_retry_at NULLS FIRST, created_at)
    WHERE status = 'pending';
