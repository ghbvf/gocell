-- Migration 027: add refresh_tokens.authz_epoch_at_issue (S4d).
--
-- Companion to migration 026. Refresh tokens were never carrying an
-- epoch_at_issue field — neither in S3 (sessions had it, refresh did not)
-- nor after 025 (sessions also lost it). This left the refresh path with
-- no server-side credential provenance: sessionrefresh re-minted access
-- tokens from live users.authz_epoch, so a stale refresh always "upgraded"
-- to the current epoch — defeating the entire credential-invalidation
-- mechanism for refresh-based attackers (PR #490 review P1).
--
-- After 027:
--   - refresh_tokens row carries authz_epoch_at_issue (set at Issue time)
--   - Rotate creates child rows with the issuing user's current epoch
--   - sessionrefresh rejects when row.authz_epoch_at_issue != user.authz_epoch
--     (single cascade entry: stale + reuse share handleReuseDetected)
--
-- DDL form: NOT NULL DEFAULT 0 (rules/go-standards.md). DEFAULT is for
-- DDL compatibility only; application-level Store.Issue requires non-zero
-- epoch in signature; storetest conformance enforces.

-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE refresh_tokens ADD COLUMN authz_epoch_at_issue BIGINT NOT NULL DEFAULT 0;

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
ALTER TABLE refresh_tokens DROP COLUMN authz_epoch_at_issue;
