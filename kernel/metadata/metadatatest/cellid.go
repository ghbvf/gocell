// Package metadatatest provides typed builders for constructing
// kernel/metadata fixtures in *_test.go files. The builders fail fast
// at fixture construction time when an invalid identifier slips in,
// turning a Soft (hand-crafted string literal) fixture into a Hard
// funnel that archtest can statically verify.
//
// Importing this package from production code is rejected by archtest
// METADATATEST-IMPORT-SCOPE-01 — it is exclusively a test-only helper.
//
// AI-robust funnel (see .claude/rules/gocell/ai-robust.md §Hard 范本目录,
// "string-typed concept funnel"):
//   - Upstream (Hard): NewCellID body shape locked by archtest
//     FIXTURE-CELLID-TYPED-BUILDER-01/A2 — single if-panic + return form.
//   - Downstream (Hard): fixture callsite identity locked by
//     FIXTURE-CELLID-TYPED-BUILDER-01/A1 — every cell-id-typed field
//     position in a kernel/metadata.* composite literal must resolve
//     to NewCellID(BasicLit) or a metadatatest package-level var.
package metadatatest

import (
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// NewCellID returns s if it satisfies metadata.MatchCellID; panic otherwise.
//
// Use in test fixtures (kernel/metadata.* struct construction with cell-id
// fields) so that an invalid literal fails fast at fixture construction
// rather than surviving until the specific test that happens to invoke
// FMT-C1. The panic is wrapped with panicregister.Approved so that
// PANIC-REGISTERED-01 accepts this call site.
func NewCellID(s string) string {
	if !metadata.MatchCellID(s) {
		panic(panicregister.Approved(
			"metadatatest-cell-id-invalid",
			errcode.Assertion("metadatatest: invalid cell id %q", s),
		))
	}
	return s
}

// Pre-defined cell-id constants for kernel/governance/*_test.go fixtures.
// Every value is validated by NewCellID at package init — if an entry below
// is ever invalid, the test binary fails to load instead of failing midway
// through a test run.
//
// Synthetic / ad-hoc cell-ids inside individual test functions still go
// through NewCellID(literal) directly; the constants below cover the
// closed enumeration of cell-ids used by multiple fixture sites.
var (
	CellIDAccessCore     = NewCellID("accesscore")
	CellIDAuditCore      = NewCellID("auditcore")
	CellIDBillingCore    = NewCellID("billingcore")
	CellIDConfigCore     = NewCellID("configcore")
	CellIDSharedCrypto   = NewCellID("sharedcrypto")
	CellIDSharedValidate = NewCellID("sharedvalidate")
	CellIDFoobar         = NewCellID("foobar")
	CellIDAA             = NewCellID("aa") // short synthetic id used by location_integration_test.go fixtures
)
