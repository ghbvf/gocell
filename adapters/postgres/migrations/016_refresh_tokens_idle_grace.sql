-- Migration 016: add idle-expiry and grace-reuse-counter columns to refresh_tokens.
--
-- X12 (REFRESH-IDLE-EXPIRE): idle_expires_at TIMESTAMPTZ NOT NULL
--   Tracks a sliding idle-expiry deadline. Issue sets idle_expires_at = now +
--   Policy.MaxIdle. Rotate updates the child row's idle_expires_at to
--   now + Policy.MaxIdle (sliding window). Tokens whose idle_expires_at <
--   now are rejected as idle-expired even if expires_at > now.
--   Zero Policy.MaxIdle (old stores before this migration is applied)
--   disables the idle check; the column default (created_at + 30d) ensures
--   pre-migration rows have a sensible idle deadline.
--
-- X14 (REFRESH-GRACE-COUNTER): first_used_at + used_times
--   first_used_at TIMESTAMPTZ NULL: set on the first Rotate of each token.
--   used_times     INT          NOT NULL DEFAULT 0: incremented every time
--   the parent token is re-presented within the grace window. When used_times
--   reaches Policy.GraceMaxReuses, the next re-present triggers cascade revoke
--   even if the re-present is within the ReuseInterval window.
--
-- These columns are additive and backward-compatible with pods that have not
-- yet been updated: the NOT NULL defaults ensure pre-migration rows continue
-- to be read and written by old binaries without errors.
--
-- ref: ory/hydra persistence/sql/persister_oauth2.go (refresh_token_rotated column pattern)
-- ref: zitadel/zitadel internal/api/oidc/token_refresh.go (idle TTL per-request reset)

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS idle_expires_at TIMESTAMPTZ NOT NULL
        DEFAULT now() + INTERVAL '30 days';

ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS first_used_at TIMESTAMPTZ NULL;

ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS used_times INT NOT NULL DEFAULT 0;

-- GC sweep index on idle_expires_at so the GC batch can efficiently find
-- idle-expired rows even when expires_at is still in the future.
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_idle_expires
    ON refresh_tokens (idle_expires_at);

-- +goose Down
DROP INDEX IF EXISTS idx_refresh_tokens_idle_expires;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS used_times;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS first_used_at;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS idle_expires_at;
