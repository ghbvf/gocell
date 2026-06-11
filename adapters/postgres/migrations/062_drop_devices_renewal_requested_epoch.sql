-- Migration 062: drop devices.renewal_requested_epoch (retired by #1820).
--
-- Background: the cert-renewal producer was stateful (#1819): it wrote a
-- renewal_requested_epoch on the devices row to suppress duplicate rotate-cert
-- commands across ticks. This was F1 (#1820): offline devices could accumulate
-- unbounded duplicate active commands because the mark only suppressed the
-- *producer* emit, not the relay re-dispatch.
--
-- Fix (#1820): the producer is now STATELESS. Correctness is owned by the
-- device command queue via active-uniqueness (state-aware IdempotencyKey partial
-- index, migration 061). The producer emits on every tick; the queue coalesces
-- duplicates to no-ops; the Sweeper's OverallDeadline releases the key on expiry.
-- renewal_requested_epoch is neither written nor read by any Go code path and can
-- be dropped.
--
-- schema_guard.go: remove {Table:"devices", Column:"renewal_requested_epoch",...}
-- from expectedColumns and expectedDefaults. Callers that performed
-- MarkCertRenewalRequested have been removed; no FK or index references remain.
--
-- Down: re-adds the column with the original DEFAULT so a rollback leaves the
-- schema at migration 061 state (no data is restored — renewal tracking is
-- purely producer-side state, not business data).
--
-- ref: adapters/postgres/migrations/056_devices_cert_renewal.sql (original ADD COLUMN)
-- ref: adapters/postgres/migrations/061_commands_idempotency_active_index.sql (state-aware uniqueness)
-- ref: issue #1820 (stateless cert-renewal producer, F1 fix)

-- +goose Up
ALTER TABLE devices DROP COLUMN IF EXISTS renewal_requested_epoch;

-- +goose Down
ALTER TABLE devices ADD COLUMN IF NOT EXISTS renewal_requested_epoch BIGINT NOT NULL DEFAULT 0;
