// Package saga implements kernel/saga/journal.Journal over PostgreSQL.
//
// Two tables under one transaction per mutation (see migration 040):
//   - saga_instances (mutable projection): status + current_version + lease
//   - saga_events (append-only log): authoritative event history,
//     PK (instance_id, version) enforces version monotonicity.
//
// Fencing model mirrors adapters/postgres outbox (ADR
// 202605051600-adr-pg-outbox-fencing.md): ClaimPending mints a fresh UUID
// lease per batch; subsequent Append / Heartbeat / MarkTerminal CAS-fence on
// (id, lease_id, lease_expires_at >= injected_now) so a zombie leader's
// write must miss the row and surface as KindConflict (Append) or
// (false, nil) (Heartbeat / MarkTerminal). The `>=` boundary (vs strict `>`)
// matches memjournal's `!Before(now)` semantic — at exact equality the lease
// is STILL VALID — and is verified by conformance subtests
// {Append,ClaimPending,Heartbeat}_ExactlyAtLeaseExpiry_*.
// Behavior matches the in-memory reference
// implementation (kernel/saga/journal.MemJournal) and is verified by
// sagajournaltest.RunConformanceSuite over a real PostgreSQL backend
// (testcontainers).
//
// Status / EventKind columns are SMALLINT mappings of saga.Status and
// journal.EventKind enums (iota+1; the zero value is the invalid sentinel and
// is never persisted). The mapping is centralized in store.go statusFromInt16
// / kindFromInt16 — no magic numbers appear in SQL bodies.
//
// ref: adapters/postgres/outbox_store.go (lease_id CAS pattern)
// ref: tools/archtest/user_repo_conformance_enrollment_test.go (SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01)
package saga
