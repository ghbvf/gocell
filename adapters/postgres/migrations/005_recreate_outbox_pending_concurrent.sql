-- +goose no transaction
-- RL-MIG-01: Recreate idx_outbox_pending as a CONCURRENTLY-built index.
-- Migration 003 created it with CREATE INDEX (blocking); this migration
-- builds a replacement with CONCURRENTLY first, then drops the old one.
-- This three-step approach eliminates the outbox-relay full-scan window
-- that exists in a naive drop-then-create sequence: the relay always has
-- at least one valid index during the migration window.
-- Cannot modify migration 003 (immutable per CLAUDE.md convention).
-- Runs outside an explicit transaction; CONCURRENTLY requires this.
-- ref: pressly/goose StatementModifiers; PostgreSQL CREATE INDEX CONCURRENTLY.
--      F-D-1: eliminate DROP→CREATE full-scan gap (PR#169 review P1).

-- +goose Up

-- Step 1: Build new concurrent index first (outbox relay still uses old idx_outbox_pending).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_outbox_pending_v2
    ON outbox_entries (next_retry_at NULLS FIRST, created_at)
    WHERE status = 'pending';

-- Step 2: Drop the legacy non-concurrent index (relay switches to idx_outbox_pending_v2).
DROP INDEX IF EXISTS idx_outbox_pending;

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS idx_outbox_pending_v2;
CREATE INDEX IF NOT EXISTS idx_outbox_pending
    ON outbox_entries (next_retry_at NULLS FIRST, created_at)
    WHERE status = 'pending';
