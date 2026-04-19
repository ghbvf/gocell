-- +goose no transaction
-- +goose Up
CREATE TABLE IF NOT EXISTS feature_flags (
    id          TEXT        NOT NULL,
    key         TEXT        NOT NULL,
    enabled     BOOLEAN     NOT NULL DEFAULT false,
    rollout_percentage INT  NOT NULL DEFAULT 0,
    description TEXT        NOT NULL DEFAULT '',
    version     INT         NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_feature_flags PRIMARY KEY (id),
    CONSTRAINT uq_feature_flags_key UNIQUE (key)
);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feature_flags_key_id
    ON feature_flags (key ASC, id ASC);

-- +goose Down
-- CAUTION: data-destructive; for dev/CI only. Production rollback should rename
-- table to feature_flags_deprecated_YYYYMMDD instead.
DROP INDEX CONCURRENTLY IF EXISTS idx_feature_flags_key_id;
DROP TABLE IF EXISTS feature_flags;
