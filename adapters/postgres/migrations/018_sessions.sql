-- Migration 018: create sessions table for accesscore session storage.
--
-- domain.Session fields: id, user_id, access_token, expires_at,
-- revoked_at (nullable), created_at, version (optimistic lock).
--
-- access_token UNIQUE constraint mirrors the lookup-by-token path used by
-- the JWT middleware and session validation; the index also enforces the
-- domain invariant that each token can belong to at most one session.
--
-- version models K8s ResourceVersion-style optimistic concurrency: the PG
-- repo increments version on every UPDATE and rejects updates where the
-- client-supplied version != DB version (ErrSessionConflict / 409).
--
-- ref: K8s apimachinery pkg/api/meta resourceVersion (optimistic concurrency)
-- ref: cells/accesscore/internal/domain/session.go (Session.Version)
-- ref: cells/accesscore/internal/mem/session_repo.go (optimistic lock logic)

-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

CREATE TABLE sessions (
    id           TEXT        PRIMARY KEY,
    user_id      TEXT        NOT NULL,
    access_token TEXT        NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL,
    version      BIGINT      NOT NULL DEFAULT 1
);

CREATE INDEX idx_sessions_user_id ON sessions (user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_sessions_user_id;
DROP TABLE IF EXISTS sessions;
-- +goose StatementEnd
