-- +goose Up
-- saga journal tables (PR-04, #959).
--
-- Two truths under one transaction in adapters/postgres/saga.PGJournal:
--   - saga_instances (mutable projection): status + current_version + lease_id
--     fencing token; updated atomically with each event append.
--   - saga_events (append-only log): authoritative event history; PK
--     (instance_id, version) enforces version monotonicity.
--
-- The lease_id CAS pattern mirrors adapters/postgres outbox (migration 014 /
-- ADR 202605051600-adr-pg-outbox-fencing.md): each ClaimPending mints a fresh
-- UUID; subsequent Append / Heartbeat / MarkTerminal CAS-fence on (id,
-- lease_id, lease_expires_at >= injected_now). Boundary `>=` (not strict `>`)
-- aligns with memjournal's fenced semantic — at exact equality the lease is
-- still valid (verified by conformance ExactlyAtLeaseExpiry subtests).
--
-- Status/Kind columns use SMALLINT mapping the saga.Status (iota+1) and
-- journal.EventKind (iota+1) enums; 0 is reserved as the invalid zero-value
-- sentinel and never persisted.
--
-- ref: adapters/postgres/migrations/014_add_outbox_lease_id.sql
-- ref: temporalio/temporal common/persistence/sql/sqlplugin/postgresql

CREATE TABLE saga_instances (
    -- id: kernel idutil.SafeID (typed string). UUID-shaped in production
    -- callers, but the kernel API is string-typed and the conformance suite
    -- uses readable IDs ("inst-foo"), so the column is TEXT.
    id                TEXT PRIMARY KEY,
    definition_id     TEXT        NOT NULL,
    -- status: saga.Status iota+1 — Pending=1, Running=2, Compensating=3,
    -- Succeeded=4, Failed=5, Compensated=6, Expired=7.
    status            SMALLINT    NOT NULL,
    current_version   BIGINT      NOT NULL DEFAULT 0,
    -- lease_id is the fencing token minted by ClaimPending (UUID v4 in
    -- production); nullable so newly Enqueued (pre-claim) and terminal
    -- (post-MarkTerminal) rows carry no lease. lease_expires_at is paired
    -- with lease_id by CHECK constraint so the two columns evolve as one
    -- fencing record. TEXT (vs UUID) so a non-UUID test lease value yields a
    -- clean RowsAffected==0 instead of a 22P02 syntax error on cast.
    lease_id          TEXT,
    lease_expires_at  TIMESTAMPTZ,
    started_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL,
    -- payload_state is reserved for optional saga state snapshots (PR-06
    -- projection materialization). The PR-04 store does not write or read
    -- this column; it is provisioned now to avoid an ALTER ADD COLUMN in a
    -- later PR. BYTEA so byte-level identity round-trips (JSON shape gate
    -- lives in the application layer, not the DB).
    payload_state     BYTEA,
    CONSTRAINT saga_instances_status_range CHECK (status BETWEEN 1 AND 7),
    CONSTRAINT saga_instances_version_nonneg CHECK (current_version >= 0),
    CONSTRAINT saga_instances_lease_paired CHECK (
        (lease_id IS NULL AND lease_expires_at IS NULL)
        OR (lease_id IS NOT NULL AND lease_expires_at IS NOT NULL)
    )
);

-- Partial index over claimable rows only (non-terminal + unleased-or-expired).
-- ClaimPending's CTE SELECT scans this index instead of the full table; index
-- bytes shrink as terminal rows accumulate.
CREATE INDEX idx_saga_instances_claimable
    ON saga_instances (started_at, id)
    WHERE status IN (1, 2, 3);

CREATE TABLE saga_events (
    instance_id  TEXT        NOT NULL,
    version      BIGINT      NOT NULL,
    -- kind: journal.EventKind iota+1 — StepStarted=1..StepCompensated=4,
    -- CompensationStarted=5, SagaSucceeded=6..SagaExpired=9.
    kind         SMALLINT    NOT NULL,
    -- step_name is required for step kinds (1..4), empty for saga-scoped kinds
    -- (CompensationStarted, terminal). Enforced at the journal layer (the
    -- Event.ValidateForAppend gate) rather than via a partial CHECK to keep
    -- schema-side validation orthogonal to kind taxonomy evolution.
    step_name    TEXT,
    -- payload BYTEA preserves byte-level identity of the JSON object the
    -- caller submitted; JSON shape validation lives in
    -- journal.Event.ValidateForAppend, not in the DB.
    payload      BYTEA,
    created_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (instance_id, version),
    -- ON DELETE CASCADE: schema integrity for test TRUNCATE saga_events,
    -- saga_instances CASCADE; production never DELETEs saga_instances (terminal
    -- archival is the responsibility of a later PR), so the cascade is
    -- defense-in-depth, not a runtime hot path.
    FOREIGN KEY (instance_id) REFERENCES saga_instances(id) ON DELETE CASCADE,
    CONSTRAINT saga_events_kind_range CHECK (kind BETWEEN 1 AND 9),
    CONSTRAINT saga_events_version_positive CHECK (version >= 1)
);

-- +goose Down
-- WARNING: Irreversible — DROP TABLE destroys every saga_instances / saga_events row.
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP INDEX IF EXISTS idx_saga_instances_claimable;
DROP TABLE IF EXISTS saga_events;
DROP TABLE IF EXISTS saga_instances;
