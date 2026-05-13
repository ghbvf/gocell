-- Migration 025: drop sessions.authz_epoch_at_issue column (S4b Batch 1C).
--
-- Background: S3 schema added authz_epoch_at_issue BIGINT NOT NULL DEFAULT 0.
-- S4a wired the field but always hard-coded 0. S4b moves epoch enforcement to
-- the JWT claim layer (authz_epoch in access token + sessionvalidate comparison
-- against users.authz_epoch). The column is now redundant and is dropped here.
--
-- ADR-credential D2 (AuthzEpoch ordering): epoch is carried in the JWT claim,
-- not a per-session snapshot column. The row-level pin approach offered no
-- additional security beyond the JWT claim comparison, so the column is removed.

-- +goose Up
ALTER TABLE sessions DROP COLUMN authz_epoch_at_issue;

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
ALTER TABLE sessions ADD COLUMN authz_epoch_at_issue BIGINT NOT NULL DEFAULT 0;
