-- +goose Up
-- +goose StatementBegin

-- Extend saga_events.kind range 1..10 → 1..11 to admit KindSagaCompensationFailed (= 11)
-- introduced by #1210 C6. This event kind is the terminal journal record for
-- saga.StatusCompensationFailed — written exclusively via MarkTerminal when
-- the compensation phase completed with at least one CompensateFunc error.
--
-- Extend saga_instances.status range 1..7 → 1..8 to admit StatusCompensationFailed (= 8).
-- StatusCompensationFailed is a new distinct terminal status that replaces the overloaded
-- StatusFailed path from StatusCompensating: "rollback failed" is now structurally
-- unambiguous from "forward failed / no rollback" (StatusFailed).
--
-- DROP + re-add is atomic under ALTER TABLE; existing rows whose kind is in 1..10
-- and status is in 1..7 remain valid against the new constraints.
-- No data backfill needed.

ALTER TABLE saga_events DROP CONSTRAINT saga_events_kind_range;
ALTER TABLE saga_events ADD CONSTRAINT saga_events_kind_range
    CHECK (kind BETWEEN 1 AND 11);

ALTER TABLE saga_instances DROP CONSTRAINT saga_instances_status_range;
ALTER TABLE saga_instances ADD CONSTRAINT saga_instances_status_range
    CHECK (status BETWEEN 1 AND 8);

-- idx_saga_instances_claimable partial index (WHERE status IN (1, 2, 3)) is
-- intentionally NOT updated: StatusCompensationFailed (=8) is terminal and
-- must never be returned by ClaimPending. Same reasoning for store.go::
-- claimPendingQuery's hardcoded status filter.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reverting requires that no saga_events row carry kind=11 and no saga_instances row carry status=8.
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

ALTER TABLE saga_events DROP CONSTRAINT saga_events_kind_range;
ALTER TABLE saga_events ADD CONSTRAINT saga_events_kind_range
    CHECK (kind BETWEEN 1 AND 10);

ALTER TABLE saga_instances DROP CONSTRAINT saga_instances_status_range;
ALTER TABLE saga_instances ADD CONSTRAINT saga_instances_status_range
    CHECK (status BETWEEN 1 AND 7);

-- +goose StatementEnd
