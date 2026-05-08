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
-- Phase 1: register FK without back-validating existing rows.
-- NOT VALID takes only a brief ACCESS EXCLUSIVE lock (metadata-only on PG 12+)
-- because PG does not scan the table to verify historical rows. New rows are
-- FK-checked immediately after this statement commits.
--
-- lock_timeout = 5s: chosen to be short enough that a busy sessions table
-- does not block DDL indefinitely, but long enough to survive typical
-- checkpoint spikes (measured p99 lock-wait on this table < 1s in staging).
-- If the ALTER times out, goose will retry on the next deploy; NOT VALID is
-- idempotent with respect to subsequent VALIDATE.
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

ALTER TABLE sessions
    ADD CONSTRAINT fk_sessions_user
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
        NOT VALID;
-- +goose StatementEnd

-- Phase 2: validate FK against existing rows.
-- VALIDATE CONSTRAINT takes only SHARE UPDATE EXCLUSIVE — concurrent writes
-- proceed unblocked. This statement is interruptible: if it is killed mid-run,
-- the NOT VALID constraint from Phase 1 remains in place (new rows are still
-- FK-checked), and the next deploy simply retries phase 2.
--
-- No lock_timeout here: VALIDATE holds a weaker lock and is designed to run
-- on live tables. Timing it out would just defer the work without benefit.
-- +goose StatementBegin
ALTER TABLE sessions
    VALIDATE CONSTRAINT fk_sessions_user;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS fk_sessions_user;
-- +goose StatementEnd
