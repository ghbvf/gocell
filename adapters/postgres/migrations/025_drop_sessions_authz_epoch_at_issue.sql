-- Migration 025: drop sessions.authz_epoch_at_issue column (S4b Batch 1C).
--
-- Up: drops sessions.authz_epoch_at_issue (replaced by JWT claim authz_epoch).
-- Down: ADD COLUMN back with DEFAULT 0 — this is a destructive Down (S3F GUC).
--
-- ─── Deployment runbook (NON-ROLLING forward migration) ─────────────────────
--
-- This is a **destructive forward migration**. The pre-S4b binary inserts
-- INTO sessions(... authz_epoch_at_issue ...); the moment this migration
-- runs that INSERT begins failing with "column does not exist". The new
-- S4b binary's schema_guard registers the column in forbiddenColumns and
-- refuses to start while the column re-exists, so there is **no rolling
-- compatibility window**. Operators must coordinate:
--
--   Forward (Up):
--     1. Drain traffic to the accesscore tier (or stop ingress).
--     2. Run `goose up` to apply this migration.
--     3. Deploy the S4b binary.
--     4. Re-enable traffic.
--
--   Rollback (Down):
--     1. Stop traffic.
--     2. Run `goose down` with GUC gocell.allow_destructive_down=true.
--     3. Re-deploy the S4a binary (its schema_guard predates the
--        forbiddenColumns entry and accepts the re-added column).
--     4. Re-enable traffic.
--
-- Rationale for not splitting into a two-phase migration: GoCell ships as
-- a single binary with no external schema consumers (project rule "不向后
-- 兼容时不留软回退"), so a brief planned downtime is cheaper than carrying
-- a dual-write transition phase. If a future deployment topology requires
-- rolling DDL, split into: (a) make column NULL-able + binary stops
-- writing it, (b) drop column.
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
SET LOCAL lock_timeout = '5s';
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
SET LOCAL lock_timeout = '5s';
ALTER TABLE sessions ADD COLUMN authz_epoch_at_issue BIGINT NOT NULL DEFAULT 0;
