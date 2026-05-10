//go:build archtest_fixture

// Dot-import fixture: a wrap call written as `WrapForCell(tr)` (no package
// selector) compiles when the wrapper package is dot-imported. The
// CELL-RAW-INFRA-WRAPPER-LOCATION-01 scanner must catch this form too —
// without an *ast.Ident branch in canonicalCalledFunc, the call site
// silently slips past the SelectorExpr-only matcher.
//
// This file lives in the same `violation` package as violation.go but uses
// dot-import on a *different* package than the normal-import in
// violation.go. Mixing dot-import and normal-import on the *same* package
// is not allowed in Go; we use kernel/outbox here because violation.go
// already normal-imports it without conflict.

package violation

import (
	. "github.com/ghbvf/gocell/kernel/outbox"
)

// CallDotImportWrapPublisher writes the wrap call without a package
// selector by relying on the dot-import. AST-wise call.Fun is *ast.Ident
// (just the function name) instead of *ast.SelectorExpr (pkg.Func).
func CallDotImportWrapPublisher(p Publisher) CellPublisher {
	return WrapPublisherForCell(p)
}

// CallDotImportWrapWriter mirrors the publisher case for the writer leg.
func CallDotImportWrapWriter(w Writer) CellWriter {
	return WrapWriterForCell(w)
}
