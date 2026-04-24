-- +migrate Up
-- Add typed observability column to outbox_entries.
-- Rows written before this migration read back as NULL → zero ObservabilityMetadata.
-- Consumer side falls back to context values; no behavior regression.
ALTER TABLE outbox_entries ADD COLUMN observability JSONB;

-- +migrate Down
ALTER TABLE outbox_entries DROP COLUMN IF EXISTS observability;
