-- Migration 008: feature_flags table (transactional) for config-core flag-write slice.
-- Prerequisites: migrations 001-007 applied. This migration creates the table only;
-- the hot-path index is created CONCURRENTLY in migration 009 so it can be applied
-- on a live production DB without blocking writers.
--
-- ref: migrations/README.md rule 1 (CONCURRENTLY requires no-transaction annotation,
-- so index creation must live in its own file to keep CREATE TABLE atomic).
-- ref: adapters/postgres/migrations/005_recreate_outbox_pending_concurrent.sql —
-- same split pattern applied for the same atomicity reason.
--
-- +goose Up
-- +goose StatementBegin
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
    CONSTRAINT uq_feature_flags_key UNIQUE (key),
    CONSTRAINT feature_flags_rollout_percentage_range CHECK (rollout_percentage BETWEEN 0 AND 100)
);
-- +goose StatementEnd

-- +goose Down
-- CAUTION: data-destructive; for dev/CI only. Production rollback should rename
-- table to feature_flags_deprecated_YYYYMMDD instead.
-- +goose StatementBegin
DROP TABLE IF EXISTS feature_flags;
-- +goose StatementEnd
