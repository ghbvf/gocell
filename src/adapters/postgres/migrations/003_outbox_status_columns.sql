-- +goose Up

-- Three-phase relay: add status tracking columns.
ALTER TABLE outbox_entries
  ADD COLUMN status        TEXT        NOT NULL DEFAULT 'pending',
  ADD COLUMN attempts      INT         NOT NULL DEFAULT 0,
  ADD COLUMN next_retry_at TIMESTAMPTZ,
  ADD COLUMN claimed_at    TIMESTAMPTZ,
  ADD COLUMN last_error    TEXT;

-- No backfill UPDATE (S4-F1): old published=true rows won't match
-- WHERE status='pending', so relay ignores them. They are cleaned up
-- by retention (72h default) naturally.

-- Remove old boolean column — no external consumers (CLAUDE.md: no backward compat).
ALTER TABLE outbox_entries DROP COLUMN published;
-- published_at is kept; writeBack sets it on successful publish.

-- Replace old index with one aligned to claim query ORDER BY (F-3).
DROP INDEX IF EXISTS idx_outbox_unpublished;
CREATE INDEX idx_outbox_pending ON outbox_entries (next_retry_at NULLS FIRST, created_at)
  WHERE status = 'pending';

-- +goose Down
DROP INDEX IF EXISTS idx_outbox_pending;
ALTER TABLE outbox_entries
  DROP COLUMN IF EXISTS status,
  DROP COLUMN IF EXISTS attempts,
  DROP COLUMN IF EXISTS next_retry_at,
  DROP COLUMN IF EXISTS claimed_at,
  DROP COLUMN IF EXISTS last_error;
ALTER TABLE outbox_entries
  ADD COLUMN published BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX idx_outbox_unpublished ON outbox_entries (created_at) WHERE published = false;
