-- Migration 028: hard DB invariant — authz_epoch columns must be > 0 (S4d P2.a).
--
-- Background: The value 0 is the "unset sentinel" for authz_epoch fields.
-- The store layer (application) already rejects zero values: domain.NewUser
-- seeds epoch=1; domain.ReconstituteUser rejects epoch<=0; sessionlogin /
-- sessionrefresh / storetest conformance all enforce non-zero at write time.
--
-- This migration makes the invariant a hard DB guarantee so that any future
-- path that accidentally bypasses the application layer is caught at the
-- storage level. Tables affected:
--
--   users.authz_epoch              — seeded to 1 by Create, incremented by BumpAuthzEpoch
--   sessions.authz_epoch_at_issue  — set to user.authz_epoch at session creation
--   refresh_tokens.authz_epoch_at_issue — set to user.authz_epoch at token issue
--
-- All three tables are provably empty at deploy: this project has no deployed
-- environment and no historical data (project invariant — no production
-- instances exist outside CI test runs). Additionally, integration fixtures
-- were synced in the same PR (S4d) to seed authz_epoch≥1 (see
-- session_store_integration_test.go::upsertUser which writes epoch=1).
-- Therefore there is provably no pre-existing zero-epoch data in any table.
-- The ADD CONSTRAINT ... CHECK (c > 0) is validated in-place (NOT VALID is
-- NOT used) and enforced immediately from the moment this migration runs.
--
-- The DEFAULT 0 is dropped from all three columns here so that any INSERT that
-- omits the column value fails explicitly rather than silently inserting the
-- sentinel. Historical origin of the DEFAULT 0 (relevant when reading the
-- migration chain in sequence):
--
--   users.authz_epoch              — DEFAULT 0 from migration 017 (original schema)
--   sessions.authz_epoch_at_issue  — DEFAULT 0 from migration 026 (DDL ALTER device)
--   refresh_tokens.authz_epoch_at_issue — DEFAULT 0 from migration 027 (DDL ALTER device)
--
-- The application always supplies the epoch value explicitly (insertUserSQL
-- sets $8=user.AuthzEpoch(); session and refresh stores set the value in
-- their INSERT statements).
--
-- schema_guard.go asserts column type / NOT NULL and registers each CHECK
-- constraint name in expectedChecks (verifyChecks path). Both this migration
-- and schema_guard.expectedChecks are authoritative — they must remain in sync.
--
-- ref: ADR docs/architecture/202605101400-adr-credential-session-protocol.md §A8

-- +goose Up
SET LOCAL lock_timeout = '5s';

-- users.authz_epoch
ALTER TABLE users ALTER COLUMN authz_epoch DROP DEFAULT;
ALTER TABLE users ADD CONSTRAINT users_authz_epoch_positive CHECK (authz_epoch > 0);

-- sessions.authz_epoch_at_issue
ALTER TABLE sessions ALTER COLUMN authz_epoch_at_issue DROP DEFAULT;
ALTER TABLE sessions ADD CONSTRAINT sessions_authz_epoch_at_issue_positive CHECK (authz_epoch_at_issue > 0);

-- refresh_tokens.authz_epoch_at_issue
ALTER TABLE refresh_tokens ALTER COLUMN authz_epoch_at_issue DROP DEFAULT;
ALTER TABLE refresh_tokens ADD CONSTRAINT refresh_tokens_authz_epoch_at_issue_positive CHECK (authz_epoch_at_issue > 0);

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
SET LOCAL lock_timeout = '5s';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_authz_epoch_positive;
ALTER TABLE users ALTER COLUMN authz_epoch SET DEFAULT 0;
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS sessions_authz_epoch_at_issue_positive;
ALTER TABLE sessions ALTER COLUMN authz_epoch_at_issue SET DEFAULT 0;
ALTER TABLE refresh_tokens DROP CONSTRAINT IF EXISTS refresh_tokens_authz_epoch_at_issue_positive;
ALTER TABLE refresh_tokens ALTER COLUMN authz_epoch_at_issue SET DEFAULT 0;
