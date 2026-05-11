-- +goose Up
CREATE TABLE IF NOT EXISTS outbox_entries (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id   TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    event_type     TEXT NOT NULL,
    payload        JSONB NOT NULL,
    metadata       JSONB DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ,
    published      BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS idx_outbox_unpublished ON outbox_entries (created_at) WHERE published = false;

-- +goose Down
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- Set by Migrator.Down's destructiveDownSessionLocker; direct goose CLI / psql bypass
-- without the GUC will RAISE EXCEPTION here.
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;

DROP INDEX IF EXISTS idx_outbox_unpublished;
DROP TABLE IF EXISTS outbox_entries;
