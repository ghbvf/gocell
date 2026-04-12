-- +goose Up

-- Keyset pagination indexes for cursor-based List endpoints (WM-6).
-- Without these, keyset ORDER BY + WHERE requires a full table sort per page.

-- audit-core: Query endpoint sorts by timestamp DESC, id ASC.
CREATE INDEX IF NOT EXISTS idx_audit_entries_ts_id
    ON audit_entries (timestamp DESC, id ASC);

-- config-core: List endpoint sorts by key ASC, id ASC.
CREATE INDEX IF NOT EXISTS idx_config_entries_key_id
    ON config_entries (key ASC, id ASC);

-- +goose Down
DROP INDEX IF EXISTS idx_audit_entries_ts_id;
DROP INDEX IF EXISTS idx_config_entries_key_id;
