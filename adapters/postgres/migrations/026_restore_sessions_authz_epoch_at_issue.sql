-- Migration 026: restore sessions.authz_epoch_at_issue column (S4d).
--
-- Reverses migration 025. The session row is the credential-provenance source
-- of truth at issue time; the access JWT carries no authz_epoch claim
-- (S4d retired it). See ADR `202605101400-adr-credential-session-protocol.md`
-- §A8 for the row-SoR model and §0 A1 RETRACTED for the holes that the drop
-- opened (concurrent login vs Invalidator.Apply serialization; sessionrefresh
-- re-mint upgrading stale grants to current epoch).
--
-- DDL form: NOT NULL DEFAULT 0 (rules/go-standards.md "新字段必须有默认值
-- 或允许 NULL"). The DEFAULT exists for DDL compatibility only — application
-- layer rejects zero-value AuthzEpochAtIssue at Store.Create time
-- (storetest conformance enforces). Migration 028 drops the DEFAULT and adds
-- CHECK (authz_epoch_at_issue > 0) to make the invariant a hard DB guarantee.
-- archtest SESSIONVALIDATE-EPOCH-SOURCE-01 pins read-path SoR to
-- view.AuthzEpochAtIssue.
--
-- Forward (Up): adds column with NOT NULL DEFAULT 0. This project has no
-- deployed environment and no historical data (project invariant — no
-- production instances exist outside CI test runs, per CLAUDE.md
-- "不考虑向后兼容"). The table is provably empty at deploy time, so the
-- DEFAULT 0 is a DDL compatibility device only (required by ALTER TABLE
-- ADD COLUMN NOT NULL without re-writing the table). Migration 028 drops
-- this DEFAULT and adds a CHECK (> 0) constraint; see 028 for the complete
-- invariant narrative. Application code requires non-zero on Create, so all
-- new sessions carry a real epoch from the moment 026 runs.
--
-- Rollback (Down): destructive — drops the column. The S4d binary's
-- schema_guard registers this column in requiredColumns, so a binary that
-- predates 026 would fail-fast on startup if 026's Down runs.

-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE sessions ADD COLUMN authz_epoch_at_issue BIGINT NOT NULL DEFAULT 0;

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
ALTER TABLE sessions DROP COLUMN authz_epoch_at_issue;
