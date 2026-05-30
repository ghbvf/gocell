-- Migration 030: commands table for kernel/command PG adapter.
--
-- Backs adapters/postgres/command_queue.go (B2.B PG-DEVICECELL-REPO), the PG
-- implementation of kernel/command.Queue + kernel/command.ActiveScanner. The
-- commands queue is L4 DeviceLatent — entries traverse the state machine
-- Pending → Sent → Delivered → {Succeeded,Failed,Expired,Canceled} through
-- distinct Queue method calls (see kernel/command/queue.go godoc).
--
-- The status column stores the SMALLINT representation of kernel/command.Status:
--   1 = StatusPending     (created, awaiting Dequeue)
--   2 = StatusSent        (claimed with lease)
--   3 = StatusDelivered   (device ACKed receipt via Report)
--   4 = StatusSucceeded   (terminal, Ack(AckSuccess))
--   5 = StatusFailed      (terminal, Ack(AckFailed))
--   6 = StatusExpired     (terminal, Ack(AckTimeout) by Sweeper)
--   7 = StatusCanceled    (terminal, Ack(AckRejected) or operator Cancel)
--
-- Sweeper contract (kernel/command/sweeper.go): a 30s tick lifecycle hook
-- (registered in examples/iotdevice/cells/devicecell/cell.go) calls
-- ActiveScanner.ScanActive(filter) to find non-terminal entries past their
-- DeadlineFor, then Queue.Ack(id, AckTimeout, now) to transition them to
-- StatusExpired. Dequeue picks ONLY StatusPending — lease recovery is the
-- sweeper's job, NOT done inline in Dequeue (avoids double defense).
--
-- ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §B2.B
-- ref: kernel/command/queue.go (Queue interface state machine doc)
-- ref: adapters/postgres/outbox_store.go (FOR UPDATE SKIP LOCKED Dequeue template)

-- +goose Up
CREATE TABLE IF NOT EXISTS commands (
    -- id is TEXT (not UUID) so it stays in lock-step with kernel/command.Entry.ID,
    -- which is declared as `string` in kernel/command/entry.go. Calling code (and
    -- the InMemQueue) generates hex IDs or accepts user-supplied identifiers;
    -- forcing UUID here would diverge from the kernel contract and break the
    -- mem+PG shared conformance suite. devices.id is TEXT for the same reason.
    id                              TEXT        PRIMARY KEY,
    -- ON DELETE RESTRICT: deleting a device with pending/active commands is
    -- prevented at the DB level; operators must terminate commands first.
    device_id                       TEXT        NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    command_type                    TEXT        NOT NULL,
    -- BYTEA matches kernel/command.Entry.Payload []byte; NOT NULL because
    -- kernel Entry.ValidateNew rejects empty Payload at creation.
    payload                         BYTEA       NOT NULL,
    -- JSONB matches kernel/command.Entry.Metadata map[string]string; default
    -- empty object so unset metadata serializes consistently.
    metadata                        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    -- kernel/command/status.go: StatusPending=1 ... StatusCanceled=7.
    status                          SMALLINT    NOT NULL,
    -- kernel/command.Entry.Attempt — incremented by Dequeue when re-Dequeueing.
    attempt                         INTEGER     NOT NULL DEFAULT 0,
    created_at                      TIMESTAMPTZ NOT NULL,
    -- Phase timestamps follow the kernel state machine. NULL until the
    -- corresponding transition fires.
    sent_at                         TIMESTAMPTZ NULL,
    delivered_at                    TIMESTAMPTZ NULL,
    completed_at                    TIMESTAMPTZ NULL,
    -- Lease expiry: NULL when Pending (no lease) or after terminal Ack.
    -- Sweeper compares lease_expiry < now() to find timed-out Sent/Delivered.
    lease_expiry                    TIMESTAMPTZ NULL,
    -- kernel/command.Timeouts.{ScheduleToSend,SendToComplete,OverallDeadline}
    -- stored as nanoseconds (time.Duration native unit); NULL = no timeout.
    timeouts_schedule_to_send_ns    BIGINT      NULL,
    timeouts_send_to_complete_ns    BIGINT      NULL,
    timeouts_overall_ns             BIGINT      NULL,
    CONSTRAINT commands_status_chk  CHECK (status BETWEEN 1 AND 7),
    CONSTRAINT commands_attempt_chk CHECK (attempt >= 0)
);

-- Dequeue path (FIFO over Pending): WHERE device_id=$1 AND status=1
-- ORDER BY created_at FOR UPDATE SKIP LOCKED. Partial index on
-- status=1 keeps it tight since terminal entries dominate at scale.
CREATE INDEX IF NOT EXISTS idx_commands_pending_fifo
    ON commands (device_id, created_at)
    WHERE status = 1;

-- Sweeper path: WHERE status IN (2, 3) AND lease_expiry < now().
-- Partial index over Sent/Delivered keeps timeout scans fast.
CREATE INDEX IF NOT EXISTS idx_commands_active_lease
    ON commands (lease_expiry)
    WHERE status IN (2, 3);

-- ScanActive(DeviceID=...) operational path: filter by device + non-terminal status.
CREATE INDEX IF NOT EXISTS idx_commands_device_active
    ON commands (device_id, status, created_at)
    WHERE status IN (1, 2, 3);

-- +goose Down
-- WARNING: Irreversible — DROP TABLE destroys every command row.
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP INDEX IF EXISTS idx_commands_device_active;
DROP INDEX IF EXISTS idx_commands_active_lease;
DROP INDEX IF EXISTS idx_commands_pending_fifo;
DROP TABLE IF EXISTS commands;
