-- +goose Up
ALTER TABLE outbox_entries ADD COLUMN IF NOT EXISTS topic TEXT NOT NULL DEFAULT '';

-- +goose Down
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE outbox_entries DROP COLUMN IF EXISTS topic;
