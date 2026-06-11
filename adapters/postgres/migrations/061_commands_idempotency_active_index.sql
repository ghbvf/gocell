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
-- Non-CONCURRENTLY / in-transaction: acceptable ONLY at example/dev scale.
-- The commands table has no production load in this context. A real fleet
-- deployment MUST NOT use this form on a live commands table, because
-- DROP INDEX + CREATE UNIQUE INDEX (non-CONCURRENTLY) holds ACCESS EXCLUSIVE
-- on commands for the duration of the index build, blocking the entire command
-- write path (Enqueue, status updates) for that window.
--
-- For production fleet deployment use instead:
--   -- +goose no transaction
--   DROP INDEX CONCURRENTLY IF EXISTS idx_commands_idempotency_key;
--   CREATE UNIQUE INDEX CONCURRENTLY idx_commands_idempotency_key ...;
-- with a phased build-verify-drop rollout (build new index → verify valid →
-- drop old index) to keep the write path unblocked throughout.
--
-- The in-transaction non-CONCURRENTLY form is kept here intentionally: at
-- small/example scale it provides DROP+CREATE atomicity (both succeed or both
-- roll back), which is strictly better than the non-atomic CONCURRENTLY pair.
--
-- schema_guard.go: idx_commands_idempotency_key registration remains
-- Unique:true, Columns:[]string{"(expr)"} — the WHERE predicate is not
-- captured by the expectedIndex struct (only name/unique/columns are checked).
--
-- ref: adapters/postgres/command_queue_store.go::enqueueInsertSQL (arbiter predicate)
-- ref: adapters/postgres/migrations/031_commands_idempotency_unique.sql (original index)
-- ref: kernel/command (Status.IsTerminal — defines the non-terminal set 1,2,3)
-- ref: issue #1820 (devicecert producer state machine, retry-release requirement)

-- +goose Up
DROP INDEX IF EXISTS idx_commands_idempotency_key;
CREATE UNIQUE INDEX idx_commands_idempotency_key
    ON commands ((metadata->>'_idempotency_key'))
    WHERE metadata->>'_idempotency_key' IS NOT NULL
      AND status IN (1, 2, 3);

-- +goose Down
-- Reverse: restore the original permanent unique index from migration 031.
DROP INDEX IF EXISTS idx_commands_idempotency_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_commands_idempotency_key
    ON commands ((metadata->>'_idempotency_key'))
    WHERE metadata->>'_idempotency_key' IS NOT NULL;
