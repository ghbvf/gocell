-- Migration 067: create saga_projection_dead_letters — the durable poison-event
-- sink for the saga-journal projection Tailer (#2110).
--
-- When business Apply returns a PERMANENT error (outbox.IsPermanent — bad payload,
-- unknown event kind, malformed id), the Tailer records the poison event here AND
-- advances its checkpoint past it in the SAME transaction (DeadLetterStore.Record +
-- OwnerCheckpointStore.AdvanceIfOwner under one TxRunner.RunInTx), so a single bad
-- event does not freeze the whole projection. This realizes the pull-model half of
-- the projection Apply contract ("permanent → routed to a durable dead-letter sink
-- + skipped, never blocking"); the push-model Coordinator routes to the broker DLX.
--
-- Design decisions:
--   - Natural composite PK (cell_id, projection_id, global_seq): one poison entry
--     per projection per journal position. No surrogate id — the natural key is
--     unique and is exactly the idempotency key Record dedups on (a re-driven skip
--     after a crash before the advance committed re-INSERTs ON CONFLICT DO NOTHING).
--     Mirrors projection_checkpoints' composite-PK style (045).
--   - error_type / error_message carry a REDACTED reason (the Tailer redacts via
--     pkg/redaction.RedactError before Record); error_type is the stable errcode
--     Code (e.g. ERR_VALIDATION_FAILED), a bounded classifier, not PII. Both
--     NOT NULL DEFAULT '' as belt-and-suspenders (Record always supplies them);
--     the column shapes are verified at startup by schema_guard.
--   - NO tenant_id / NO RLS: like projection_checkpoints (045) and projection_events
--     (058), this is a framework-internal, cell/projection-keyed operational table
--     written under the tailer's system identity (the reconcile/tailer path is
--     tenantless). It is not a tenant-scoped business table.
--
-- The durable journal (projection_events / saga journal) still retains the poison
-- event at its global_seq; this table is the queryable poison registry an operator
-- triages from (find by global_seq, read error_type/message) without scanning the
-- whole journal. Recovery: fix the bug, rewind projection_checkpoints.offset_seq
-- past the poison global_seq (Apply is idempotent), then DELETE the recovered rows
-- here as an admin (the serving role cannot — see the REVOKE below).
--
-- No CONCURRENTLY: fresh table in the ordered migration set, no live rows to lock.
--
-- ref: JasperFx/marten async-daemon DeadLetterEvent (mt_doc_deadletterevent) — skip-and-dead-letter on apply error.
-- ref: AxonFramework SequencedDeadLetterQueue (dead_letter_entry) — durable dead-letter store for a streaming processor.
-- ref: docs/architecture/202606191200-2110-adr-saga-tailer-poison-dead-letter.md.

-- +goose Up
CREATE TABLE IF NOT EXISTS saga_projection_dead_letters (
    cell_id       TEXT        NOT NULL,
    projection_id TEXT        NOT NULL,
    global_seq    BIGINT      NOT NULL,
    event_id      TEXT        NOT NULL,
    stream        TEXT        NOT NULL,
    error_type    TEXT        NOT NULL DEFAULT '',
    error_message TEXT        NOT NULL DEFAULT '',
    occurred_at   TIMESTAMPTZ NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cell_id, projection_id, global_seq)
);

-- Least-privilege for the serving role: the Tailer only INSERTs poison entries
-- (DeadLetterStore.Record) and never reads from this table. Ops/replay reads are
-- admin actions served by a dedicated admin/read-only role — not the serving role.
-- deploy/postgres/init/10-restricted-role.sh grants gocell_app the default
-- SELECT/INSERT/UPDATE/DELETE on every future table, so this table is born with all
-- four privileges; revoke SELECT/UPDATE/DELETE so the DB engine enforces INSERT-only
-- (least-privilege: serving role retains only INSERT). This is stricter than
-- projection_events (058) which keeps SELECT for replay/Position reads — here there
-- is no serving-path read at all. The IF EXISTS guard makes this a no-op where
-- gocell_app is not provisioned (dev/memory mode, template-build migration).
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gocell_app') THEN
    REVOKE SELECT, UPDATE, DELETE ON saga_projection_dead_letters FROM gocell_app;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- WARNING: dropping saga_projection_dead_letters PERMANENTLY DELETES the poison-event
-- registry; operators lose the record of which events were skipped and why.
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP TABLE IF EXISTS saga_projection_dead_letters;
