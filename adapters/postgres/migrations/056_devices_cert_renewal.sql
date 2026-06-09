-- Migration 056: device certificate renewal state for iotdevice devicecell.
--
-- Supports issue #1757: a devicecell reconcile loop scans certificates nearing
-- expiry and emits an idempotent async rotate-cert command keyed by
-- (device_id, cert_epoch). The state lives on devices because cert epoch and
-- expiry are properties of the device aggregate, not the command queue.

-- +goose Up
ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS cert_epoch BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS cert_expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '90 days');

ALTER TABLE devices
    ADD CONSTRAINT devices_cert_epoch_positive CHECK (cert_epoch >= 1);

CREATE INDEX IF NOT EXISTS idx_devices_cert_expires_at
    ON devices (cert_expires_at, id);

-- +goose Down
DROP INDEX IF EXISTS idx_devices_cert_expires_at;

ALTER TABLE devices
    DROP CONSTRAINT IF EXISTS devices_cert_epoch_positive,
    DROP COLUMN IF EXISTS cert_expires_at,
    DROP COLUMN IF EXISTS cert_epoch;
