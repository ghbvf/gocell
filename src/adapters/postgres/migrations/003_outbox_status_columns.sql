-- +goose Up

-- Three-phase relay: add status tracking columns.
ALTER TABLE outbox_entries
  ADD COLUMN status        TEXT        NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'claiming', 'published', 'dead')),
  ADD COLUMN attempts      INT         NOT NULL DEFAULT 0,
  ADD COLUMN next_retry_at TIMESTAMPTZ,
  ADD COLUMN claimed_at    TIMESTAMPTZ,
  ADD COLUMN last_error    TEXT,
  ADD COLUMN dead_at       TIMESTAMPTZ;

-- Backfill: map existing published state to new status column BEFORE dropping
-- the old column. Without this, published=true rows default to status='pending'
-- and the relay would re-publish them (event replay).
UPDATE outbox_entries SET status = 'published' WHERE published = true;

-- Remove old boolean column — no external consumers (CLAUDE.md: no backward compat).
ALTER TABLE outbox_entries DROP COLUMN published;
-- published_at is kept; writeBack sets it on successful publish.

-- Replace old index with one aligned to claim query ORDER BY.
DROP INDEX IF EXISTS idx_outbox_unpublished;
CREATE INDEX idx_outbox_pending ON outbox_entries (next_retry_at NULLS FIRST, created_at)
  WHERE status = 'pending';

-- +goose Down
DROP INDEX IF EXISTS idx_outbox_pending;
ALTER TABLE outbox_entries
  DROP COLUMN IF EXISTS dead_at,
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS attempts,
  DROP COLUMN IF EXISTS next_retry_at,
  DROP COLUMN IF EXISTS claimed_at,
  DROP COLUMN IF EXISTS last_error;
-- Restore old column; entries that were status='published' before rollback
-- get published=true via backfill from published_at.
ALTER TABLE outbox_entries
  ADD COLUMN published BOOLEAN NOT NULL DEFAULT false;
UPDATE outbox_entries SET published = true WHERE published_at IS NOT NULL;
CREATE INDEX idx_outbox_unpublished ON outbox_entries (created_at) WHERE published = false;
