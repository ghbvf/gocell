-- Migration 017: users table for accesscore PG repos.
--
-- Backs cells/accesscore/internal/adapters/postgres/user_repo.go (S3+S5).
-- ADR-credential D2 mandates `authz_epoch BIGINT NOT NULL DEFAULT 0` for
-- login-vs-role-revoke ordering — every credential state change bumps the
-- column; JWTs snapshot the epoch at issuance and validate paths reject
-- claim.epoch < user.authz_epoch.
--
-- Schema mirrors cells/accesscore/internal/domain/user.go (S2) one-to-one:
--   * UserStatus = active|suspended|locked
--   * UserSource = identity|setup
--   * password_reset_required flag for first-admin / forced-reset flow
--
-- Closes ACCESSCORE-PG-USERS-MIGRATION-01 (was: PG users table missing).
--
-- ref: dexidp/dex storage/sql/migrations.go user table layout
-- ref: ory/kratos persistence/sql/migrations user identity layout

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS users (
    id                       UUID        PRIMARY KEY,
    username                 TEXT        NOT NULL,
    email                    TEXT        NOT NULL,
    password_hash            TEXT        NOT NULL,
    password_reset_required  BOOLEAN     NOT NULL DEFAULT FALSE,
    status                   TEXT        NOT NULL,
    creation_source          TEXT        NOT NULL,
    -- ADR-credential D2: monotonic epoch bumped on every credential state
    -- change (role assignment, password reset, lock, delete). JWTs include
    -- this snapshot in the epoch claim; validate rejects when claim.epoch <
    -- users.authz_epoch.
    authz_epoch              BIGINT      NOT NULL DEFAULT 0,
    created_at               TIMESTAMPTZ NOT NULL,
    updated_at               TIMESTAMPTZ NOT NULL
);

-- Username and email are case-sensitive identifiers under the ADR-credential
-- model; case folding is the application's job. UNIQUE indexes enforce row
-- uniqueness in PG so concurrent INSERTs collapse to a deterministic
-- ErrAuthUserDuplicate path.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username
    ON users (username);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email
    ON users (email);

-- Status filter for admin lifecycle (count active admins, suspend sweeps).
CREATE INDEX IF NOT EXISTS idx_users_status
    ON users (status);

-- +goose Down
-- WARNING: Irreversible — DROP TABLE destroys every user row including the
-- bootstrap admin. Coordinate with operator before running in any
-- environment that has real data.
DROP INDEX IF EXISTS idx_users_status;
DROP INDEX IF EXISTS idx_users_email;
DROP INDEX IF EXISTS idx_users_username;
DROP TABLE IF EXISTS users;
