-- Migration 017: create users table for accesscore identity storage.
--
-- domain.User fields: id, username, email, password_hash,
-- password_reset_required, status (active/suspended/locked),
-- creation_source (identity/setup), created_at, updated_at.
--
-- Two UNIQUE indexes enforce the no-duplicate-username and no-duplicate-email
-- invariants that mem.UserRepository enforces via the byName map.
--
-- ref: Ory Kratos identities table (username + email unique constraints)
-- ref: cells/accesscore/internal/domain/user.go (UserStatus / UserSource consts)

-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

CREATE TABLE users (
    id                     TEXT        PRIMARY KEY,
    username               TEXT        NOT NULL,
    email                  TEXT        NOT NULL,
    password_hash          TEXT        NOT NULL,
    password_reset_required BOOLEAN    NOT NULL DEFAULT FALSE,
    status                 TEXT        NOT NULL DEFAULT 'active',
    creation_source        TEXT        NOT NULL DEFAULT 'identity',
    created_at             TIMESTAMPTZ NOT NULL,
    updated_at             TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX idx_users_username ON users (username);
CREATE UNIQUE INDEX idx_users_email    ON users (email);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS idx_users_email;
DROP INDEX IF EXISTS idx_users_username;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
