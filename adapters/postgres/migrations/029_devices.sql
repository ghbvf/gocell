-- Migration 029: devices table for examples/iotdevice devicecell PG repo.
--
-- Backs examples/iotdevice/cells/devicecell/internal/adapters/postgres/device_repo.go
-- (B2.B PG-DEVICECELL-REPO). The cell is L4 DeviceLatent — devices represent
-- long-latency endpoints addressed by commands (see migration 030_commands.sql).
--
-- Schema mirrors examples/iotdevice/cells/devicecell/internal/domain/device.go:
--   * Status = "online" | "offline" (enum enforced by CHECK)
--   * id is a free-form string (typical IoT device identifier; not UUID)
--   * last_seen is wall-clock timestamp of last observed liveness ping
--
-- ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §B2.B
-- ref: adapters/postgres/migrations/017_users.sql (B2.A schema range pattern)

-- +goose Up
CREATE TABLE IF NOT EXISTS devices (
    id         TEXT        PRIMARY KEY,
    name       TEXT        NOT NULL,
    status     TEXT        NOT NULL,
    last_seen  TIMESTAMPTZ NOT NULL,
    CONSTRAINT devices_status_chk CHECK (status IN ('online', 'offline'))
);

-- List/filter by status (e.g., admin dashboards counting online devices).
CREATE INDEX IF NOT EXISTS idx_devices_status ON devices (status);

-- +goose Down
-- WARNING: Irreversible — DROP TABLE destroys every device row including any
-- pending command FK targets (commands.device_id REFERENCES devices(id) with
-- ON DELETE RESTRICT, so the DROP will cascade-fail if commands rows exist).
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX IF EXISTS idx_devices_status;
DROP TABLE IF EXISTS devices;
