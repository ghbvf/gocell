-- Migration 061: replace the permanent idempotency key index with a
-- state-aware active uniqueness index on commands._idempotency_key.
--
-- Background: Migration 031 created a permanent partial unique index that
-- enforces idempotency across ALL rows (including terminal ones). This means
-- once a command with a given idempotency key reaches a terminal state
-- (Succeeded=4, Failed=5, Expired=6, Canceled=7) the key is permanently
-- consumed — a retry with the same key silently no-ops even though the prior
-- attempt failed, violating the intended retry-release semantics.
--
-- Fix: replace the permanent index with one that is predicated on non-terminal
-- status (1=Pending, 2=Sent, 3=Delivered). When a command enters a terminal
-- state it drops out of the index, releasing the key for reuse. A new Enqueue
-- with the same key inserts a fresh row — the command is eligible for retry.
--
-- Model: River queue "ByState" uniqueness / Temporal single-open-workflow
-- model: only one active (non-terminal) instance of a workflow key at a time;
-- terminal rows do not block new submissions.
--
-- Non-terminal statuses (source of truth: kernel/command Status.IsTerminal()):
--   1=Pending, 2=Sent, 3=Delivered  → active / in-progress, KEY IS HELD
--   4=Succeeded, 5=Failed, 6=Expired, 7=Canceled → terminal, KEY IS RELEASED
--
-- The ON CONFLICT predicate in enqueueInsertSQL (command_queue_store.go) MUST
-- EXACTLY MATCH this index predicate for PG to infer the arbiter and fire
-- DO NOTHING on duplicate key within the active window.
--
-- Concurrency: this is index DDL on `commands`, a shared adapters/postgres
-- table on the general startup path (durable deployments auto-run Migrator.Up).
-- Per migrations/README.md rule 1 it uses `-- +goose no transaction` so the
-- DROP/CREATE run CONCURRENTLY and never hold ACCESS EXCLUSIVE on the command
-- write path — matching the established pattern in 057_devices_cert_expiry_index.
--
-- No duplicate-key gap during the swap: this index predicate change is coupled
-- to the enqueueInsertSQL ON CONFLICT predicate change in the same release, so
-- the old binary (predicate without `status IN (1,2,3)`) and the new binary are
-- incompatible with each other's index — the cutover is a coordinated, drained
-- deploy (drain → goose up → deploy new binary). With writes drained during the
-- swap, the brief window between DROP and CREATE admits no concurrent Enqueue,
-- so a same-name DROP+CREATE (rather than a phased build-verify-drop rename) is
-- both correct and minimal here.
--
-- Re-runnability (migrations/README.md rule 2 / MIGRATION-NO-TRANSACTION-RERUN-
-- SAFE-01): every statement is `IF [NOT] EXISTS`, so an interrupted run is safe
-- to replay. An INVALID index residue from an interrupted CONCURRENTLY build is
-- caught by Migrator.Up's DetectInvalidIndexes pre-check (rule 5) before any
-- migration runs, so the `CREATE … IF NOT EXISTS` silent-skip footgun cannot
-- bypass it.
--
-- schema_guard.go: idx_commands_idempotency_key registration remains
-- Unique:true, Columns:[]string{"(expr)"} — the WHERE predicate is not
-- captured by the expectedIndex struct (only name/unique/columns are checked).
--
-- ref: adapters/postgres/command_queue_store.go::enqueueInsertSQL (arbiter predicate)
-- ref: adapters/postgres/migrations/031_commands_idempotency_unique.sql (original index)
-- ref: adapters/postgres/migrations/057_devices_cert_expiry_index.sql (no-transaction CONCURRENTLY pattern)
-- ref: kernel/command (Status.IsTerminal — defines the non-terminal set 1,2,3)
-- ref: issue #1820 (devicecert producer state machine, retry-release requirement)

-- +goose no transaction

-- +goose Up
DROP INDEX CONCURRENTLY IF EXISTS idx_commands_idempotency_key;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_commands_idempotency_key
    ON commands ((metadata->>'_idempotency_key'))
    WHERE metadata->>'_idempotency_key' IS NOT NULL
      AND status IN (1, 2, 3);

-- +goose Down
-- Reverse: restore the original permanent unique index from migration 031.
DROP INDEX CONCURRENTLY IF EXISTS idx_commands_idempotency_key;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_commands_idempotency_key
    ON commands ((metadata->>'_idempotency_key'))
    WHERE metadata->>'_idempotency_key' IS NOT NULL;
