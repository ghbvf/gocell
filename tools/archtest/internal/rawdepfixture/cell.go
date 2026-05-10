// Package rawdepfixture is a deliberate CELL-RAW-DEPS-01 violation, used by
// TestCellRawDeps01_ScannerCatchesViolation as a real-source negative fixture.
//
// Per ai-collab.md §L4 "real source AST capture (AI 难造假)": the scanner
// must be exercised against a package loaded by packages.Load, not a
// hand-crafted AST. This package is loaded only by archtest via
// typeseval.SharedResolver; it is never compiled into the main binary.
//
// The fixture defines WithEvilTxManager, which exposes a raw infra type
// (persistence.TxRunner) under a function name NOT in the CELL-RAW-DEPS-01
// allowlist (allowlist only authorizes WithTxManager → persistence.TxRunner;
// a distinct name must be caught).
package rawdepfixture

import "github.com/ghbvf/gocell/kernel/persistence"

// Option is a placeholder functional-option type matching the pattern
// the scanner looks for (exported With* top-level function).
type Option func(any)

// WithEvilTxManager exposes a raw infra type (persistence.TxRunner) that is
// NOT covered by the CELL-RAW-DEPS-01 allowlist. The function name differs
// from "WithTxManager", so no allowlist entry authorizes it. The scanner must
// report exactly one violation for this function.
func WithEvilTxManager(tx persistence.TxRunner) Option {
	return func(_ any) {}
}
