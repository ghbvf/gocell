-- Migration 022: add `version` column to users for optimistic concurrency.
--
-- Fixes B2.A external-review P1#1b: identitymanage Update used a
-- "Get → mutate → Update full row" pattern with no row lock or version check.
-- Concurrent admin Lock + user self-update could lose-write the locked_at /
-- status fields because the second writer overwrote with a stale snapshot.
--
-- This migration introduces K8s-ResourceVersion-style optimistic concurrency:
-- every UPDATE sets version = version + 1 and gates on the prior value via
-- WHERE id = $1 AND version = $2. RowsAffected = 0 → ErrConcurrentUpdate (409).
--
-- The accesscore repo layer enforces this through dedicated PATCH methods
-- (UpdateProfile / Lock / Unlock / RotatePassword) — there is no global
-- "update everything" entry point. Each PATCH only writes the fields it owns,
-- so the admin-lock vs self-update collision is now both impossible
-- structurally (different fields) AND detected by CAS (concurrent updates to
-- the same field surface as conflict).
--
-- Sessions already carry the equivalent column (see 018_sessions.sql), so
-- after this migration both writable accesscore aggregates use the same
-- pattern. Roles are immutable (id+name set once at insert; permissions are
-- the only mutable column and are guarded by single-admin partial UNIQUE +
-- pg_advisory_xact_lock per role on revoke), so they don't need a version
-- column.
--
-- ref: kubernetes apimachinery pkg/api/meta resourceVersion
-- ref: ory/kratos optimistic-lock pattern (UPDATE … WHERE version = $n)
-- ref: cells/accesscore/internal/domain/user.go (User aggregate)

-- +goose Up
-- PG 12+ ADD COLUMN with constant DEFAULT is metadata-only — no table rewrite
-- occurs; only pg_attribute is updated. 5s lock_timeout is generous for this
-- operation (expected lock-wait p99 < 200ms on the users table in staging).
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

ALTER TABLE users
    ADD COLUMN version BIGINT NOT NULL DEFAULT 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users
    DROP COLUMN IF EXISTS version;
-- +goose StatementEnd
