-- Migration 020: add foreign key constraints to role_assignments.
--
-- Fixes B2.A external-review P1#1: role_assignments was a "uniqueness table"
-- without referential integrity — AssignToUser could write orphan roleIDs and
-- deleting a user left behind stale admin "occupation" rows indefinitely.
--
-- Two constraints are added:
--
--   fk_role_assignments_user (user_id → users.id) ON DELETE CASCADE
--     Deleting a user automatically removes all their role assignments.
--     Resolves both the orphan-assignment risk and the admin-row occupation
--     problem: after user deletion, the admin role becomes assignable again.
--
--   fk_role_assignments_role (role_id → roles.id) ON DELETE RESTRICT
--     A role that has active assignments cannot be deleted.
--     Prevents accidental bulk-revoke by dropping the role row; the operator
--     must explicitly remove all assignments before the DROP succeeds.
--
-- ref: ory/kratos persistence/sql FK + cascade migration pattern
-- ref: PostgreSQL FK constraint documentation (docs/ddl-constraints.html)

-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

ALTER TABLE role_assignments
    ADD CONSTRAINT fk_role_assignments_user
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    ADD CONSTRAINT fk_role_assignments_role
        FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE RESTRICT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';

ALTER TABLE role_assignments
    DROP CONSTRAINT IF EXISTS fk_role_assignments_role,
    DROP CONSTRAINT IF EXISTS fk_role_assignments_user;
-- +goose StatementEnd
