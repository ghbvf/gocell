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

// Pre-defined cell-id constants for kernel/* test fixtures + tooling fixtures.
// Every value is validated by NewCellID at package init — if an entry below
// is ever invalid, the test binary fails to load instead of failing midway
// through a test run.
//
// Synthetic / ad-hoc cell-ids inside individual test functions still go
// through NewCellID(literal) directly; the constants below cover the
// closed enumeration of cell-ids used by multiple fixture sites.
//
// New constants are added on demand when a literal appears at ≥ 3
// fixture sites. One-off literals stay inline via NewCellID(literal).
var (
	// Platform / production cell ids:
	CellIDAccessCore     = NewCellID("accesscore")
	CellIDAuditCore      = NewCellID("auditcore")
	CellIDBillingCore    = NewCellID("billingcore")
	CellIDConfigCore     = NewCellID("configcore")
	CellIDSharedCrypto   = NewCellID("sharedcrypto")
	CellIDSharedValidate = NewCellID("sharedvalidate")

	// Generic synthetic cell ids used by codegen / governance fixtures.
	// Where a fixture previously embedded a non-compliant id (kebab,
	// single-char, leading-digit, uppercase), it is migrated to a
	// pattern-compliant equivalent below and the fixture rewritten to use
	// the constant. The legacy non-compliant literal is recorded in a
	// trailing comment for grep-traceability.
	CellIDFoobar     = NewCellID("foobar")
	CellIDAA         = NewCellID("aa")         // legacy "a"
	CellIDBB         = NewCellID("bb")         // legacy "b"
	CellIDCC         = NewCellID("cc")         // legacy "c"
	CellIDXX         = NewCellID("xx")         // legacy "x"
	CellIDYY         = NewCellID("yy")         // legacy "y"
	CellIDDemo       = NewCellID("demo")
	CellIDTestCell   = NewCellID("testcell")   // legacy "test-cell"
	CellIDOrderCell  = NewCellID("ordercell")
	CellIDSampleCore = NewCellID("samplecore")
	CellIDAlpha      = NewCellID("alpha")
	CellIDBeta       = NewCellID("beta")
	CellIDDeviceCell = NewCellID("devicecell")
	CellIDGood       = NewCellID("good")
	CellIDPlain      = NewCellID("plain")
	CellIDSomeCore   = NewCellID("somecore")
	CellIDMyCell     = NewCellID("mycell")
	CellIDMyCore     = NewCellID("mycore")
	CellIDCellA      = NewCellID("cella")      // legacy "cell-a"
	CellIDCellB      = NewCellID("cellb")      // legacy "cell-b"
	CellIDCellC      = NewCellID("cellc")      // legacy "cell-c"
	CellIDEdgeBFF    = NewCellID("edgebff")    // legacy "edge-bff"
	CellIDExtGateway = NewCellID("extgateway") // legacy "ext-gateway"
	CellIDAppCore    = NewCellID("appcore")    // legacy "app-core"
	CellIDSvcA       = NewCellID("svca")       // legacy "svc-a"
	CellIDSvcB       = NewCellID("svcb")       // legacy "svc-b"
	CellIDL1Cell     = NewCellID("l1cell")     // legacy "l1-cell"
	CellIDMyL0Cell   = NewCellID("myl0cell")   // legacy "myL0cell" (uppercase)
)
