-- Migration 022: add password_version for narrow-scope CAS on ChangePassword path.
--
-- S6: password_version is a monotonic counter bumped exclusively by
-- UpdatePassword. The ChangePassword service issues an UPDATE...WHERE
-- password_version=$expected and treats 0 affected rows as
-- ErrVersionConflict — rejecting stale views without touching authz_epoch,
-- status, or updated_at (Auth0 / Ory Kratos / GitHub style narrow-scope CAS).
--
-- users was created in migration 017 and has no pre-existing rows in any
-- production deployment before S6 lands, so `NOT NULL DEFAULT 0` is safe.
--
-- ref: ory/kratos persistence/sql/migrations password credential versioning
-- ref: dexidp/dex storage/sql credential state column pattern

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE users
    ADD COLUMN password_version BIGINT NOT NULL DEFAULT 0;

COMMENT ON COLUMN users.password_version IS
    'Monotonic CAS counter for password updates. Bumped by UpdatePassword; UPDATE...WHERE password_version=$expected rejects stale views via ErrVersionConflict.';

-- +goose Down
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- Set by Migrator.Down's destructiveDownSessionLocker; direct goose CLI / psql bypass
-- without the GUC will RAISE EXCEPTION here.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE users DROP COLUMN IF EXISTS password_version;
