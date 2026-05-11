-- Migration 012: add typed observability column to outbox_entries.
--
-- Pre-FU1, reserved observability keys (trace_id / traceparent / request_id /
-- correlation_id) lived in the same metadata JSONB column as producer
-- business metadata. PR246-FU1 splits them into a separate observability
-- JSONB column owned exclusively by the kernel observability bridge
-- (kernel/outbox.ObservabilityMetadata struct). The producer-facing
-- metadata column keeps its original producer-owned namespace.
--
-- Rows written before this migration read back as NULL → zero
-- ObservabilityMetadata; consumer side falls back to context values, so
-- no behaviour regression on pre-migration rows.
--
-- ref: PR246-FU1 finding ② — typed observability field on Entry.

-- +goose Up
ALTER TABLE outbox_entries ADD COLUMN IF NOT EXISTS observability JSONB;

-- +goose Down
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
ALTER TABLE outbox_entries DROP COLUMN IF EXISTS observability;
