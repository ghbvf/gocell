-- Migration 057: add cert_expires_at index on devices for renewal-candidate scan.
-- Split from 056 because CREATE INDEX CONCURRENTLY cannot run inside a transaction
-- (goose wraps transactional migrations in BEGIN/COMMIT). The columns themselves
-- are added in 056 (transactional, safe to roll back atomically); this migration
-- adds only the index using the no-transaction directive so CONCURRENTLY is legal.
--
-- The composite index (cert_expires_at, id) backs the renewal-candidate range scan
-- that finds devices whose cert expires within a configurable look-ahead window:
--   WHERE cert_expires_at IS NOT NULL AND cert_expires_at <= $threshold
--   ORDER BY cert_expires_at, id
-- Leading on cert_expires_at enables the range predicate to drive an index scan;
-- id is the tiebreaker that makes the ORDER BY satisfiable without a sort.
--
-- IF NOT EXISTS makes the Up step idempotent — safe to retry if interrupted.
-- An INVALID index residue from a prior interrupted CONCURRENTLY run is detected
-- by Migrator.Up via DetectInvalidIndexes before any migration executes.
--
-- ref: adapters/postgres/migrations/056_devices_cert_renewal.sql — column definitions
-- ref: migrations/README.md rule 1 — CONCURRENTLY requires no-transaction annotation
-- ref: migrations/README.md rule 3 — Down path uses DROP INDEX CONCURRENTLY
-- ref: issue #1819 (durable cert state)

-- +goose no transaction

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_devices_cert_expires_at
    ON devices (cert_expires_at, id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_devices_cert_expires_at;
