// Package adminprovision encapsulates the idempotent, race-safe "bring the
// first admin into existence" domain logic shared by two consumers:
//
//   - cells/accesscore/initialadmin: headless startup Lifecycle that writes
//     a credential file (auto-generated password) on first run.
//   - cells/accesscore/slices/setup: interactive POST /api/v1/access/setup/admin
//     HTTP endpoint (operator-supplied password).
//
// The package is caller-tx-neutral: Ensure does not open its own transaction
// and does not emit events, so callers compose it with whichever persistence
// boundary they own (no tx for initialadmin, TxRunner + outbox for setup).
// Ensure is serialized internally via sync.Mutex so fast-path → Create → Assign
// is atomic within a single process; multi-instance deployments must add a
// cross-process lock (e.g. pg_advisory_xact_lock) in the PG adapter.
//
// Outcomes are modeled as a ProvisionOutcome enum rather than a boolean so
// callers can distinguish fresh creates (write credfile / emit event), prior
// completions (silent skip / 410), concurrent-replica races (silent skip),
// and orphan-recovery resumption (previous crashed run).
package adminprovision
