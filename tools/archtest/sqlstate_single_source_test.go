//go:build archtest

// INVARIANT: SQLSTATE-SINGLE-SOURCE-01
//
// # Package archtest — SQLSTATE-SINGLE-SOURCE-01
//
// pkg/pgquery is the single source for the SQLSTATE codes it owns —
// unique-violation (23505), foreign-key-violation (23503) and PL/pgSQL
// RAISE EXCEPTION (P0001). Before DX4 these classifiers were copy-pasted into
// adapters/postgres and two cell-private PG adapters; PR #578 deleted two of
// the duplicates but added NO enforcement, so the iotdevice copy survived and
// falsified the "single source" claim. This rule machine-guards the invariant
// so a new cell-private PG adapter cannot silently re-implement an owned
// classifier.
//
// # Rule (value-scoped, actionable)
//
// Outside pkg/pgquery, no comparison may test a pgconn.PgError.Code field
// against an owned SQLSTATE literal. Two AST forms are covered:
//
//   - BinaryExpr  `<x>.Code == "23505"` / `!=` (either operand order)
//   - SwitchStmt  `switch <x>.Code { case "23503": … }`
//
// where `<x>` resolves via *types.Info to github.com/jackc/pgx/v5/pgconn.PgError
// (pointer dereferenced) and the literal resolves via EvaluateConstString to a
// member of ownedSQLStates. The owned set is sourced directly from the
// pkg/pgquery exported consts (single source — adding a fourth owned classifier
// const there extends this rule automatically).
//
// The transient/connection classifier in adapters/postgres/classify.go reads
// pgErr.Code but compares it against 40001 / 40P01 / class-prefix 08 — codes
// pkg/pgquery does NOT own. Those are a SEPARATE single source (one copy) and
// are correctly NOT flagged because they are not in ownedSQLStates. No
// carve-out is required: accesscore isLastAdminProtected was refactored to
// delegate the P0001 classification to pgquery.IsRaiseException (it no longer
// reads .Code), and the iotdevice duplicate was deleted.
//
// # AI-robust grading (ai-robust.md §三档分级)
//
// Medium (type-aware archtest; archtest-bound, not compile-time — Go cannot
// forbid reading a struct field, same ceiling and precedent as
// PANIC-REGISTERED-01 / ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01).
// Form-uniqueness: the receiver type is resolved via *types.Info to the exact
// named type pgconn.PgError and the literal via EvaluateConstString to the
// owned-const set — there is no "looks-like-but-isn't" gray zone. There is no
// string anchor, no comment escape, no carve-out map. A companion positive-
// anchor self-check (TestSQLStateSingleSource_SelfCheck) freezes the exact set
// of production files permitted to read pgconn.PgError.Code, so the blind spots
// below cannot silently hide a new duplicate.
//
// # Blind spots (ai-robust.md §"工具选定后强制盲区自检")
//
// BS-1 Non-constant RHS: `code := "23505"; pgErr.Code == code` —
// EvaluateConstString folds simple const idents but a runtime variable defeats
// it, so the value-scoped rule would not flag it. Accepted: a regressing
// duplicate hardcodes the literal (that IS the copy-paste). Covered by the
// self-check positive anchor: ANY new production file reading pgErr.Code (in
// any form) fails regardless of operand const-ness.
//
// BS-2 Switch tag indirection: `c := pgErr.Code; switch c { case "23505": }` —
// the switch tag is the local `c`, not the selector, so the SwitchStmt arm is
// not matched. Same compensation as BS-1 (self-check anchors the .Code read at
// the assignment site).
//
// BS-3 Reflective / generic field access: impossible to express meaningfully
// for an exported wire struct without a literal selector; not a realistic
// duplicate shape. Compensation: self-check positive anchor.
//
// # RED fixture
//
// tools/archtest/internal/sqlstatesinglesourcefixture/fixture.go provides 3
// RED cases (badUnique / badNotRaise / badFKSwitch) and 3 GREEN controls
// (okTransient / okConstraint / okErrorsAs). The expected-violation set is
// expectedSQLStateFixtureLines, asserted exactly by
// TestSQLStateSingleSource_RedFixtureDetected.
//
// ref: ADR docs/architecture/202605191200-adr-sqlstate-single-source.md
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/pgquery"
)

const (
	sqlStateSingleSourceRuleID = "SQLSTATE-SINGLE-SOURCE-01"
	pgconnImportPath           = "github.com/jackc/pgx/v5/pgconn"
	pgErrorTypeName            = "PgError"
	pgErrorCodeFieldName       = "Code"
	// pgquerySingleSourcePrefix is the only package permitted to compare
	// pgconn.PgError.Code against an owned SQLSTATE literal — it IS the source.
	pgquerySingleSourcePrefix = "pkg/pgquery/"
)

// ownedSQLStates is the set of SQLSTATE codes pkg/pgquery is the single source
// for. Sourced from the pkg/pgquery exported consts so the set tracks that
// package automatically (no second literal definition to drift).
var ownedSQLStates = map[string]struct{}{
	pgquery.SQLStateUniqueViolation:     {},
	pgquery.SQLStateForeignKeyViolation: {},
	pgquery.SQLStateRaiseException:      {},
}

// expectedPgErrorCodeReaders is the authoritative positive anchor: the exact
// set of production files permitted to read pgconn.PgError.Code.
//
//   - pkg/pgquery/sqlstate.go — the single source for owned classifiers.
//   - adapters/postgres/classify.go — the transient/connection classifier, a
//     SEPARATE single source (40001 / 40P01 / class 08), not a duplicate.
//
// Adding a third reader, or deleting/renaming either of these, fails
// TestSQLStateSingleSource_SelfCheck immediately — this is what closes the
// value-scoped rule's blind spots (BS-1..BS-3).
var expectedPgErrorCodeReaders = map[string]struct{}{
	"pkg/pgquery/sqlstate.go":       {},
	"adapters/postgres/classify.go": {},
}

// TestSQLStateSingleSource guards SQLSTATE-SINGLE-SOURCE-01 over the production
// package set (generated/ excluded). After DX4 + this PR it must be zero.
func TestSQLStateSingleSource(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := Run(t, Production(TypedOpts{}), sqlStateSingleSourceRule)
	Report(t, sqlStateSingleSourceRuleID, diags)
}

// sqlStateSingleSourceRule is the Rule fn, extracted so the production scan and
// the RED-fixture scan share one core.
func sqlStateSingleSourceRule(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasPrefix(rel, pgquerySingleSourcePrefix) {
			continue // pkg/pgquery IS the single source
		}
		diags = append(diags, scanOwnedCodeBinary(p.Fset, file, rel, p.TypesInfo)...)
		diags = append(diags, scanOwnedCodeSwitch(p.Fset, file, rel, p.TypesInfo)...)
	}
	return diags
}

// scanOwnedCodeBinary flags `<pgconn.PgError>.Code == "<owned>"` (and !=,
// either operand order).
func scanOwnedCodeBinary(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.BinaryExpr](file, func(be *ast.BinaryExpr) {
		if be.Op != token.EQL && be.Op != token.NEQ {
			return
		}
		if !comparesOwnedCode(be.X, be.Y, info) && !comparesOwnedCode(be.Y, be.X, info) {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: fset.Position(be.Pos()).Line,
			Message: "compares pgconn.PgError.Code to an owned SQLSTATE literal " +
				"(23505/23503/P0001) outside pkg/pgquery; call pgquery.IsUniqueViolation / " +
				"IsForeignKeyViolation / IsRaiseException instead (single source)",
		})
	})
	return diags
}

// scanOwnedCodeSwitch flags `switch <pgconn.PgError>.Code { case "<owned>": }`.
func scanOwnedCodeSwitch(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.SwitchStmt](file, func(sw *ast.SwitchStmt) {
		if sw.Tag == nil || !isPgErrorCodeSelector(sw.Tag, info) {
			return
		}
		EachInChildren[ast.CaseClause](sw.Body, func(cc *ast.CaseClause) {
			if !caseClauseHasOwnedLiteral(cc, info) {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: fset.Position(cc.Pos()).Line,
				Message: "switch on pgconn.PgError.Code with an owned SQLSTATE case " +
					"(23505/23503/P0001) outside pkg/pgquery; use the pgquery.Is* " +
					"classifiers instead (single source)",
			})
		})
	})
	return diags
}

// caseClauseHasOwnedLiteral reports whether any expr in cc.List const-folds to
// a member of ownedSQLStates.
func caseClauseHasOwnedLiteral(cc *ast.CaseClause, info *types.Info) bool {
	for _, e := range cc.List {
		if s, ok := EvaluateConstString(info, e); ok {
			if _, owned := ownedSQLStates[s]; owned {
				return true
			}
		}
	}
	return false
}

// comparesOwnedCode reports whether sel is a pgconn.PgError.Code selector and
// lit const-folds to an owned SQLSTATE value.
func comparesOwnedCode(sel, lit ast.Expr, info *types.Info) bool {
	if !isPgErrorCodeSelector(sel, info) {
		return false
	}
	s, ok := EvaluateConstString(info, lit)
	if !ok {
		return false
	}
	_, owned := ownedSQLStates[s]
	return owned
}

// isPgErrorCodeSelector reports whether expr is a SelectorExpr `<x>.Code` whose
// receiver <x> resolves via *types.Info to github.com/jackc/pgx/v5/pgconn.PgError
// (pointer dereferenced).
func isPgErrorCodeSelector(expr ast.Expr, info *types.Info) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != pgErrorCodeFieldName {
		return false
	}
	return isPgconnPgErrorType(sel.X, info)
}

// isPgconnPgErrorType resolves expr's static type to pgconn.PgError (deref *T).
func isPgconnPgErrorType(expr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	t := tv.Type
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == pgconnImportPath && obj.Name() == pgErrorTypeName
}

// expectedSQLStateFixtureLines pins the RED diagnostics to the fixture source
// lines. badUnique BinaryExpr at 40, badNotRaise BinaryExpr at 49, badFKSwitch
// CaseClause at 62. GREEN controls (okTransient/okConstraint/okErrorsAs) yield 0.
var expectedSQLStateFixtureLines = []int{40, 49, 62}

// TestSQLStateSingleSource_RedFixtureDetected asserts an exact-set match on the
// fixture so a rule regression (missed RED or spurious GREEN hit) fails CI.
func TestSQLStateSingleSource_RedFixtureDetected(t *testing.T) {
	t.Parallel()

	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/sqlstatesinglesourcefixture/..."},
	),

		sqlStateSingleSourceRule)

	got := make([]int, 0, len(diags))
	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
		got = append(got, d.Line)
	}
	sort.Ints(got)

	want := append([]int(nil), expectedSQLStateFixtureLines...)
	sort.Ints(want)
	assert.Equal(t, want, got,
		"SQLSTATE-SINGLE-SOURCE-01 RED fixture: produced diagnostic line set must "+
			"exactly match expectedSQLStateFixtureLines; update both the fixture and "+
			"the expected set together when changing the fixture intentionally")
}

// TestSQLStateSingleSource_SelfCheck is the positive-anchor companion that
// closes blind spots BS-1..BS-3: it freezes the exact set of production files
// permitted to read pgconn.PgError.Code at all. A new duplicate that evades the
// value-scoped rule (non-const RHS, switch-tag indirection, …) still introduces
// a .Code read in a new file and fails here; deleting/moving a sanctioned
// reader also fails (so the anchor cannot rot to trivially-true).
func TestSQLStateSingleSource_SelfCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	actual := map[string]struct{}{}
	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if isPgErrorCodeSelector(sel, p.TypesInfo) {
					actual[rel] = struct{}{}
				}
			})
		}
		return nil
	})

	assert.Equal(t, sortedStructKeys(expectedPgErrorCodeReaders), sortedStructKeys(actual),
		"SQLSTATE-SINGLE-SOURCE-01 self-check: the set of production files reading "+
			"pgconn.PgError.Code changed. A new reader is a likely duplicated SQLSTATE "+
			"classifier — route it through pkg/pgquery. If a sanctioned reader moved, "+
			"update expectedPgErrorCodeReaders AND the ADR.")
}

func sortedStructKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
