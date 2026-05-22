// Package adminprovision encapsulates the idempotent, race-safe "bring the
// first admin into existence" domain logic. After PR #392 the only consumer
// is cells/accesscore/slices/setup (the interactive POST /api/v1/access/setup/admin
// endpoint with an operator-supplied password); the headless initialadmin
// Lifecycle has been deleted.
//
// The package is caller-tx-neutral: Ensure does not open its own transaction
// and does not emit events, so callers compose it with whichever persistence
// boundary they own (TxRunner + outbox for setup). Ensure is NOT internally
// serialized — the in-process sync.Mutex was removed once PR #482 wired the
// PG adapter; concurrent invocations must be serialized by the caller through
// a RunInTx + ports.SetupLockAcquirer pair:
//
//   - PG mode: accesspg.NewBundle(pool, txm, clk).SetupLock() uses pg_advisory_xact_lock for cross-pod
//     mutual exclusion.
//   - Memstore mode: accesscore.NoopSetupLock — memTxRunner.RunInTx holds
//     store.mu for the whole closure, already serializing goroutines.
//
// The cell-level accesscore.WithSetupLock is mandatory and rejects nil at
// phase0 (cells/accesscore/cell.go), so a composition root that forgets to
// wire either lock fails fast at startup, not at the first concurrent setup.
//
// Outcomes are modeled as a ProvisionOutcome enum rather than a boolean so
// callers can distinguish fresh creates (write credfile / emit event), prior
// completions (silent skip / 410), and concurrent-replica races (silent skip).
package adminprovision
