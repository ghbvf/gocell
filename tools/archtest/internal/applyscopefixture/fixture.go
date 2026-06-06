//go:build archtest_fixture

// Package applyscopefixture is an archtest RED fixture for
// TENANT-APPLYSCOPE-WRITE-CALLER-01. It calls
// persistence.CellTxManager.ApplyTenantScope (the mid-tx RLS scope-write entry)
// from a file OUTSIDE the method-caller allowlist. The go/types method-resolution
// detector must resolve the selector (info.Uses → *types.Func) and flag this
// reference — a 0 result means the detector regressed and any cell holding a
// CellTxManager could scope a transaction to an arbitrary tenant mid-flight.
//
// Method-call resolution is alias/qualifier-invariant by construction: the call
// site is `tx.ApplyTenantScope(...)`, with no package qualifier at all (the
// method binds to the receiver's type, not the import name), so a single fixture
// covers the alias form too; dot-import does not apply to methods.
//
// The other leg of the funnel — scopedtx.ApplyScope (the accesscore mid-flight
// helper) — cannot be exercised by a RED fixture here: it lives in
// cells/accesscore/internal/scopedtx, an internal package that tools/archtest
// cannot import. That internal-package visibility is itself the natural upstream
// bound for that leg (no external package can call it).
//
// (Build-tag gated behind archtest_fixture so it stays out of normal / Production
// builds — where it would be a real out-of-allowlist caller — and is loaded only
// by the Fixture() RunScope.)
package applyscopefixture

import (
	"context"

	"github.com/ghbvf/gocell/kernel/persistence"
)

// badMidTxScope writes a mid-tx RLS scope by calling CellTxManager.ApplyTenantScope
// directly. Outside the allowlist, the detector must report it.
func badMidTxScope(ctx context.Context, tx persistence.CellTxManager, tid string) error {
	return tx.ApplyTenantScope(ctx, tid)
}
