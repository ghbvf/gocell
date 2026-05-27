// Package metadatatest provides typed builders for constructing
// kernel/metadata fixtures in *_test.go files. The builders fail fast
// at fixture construction time when an invalid identifier slips in,
// turning a Soft (hand-crafted string literal) fixture into a Hard
// funnel that archtest can statically verify.
//
// Importing this package from production code is rejected by archtest
// METADATATEST-IMPORT-SCOPE-01 — it is exclusively a test-only helper.
//
// Scope note: FIXTURE-CELLID-TYPED-BUILDER-01/A1 currently enforces typed
// builder usage in kernel/ only. Test fixtures in runtime/, cells/, cmd/,
// examples/, and tools/ (outside the migrated tools/codegen +
// tools/generatedverify subset) may still embed bare cell-id literals (Soft
// state). Backlog issue #1201 tracks scope expansion to non-kernel/ packages.
//
// AI-robust funnel (see .claude/rules/gocell/ai-robust.md §Hard 范本目录,
// "string-typed concept funnel"):
//   - Upstream (Hard): NewCellID body shape locked by archtest
//     FIXTURE-CELLID-TYPED-BUILDER-01/A2 — single if-panic + return form.
//   - Downstream (Hard): fixture callsite identity locked by
//     FIXTURE-CELLID-TYPED-BUILDER-01/A1 — every cell-id-typed field
//     position in a kernel/metadata.* composite literal must resolve
//     to NewCellID(BasicLit) or a metadatatest package-level var.
//
// # Usage
//
// In test fixtures, replace bare string literals at cell-id field
// positions with metadatatest constants or NewCellID(literal):
//
//	// Before — A1 archtest rejects:
//	Cells: map[string]*metadata.CellMeta{"accesscore": {ID: "accesscore", ...}}
//
//	// After — using pre-validated constant (preferred for shared ids):
//	Cells: map[string]*metadata.CellMeta{
//	    metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore, ...},
//	}
//
//	// After — using NewCellID for one-off ids:
//	Cells: map[string]*metadata.CellMeta{
//	    metadatatest.NewCellID("freshid"): {ID: metadatatest.NewCellID("freshid"), ...},
//	}
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
//
// The argument s is expected to be a compile-time string literal per
// FIXTURE-CELLID-TYPED-BUILDER-01/A1 enforcement — callers that pass a
// variable or runtime-computed value are rejected by A1. The use of s
// as a dynamic argument to errcode.Assertion(format, s) is intentional:
// this is a B-class programmer-error panic (invalid literal supplied at
// fixture construction time), not user input, so embedding s in the
// assertion message is acceptable per the panic taxonomy in
// .claude/rules/gocell/error-handling.md §Panic taxonomy.
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
	//
	// Naming provenance for AI co-authors:
	//
	//   CellIDAA/BB/CC/XX/YY — compliant substitutes for single-char legacy
	//   ids ('a' → 'aa', 'b' → 'bb', etc.). The doubling is a migration
	//   artifact; new ids should use descriptive names.
	//
	//   CellIDCellA/B/C — kebab legacy 'cell-a'/'cell-b'/'cell-c' de-dashed
	//   per FMT-16 no-dash rule. Do not mirror this pattern for new ids;
	//   prefer a semantic name (e.g. 'authcell' instead of 'cella').
	//
	//   CellIDEdgeBFF — 'BFF' stands for Backend-For-Frontend (common
	//   abbreviation). The value is 'edgebff', not 'edge-bff' (no dashes).
	//
	//   CellIDMyL0Cell — name contains consistency level 'L0'; migrated from
	//   legacy 'myL0cell' (uppercase). The L0 in the name is retained for
	//   grep-traceability only; new constants should not encode level in
	//   their name.
	//
	// These constants are migration artifacts. New fixtures should use
	// descriptive cell ids via NewCellID(literal) rather than mirroring
	// these naming patterns.
	CellIDFoobar     = NewCellID("foobar")
	CellIDAA         = NewCellID("aa") // legacy "a"
	CellIDBB         = NewCellID("bb") // legacy "b"
	CellIDCC         = NewCellID("cc") // legacy "c"
	CellIDXX         = NewCellID("xx") // legacy "x"
	CellIDYY         = NewCellID("yy") // legacy "y"
	CellIDDemo       = NewCellID("demo")
	CellIDTestCell   = NewCellID("testcell") // legacy "test-cell"
	CellIDOrderCell  = NewCellID("ordercell")
	CellIDSampleCore = NewCellID("samplecore")
	CellIDAlpha      = NewCellID("alpha")
	CellIDBeta       = NewCellID("beta")
	CellIDDeviceCell = NewCellID("devicecell")
	CellIDGood       = NewCellID("good")
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
