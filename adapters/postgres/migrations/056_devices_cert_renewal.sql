-- Migration 056: promote cert-renewal state from ephemeral store to devices row.
--
-- Background: cert epoch, expiry, and renewal-requested epoch were previously
-- held only in an ephemeral (in-memory) store introduced in #1757 and #1812.
-- On process restart that state was lost, breaking the renewal state machine.
-- This migration makes all three fields durable on the devices row so the
-- devicecell can resume cert-renewal tracking after a crash or redeploy (#1819).
--
-- Column semantics:
--   cert_epoch                 — monotonic fencing token; positive-only (CHECK).
--                                DEFAULT 1 is load-bearing for raw-SQL inserts that
--                                omit the column. The Go write path normalises a
--                                zero epoch to 1 on insert, so the DB default is
--                                never relied upon by the application path.
--   cert_expires_at            — wall-clock expiry of the currently-issued cert.
--                                NULLABLE with NO DEFAULT: a row that has never had
--                                a cert issued has NULL expiry and is skipped by the
--                                renewal-candidate range scan. deviceregister always
--                                writes a real expiry on cert issuance.
--   renewal_requested_epoch    — epoch of the last renewal request; 0 = no request
--                                pending. DEFAULT 0 is load-bearing for raw-SQL
--                                inserts that omit the column.
--
-- Idempotency note: the Up section uses "DROP CONSTRAINT IF EXISTS; ADD CONSTRAINT"
-- because PostgreSQL does not support "ADD CONSTRAINT IF NOT EXISTS" directly.
-- The DROP+ADD idiom is the canonical rerun-safe equivalent (cf. migration 028
-- which documents this pattern in detail, and the Down section of migration 023).
--
-- schema_guard.go registers cert_epoch, cert_expires_at, renewal_requested_epoch
-- in expectedColumns, cert_epoch and renewal_requested_epoch defaults in
-- expectedDefaults, and devices_cert_epoch_positive in expectedChecks. Both this
-- migration and those registries are authoritative — they must remain in sync.
--
-- ref: adapters/postgres/migrations/028_authz_epoch_positive_constraints.sql
--      (rerun-safe DROP+ADD idempotency pattern and authz_epoch positive CHECK)
-- ref: adapters/postgres/migrations/029_devices.sql (devices table definition)
-- ref: issue #1757, #1812 (ephemeral cert store origin)
-- ref: issue #1819 (durable cert state, this migration)

-- +goose Up
ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS cert_epoch BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS cert_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS renewal_requested_epoch BIGINT NOT NULL DEFAULT 0;

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_cert_epoch_positive;
ALTER TABLE devices ADD CONSTRAINT devices_cert_epoch_positive CHECK (cert_epoch >= 1);

-- +goose Down
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_cert_epoch_positive;
ALTER TABLE devices
    DROP COLUMN IF EXISTS renewal_requested_epoch,
    DROP COLUMN IF EXISTS cert_expires_at,
    DROP COLUMN IF EXISTS cert_epoch;
