-- +goose Up

-- Keyset pagination indexes for cursor-based List endpoints (WM-6).
-- Without these, keyset ORDER BY + WHERE requires a full table sort per page.
-- Guarded with table-existence checks because these tables are created by
-- cell-specific migrations, not the shared outbox migration.

-- audit-core: Query endpoint sorts by timestamp DESC, id ASC.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'audit_entries') THEN
        CREATE INDEX IF NOT EXISTS idx_audit_entries_ts_id
            ON audit_entries (timestamp DESC, id ASC);
    END IF;
END $$;

-- config-core: List endpoint sorts by key ASC, id ASC.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'config_entries') THEN
        CREATE INDEX IF NOT EXISTS idx_config_entries_key_id
            ON config_entries (key ASC, id ASC);
    END IF;
END $$;

-- +goose Down
DROP INDEX IF EXISTS idx_audit_entries_ts_id;
DROP INDEX IF EXISTS idx_config_entries_key_id;
