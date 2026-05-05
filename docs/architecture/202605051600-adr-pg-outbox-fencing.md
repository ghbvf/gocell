# ADR — PG Outbox Claim Fencing Token

- **Date**: 2026-05-05
- **Status**: Accepted (shipped via fix/221-pg-outbox-fencing; cutover hardened in fix/225-outbox-fu-closure)
- **Closes**: backlog2 B2-A-01 (P0); B2-A-04 / B2-A-05 / B2-A-06 / B2-A-07 / B2-A-12 (P1/P2 absorbed)
- **Roadmap**: 029 §并行轨道 Track B B1 (PR-V1-DATA-OUTBOX-FENCING) absorbing B6 (PR-V1-PG-OUTBOX-RELAY-HARDEN); cutover updated by 030 § B' N8 (PR-V1-OUTBOX-FU-CLOSURE)

## Context

The PG `outbox_entries` table backs L2 OutboxFact: a worker `ClaimPending`s a batch (status `pending` → `claiming`), publishes to the broker, then `MarkPublished`/`MarkRetry`/`MarkDead` to record the outcome. A separate `ReclaimStale` sweep recovers rows whose worker crashed mid-publish (status `claiming` and `claimed_at` past TTL → back to `pending`).

Before this PR, the four "mark" CAS SQLs only checked `status = 'claiming'`:

```sql
UPDATE outbox_entries SET status = 'published', published_at = now()
  WHERE id = $id AND status = 'claiming';
```

A surviving worker A whose `Claim` succeeded but whose broker publish was still in flight could complete after a sequence of:

1. ReclaimStale runs (claimed_at > TTL) → row reset to pending
2. Worker B re-`Claim`s the same row
3. Worker A's broker publish finally returns; A calls `MarkPublished(id)`

A's CAS still matches B's `claiming` row — A overwrites B's row to `published`, even though B's broker publish hasn't run. The L2 OutboxFact "exactly once published" invariant is broken (the message may never be published or may be silently dropped from the relay's perspective).

This is a textbook missing-fencing-token race (Lamport / Kleppmann DDIA Ch.8). The fix is well-trod prior art across PG-backed job queues.

## Decision

Add a `lease_id UUID` column to `outbox_entries`. Every `ClaimPending` issues a fresh `uuid.NewString()` and stamps it into the materialized rows in the same transaction. The four mark CAS SQLs add `AND lease_id = $N`; `ReclaimStale` clears the lease on the back-to-pending branch (and rotates the new claim's lease via the next `Claim` call).

The runtime `Store` interface threads `leaseID` through `MarkPublished(ctx, id, leaseID)` / `MarkRetry(...)` / `MarkDead(...)`. `ClaimedEntry` carries `LeaseID string` so relay's writeBack passes the entry's own lease back. A `MarkRetry`/`MarkDead` returning `(false, nil)` (lease lost) is now a Warn log path with no stats counting — the new lease holder will produce the canonical outcome.

### Open-source benchmark

| Reference | File | Adopted pattern |
|---|---|---|
| **graphile/worker** | `sql/000001.sql` `get_job` / `complete_job` / `fail_job` | Double-column fencing (`locked_by` / `locked_at`); mark CAS `WHERE locked_by = worker_id`; `RowsAffected==0` is a no-op + log. GoCell's `lease_id + claimed_at` is the same shape with `lease_id` as UUID instead of TEXT(hostname). |
| **jackc/pgxjob** | `pgxjob.go` claim CTE | `worker_id UUID` selection: nullable, written inside a CTE-driven `UPDATE … FROM picked` clause — the structural equivalent to GoCell's existing `claimPendingQuery`. |
| **riverqueue/river** | `riverdriver/riverpgxv5/internal/dbsqlc/*.sql` | River's `attempted_by text[]` is **not** ownership — it's append-only audit; River relies on `FOR UPDATE` row-locking inside a single TX. GoCell's mark/reclaim are short independent TX (no row lock spanning them), so explicit token CAS is required. |
| **bensheldon/good_job** | RFC #831 | Worker-process-FK extension (`locked_by_id` → `good_job_processes`) is a v1.1+ upgrade path — `lease_id UUID` leaves it open. |
| **pgmq (tembo-io)** | `q_*` table `vt + read_ct` | vt-only (visibility timeout) is equivalent to GoCell's existing `claimed_at + TTL` — sufficient against time-window-out staleness, **insufficient against in-window concurrent re-claim**. This is precisely the B2-A-01 root cause; vt-only would not fix it. |

### Rejected alternatives

- **PG advisory lock** (`pg_try_advisory_xact_lock`): single shared lock space across the whole DB; cannot express "worker N holds these M leases concurrently"; cross-cell interference once multiple consumer groups join.
- **Monotonic `claim_version int`**: equivalent strength to UUID, but GoCell IDs are already strings and the type uniformity matters for serialization; UUID has no overflow story; `gen_random_uuid()` is broker-portable for future replication.
- **`locked_by TEXT` (hostname)**: graphile's choice; relies on hostname uniqueness across replicas; fragile in K8s pod-rescheduling. UUID-per-claim is dependency-free.
- **`lease_until TIMESTAMPTZ` separate column**: graphile / pgxjob / gue all derive expiry from `claimed_at + TTL`; adding a column means an extra index target with no semantic gain.

### Rejected: dropping the down migration

CLAUDE.md "no backward compat" applies to runtime/API surface, not to migrator tooling. The repo convention (`003_outbox_status_columns.sql` etc.) ships full Up/Down. We follow suit; the Down drops the index then the column, which is non-lossy because pre-PR rows have `lease_id = NULL` anyway.

## Consequences

- **Migration 014**: new column nullable, no default, with a partial index `idx_outbox_claiming_lease` keyed on lease_id WHERE status='claiming'. ALTER TABLE ADD COLUMN with no default is O(metadata) — no table rewrite. The migration also fail-closed checks for any in-flight `claiming` rows; presence aborts so operators drain workers before applying.
- **Migration 015 (N8 cutover)**: adds CHECK constraint `outbox_claiming_requires_lease` that DB-enforces `status <> 'claiming' OR lease_id IS NOT NULL`. Any rolling-deploy attempt by a stale pre-014 binary to write `claiming + NULL lease_id` directly raises SQLSTATE 23514, eliminating the rolling-deploy footgun without a runtime probe. The previous startup probe `VerifyOutboxLeaseInvariant` and its dedicated errcode `ErrAdapterPGOutboxLeaseInvariant` are deleted in the same change — DB CHECK is the single source of truth and the `lease_id IS NULL` three-valued-logic edge case in reclaimStale CAS is naturally subsumed (the offending state cannot exist in the table).
- **Interface delta**: `outbox.Store.Mark{Published,Retry,Dead}` gain `leaseID string`; `ClaimedEntry` gains `LeaseID string`. Direct change with no shim — there are no external Store consumers per CLAUDE.md.
- **B6 absorbed in same PR**: B2-A-04/05/06/07/12 all live in `adapters/postgres/outbox_*.go` + `runtime/outbox/relay.go` and overlap the same edit window. Splitting would force every PR to rebase across `outbox_store.go` and `relay.go`. They ship together as PR-V1-DATA-OUTBOX-FENCING-AND-RELAY-HARDEN.
- **Static guards**: three new archtest gates lock the regression doors:
  - `OUTBOX-LEASE-ID-CAS-01`: five SQL constants must reference `lease_id`.
  - `OUTBOX-MARK-RETURNS-BOOL-01`: relay writeBack callers must bind the `updated bool`.
  - `OUTBOX-METADATA-MAX-BYTES-01`: writer must reference the `MaxMetadataBytes` cap.
- **Observability**: `outbox: stale lease lost fail-write` Warn log carries `entry_id`, `lease_id`, `outcome` so operators can correlate stats drift with reclaim activity. No new metric series — the existing `outbox_relay_*` counters are correct under the new semantics.

## Out of scope (separate Lane B PRs)

- **B2-A-26** Redis idempotency Commit/Release race (Lua atomic CAS) — same pattern, different adapter.
- **B5** PG refresh-store cross-store TX ACID.
- **B11** Redis cell-namespace prefix.
- **B4** PG migrator advisory locker.

## References

- ref: graphile/worker `sql/000001.sql` get_job/complete_job
- ref: jackc/pgxjob `pgxjob.go` claim CTE
- Background: Lamport fencing tokens (Kleppmann DDIA §8.4); HashiCorp raft `Term`/`LeaderID`. (Conceptual lineage; not direct source.)
