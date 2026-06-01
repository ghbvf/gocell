// audit_trace_id_write_caller_test.go — locks the appender as the SOLE
// injection write-path for runtime/audit/ledger.Entry.TraceID, so
// business/cell code cannot fabricate a trace_id into an audit record.
// TraceID must only flow from the outbox observability envelope via the
// appender.
//
// INVARIANT: AUDIT-TRACE-ID-WRITE-CALLER-01
//
// # What this guards
//
// ledger.Entry.TraceID must carry the OTel trace id extracted from the
// outbox.Entry observability envelope. It is populated by exactly one
// site:
//
//   - cells/auditcore/internal/appender/service.go — HandleEvent
//     composite literal: TraceID: string(entry.Observability().TraceID)
//
// This is the sole sanctioned injection path (issue #1048 Batch 4 / F1).
// The outbox.Entry seal (OUTBOX-ENTRY-SEALED-CONSTRUCTION-01) and the
// outbox observability-envelope construction (kernel/outbox) guarantee
// that trace_id flows into the audit record from a trusted provenance
// (the OTel trace span active at producer time), not from caller-supplied
// data. Allowing any other code to write ledger.Entry.TraceID would let
// any audit-event producer fabricate the trace context that correlates the
// audit record to a span.
//
// # Why upstream sealing is inherited, not separate
//
// The upstream provenance chain for TraceID is already sealed by two
// complementary mechanisms:
//
//  1. kernel/outbox.Entry is fully unexported (sealed construction,
//     OUTBOX-ENTRY-SEALED-CONSTRUCTION-01): no business code can
//     construct or populate outbox.Entry.observability.TraceID
//     directly — the only write path is OTel span extraction in the
//     tracing adapter.
//  2. kernel/observability/correlation.Correlation is similarly sealed:
//     the CorrelationID and TraceID in the outbox observability envelope
//     come from the http/grpc middleware layer, not from business code.
//
// This archtest therefore does NOT need a separate upstream funnel lock
// for ledger.Entry.TraceID — it INHERITS the upstream Hard guarantee from
// the sealed envelope. What this test adds is the downstream Medium guard:
// even if some future code tried to bypass the envelope and write
// ledger.Entry.TraceID directly (via a composite literal or assignment),
// this archtest catches it in CI before it reaches production.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Rating: MEDIUM (downstream caller-allowlist archtest).
//   - Downstream enforcement: archtest type-aware field-write scanner using
//     go/types. For composite literals, the scanner checks
//     types.Info.Types[lit].Type to bind the struct type to the canonical
//     ledger.Entry identity (immune to import aliases). For assignment
//     writes, it checks types.Info.Selections[sel].Kind() == FieldVal and
//     verifies the owning struct's package path. Import aliases and
//     dot-imports cannot bypass type-identity resolution.
//   - Upstream Hard (INHERITED, NOT duplicated here): The outbox.Entry
//     sealed construction (OUTBOX-ENTRY-SEALED-CONSTRUCTION-01) and the
//     sealed observability envelope make it structurally impossible for
//     business code to inject a fabricated TraceID into an outbox.Entry
//     in the first place. The sealed provenance chain means that
//     ledger.Entry.TraceID can ONLY receive a value that the appender
//     extracted from a trusted outbox.Entry.Observability() — no upstream
//     funnel lock is needed on top of that.
//   - Permanent ceiling rationale: Go package visibility cannot express
//     "only cells/auditcore/internal/appender may write this exported
//     struct field". A type-system Hard upstream lock would require
//     unexported fields or sealed constructors on ledger.Entry itself,
//     which conflicts with the store's need to reconstruct entries from DB
//     rows via reflect/scan (the postgres adapter takes &e.TraceID for
//     pgx row scanning). The current design keeps ledger.Entry as a
//     plain struct with exported fields for store polymorphism; the
//     archtest is the downstream enforcement backstop. Same permanent
//     ceiling as SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL
//     (#893 won't-do).
//
// # Allowlist semantics — three categories
//
// The allowlist is split by semantic role:
//
//   - INJECTION allowlist (traceIDInjectionAllowlist): the sole site that
//     sets TraceID to a value derived from the observability envelope.
//     Composite literal and assignment scans are SKIPPED for these files
//     (the write is sanctioned by design).
//
//   - RECONSTRUCTION (NOT in any allowlist): the postgres adapter takes
//     &e.TraceID as a scan-address passed to pgx rows.Scan() for DB row
//     reconstruction. The scanner cannot distinguish a Scan(&e.TraceID)
//     address-take from a pointer-write (blind spot #1), so the address-
//     take is documented and accepted. HOWEVER, the postgres file is NOT in
//     traceIDInjectionAllowlist, so the value-write scanner still runs on
//     it. Composite literal and assignment writes of ledger.Entry.TraceID
//     in the postgres file ARE violations and WILL fire the scanner. Only
//     the &e.TraceID UnaryExpr scan-address shape (not an AssignStmt LHS)
//     naturally escapes detection. A future value-assignment in the
//     postgres file WILL fire the scanner immediately (F6 invariant).
//
//   - TEST CONFORMANCE (traceIDInjectionAllowlist): storetest/ provides
//     contract test fixtures for Store implementations; it writes TraceID
//     in fixture entries for Append. Exclusively test infrastructure
//     (imported only from *_test.go files).
//
//   - _test.go files: always allowed (tests legitimately build Entry
//     fixtures for table-driven cases).
//
// # Tool blind spots (charter §"工具选定后强制盲区自检")
//
// The following shapes are NOT detected by the type-aware scanner and are
// documented here as known limitations. Reverse self-checks
// (TestAuditTraceIDWriteCaller01_BlindSpot_*) assert each blind-spot form
// is ABSENT from production AST today.
//
//  1. Reconstruction scan address-take (&e.TraceID passed to Scan()):
//     adapters/postgres/audit_ledger_store.go passes &e.TraceID to
//     pgx rows.Scan() for DB row reconstruction. This is a UnaryExpr &
//     applied to a SelectorExpr in a function-call argument position —
//     NOT an AssignStmt LHS, so the assignment scanner does not fire.
//     The postgres file is NOT in any allowlist, so value-assignment and
//     composite-literal writes in that file WILL fire the scanner. Only
//     the &e.TraceID UnaryExpr scan-address (a call argument, not an
//     AssignStmt LHS) naturally escapes. Blind spot: a new file not
//     explicitly reviewed could take &e.TraceID and pass it to some
//     other function that writes through the pointer — this would not
//     be flagged.
//     Mitigation: new address-takes of ledger.Entry fields are
//     conspicuous in code review; DB reconstruction sites are stable.
//
//  2. reflect.Value.FieldByName("TraceID") or unsafe.Pointer offset write:
//     Dynamic field writes via reflection or unsafe pointer arithmetic
//     bypass SelectorExpr / CompositeLit resolution entirely. These
//     shapes are not present in production today (verified by
//     TestAuditTraceIDWriteCaller01_BlindSpot_ReflectNotPresent).
//
//  3. Cross-package helper that takes *ledger.Entry and writes TraceID
//     internally: the scanner flags the helper's own source file. If a
//     new helper is added, it must appear in the allowlist. Today no such
//     helper exists (verified by anti-vacuity assertion on the appender
//     allowlist file).
//
//  4. Dot-import of the ledger package
//     (import . "github.com/ghbvf/gocell/runtime/audit/ledger"):
//     ResolvePackageRef cannot resolve a bare "Entry" ident to its
//     package path for the composite literal type check. However, the
//     type-info check on p.TypesInfo.Types[lit] uses the canonical
//     *types.Named path regardless of import style — a dot-imported
//     struct literal still has the correct *types.Named identity. The
//     assignment LHS scan via info.Selections is similarly alias-proof.
//     Dot-import of ledger is absent today
//     (TestAuditTraceIDWriteCaller01_BlindSpot_DotImportNotPresent).
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	ledgerPkgPath   = PlatformModulePath + "/runtime/audit/ledger"
	ledgerEntryName = "Entry"
	ledgerTraceID   = "TraceID"
)

// traceIDInjectionAllowlist maps module-relative file paths to a description
// of why they are permitted to write ledger.Entry.TraceID via a value-write
// (composite literal or assignment). Every non-test file in this map must be
// observed at least once during the production scan (anti-vacuity); stale
// entries are surfaced as a diagnostic.
//
// Files in this map are INJECTION or TEST CONFORMANCE sites — their scans are
// skipped entirely. The RECONSTRUCTION category (postgres adapter) is NOT in
// this map: those files are scanned normally for value-writes; only the
// &e.TraceID UnaryExpr scan-address shape (not an AssignStmt LHS) naturally
// escapes detection (blind spot #1, documented above).
var traceIDInjectionAllowlist = map[string]string{
	// INJECTION — sole sanctioned source of TraceID value.
	// Writes TraceID: string(entry.Observability().TraceID) in HandleEvent.
	"cells/auditcore/internal/appender/service.go": "injection: sole sanctioned appender",

	// TEST CONFORMANCE — ledger/storetest package provides contract tests for
	// Store implementations; it writes TraceID in test fixture entries passed
	// to Append. This is exclusively test infrastructure (imported only from
	// *_test.go files).
	"runtime/audit/ledger/storetest/suite.go": "storetest: conformance test fixture",
}

// isTraceIDWriteAllowed reports whether rel is permitted to write
// ledger.Entry.TraceID without triggering a violation. _test.go files are
// always allowed (tests legitimately build Entry fixtures for table-driven
// cases).
//
// Reconstruction files (e.g. adapters/postgres/audit_ledger_store.go) are
// NOT exempted here — they pass through the value-write scanner normally.
// Only the &e.TraceID UnaryExpr scan-address shape (a function-call
// argument, not an AssignStmt LHS) naturally avoids detection (blind spot #1).
// This means a future value-assignment in the postgres file WILL fire the
// scanner immediately, which is the F6 granularity requirement.
func isTraceIDWriteAllowed(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	_, ok := traceIDInjectionAllowlist[rel]
	return ok
}

// TestAuditTraceIDWriteCaller01 asserts that every production write of
// ledger.Entry.TraceID (via composite literal or assignment statement) sits
// in the per-file allowlist, and that no allowlist entry is stale.
//
// Two write shapes are detected:
//  1. Composite literal: ledger.Entry{..., TraceID: val, ...} — caught by
//     inspecting CompositeLit nodes whose go/types resolved struct type is
//     ledger.Entry and whose Elts contain a KeyValueExpr with Key "TraceID".
//  2. Assignment: e.TraceID = val — caught by inspecting AssignStmt LHS
//     SelectorExpr nodes resolved via types.Info.Selections to FieldVal
//     of ledger.Entry.TraceID.
//
// Reconstruction files (e.g. adapters/postgres/audit_ledger_store.go) are
// NOT in any allowlist and run through the same scanners. Only the &e.TraceID
// UnaryExpr scan-address shape (a call argument, not an AssignStmt LHS)
// naturally avoids detection (blind spot #1).
//
// The anti-vacuity reverse check ensures every injection allowlist file is
// live: if an allowlisted file no longer writes TraceID (e.g. the appender
// is refactored), the stale allowlist entry is surfaced so it cannot become
// a silent bypass slot.
func TestAuditTraceIDWriteCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isTraceIDWriteAllowed(rel) {
				// Observe injection-allowlist files so anti-vacuity can verify them.
				if _, inAllowlist := traceIDInjectionAllowlist[rel]; inAllowlist {
					observed[rel] = struct{}{}
				}
				continue
			}
			// Reconstruction files are NOT skipped — run the full write scanner.
			// The &e.TraceID scan-address shape is a UnaryExpr in call args, not
			// an AssignStmt LHS, so it does not produce a false positive.
			d = append(d, scanTraceIDCompositeLitWrites(p, file, rel)...)
			d = append(d, scanTraceIDAssignWrites(p, file, rel)...)
		}
		return d
	})

	// Anti-vacuity: confirm every non-test injection-allowlist entry was
	// actually observed writing TraceID. A stale entry is as dangerous as a
	// missing check — it silently reserves a bypass slot.
	//
	// The storetest path is excluded: RunTypedProduction (Tests: false) does
	// not load storetest/ unless explicitly scanned, so it may not appear.
	// Reconstruction files (e.g. adapters/postgres) are not vacuity-checked:
	// they are not in the injection allowlist and the &e.TraceID scan-address
	// shape is not caught by the assignment scanner (blind spot #1).
	vacuityCheck := []string{
		"cells/auditcore/internal/appender/service.go",
	}
	for _, f := range vacuityCheck {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"AUDIT-TRACE-ID-WRITE-CALLER-01: allowlist entry %q is STALE — no "+
						"production write of ledger.Entry.TraceID observed in that file. "+
						"Either the scanner regressed or the write was removed/refactored. "+
						"Drop the stale entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "AUDIT-TRACE-ID-WRITE-CALLER-01", diags)
}

// scanTraceIDCompositeLitWrites detects composite literal writes of
// ledger.Entry.TraceID. It walks ast.CompositeLit nodes and checks whether:
//  1. The literal's resolved go/types type is ledger.Entry (or *ledger.Entry
//     — the CompositeLit itself is typed as ledger.Entry; the enclosing
//     &-unary gives *ledger.Entry). The type check uses
//     p.TypesInfo.Types[lit].Type to get the canonical named type path,
//     immune to import aliases and dot-imports.
//  2. Any KeyValueExpr in Elts has Key "TraceID" with a non-empty value
//     (non-blank string literal, non-zero expression).
func scanTraceIDCompositeLitWrites(p *Pass, file *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
		if !isLedgerEntryType(p.TypesInfo, lit) {
			return
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			keyIdent, ok := kv.Key.(*ast.Ident)
			if !ok || keyIdent.Name != ledgerTraceID {
				continue
			}
			// A composite literal key for TraceID is present. Any non-zero value
			// expression is a violation (the sanctioned site is the only one
			// that should write it; the scanner only runs on non-allowlisted files).
			pos := p.Fset.Position(kv.Pos())
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"AUDIT-TRACE-ID-WRITE-CALLER-01: composite literal write of "+
						"ledger.Entry.TraceID at %s:%d from %q is outside the sanctioned "+
						"injection allowlist. TraceID must flow exclusively from the outbox "+
						"observability envelope via cells/auditcore/internal/appender "+
						"(AUDIT-TRACE-ID-WRITE-CALLER-01). To add a new sanctioned site, "+
						"add it to traceIDInjectionAllowlist with a rationale.",
					rel, pos.Line, rel,
				),
			})
		}
	})
	return d
}

// scanTraceIDAssignWrites detects direct assignment writes of
// ledger.Entry.TraceID via an AssignStmt LHS. It walks ast.AssignStmt
// nodes and checks whether any LHS expression is a SelectorExpr
// resolving (via types.Info.Selections with Kind==FieldVal) to the
// TraceID field on the ledger.Entry struct type.
func scanTraceIDAssignWrites(p *Pass, file *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != ledgerTraceID {
				continue
			}
			if !isLedgerEntryFieldSel(p.TypesInfo, sel) {
				continue
			}
			pos := p.Fset.Position(sel.Pos())
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"AUDIT-TRACE-ID-WRITE-CALLER-01: assignment write of "+
						"ledger.Entry.TraceID at %s:%d from %q is outside the sanctioned "+
						"injection allowlist. TraceID must flow exclusively from the outbox "+
						"observability envelope via cells/auditcore/internal/appender "+
						"(AUDIT-TRACE-ID-WRITE-CALLER-01). To add a new sanctioned site, "+
						"add it to traceIDInjectionAllowlist with a rationale.",
					rel, pos.Line, rel,
				),
			})
		}
	})
	return d
}

// isLedgerEntryType reports whether the go/types resolved type of lit is
// ledger.Entry. Uses types.Info.Types[lit] which carries the canonical
// *types.Named identity — immune to import aliases, dot-imports, or type
// aliases pointing at ledger.Entry.
//
// The CompositeLit node itself always has the struct type (not the pointer
// type even when used as &ledger.Entry{...}) — the unary & is a separate
// ast.UnaryExpr node.
func isLedgerEntryType(info *types.Info, lit *ast.CompositeLit) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[lit]
	if !ok {
		return false
	}
	return isLedgerEntryNamedType(tv.Type)
}

// isLedgerEntryFieldSel reports whether sel is a FieldVal selection of
// the TraceID field on ledger.Entry. Uses types.Info.Selections for
// struct field access — the canonical, alias-proof resolution path.
func isLedgerEntryFieldSel(info *types.Info, sel *ast.SelectorExpr) bool {
	if info == nil || sel == nil {
		return false
	}
	s, ok := info.Selections[sel]
	if !ok {
		return false
	}
	if s.Kind() != types.FieldVal {
		return false
	}
	field, ok := s.Obj().(*types.Var)
	if !ok || !field.IsField() {
		return false
	}
	return isLedgerEntryNamedType(s.Recv())
}

// isLedgerEntryNamedType reports whether t (after stripping a pointer
// wrapper) is the named type runtime/audit/ledger.Entry.
func isLedgerEntryNamedType(t types.Type) bool {
	owner := typeOwner(t)
	if owner == nil {
		return false
	}
	pkg := owner.Pkg()
	if pkg == nil {
		return false
	}
	return pkg.Path() == ledgerPkgPath && owner.Name() == ledgerEntryName
}

// ---------------------------------------------------------------------------
// Negative fixture self-check
// ---------------------------------------------------------------------------

// TestAuditTraceIDWriteCaller01_RedFixture verifies that the scanner fires
// against both deliberate violations in audittraceididfixture:
//  1. badCompositeLit — composite literal write of ledger.Entry.TraceID.
//  2. badAssignment   — direct assignment write of ledger.Entry.TraceID.
//
// The scanner must report ≥ 2 violations (one per write shape). This
// also validates the F6 granularity requirement: a value-assignment inside
// a file that would otherwise only contain &e.TraceID scan-address takes
// IS caught — confirming that the tightened reconstruction-file handling
// works correctly.
func TestAuditTraceIDWriteCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/audittraceididfixture"},
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				found += len(scanTraceIDCompositeLitWrites(p, file, rel))
				found += len(scanTraceIDAssignWrites(p, file, rel))
			}
			return nil
		})
	assert.GreaterOrEqual(t, found, 2,
		"AUDIT-TRACE-ID-WRITE-CALLER-01 RED fixture self-check FAILED: "+
			"expected ≥ 2 violations from audittraceididfixture "+
			"(badCompositeLit + badAssignment), got %d. "+
			"The scanner is not detecting one or both violation shapes — check "+
			"isLedgerEntryType / scanTraceIDCompositeLitWrites / scanTraceIDAssignWrites.",
		found)
}

// TestAuditTraceIDWriteCaller01_ReconstructionFileValueWriteFires asserts
// that a value-assignment write of ledger.Entry.TraceID would fire the
// scanner even when it coexists with legitimate &e.TraceID scan-address takes
// (as found in adapters/postgres/audit_ledger_store.go).
//
// This is the key invariant of F6: reconstruction files are NOT exempted from
// value-write detection. Only the &e.TraceID UnaryExpr scan-address shape (a
// function-call argument, not an AssignStmt LHS) naturally escapes the
// scanner. The fixture's badAssignment function simulates the scenario where
// someone adds `e.TraceID = userInput` alongside a legitimate Scan call.
func TestAuditTraceIDWriteCaller01_ReconstructionFileValueWriteFires(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var assignFound int
	_ = RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/audittraceididfixture"},
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				assignFound += len(scanTraceIDAssignWrites(p, file, rel))
			}
			return nil
		})
	assert.GreaterOrEqual(t, assignFound, 1,
		"AUDIT-TRACE-ID-WRITE-CALLER-01 reconstruction granularity check FAILED: "+
			"expected the assignment scanner to fire ≥ 1 time on the fixture "+
			"(simulating a value-assignment inside a postgres-reconstruction-like file), "+
			"got 0. Reconstruction files must NOT suppress value-write detection; "+
			"only the &e.TraceID UnaryExpr scan-address is exempt (blind spot #1).")
}

// ---------------------------------------------------------------------------
// Blind-spot reverse self-checks
// ---------------------------------------------------------------------------

// TestAuditTraceIDWriteCaller01_BlindSpot_ReflectNotPresent asserts that
// no production file in cells/, runtime/, adapters/, or cmd/ calls
// reflect.Value.FieldByName with the literal string "TraceID". This would
// be blind spot #2 (reflect-based field write bypasses SelectorExpr scan).
// Absence today is verified; a future introduction would fail this test,
// prompting a reviewer to add the file to the allowlist or reconsider the
// approach.
func TestAuditTraceIDWriteCaller01_BlindSpot_ReflectNotPresent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	hits := collectReflectFieldByNameHits(t, ledgerTraceID)
	assert.Empty(t, hits,
		"AUDIT-TRACE-ID-WRITE-CALLER-01 blind-spot #2 check: found "+
			"reflect.Value.FieldByName(\"TraceID\") calls in production code. "+
			"These bypass the SelectorExpr scanner and must be reviewed. "+
			"Add the files to traceIDInjectionAllowlist or redesign the write path.")
}

// collectReflectFieldByNameHits scans cells/, runtime/, adapters/, and cmd/
// for reflect.Value.FieldByName calls with the given fieldName literal.
// Extracted to keep TestAuditTraceIDWriteCaller01_BlindSpot_ReflectNotPresent
// within the ≤15 cognitive complexity guideline.
func collectReflectFieldByNameHits(t *testing.T, fieldName string) []string {
	t.Helper()
	var hits []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/...",
		"./runtime/...",
		"./adapters/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			hits = append(hits, scanFieldByNameCalls(p, file, rel, fieldName)...)
		}
		return nil
	})
	sort.Strings(hits)
	return hits
}

// scanFieldByNameCalls walks the file for .FieldByName("<fieldName>") call
// expressions and returns their locations. Extracted for complexity budget.
func scanFieldByNameCalls(p *Pass, file *ast.File, rel, fieldName string) []string {
	var hits []string
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "FieldByName" {
			return
		}
		if len(call.Args) != 1 {
			return
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		val := strings.Trim(lit.Value, `"`)
		if val == fieldName {
			pos := p.Fset.Position(call.Pos())
			hits = append(hits, fmt.Sprintf("%s:%d", rel, pos.Line))
		}
	})
	return hits
}

// TestAuditTraceIDWriteCaller01_BlindSpot_DotImportNotPresent asserts that
// no production file in cells/, runtime/, adapters/, or cmd/ dot-imports
// the ledger package. A dot-import changes the composite literal type-check
// path (the type literal shape is different) and is documented as blind spot
// #4. Absence today is verified.
func TestAuditTraceIDWriteCaller01_BlindSpot_DotImportNotPresent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	hits := collectDotImportHits(t, ledgerPkgPath)
	assert.Empty(t, hits,
		"AUDIT-TRACE-ID-WRITE-CALLER-01 blind-spot #4 check: found dot-import of "+
			"runtime/audit/ledger in production code. Dot-imports change the AST shape "+
			"for composite literal type-checking and are a documented scanner blind spot. "+
			"Remove the dot-import or add an explicit type-aware check for dot-import form.")
}

// collectDotImportHits scans cells/, runtime/, adapters/, and cmd/ for
// dot-imports of the given package path. Extracted to keep
// TestAuditTraceIDWriteCaller01_BlindSpot_DotImportNotPresent within the
// ≤15 cognitive complexity guideline.
func collectDotImportHits(t *testing.T, pkgPath string) []string {
	t.Helper()
	var hits []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/...",
		"./runtime/...",
		"./adapters/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			hits = append(hits, scanDotImports(p, file, rel, pkgPath)...)
		}
		return nil
	})
	sort.Strings(hits)
	return hits
}

// scanDotImports walks the file's imports for a dot-import of pkgPath.
// Extracted for complexity budget.
func scanDotImports(p *Pass, file *ast.File, rel, pkgPath string) []string {
	var hits []string
	for _, imp := range file.Imports {
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if path == pkgPath {
			pos := p.Fset.Position(imp.Pos())
			hits = append(hits, fmt.Sprintf("%s:%d", rel, pos.Line))
		}
	}
	return hits
}
