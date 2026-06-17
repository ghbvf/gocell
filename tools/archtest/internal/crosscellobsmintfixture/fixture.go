//go:build archtest_fixture

// Package crosscellobsmintfixture is the RED fixture for
// CROSSCELLOBS-MINTER-FUNNEL-01.
//
// It references transport.NewCrossCellObs — the sealed cross-cell observability
// bundle minter — from OUTSIDE framework/runtime/composition (the sole sanctioned
// minter) two ways: a direct call (ForgeBundle) and a function-value reference
// (ForgeViaFuncValue, the func-value blind spot a CallExpr.Fun-only scan would
// miss). Both are the forge the funnel must flag: a package that mints its own
// CrossCellObs can pair the remote transport with a DIFFERENT tracer than the one
// composition.Builder threads into in-process spans via bootstrap.WithTracer,
// breaking the "remote and in-proc spans share one tracer" single-source invariant
// (ADR 202606131142-1423 D4).
//
// SafeOtherCtor is the GREEN control: it constructs a DIFFERENT transport value
// (NewInProcess), which the scanner must NOT flag — the funnel keys on the
// NewCrossCellObs symbol, not on any transport selector.
//
// DO NOT use this package in production code.
package crosscellobsmintfixture

import "github.com/ghbvf/gocell/framework/runtime/transport"

// ForgeBundle mints a CrossCellObs outside composition via a direct call — the
// violation the funnel must catch.
func ForgeBundle() transport.CrossCellObs {
	return transport.NewCrossCellObs(nil, nil)
}

// ForgeViaFuncValue launders the minter through a function value before calling
// it: the call expression's Fun is the local `mint`, so a CallExpr.Fun-only scan
// would MISS this. The funnel's SelectorExpr-level scan resolves the
// transport.NewCrossCellObs reference on the assignment RHS, so it is still
// flagged. Regression guard for the func-value blind spot.
func ForgeViaFuncValue() transport.CrossCellObs {
	mint := transport.NewCrossCellObs
	return mint(nil, nil)
}

// SafeOtherCtor constructs a different transport value (NewInProcess) — the GREEN
// control the scanner must leave unflagged (it keys on NewCrossCellObs, not any
// transport selector).
func SafeOtherCtor() {
	_ = transport.NewInProcess(nil)
}
