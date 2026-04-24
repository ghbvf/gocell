// Package adminprovision encapsulates the idempotent, race-safe "bring the
// first admin into existence" domain logic shared by two consumers:
//
//   - cells/accesscore/initialadmin: headless startup Lifecycle that writes
//     a credential file (auto-generated password) on first run.
//   - cells/accesscore/slices/setup: interactive POST /api/v1/setup/admin
//     HTTP endpoint (operator-supplied password).
//
// The package is caller-tx-neutral: Ensure does not open its own transaction
// and does not emit events, so callers compose it with whichever persistence
// boundary they own (no tx for initialadmin, TxRunner + outbox for setup).
//
// Outcomes are modeled as a ProvisionOutcome enum rather than a boolean so
// callers can distinguish fresh creates (write credfile / emit event), prior
// completions (silent skip / 409), concurrent-replica races (silent skip),
// and orphan-recovery resumption (previous crashed run).
package adminprovision
