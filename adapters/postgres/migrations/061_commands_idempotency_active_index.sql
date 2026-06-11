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
-- Non-CONCURRENTLY: matches how migration 031 created the original index.
-- This migration runs inside a goose transaction at example scale (no
-- pre-existing production load); plain CREATE avoids the CONCURRENTLY
-- footgun inside a transaction block.
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
