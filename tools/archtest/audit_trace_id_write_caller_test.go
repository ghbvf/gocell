//go:build archtest

// audit_trace_id_write_caller_test.go — locks the appender as the SOLE
// injection write-path for runtime/audit/ledger.Entry.TraceID and
// runtime/audit/ledger.Entry.CorrelationID, so business/cell code cannot
// fabricate these observability ids into an audit record. Both fields must
// only flow from the outbox observability envelope via the appender.
//
// INVARIANT: AUDIT-TRACE-ID-WRITE-CALLER-01
//
// # What this guards
//
// ledger.Entry.TraceID and ledger.Entry.CorrelationID must carry the OTel
// trace id and cross-cell correlation id extracted from the outbox.Entry
// observability envelope. They are both populated by exactly one site:
//
//   - corecells/auditcore/internal/appender/service.go — HandleEvent
//     composite literal: TraceID: corr.TraceID() and
//     CorrelationID: corr.CorrelationID(), where corr =
//     correlation.New(string(obs.TraceID), string(obs.RequestID), string(obs.CorrelationID))
//     and obs = entry.Observability()
//
// This is the sole sanctioned WRITE site (issue #1048 Batch F). Scope this
// rule precisely: it enforces the write LOCATION (only the appender + test
// conformance may write ledger.Entry.TraceID / .CorrelationID), NOT the
// value's source. Within the appender, the written value actually being the
// obs-envelope value (corr.TraceID() / corr.CorrelationID()) rests on two
// non-archtest facts: (a) the appender is a single, small, reviewed injection
// site, and (b) the anti-vacuity check below fails CI if that write ever
// disappears. The trustworthiness of the obs-envelope value itself is the
// Hard, inherited part (below). Allowing any OTHER code to write these fields
// would let an audit-event producer fabricate the observability context that
// correlates the audit record — that is exactly what the location allowlist
// forecloses.
//
// # Why the upstream guarantee is inherited, not separate
//
// The obs-envelope values the appender writes are trustworthy because of one
// Hard mechanism — plus one value-object wrapper that is NOT itself a
// provenance gate (F6):
//
//  1. kernel/outbox.Entry is fully unexported (sealed construction,
//     OUTBOX-ENTRY-SEALED-CONSTRUCTION-01): no business code can construct or
//     populate outbox.Entry.observability.TraceID or .CorrelationID directly —
//     the only write path is OTel span extraction in the tracing adapter.
//     This is the Hard upstream guarantee this rule inherits.
//  2. kernel/observability/correlation.Correlation has unexported fields, so
//     external code cannot build one via a struct literal — but correlation.New
//     is a PUBLIC general constructor. The field seal prevents literal
//     construction; it does NOT gate provenance. Provenance comes ONLY from
//     fact 1: the appender feeds New the values it read from the sealed
//     entry.Observability() envelope. Do NOT cite the Correlation seal as a
//     provenance guarantee.
//
// This archtest therefore does NOT add a separate upstream funnel lock for
// ledger.Entry.TraceID / .CorrelationID — it inherits the Hard guarantee from
// the sealed outbox.Entry envelope (fact 1) and adds the downstream Medium
// location guard: if some future code wrote these fields outside the appender
// (composite literal or assignment), this archtest catches it in CI before it
// reaches production.
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
//     business code to inject a fabricated TraceID or CorrelationID into an
//     outbox.Entry in the first place — so entry.Observability() is a trusted
//     source. This rule does not machine-verify that the appender writes that
//     trusted value (the downstream guard is location-only, see "# What this
//     guards"); it relies on the single reviewed appender site + the
//     anti-vacuity check for that last hop. No upstream funnel lock is added
//     on top of the inherited envelope seal.
//   - Permanent ceiling rationale: Go package visibility cannot express
//     "only corecells/auditcore/internal/appender may write these exported
//     struct fields". A type-system Hard upstream lock would require
//     unexported fields or sealed constructors on ledger.Entry itself,
//     which conflicts with the store's need to reconstruct entries from DB
//     rows via reflect/scan (the postgres adapter takes &e.TraceID and
//     &e.CorrelationID for pgx row scanning). The current design keeps
//     ledger.Entry as a plain struct with exported fields for store
//     polymorphism; the archtest is the downstream enforcement backstop.
//     Hard-ification path (seal ledger.Entry per the outbox.Entry
//     sealed-construction pattern) is tracked as a deliberate
//     won't-do-now at gh #1501; same permanent ceiling as #851 / #893.
//     Same permanent ceiling as SPAN-SETATTR-HOLDER-SEAL (#851) /
//     HEALTHZ-HOLDER-SEAL (#893 won't-do).
//
// # Allowlist semantics — three categories
//
// The allowlist is split by semantic role:
//
//   - INJECTION allowlist (observabilityIDInjectionAllowlist): the sole site
//     that sets TraceID and CorrelationID to values derived from the
//     observability envelope. Composite literal and assignment scans are
//     SKIPPED for these files (the write is sanctioned by design).
//
//   - RECONSTRUCTION (NOT in any allowlist): the postgres adapter takes
//     &e.TraceID and &e.CorrelationID as scan-addresses passed to pgx
//     rows.Scan() for DB row reconstruction. The scanner cannot distinguish
//     a Scan(&e.TraceID) address-take from a pointer-write (blind spot #1),
//     so the address-takes are documented and accepted. HOWEVER, the
//     postgres file is NOT in observabilityIDInjectionAllowlist, so the
//     value-write scanner still runs on it. Composite literal and assignment
//     writes of ledger.Entry.TraceID or .CorrelationID in the postgres file
//     ARE violations and WILL fire the scanner. Only the &e.Field UnaryExpr
//     scan-address shape (not an AssignStmt LHS) naturally escapes detection.
//     A future value-assignment in the postgres file WILL fire the scanner
//     immediately (F6 invariant).
//
//   - TEST CONFORMANCE (observabilityIDInjectionAllowlist): storetest/
//     provides contract test fixtures for Store implementations; it writes
//     TraceID and CorrelationID in fixture entries for Append. Exclusively
//     test infrastructure (imported only from *_test.go files).
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
//  1. Reconstruction scan address-take (&e.TraceID or &e.CorrelationID
//     passed to Scan()): adapters/postgres/audit_ledger_store.go passes
//     both as scan-address args for pgx row scanning. These are UnaryExpr &
//     applied to SelectorExpr in function-call argument positions — NOT
//     AssignStmt LHS, so the assignment scanner does not fire. The postgres
//     file is NOT in any allowlist, so value-assignment and composite-literal
//     writes in that file WILL fire the scanner. Only the UnaryExpr
//     scan-address shape (a call argument, not an AssignStmt LHS) naturally
//     escapes. Blind spot: a new file not explicitly reviewed could take
//     &e.TraceID or &e.CorrelationID and pass it to some other function that
//     writes through the pointer — this would not be flagged.
//     Mitigation: new address-takes of ledger.Entry fields are conspicuous
//     in code review; DB reconstruction sites are stable.
//
//  2. reflect.Value.FieldByName("TraceID") or FieldByName("CorrelationID")
//     or unsafe.Pointer offset write: Dynamic field writes via reflection
//     or unsafe pointer arithmetic bypass SelectorExpr / CompositeLit
//     resolution entirely. These shapes are not present in production today
//     (verified by TestAuditTraceIDWriteCaller01_BlindSpot_ReflectNotPresent).
//
//  3. Cross-package helper that takes *ledger.Entry and writes TraceID or
//     CorrelationID internally: the scanner flags the helper's own source
//     file. If a new helper is added, it must appear in the allowlist.
//     Today no such helper exists (verified by anti-vacuity assertion on
//     the appender allowlist file).
//
// # Handled — NOT a blind spot (F10: corrects a prior misclassification)
//
//   - Dot-import of the ledger package
//     (import . "github.com/ghbvf/gocell/runtime/audit/ledger"): a bare "Entry"
//     ident is unresolvable by name-based ResolvePackageRef, but this scanner
//     never relies on name resolution — the composite-literal check reads
//     p.TypesInfo.Types[lit].Type and the assignment check reads
//     info.Selections, both of which carry the canonical *types.Named identity
//     regardless of import style. A dot-imported ledger.Entry write is
//     therefore still detected. Dot-import of ledger is also absent today, and
//     TestAuditTraceIDWriteCaller01_DotImportAbsent keeps it absent as HYGIENE
//     (uniform AST shape), not as a correctness backstop.
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
)

// auditTraceIDRuleID is the single source for this rule's ID. It is used in the
// Report call and every diagnostic / assertion message so the string literal is
// declared once (F9 — was repeated across ~7 sites).
const auditTraceIDRuleID = "AUDIT-TRACE-ID-WRITE-CALLER-01"

// ledgerObservabilityFields is the closed set of ledger.Entry observability-id
// fields guarded by this rule. Both TraceID and CorrelationID flow from the
// same sealed read-model through the appender, so both must be guarded.
var ledgerObservabilityFields = []string{"TraceID", "CorrelationID"}

// observabilityIDInjectionAllowlist maps module-relative file paths to a
// description of why they are permitted to write ledger.Entry.TraceID or
// ledger.Entry.CorrelationID via a value-write (composite literal or
// assignment). Every non-test file in this map must be observed at least once
// during the production scan (anti-vacuity); stale entries are surfaced as a
// diagnostic.
//
// Files in this map are INJECTION or TEST CONFORMANCE sites — their scans are
// skipped entirely. The RECONSTRUCTION category (postgres adapter) is NOT in
// this map: those files are scanned normally for value-writes; only the
// &e.Field UnaryExpr scan-address shape (not an AssignStmt LHS) naturally
// escapes detection (blind spot #1, documented above).
var observabilityIDInjectionAllowlist = map[string]string{
	// INJECTION — sole sanctioned source of TraceID and CorrelationID values.
	// Writes TraceID: corr.TraceID() and CorrelationID: corr.CorrelationID()
	// in HandleEvent (via correlation.New(string(obs.TraceID), string(obs.RequestID),
	// string(obs.CorrelationID)), where obs = entry.Observability()).
	"corecells/auditcore/internal/appender/service.go": "injection: sole sanctioned appender",

	// TEST CONFORMANCE — ledger/storetest package provides contract tests for
	// Store implementations; it writes TraceID and CorrelationID in test
	// fixture entries passed to Append. This is exclusively test infrastructure
	// (imported only from *_test.go files).
	"runtime/audit/ledger/storetest/suite.go": "storetest: conformance test fixture",
	// TEST CONFORMANCE — same package as suite.go; fuzz.go provides round-trip
	// fuzz harness fixtures for Store conformance. Writes CorrelationID in
	// generated test entries to seed and verify the fuzz corpus. Exclusively
	// test infrastructure (package storetest, imported only from *_test.go files).
	"runtime/audit/ledger/storetest/fuzz.go": "storetest: fuzz conformance fixture",
}

// isObservabilityIDWriteAllowed reports whether rel is permitted to write
// ledger.Entry.TraceID or ledger.Entry.CorrelationID without triggering a
// violation. _test.go files are always allowed (tests legitimately build
// Entry fixtures for table-driven cases).
//
// Reconstruction files (e.g. adapters/postgres/audit_ledger_store.go) are
// NOT exempted here — they pass through the value-write scanner normally.
// Only the &e.Field UnaryExpr scan-address shape (a function-call argument,
// not an AssignStmt LHS) naturally avoids detection (blind spot #1). This
// means a future value-assignment in the postgres file WILL fire the scanner
// immediately, which is the F6 granularity requirement.
func isObservabilityIDWriteAllowed(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	_, ok := observabilityIDInjectionAllowlist[rel]
	return ok
}

// TestAuditTraceIDWriteCaller01 asserts that every production write of
// ledger.Entry.TraceID or ledger.Entry.CorrelationID (via composite literal
// or assignment statement) sits in the per-file allowlist, and that no
// allowlist entry is stale.
//
// Two write shapes are detected for each guarded field:
//  1. Composite literal: ledger.Entry{..., TraceID: val, ...} — caught by
//     inspecting CompositeLit nodes whose go/types resolved struct type is
//     ledger.Entry and whose Elts contain a KeyValueExpr with Key "TraceID"
//     or "CorrelationID".
//  2. Assignment: e.TraceID = val — caught by inspecting AssignStmt LHS
//     SelectorExpr nodes resolved via types.Info.Selections to FieldVal
//     of ledger.Entry.TraceID or ledger.Entry.CorrelationID.
//
// Reconstruction files (e.g. adapters/postgres/audit_ledger_store.go) are
// NOT in any allowlist and run through the same scanners. Only the
// &e.Field UnaryExpr scan-address shape (a call argument, not an AssignStmt
// LHS) naturally avoids detection (blind spot #1).
//
// The anti-vacuity reverse check ensures every injection allowlist file is
// live: if an allowlisted file no longer writes TraceID or CorrelationID
// (e.g. the appender is refactored), the stale allowlist entry is surfaced
// so it cannot become a silent bypass slot.
func TestAuditTraceIDWriteCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isObservabilityIDWriteAllowed(rel) {
				// F2: mark an injection-allowlist file observed ONLY when it
				// actually writes a guarded field — not merely because it loaded.
				// If a refactor drops the write (e.g. the appender stops setting
				// TraceID), the anti-vacuity check below then surfaces the entry
				// as STALE instead of silently reserving a dead bypass slot. The
				// scanners' diagnostics are consumed here purely as a presence
				// count, never reported (these files are sanctioned by design).
				if _, inAllowlist := observabilityIDInjectionAllowlist[rel]; inAllowlist {
					if obsIDWriteCount(p, file, rel) > 0 {
						observed[rel] = struct{}{}
					}
				}
				continue
			}
			// Reconstruction files are NOT skipped — run the full write scanner.
			// The &e.Field scan-address shape is a UnaryExpr in call args, not
			// an AssignStmt LHS, so it does not produce a false positive.
			d = append(d, scanObsIDCompositeLitWrites(p, file, rel)...)
			d = append(d, scanObsIDAssignWrites(p, file, rel)...)
		}
		return d
	})

	// Anti-vacuity: confirm every non-test injection-allowlist entry was
	// actually observed writing TraceID or CorrelationID. A stale entry is as
	// dangerous as a missing check — it silently reserves a bypass slot.
	//
	// The storetest path is excluded: Production(Tests: false) does not load
	// storetest/ (it is not in the main packages.Load set for non-test files),
	// so it may not appear. Reconstruction files (e.g. adapters/postgres) are
	// not vacuity-checked: they are not in the injection allowlist and the
	// &e.Field scan-address shape is not caught by the assignment scanner
	// (blind spot #1).
	vacuityCheck := []string{
		"corecells/auditcore/internal/appender/service.go",
	}
	for _, f := range vacuityCheck {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					auditTraceIDRuleID+": allowlist entry %q is STALE — no "+
						"production write of ledger.Entry.TraceID or .CorrelationID "+
						"observed in that file. Either the scanner regressed or the write "+
						"was removed/refactored. Drop the stale entry so it cannot become "+
						"a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, auditTraceIDRuleID, diags)
}

// obsIDWriteCount returns the number of guarded ledger.Entry observability-id
// field writes (composite literal + assignment) detected in file. The
// anti-vacuity check (F2) uses it to confirm an injection-allowlist file is
// LIVE — the scanners' diagnostics are consumed purely as a presence count
// here, never reported (allowlist files are sanctioned by design).
func obsIDWriteCount(p *Pass, file *ast.File, rel string) int {
	return len(scanObsIDCompositeLitWrites(p, file, rel)) +
		len(scanObsIDAssignWrites(p, file, rel))
}

// ledgerLitGuardedWrite is one guarded observability-id field set by a
// ledger.Entry composite literal, with the source position of its element.
type ledgerLitGuardedWrite struct {
	field string
	pos   token.Pos
}

// ledgerStructType returns the *types.Struct underlying lit's resolved type, or
// nil when type info is unavailable. Used to map positional composite-literal
// elements to struct fields by declaration index (F3).
func ledgerStructType(info *types.Info, lit *ast.CompositeLit) *types.Struct {
	if info == nil {
		return nil
	}
	tv, ok := info.Types[lit]
	if !ok {
		return nil
	}
	st, ok := tv.Type.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	return st
}

// guardedWritesInLedgerLit returns every guarded observability-id field set by a
// ledger.Entry composite literal, covering BOTH element forms:
//   - keyed:      ledger.Entry{TraceID: v} — matched by KeyValueExpr key name.
//   - positional: ledger.Entry{a, b, ..., v, ...} (F3) — each element index is
//     mapped to the struct field at that index via the resolved *types.Struct,
//     so an unkeyed literal can no longer slip past the keyed-only scan. (Go
//     forbids mixing keyed and positional elements, so the first element's form
//     determines the whole literal.)
func guardedWritesInLedgerLit(info *types.Info, lit *ast.CompositeLit) []ledgerLitGuardedWrite {
	var out []ledgerLitGuardedWrite
	keyed := len(lit.Elts) > 0
	if keyed {
		_, keyed = lit.Elts[0].(*ast.KeyValueExpr)
	}
	if keyed {
		// EachInChildren iterates the CompositeLit's direct elements without a
		// raw for-range + type assertion over []ast.Expr
		// (SCANNER-FRAMEWORK-USAGE-01 Path B).
		EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
			keyIdent, ok := kv.Key.(*ast.Ident)
			if !ok || !isGuardedObsIDField(keyIdent.Name) {
				return
			}
			out = append(out, ledgerLitGuardedWrite{field: keyIdent.Name, pos: kv.Pos()})
		})
		return out
	}
	// Positional form. The for-range over []ast.Expr carries NO type assertion
	// in its body (only elt.Pos(), an interface method), so it is the allowed
	// "_no_assertion" shape, not SCANNER-FRAMEWORK-USAGE-01 Path B.
	st := ledgerStructType(info, lit)
	if st == nil {
		return out
	}
	for i, elt := range lit.Elts {
		if i >= st.NumFields() {
			break
		}
		if isGuardedObsIDField(st.Field(i).Name()) {
			out = append(out, ledgerLitGuardedWrite{field: st.Field(i).Name(), pos: elt.Pos()})
		}
	}
	return out
}

// scanObsIDCompositeLitWrites detects composite literal writes of any guarded
// ledger.Entry observability-id field (TraceID or CorrelationID). It walks
// ast.CompositeLit nodes whose resolved go/types type is ledger.Entry (the
// type check uses p.TypesInfo.Types[lit].Type — the canonical named type path,
// immune to import aliases and dot-imports) and reports every guarded field set
// via guardedWritesInLedgerLit (keyed or positional). The scanner only runs on
// non-allowlisted files, so any guarded element present is a violation.
func scanObsIDCompositeLitWrites(p *Pass, file *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
		if !isLedgerEntryType(p.TypesInfo, lit) {
			return
		}
		for _, w := range guardedWritesInLedgerLit(p.TypesInfo, lit) {
			pos := p.Fset.Position(w.pos)
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					auditTraceIDRuleID+": composite literal write of "+
						"ledger.Entry.%s at %s:%d is outside the sanctioned "+
						"injection allowlist. TraceID and CorrelationID must flow "+
						"exclusively from the outbox observability envelope via "+
						"corecells/auditcore/internal/appender. To add a new sanctioned "+
						"site, add it to observabilityIDInjectionAllowlist with a rationale.",
					w.field, rel, pos.Line,
				),
			})
		}
	})
	return d
}

// scanObsIDAssignWrites detects direct assignment writes of any guarded
// ledger.Entry observability-id field (TraceID or CorrelationID) via an
// AssignStmt LHS. It walks ast.AssignStmt nodes and checks whether any LHS
// expression is a SelectorExpr resolving (via types.Info.Selections with
// Kind==FieldVal) to a guarded field on the ledger.Entry struct type.
func scanObsIDAssignWrites(p *Pass, file *ast.File, rel string) []Diagnostic {
	var d []Diagnostic
	EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
		// EachInChildren visits direct children of as, which includes both Lhs
		// and Rhs SelectorExprs. The exprInList guard restricts matches to
		// write-side (LHS) semantics, avoiding false positives on RHS reads.
		// Using EachInChildren avoids the raw for-range + type assertion over
		// []ast.Expr that would self-trigger SCANNER-FRAMEWORK-USAGE-01 Path B.
		EachInChildren[ast.SelectorExpr](as, func(sel *ast.SelectorExpr) {
			if sel.Sel == nil || !exprInList(as.Lhs, sel) {
				return // LHS-only: write semantics
			}
			if !isGuardedObsIDField(sel.Sel.Name) {
				return
			}
			if !isLedgerEntryFieldSel(p.TypesInfo, sel) {
				return
			}
			pos := p.Fset.Position(sel.Pos())
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					auditTraceIDRuleID+": assignment write of "+
						"ledger.Entry.%s at %s:%d from %q is outside the sanctioned "+
						"injection allowlist. TraceID and CorrelationID must flow "+
						"exclusively from the outbox observability envelope via "+
						"corecells/auditcore/internal/appender "+
						"(AUDIT-TRACE-ID-WRITE-CALLER-01). To add a new sanctioned site, "+
						"add it to observabilityIDInjectionAllowlist with a rationale.",
					sel.Sel.Name, rel, pos.Line, rel,
				),
			})
		})
	})
	return d
}

// exprInList reports whether target is an element of list (identity compare,
// no type assertion — avoids self-triggering SCANNER-FRAMEWORK-USAGE-01).
// Used to restrict EachInChildren[ast.SelectorExpr] hits to write-side (LHS)
// positions in an AssignStmt.
func exprInList(list []ast.Expr, target ast.Expr) bool {
	for _, e := range list {
		if e == target {
			return true
		}
	}
	return false
}

// isGuardedObsIDField reports whether fieldName is in the closed set of
// ledger.Entry observability-id fields guarded by this rule.
func isGuardedObsIDField(fieldName string) bool {
	for _, f := range ledgerObservabilityFields {
		if f == fieldName {
			return true
		}
	}
	return false
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

// isLedgerEntryFieldSel reports whether sel is a FieldVal selection of a
// guarded field on ledger.Entry. Uses types.Info.Selections for struct field
// access — the canonical, alias-proof resolution path.
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
// against every deliberate violation in audittraceidfixture, covering BOTH
// guarded fields (F4) AND both composite-literal forms (F3):
//  1. badCompositeLit          — KEYED composite literal writes of TraceID AND
//     CorrelationID (two violations).
//  2. badPositionalLit         — POSITIONAL composite literal; CorrelationID
//     (idx 8) and TraceID (idx 9) positions (two violations, F3).
//  3. badAssignment            — direct assignment write of TraceID.
//  4. badAssignmentCorrelation — direct assignment write of CorrelationID.
//
// The scanner must report exactly 6 violations (4 composite + 2 assignment).
// This also validates the reconstruction-granularity requirement: a
// value-assignment inside a file that would otherwise only contain &e.Field
// scan-address takes IS caught. The count is exact (not ≥ 6) so that a
// regression which wrongly flags addressTake (blind-spot #1) would produce
// found==7 and fail, catching over-detection as well as under-detection.
func TestAuditTraceIDWriteCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/audittraceidfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				found += len(scanObsIDCompositeLitWrites(p, file, rel))
				found += len(scanObsIDAssignWrites(p, file, rel))
			}
			return nil
		})
	assert.Equal(t, 6, found,
		auditTraceIDRuleID+" RED fixture self-check FAILED: "+
			"expected exactly 6 violations from audittraceidfixture "+
			"(badCompositeLit keyed ×2, badPositionalLit positional ×2, "+
			"badAssignment, badAssignmentCorrelation); addressTake (blind-spot #1) "+
			"must produce 0. Got %d — if found<6 the scanner missed a violation "+
			"shape (e.g. positional/F3 regression); if found==7 the scanner wrongly "+
			"flagged addressTake (regression in blind-spot #1 handling). Check "+
			"isLedgerEntryType / scanObsIDCompositeLitWrites / scanObsIDAssignWrites.",
		found)
}

// TestAuditTraceIDWriteCaller01_ReconstructionFileValueWriteFires asserts
// that a value-assignment write of ledger.Entry.TraceID would fire the
// scanner even when it coexists with legitimate &e.TraceID scan-address takes
// (as found in adapters/postgres/audit_ledger_store.go).
//
// This is the key invariant of F6: reconstruction files are NOT exempted from
// value-write detection. Only the &e.Field UnaryExpr scan-address shape (a
// function-call argument, not an AssignStmt LHS) naturally escapes the
// scanner. The fixture's badAssignment function simulates the scenario where
// someone adds `e.TraceID = userInput` alongside a legitimate Scan call.
func TestAuditTraceIDWriteCaller01_ReconstructionFileValueWriteFires(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var assignFound int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/audittraceidfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				assignFound += len(scanObsIDAssignWrites(p, file, rel))
			}
			return nil
		})
	assert.GreaterOrEqual(t, assignFound, 1,
		auditTraceIDRuleID+" reconstruction granularity check FAILED: "+
			"expected the assignment scanner to fire ≥ 1 time on the fixture "+
			"(simulating a value-assignment inside a postgres-reconstruction-like file), "+
			"got 0. Reconstruction files must NOT suppress value-write detection; "+
			"only the &e.Field UnaryExpr scan-address is exempt (blind spot #1).")
}

// ---------------------------------------------------------------------------
// Blind-spot reverse self-checks
// ---------------------------------------------------------------------------

// TestAuditTraceIDWriteCaller01_BlindSpot_ReflectNotPresent asserts that
// no production file in cells/, runtime/, adapters/, or cmd/ calls
// reflect.Value.FieldByName with the literal string "TraceID" or
// "CorrelationID". This would be blind spot #2 (reflect-based field write
// bypasses SelectorExpr scan). Absence today is verified; a future
// introduction would fail this test, prompting a reviewer to add the file
// to the allowlist or reconsider the approach.
func TestAuditTraceIDWriteCaller01_BlindSpot_ReflectNotPresent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var hits []string
	for _, fieldName := range ledgerObservabilityFields {
		hits = append(hits, collectReflectFieldByNameHitsAudit(t, fieldName)...)
	}
	sort.Strings(hits)
	assert.Empty(t, hits,
		auditTraceIDRuleID+" blind-spot #2 check: found "+
			"reflect.Value.FieldByName(\"TraceID\" or \"CorrelationID\") calls in "+
			"production code. These bypass the SelectorExpr scanner and must be "+
			"reviewed. Add the files to observabilityIDInjectionAllowlist or "+
			"redesign the write path.")
}

// collectReflectFieldByNameHitsAudit scans cells/, runtime/, adapters/, and
// cmd/ for reflect.Value.FieldByName calls with the given fieldName literal.
// Extracted to keep TestAuditTraceIDWriteCaller01_BlindSpot_ReflectNotPresent
// within the ≤15 cognitive complexity guideline.
func collectReflectFieldByNameHitsAudit(t *testing.T, fieldName string) []string {
	t.Helper()
	var hits []string
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			hits = append(hits, scanFieldByNameCallsAudit(p, file, rel, fieldName)...)
		}
		return nil
	})
	return hits
}

// scanFieldByNameCallsAudit walks the file for .FieldByName("<fieldName>")
// call expressions and returns their locations. Extracted for complexity
// budget.
func scanFieldByNameCallsAudit(p *Pass, file *ast.File, rel, fieldName string) []string {
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

// TestAuditTraceIDWriteCaller01_DotImportAbsent asserts that no production file
// in cells/, runtime/, adapters/, or cmd/ dot-imports the ledger package.
//
// F10: a dot-import is NOT a scanner blind spot — both the composite-literal
// check (p.TypesInfo.Types[lit].Type) and the assignment check
// (info.Selections) resolve the canonical *types.Named identity regardless of
// import style, so a dot-imported ledger.Entry write is still detected (see
// package godoc "blind spot #4", which documents exactly this). This absence
// assertion is therefore HYGIENE, not a correctness backstop: dot-imports are
// avoided project-wide for readability, and keeping the ledger package
// non-dot-imported keeps the AST shape uniform with the keyed/positional cases
// the scanner is exercised against.
func TestAuditTraceIDWriteCaller01_DotImportAbsent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	hits := collectDotImportHitsAudit(t, ledgerPkgPath)
	assert.Empty(t, hits,
		auditTraceIDRuleID+" dot-import hygiene check: found dot-import of "+
			"runtime/audit/ledger in production code. The scanner still resolves "+
			"the canonical type via go/types (not a correctness gap), but "+
			"dot-imports are avoided project-wide — remove it.")
}

// collectDotImportHitsAudit scans cells/, runtime/, adapters/, and cmd/ for
// dot-imports of the given package path. Extracted to keep
// TestAuditTraceIDWriteCaller01_BlindSpot_DotImportNotPresent within the
// ≤15 cognitive complexity guideline.
func collectDotImportHitsAudit(t *testing.T, pkgPath string) []string {
	t.Helper()
	var hits []string
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			hits = append(hits, scanDotImportsAudit(p, file, rel, pkgPath)...)
		}
		return nil
	})
	sort.Strings(hits)
	return hits
}

// scanDotImportsAudit walks the file's imports for a dot-import of pkgPath.
// Extracted for complexity budget.
func scanDotImportsAudit(p *Pass, file *ast.File, rel, pkgPath string) []string {
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
