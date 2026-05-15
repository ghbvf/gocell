-- Migration 026: restore sessions.authz_epoch_at_issue column (S4d).
--
-- Reverses migration 025. ADR §0 A1 (which justified the drop as "JWT claim
-- mirror, zero extra defense") is RETRACTED in PR S4d: the column is the
-- credential provenance source-of-truth, not a JWT claim copy. Without it
-- the access JWT carries the epoch but the session row does not, leaving
-- three holes that 025 silently opened:
--
--   1. Concurrent login vs Invalidator.Apply: no row-level pin means login
--      can issue tokens after a credential revocation tx already advanced
--      users.authz_epoch (no PG row-lock contention between user read and
--      session INSERT, see also: GetByIDForUpdate added in this PR).
--   2. JWT claim is re-minted on refresh from live users.authz_epoch — a
--      stale grant therefore "upgrades" to current epoch on next refresh.
--      The fix lives in refresh_tokens.authz_epoch_at_issue (migration 027);
--      this column is the parallel for the session-level credential.
--   3. sessionvalidate compares user.authz_epoch with the JWT claim, but
--      the claim is not provenance — see ADR §0 A8.
--
-- DDL form: NOT NULL DEFAULT 0 (rules/go-standards.md "新字段必须有默认值
-- 或允许 NULL"). The DEFAULT exists for DDL compatibility only — application
-- layer rejects zero-value AuthzEpochAtIssue at Store.Create time
-- (storetest conformance enforces). archtest SESSIONVALIDATE-EPOCH-SOURCE-01
-- pins read-path SoR to view.AuthzEpochAtIssue (not claim.AuthzEpoch).
--
-- Forward (Up): adds column with NOT NULL DEFAULT 0. Pre-existing rows
-- (only in dev/test environments — no deployed envs per CLAUDE.md
-- "不考虑向后兼容") receive 0; those rows will never validate (epoch != 0
-- in users.authz_epoch after any bump) — equivalent to forcing re-login,
-- which is the expected behavior when schema semantics change. The accompanying
-- application code requires non-zero on Create, so all new sessions carry
-- a real epoch.
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
