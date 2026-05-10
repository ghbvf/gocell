-- Migration 018: add UNIQUE INDEX on audit_entries(namespace, event_id).
--
-- Design decisions:
--   - The application-layer idempotency fingerprint in LedgerStore.checkFingerprint
--     guards against duplicate EventIDs at the store level (F-CR-2). This index
--     provides a second-line DB guard that prevents a concurrent bypass (two
--     Append calls with the same EventID racing past the application check before
--     either INSERT completes).
--   - Since audit_entries is a new table introduced in migration 017 and
--     deployed together with this migration, there are no pre-existing rows that
--     could violate the uniqueness constraint. CREATE UNIQUE INDEX (without
--     CONCURRENTLY) is safe here.
--   - Future index additions after the first production deploy of migration 017
--     must use CREATE INDEX CONCURRENTLY (table no longer empty).
--
-- ref: adapters/postgres/audit_ledger_store.go selectFingerprintSQL (F-CR-2)
-- ref: Watermill router.go — message.UUID dedup key
-- ref: NServiceBus MessageDeduplicationBehavior — message ID idempotency

-- +goose Up
CREATE UNIQUE INDEX IF NOT EXISTS uq_audit_namespace_event_id
    ON audit_entries (namespace, event_id);

-- +goose Down
-- WARNING: Dropping this index removes the DB-level duplicate-EventID guard.
-- The application-layer fingerprint check remains, but concurrent inserts
-- that race past the check will no longer be caught at the DB level.
DROP INDEX IF EXISTS uq_audit_namespace_event_id;
