-- Migration 009: hot-path index for feature_flags (CREATE INDEX CONCURRENTLY).
-- Split from 008 because CONCURRENTLY cannot run inside a transaction and
-- keeping CREATE TABLE atomic requires a transactional wrapper (see 008).
--
-- Crash recovery: if this migration fails mid-way, PostgreSQL leaves an
-- INVALID index entry; adapters/postgres.DetectInvalidIndexes surfaces a
-- warning at startup so the operator can DROP INDEX IF EXISTS and re-run.
--
-- ref: migrations/README.md rule 1 (CONCURRENTLY requires no-transaction annotation).
-- ref: adapters/postgres/migrations/005_recreate_outbox_pending_concurrent.sql.
--
-- +goose no transaction
-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feature_flags_key_id
    ON feature_flags (key ASC, id ASC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_feature_flags_key_id;
