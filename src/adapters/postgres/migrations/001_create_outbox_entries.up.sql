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
