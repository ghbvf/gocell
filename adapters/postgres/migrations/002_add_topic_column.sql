-- +goose Up
ALTER TABLE outbox_entries ADD COLUMN IF NOT EXISTS topic TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE outbox_entries DROP COLUMN IF EXISTS topic;
