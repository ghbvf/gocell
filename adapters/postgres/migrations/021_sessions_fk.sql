-- Migration 021: add foreign key constraint to sessions.user_id.
--
-- Fixes B2.A external-review P1#1c: sessions.user_id was a plain TEXT column
-- without referential integrity. Deleting a user left orphan sessions in the
-- table indefinitely, and integration tests that wanted to verify "user delete
-- → sessions cleared" had to write app-level cascade code.
--
-- Constraint added:
--
--   fk_sessions_user (user_id → users.id) ON DELETE CASCADE
--     Deleting a user automatically removes all that user's sessions. Pairs
--     with 020 fk_role_assignments_user (which already cascades on user delete);
--     the two together close the orphan story for accesscore.
--
-- Note: revoke is a soft action (UPDATE sessions SET revoked_at = NOW(),
-- migration 022 onwards in B2.2) and does not interact with this FK. Hard
-- DELETE remains available for GC paths and is what triggers the CASCADE.
--
-- ref: ory/kratos persistence/sql session FK + cascade migration pattern
-- ref: PostgreSQL FK constraint documentation (docs/ddl-constraints.html)

-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

ALTER TABLE sessions
    ADD CONSTRAINT fk_sessions_user
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS fk_sessions_user;
-- +goose StatementEnd
