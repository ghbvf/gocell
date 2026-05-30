-- +goose Up
ALTER TABLE outbox_entries ADD COLUMN IF NOT EXISTS topic TEXT NOT NULL DEFAULT '';

-- +goose Down
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

ALTER TABLE outbox_entries DROP COLUMN IF EXISTS topic;
