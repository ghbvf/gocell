-- +goose Up
-- +goose StatementBegin

-- Extend saga_events.kind range from 1..9 to 1..10 to admit
-- KindStepCompensationFailed (= 10) introduced by #1181.
--
-- Background: PR #1181 splits per-step compensate outcomes into two distinct
-- event kinds — KindStepCompensated (success) keeps wire value 4, and a new
-- KindStepCompensationFailed (failure) takes wire value 10 (appended at the
-- end of the iota chain to preserve existing kind values 1..9). The previous
-- runCompensation pathway co-opted KindStepFailed for compensate failures,
-- which the journal's Compensating-phase projection (memjournal.applyProjection
-- / pgsaga.projectAppend) rejected as out-of-phase, leaving compensate
-- failures only in slog and breaking event-replay reconstruction.
--
-- DROP + re-add is atomic under ALTER TABLE; existing rows whose kind is
-- in 1..9 stay valid against the new constraint. No data backfill needed.

ALTER TABLE saga_events DROP CONSTRAINT saga_events_kind_range;
ALTER TABLE saga_events ADD CONSTRAINT saga_events_kind_range
    CHECK (kind BETWEEN 1 AND 10);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reverting requires that no saga_events row carry kind=10.
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

ALTER TABLE saga_events DROP CONSTRAINT saga_events_kind_range;
ALTER TABLE saga_events ADD CONSTRAINT saga_events_kind_range
    CHECK (kind BETWEEN 1 AND 9);

-- +goose StatementEnd
