// INVARIANT: SAGA-STATUS-FANOUT-COVERAGE-01
//
// saga_status_fanout_coverage_test.go — enforces the contract-fanout closure
// for the saga.Status (and journal.EventKind) enums: when a constant is added,
// its fanout carriers must stay in lockstep, or CI goes red.
//
// Motivation: PR #1210 C6 added saga.StatusCompensationFailed (=8) plus
// journal.KindStepCompensationFailed (=10) / KindSagaCompensationFailed (=11),
// but every fanout carrier (the readyz status table, the conformance suite, the
// alerting kind legend, the terminal→kind mapping) silently drifted and was only
// fixed after N rounds of review. .claude/rules/gocell/contract-fanout.md
// mandates the fanout but had no machine guard. This archtest is that guard.
//
// # Authoritative source (type-aware; mirrors PROBENAME-SEALED-FUNNEL-01)
//
// The saga.Status / journal.EventKind const SETS are enumerated type-aware via
// info.Defs + a *types.Const predicate (sfcIsTypedConst) — NOT by grepping a
// "Status"/"Kind" name prefix. Renaming a member keeps it in the resolved set
// (its renamed identifier must still appear in every carrier); renaming the type
// trips the >= len floor guards (require.GreaterOrEqual) instead of vacuously
// passing. There is no hand-maintained golden list: the const set IS the golden
// source, so adding a const auto-tightens the rule.
//
// Wire forms are read from the source's own authority, never re-derived:
//   - saga.Status readyz rows use the bare identifier (StatusPending → "Pending"
//     via TrimPrefix — an exact string op, not a naming convention).
//   - journal.EventKind legend entries use the snake_case label parsed out of the
//     EventKind.String() switch (sfcCollectKindConsts) — no camelCase→snake
//     reimplementation.
//   - the terminal Status subset is parsed from the Status.IsTerminal() switch
//     (sfcCollectIsTerminalSet), the type's own canonical definition.
//
// # Sub-rule index
//
//   - C1: conformance terminal coverage — every TERMINAL saga.Status appears as
//     the 4th arg of a MarkTerminal(...) call in kernel/saga/sagajournaltest.
//     (Non-terminal statuses are NOT required here: StatusPending is never named
//     in that package; readyz C2 covers all 8.)
//   - C2a/C2b: docs/ops/readyz.md status table forward (every const has a row) +
//     reverse (every row maps to a const).
//   - C3a/C3b: docs/ops/alerting-rules.md "kind 速查" legend forward (every
//     EventKind appears with matching numeric value) + reverse (every legend
//     entry maps to a const).
//   - C4: journal.TerminalEventKind switch exhaustiveness — its case set equals
//     the IsTerminal() terminal set (no missing, no extra). This is the sole
//     Status→Kind mapping and the exact site that drifts on a terminal addition.
//
// # Blind-spot catalog (forms the chosen tools cannot see) + reverse self-checks
//
//   - B1 (row-count lock): a readyz table reformat (dropped backticks, changed
//     columns) could silently empty the regex parser → C2 passes vacuously.
//     Guarded by the C2 sub-test asserting parsed-row-count == status-const-count.
//   - B2 (numeric Status conversion): saga.Status(8) as a MarkTerminal arg would
//     bypass C1's identifier resolution. Guarded by
//     TestSagaStatusFanoutCoverage_BlindSpotNoNumericStatusConv (bans
//     saga.Status(<int literal>) in the conformance package).
//   - B3 (legend entry-count lock): a legend reformat could empty the parser →
//     C3 passes vacuously. Guarded by the C3 sub-test asserting parsed-entry-
//     count == EventKind-const-count.
//   - B4 (switch-form coupling): C4 / the terminal-set derivation parse the
//     case clauses of journal.TerminalEventKind and Status.IsTerminal(). If
//     either is refactored away from a switch (e.g. to a map lookup or
//     slices.Contains), sfcCollectSwitchStatusCases yields an empty set. This is
//     NOT a vacuous pass: the require.NotEmpty floor guards in
//     TestSagaStatusFanoutCoverage (run against real source) fire and name both
//     root causes (switch-form change OR type rename) — those floor guards ARE
//     B4's reverse self-check.
//   - RED-fixture: TestSagaStatusFanoutCoverage_REDFixture exercises the shared
//     coverage matcher (sfcMissing) on a synthetic dropped entry (C1/C2a teeth);
//     TestSagaStatusFanoutCoverage_REDFixtureLegendForward and
//     ..._REDFixtureTerminalEventKind do the same for the C3a (sfcDiagsLegendForward)
//     and C4 (sfcDiagsTerminalEventKind) detection paths, proving the live rule
//     emits diagnostics on drift even though it is green on aligned source.
//     Parser drift is covered by TestSfcParseReadyzStatusTable /
//     TestSfcParseKindLegend (real red→green on synthetic + malformed inputs).
//
// # AI-robust grading: Medium (permanent ceiling — open enum by design)
//
// This is a coverage/fanout test, NOT a funnel — §Funnel 双向锁评级 (下游/上游
// split) does NOT apply; it gets one composite grade.
//
//   - Not Hard: saga.Status / journal.EventKind are open uint8 enums. Go has no
//     construct making "added a const without a carrier" a compile error; a
//     sealed marker would guard type membership, not const-set ⇄ carrier
//     coverage. The only true-Hard route (codegen-funnel + golden generating the
//     readyz table + conformance subtests + legend, regenerate-and-diff) is
//     defeated by the conformance subtests' non-mechanical setup (the legal
//     source phase per terminal status derives from statusTransitions, not from
//     the const) and by markdown carriers that no Go type can constrain. It is
//     also disproportionate for an 8/11-member enum. Documented here as a
//     permanent ceiling, no Hard-upgrade tracking issue (won't-do, mirrors
//     PROBENAME-SEALED-FUNNEL-01 A1's lang-ceiling stance).
//   - Solidly Medium: enumeration is type-aware (resolves actual
//     saga.Status / journal.EventKind *types.Const, rename-resistant) with floor
//     guards against vacuous pass; the markdown carriers are parsed structurally
//     and locked forward AND reverse plus a row/entry-count blind-spot check.
//     Residual gap (honest): the invariant is archtest-bound (a same-PR test
//     deletion bypasses it) — but hack/verify-archtest.sh auto-discovers every
//     Test* and ARCHTEST-VERIFY-COVERAGE-01 cross-checks discovery vs AST, so
//     deleting the test itself is caught.
//
// ref: tools/archtest/probename_sealed_funnel_test.go (typed const-enum + floor guard)
// ref: tools/archtest/saga_journal_conformance_enrollment_test.go (saga sibling + RED fixture)
// ref: tools/archtest/reverse_coverage_invariants_test.go (forward/reverse coverage pattern)
// ref: .claude/rules/gocell/contract-fanout.md (the fanout obligation this guards)
package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sfcStatusPkgPath      = "github.com/ghbvf/gocell/kernel/saga"
	sfcJournalPkgPath     = "github.com/ghbvf/gocell/kernel/saga/journal"
	sfcConformancePkgPath = "github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
	sfcStatusTypeName     = "Status"
	sfcEventKindTypeName  = "EventKind"
	sfcReadyzDocRel       = "docs/ops/readyz.md"
	sfcAlertingDocRel     = "docs/ops/alerting-rules.md"
)

// sfcStatusConst is one resolved saga.Status enum member.
type sfcStatusConst struct {
	Name     string // e.g. "StatusCompensationFailed"
	Stripped string // e.g. "CompensationFailed" (readyz table identifier form)
	Value    int64
}

// sfcKindConst is one resolved journal.EventKind enum member.
type sfcKindConst struct {
	Name  string // e.g. "KindSagaCompensationFailed"
	Wire  string // e.g. "saga_compensation_failed" (from EventKind.String() switch)
	Value int64
}

// ─── type-aware predicates / collectors ─────────────────────────────────────

// sfcIsTypedConst reports whether obj is a *types.Const whose named type is
// pkgPath.typeName.
func sfcIsTypedConst(obj types.Object, pkgPath, typeName string) bool {
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	named, ok := c.Type().(*types.Named)
	if !ok {
		return false
	}
	tobj := named.Obj()
	return tobj.Pkg() != nil && tobj.Pkg().Path() == pkgPath && tobj.Name() == typeName
}

// sfcReceiverIsType reports whether fd's receiver resolves to pkgPath.typeName
// (value or pointer receiver).
func sfcReceiverIsType(fd *ast.FuncDecl, info *types.Info, pkgPath, typeName string) bool {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	t := info.TypeOf(fd.Recv.List[0].Type)
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	o := named.Obj()
	return o.Pkg() != nil && o.Pkg().Path() == pkgPath && o.Name() == typeName
}

// sfcConstName resolves an ident / selector expression to the name of a const of
// type pkgPath.typeName.
func sfcConstName(expr ast.Expr, info *types.Info, pkgPath, typeName string) (string, bool) {
	var ident *ast.Ident
	switch e := expr.(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return "", false
	}
	obj, ok := info.Uses[ident]
	if !ok || !sfcIsTypedConst(obj, pkgPath, typeName) {
		return "", false
	}
	return obj.Name(), true
}

// sfcCaseReturnString returns the single string literal a CaseClause body
// returns (e.g. `case X: return "x"`), or "" if not that shape.
func sfcCaseReturnString(cc *ast.CaseClause) string {
	for _, stmt := range cc.Body {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		if lit, ok := ret.Results[0].(*ast.BasicLit); ok {
			if s, ok := StringLitValue(lit); ok {
				return s
			}
		}
	}
	return ""
}

// sfcCollectStatusConsts enumerates the saga.Status const set in p (the saga
// package pass).
func sfcCollectStatusConsts(p *Pass) []sfcStatusConst {
	var out []sfcStatusConst
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.GenDecl](f, func(gd *ast.GenDecl) {
			EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
				for _, name := range vs.Names {
					obj, ok := p.TypesInfo.Defs[name]
					if !ok || !sfcIsTypedConst(obj, sfcStatusPkgPath, sfcStatusTypeName) {
						continue
					}
					v, ok := constant.Int64Val(obj.(*types.Const).Val())
					if !ok {
						continue
					}
					out = append(out, sfcStatusConst{
						Name:     name.Name,
						Stripped: strings.TrimPrefix(name.Name, "Status"),
						Value:    v,
					})
				}
			})
		})
	}
	return out
}

// sfcCollectKindConsts enumerates the journal.EventKind const set in p (the
// journal package pass), pairing each const with the wire label its String()
// switch returns.
func sfcCollectKindConsts(p *Pass) []sfcKindConst {
	values := map[string]int64{}
	var order []string
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.GenDecl](f, func(gd *ast.GenDecl) {
			EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
				for _, name := range vs.Names {
					obj, ok := p.TypesInfo.Defs[name]
					if !ok || !sfcIsTypedConst(obj, sfcJournalPkgPath, sfcEventKindTypeName) {
						continue
					}
					v, ok := constant.Int64Val(obj.(*types.Const).Val())
					if !ok {
						continue
					}
					if _, dup := values[name.Name]; !dup {
						order = append(order, name.Name)
					}
					values[name.Name] = v
				}
			})
		})
	}
	wire := sfcParseEventKindStringWire(p)
	out := make([]sfcKindConst, 0, len(order))
	for _, n := range order {
		out = append(out, sfcKindConst{Name: n, Wire: wire[n], Value: values[n]})
	}
	return out
}

// sfcParseEventKindStringWire parses the EventKind.String() switch into a
// const-name → wire-label map (the authoritative wire form, read from source).
func sfcParseEventKindStringWire(p *Pass) map[string]string {
	out := map[string]string{}
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Name.Name != "String" || !sfcReceiverIsType(fd, p.TypesInfo, sfcJournalPkgPath, sfcEventKindTypeName) {
				return
			}
			EachInSubtree[ast.CaseClause](fd, func(cc *ast.CaseClause) {
				if len(cc.List) == 0 {
					return
				}
				label := sfcCaseReturnString(cc)
				if label == "" {
					return
				}
				for _, e := range cc.List {
					if name, ok := sfcConstName(e, p.TypesInfo, sfcJournalPkgPath, sfcEventKindTypeName); ok {
						out[name] = label
					}
				}
			})
		})
	}
	return out
}

// sfcCollectSwitchStatusCases parses a func/method named fnName (receiver
// matching recvPkg.recvType, or recvType=="" for a package func) and returns the
// set of saga.Status const names appearing in its non-default case clauses.
func sfcCollectSwitchStatusCases(p *Pass, fnName, recvType string) map[string]bool {
	out := map[string]bool{}
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Name.Name != fnName {
				return
			}
			if recvType == "" {
				if fd.Recv != nil {
					return
				}
			} else if !sfcReceiverIsType(fd, p.TypesInfo, sfcStatusPkgPath, recvType) {
				return
			}
			EachInSubtree[ast.CaseClause](fd, func(cc *ast.CaseClause) {
				for _, e := range cc.List {
					if name, ok := sfcConstName(e, p.TypesInfo, sfcStatusPkgPath, sfcStatusTypeName); ok {
						out[name] = true
					}
				}
			})
		})
	}
	return out
}

// sfcCollectMarkTerminalTargets scans the conformance package for
// MarkTerminal(...) calls and returns the set of saga.Status const names passed
// as the 4th positional argument.
func sfcCollectMarkTerminalTargets(p *Pass) map[string]bool {
	out := map[string]bool{}
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue // consistency with peer collectors; conformance calls live in conformance.go
		}
		EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "MarkTerminal" || len(call.Args) < 4 {
				return
			}
			if name, ok := sfcConstName(call.Args[3], p.TypesInfo, sfcStatusPkgPath, sfcStatusTypeName); ok {
				out[name] = true
			}
		})
	}
	return out
}

// sfcLoadDocs loads the two saga fanout doc carriers via the sanctioned content
// reader (gosec-clean, no os.ReadFile), keyed by module-relative path.
func sfcLoadDocs(t *testing.T, root string) map[string][]byte {
	t.Helper()
	sc := DirsScope(root, []string{"docs/ops"}, MatchRels(func(rel string) bool {
		return strings.HasSuffix(rel, "/readyz.md") || strings.HasSuffix(rel, "/alerting-rules.md")
	}))
	files, err := LoadContentFiles(sc, []string{".md"})
	require.NoError(t, err, "load saga fanout docs under docs/ops")
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		out[f.Rel] = f.Bytes
	}
	return out
}

// ─── pure doc parsers (unit-tested) ─────────────────────────────────────────

// sfcParseReadyzStatusTable extracts the backticked first-column identifiers
// from the saga lifecycle status table in readyz.md. The table is delimited by
// its header signature so unrelated backticks elsewhere in the doc are ignored.
func sfcParseReadyzStatusTable(md []byte) []string {
	rowRe := regexp.MustCompile("^\\|\\s*`([A-Za-z]+)`\\s*\\|")
	var out []string
	inTable := false
	for _, line := range strings.Split(string(md), "\n") {
		s := strings.TrimSpace(line)
		if !inTable {
			if strings.HasPrefix(s, "| Status |") && strings.Contains(s, "| Value |") {
				inTable = true
			}
			continue
		}
		if strings.HasPrefix(s, "|") && strings.Trim(s, "|-: ") == "" {
			continue // separator row (|---|---|...)
		}
		m := rowRe.FindStringSubmatch(s)
		if m == nil {
			break // first non-row line ends the table
		}
		out = append(out, m[1])
	}
	return out
}

// sfcParseKindLegend extracts the "kind 速查" legend (N=wire_form entries) from
// alerting-rules.md. The legend region runs from the "速查" marker line to the
// first line containing the "。" terminator — and is also bounded by the first
// blank line, so that if the "。" terminator is ever removed by a doc edit the
// parser does not run away over the rest of the document (which could match
// stray `N=snake_case` text and produce same-count-wrong-entries drift that the
// B3 count lock would not catch).
func sfcParseKindLegend(md []byte) map[int]string {
	lines := strings.Split(string(md), "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "kind") && strings.Contains(l, "速查") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		if i > start && strings.TrimSpace(lines[i]) == "" {
			break // blank line bounds the contiguous legend block
		}
		b.WriteString(lines[i])
		b.WriteByte('\n')
		if strings.Contains(lines[i], "。") {
			break
		}
	}
	entryRe := regexp.MustCompile(`(\d+)=([a-z_]+)`)
	out := map[int]string{}
	for _, m := range entryRe.FindAllStringSubmatch(b.String(), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out[n] = m[2]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ─── shared coverage matcher ────────────────────────────────────────────────

// sfcMissing returns the required entries absent from carrier (the coverage-gap
// primitive shared by C1/C2a and exercised by the RED-fixture self-test).
func sfcMissing(required []string, carrier map[string]bool) []string {
	var out []string
	for _, r := range required {
		if !carrier[r] {
			out = append(out, r)
		}
	}
	return out
}

// ─── diag builders (one per sub-rule; see file-header sub-rule index) ────────

// sfcDiagsConformanceTerminal builds C1 diagnostics: terminal saga.Status consts
// with no MarkTerminal(...) subtest in the conformance package.
func sfcDiagsConformanceTerminal(statuses []sfcStatusConst, isTerminal, markTargets map[string]bool) []Diagnostic {
	var terminal []string
	for _, s := range statuses {
		if isTerminal[s.Name] {
			terminal = append(terminal, s.Name)
		}
	}
	var diags []Diagnostic
	for _, name := range sfcMissing(terminal, markTargets) {
		diags = append(diags, Diagnostic{Rel: sfcConformancePkgPath, Message: fmt.Sprintf(
			"terminal saga.%s has no MarkTerminal(...) subtest in %s — add a conformance case exercising it",
			name, sfcConformancePkgPath)})
	}
	return diags
}

// sfcDiagsReadyzForward builds C2a diagnostics: saga.Status consts with no row
// in the readyz.md lifecycle table.
func sfcDiagsReadyzForward(statuses []sfcStatusConst, tableSet map[string]bool) []Diagnostic {
	var stripped []string
	for _, s := range statuses {
		stripped = append(stripped, s.Stripped)
	}
	var diags []Diagnostic
	for _, name := range sfcMissing(stripped, tableSet) {
		diags = append(diags, Diagnostic{Rel: sfcReadyzDocRel, Message: fmt.Sprintf(
			"saga.Status%s missing a row in the readyz.md lifecycle table", name)})
	}
	return diags
}

// sfcDiagsReadyzReverse builds C2b diagnostics: readyz.md table rows that do not
// map to any saga.Status const (dangling row after a rename/removal).
func sfcDiagsReadyzReverse(tableIdents []string, strippedSet map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for _, id := range tableIdents {
		if !strippedSet[id] {
			diags = append(diags, Diagnostic{Rel: sfcReadyzDocRel, Message: fmt.Sprintf(
				"readyz.md status table row %q has no matching saga.Status const (dangling/renamed)", id)})
		}
	}
	return diags
}

// sfcDiagsLegendForward builds C3a diagnostics: journal.EventKind consts missing
// from, or value-mismatched against, the alerting-rules.md "kind 速查" legend.
func sfcDiagsLegendForward(kinds []sfcKindConst, legend map[int]string) []Diagnostic {
	var diags []Diagnostic
	for _, k := range kinds {
		if k.Wire == "" {
			diags = append(diags, Diagnostic{Rel: sfcJournalPkgPath, Message: fmt.Sprintf(
				"journal.%s wire form unresolved from EventKind.String() switch", k.Name)})
			continue
		}
		if legend[int(k.Value)] != k.Wire {
			diags = append(diags, Diagnostic{Rel: sfcAlertingDocRel, Message: fmt.Sprintf(
				"journal.%s (=%d, %q) missing/mismatched in alerting-rules.md kind legend (got %q)",
				k.Name, k.Value, k.Wire, legend[int(k.Value)])})
		}
	}
	return diags
}

// sfcDiagsLegendReverse builds C3b diagnostics: alerting-rules.md legend entries
// that do not map to any journal.EventKind const (dangling/renamed).
func sfcDiagsLegendReverse(legend map[int]string, kinds []sfcKindConst) []Diagnostic {
	byVal := map[int]string{}
	for _, k := range kinds {
		byVal[int(k.Value)] = k.Wire
	}
	var diags []Diagnostic
	for n, name := range legend {
		if byVal[n] != name {
			diags = append(diags, Diagnostic{Rel: sfcAlertingDocRel, Message: fmt.Sprintf(
				"alerting kind legend entry %d=%q has no matching journal.EventKind const", n, name)})
		}
	}
	return diags
}

// sfcDiagsTerminalEventKind builds C4 diagnostics: the journal.TerminalEventKind
// switch case set must equal the Status.IsTerminal() terminal set (both
// directions — missing case, or extra case not classified terminal).
func sfcDiagsTerminalEventKind(isTerminal, tekCases map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for name := range isTerminal {
		if !tekCases[name] {
			diags = append(diags, Diagnostic{Rel: sfcJournalPkgPath, Message: fmt.Sprintf(
				"terminal saga.%s missing a case in journal.TerminalEventKind switch", name)})
		}
	}
	for name := range tekCases {
		if !isTerminal[name] {
			diags = append(diags, Diagnostic{Rel: sfcJournalPkgPath, Message: fmt.Sprintf(
				"journal.TerminalEventKind has case saga.%s which Status.IsTerminal() does not classify terminal", name)})
		}
	}
	return diags
}

// ─── main production scan ────────────────────────────────────────────────────

// TestSagaStatusFanoutCoverage enforces SAGA-STATUS-FANOUT-COVERAGE-01 (C1–C4 +
// B1/B3 count locks) against the live source tree.
func TestSagaStatusFanoutCoverage(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)

	var statuses []sfcStatusConst
	var kinds []sfcKindConst
	isTerminal := map[string]bool{}
	tekCases := map[string]bool{}
	markTargets := map[string]bool{}

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/saga/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			switch p.Pkg.Path() {
			case sfcStatusPkgPath:
				statuses = append(statuses, sfcCollectStatusConsts(p)...)
				for k := range sfcCollectSwitchStatusCases(p, "IsTerminal", sfcStatusTypeName) {
					isTerminal[k] = true
				}
			case sfcJournalPkgPath:
				kinds = append(kinds, sfcCollectKindConsts(p)...)
				for k := range sfcCollectSwitchStatusCases(p, "TerminalEventKind", "") {
					tekCases[k] = true
				}
			case sfcConformancePkgPath:
				for k := range sfcCollectMarkTerminalTargets(p) {
					markTargets[k] = true
				}
			}
			return nil
		})

	require.GreaterOrEqual(t, len(statuses), 8,
		"SAGA-STATUS-FANOUT-COVERAGE-01: resolved %d saga.Status consts (<8) — type likely renamed/moved; "+
			"floor guard prevents a vacuous pass", len(statuses))
	require.GreaterOrEqual(t, len(kinds), 11,
		"SAGA-STATUS-FANOUT-COVERAGE-01: resolved %d journal.EventKind consts (<11) — type likely renamed/moved", len(kinds))
	require.NotEmpty(t, isTerminal, "SAGA-STATUS-FANOUT-COVERAGE-01: Status.IsTerminal() terminal set resolved empty — "+
		"either IsTerminal() was refactored away from a switch (sfcCollectSwitchStatusCases requires a switch body; see blind-spot B4) "+
		"or the receiver type was renamed/moved from saga.Status")
	require.NotEmpty(t, tekCases, "SAGA-STATUS-FANOUT-COVERAGE-01: journal.TerminalEventKind case set resolved empty — "+
		"either TerminalEventKind was refactored away from a switch (see blind-spot B4) or saga.Status was renamed/moved")

	docs := sfcLoadDocs(t, root)
	require.NotEmpty(t, docs[sfcReadyzDocRel], "load %s", sfcReadyzDocRel)
	require.NotEmpty(t, docs[sfcAlertingDocRel], "load %s", sfcAlertingDocRel)

	tableIdents := sfcParseReadyzStatusTable(docs[sfcReadyzDocRel])
	legend := sfcParseKindLegend(docs[sfcAlertingDocRel])

	tableSet := map[string]bool{}
	for _, id := range tableIdents {
		tableSet[id] = true
	}
	strippedSet := map[string]bool{}
	for _, s := range statuses {
		strippedSet[s.Stripped] = true
	}

	t.Run("C1_ConformanceTerminalCoverage", func(t *testing.T) {
		t.Parallel()
		Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C1", sfcDiagsConformanceTerminal(statuses, isTerminal, markTargets))
	})
	t.Run("C2a_ReadyzTableForward", func(t *testing.T) {
		t.Parallel()
		Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C2a", sfcDiagsReadyzForward(statuses, tableSet))
	})
	t.Run("C2b_ReadyzTableReverse", func(t *testing.T) {
		t.Parallel()
		Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C2b", sfcDiagsReadyzReverse(tableIdents, strippedSet))
	})
	t.Run("B1_ReadyzRowCount", func(t *testing.T) {
		t.Parallel()
		assert.Len(t, tableIdents, len(statuses),
			"SAGA-STATUS-FANOUT-COVERAGE-01/B1: readyz.md table row count (%d) != saga.Status const count (%d) — "+
				"table reformat may have silently emptied the parser", len(tableIdents), len(statuses))
	})
	t.Run("C3a_AlertingLegendForward", func(t *testing.T) {
		t.Parallel()
		Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C3a", sfcDiagsLegendForward(kinds, legend))
	})
	t.Run("C3b_AlertingLegendReverse", func(t *testing.T) {
		t.Parallel()
		Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C3b", sfcDiagsLegendReverse(legend, kinds))
	})
	t.Run("B3_AlertingLegendEntryCount", func(t *testing.T) {
		t.Parallel()
		assert.Len(t, legend, len(kinds),
			"SAGA-STATUS-FANOUT-COVERAGE-01/B3: alerting-rules.md kind legend entry count (%d) != journal.EventKind const count (%d) — "+
				"legend reformat may have silently emptied the parser", len(legend), len(kinds))
	})
	t.Run("C4_TerminalEventKindExhaustive", func(t *testing.T) {
		t.Parallel()
		Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C4", sfcDiagsTerminalEventKind(isTerminal, tekCases))
	})
}

// ─── blind-spot reverse self-check (B2) ──────────────────────────────────────

// TestSagaStatusFanoutCoverage_BlindSpotNoNumericStatusConv (B2) asserts the
// conformance package never uses a saga.Status(<int literal>) conversion, which
// would bypass C1's identifier-based MarkTerminal coverage.
func TestSagaStatusFanoutCoverage_BlindSpotNoNumericStatusConv(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/saga/sagajournaltest/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != sfcConformancePkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if len(call.Args) != 1 {
						return
					}
					tv, ok := p.TypesInfo.Types[call.Fun]
					if !ok || !tv.IsType() {
						return
					}
					named, ok := tv.Type.(*types.Named)
					if !ok {
						return
					}
					o := named.Obj()
					if o.Pkg() == nil || o.Pkg().Path() != sfcStatusPkgPath || o.Name() != sfcStatusTypeName {
						return
					}
					if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT {
						diags = append(diags, Diagnostic{Rel: rel, Line: p.Fset.Position(call.Pos()).Line, Message: fmt.Sprintf(
							"saga.Status(<int literal>) conversion at %s bypasses identifier-based MarkTerminal coverage — use a named saga.Status const", rel)})
					}
				})
			}
			return nil
		})
	Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/B2", diags)
}

// ─── RED-fixture + pure-helper unit tests ────────────────────────────────────

// TestSagaStatusFanoutCoverage_REDFixture exercises the shared coverage matcher
// (sfcMissing) on a synthetic dropped entry, proving the C1/C2a teeth detect
// drift even though the live rule is green on aligned source.
func TestSagaStatusFanoutCoverage_REDFixture(t *testing.T) {
	t.Parallel()
	required := []string{
		"Pending", "Running", "Compensating", "Succeeded",
		"Failed", "Compensated", "Expired", "CompensationFailed",
	}
	full := map[string]bool{}
	for _, r := range required {
		full[r] = true
	}
	assert.Empty(t, sfcMissing(required, full), "full carrier must report zero missing")

	dropped := map[string]bool{}
	for _, r := range required {
		dropped[r] = true
	}
	delete(dropped, "CompensationFailed")
	assert.Equal(t, []string{"CompensationFailed"}, sfcMissing(required, dropped),
		"dropping a carrier entry must surface it as missing (drift detection)")
}

// TestSagaStatusFanoutCoverage_REDFixtureLegendForward proves the C3a detection
// path (sfcDiagsLegendForward) fires when a legend entry is missing, mismatched,
// or has an unresolved wire form — not just that the parser works.
func TestSagaStatusFanoutCoverage_REDFixtureLegendForward(t *testing.T) {
	t.Parallel()
	kinds := []sfcKindConst{
		{Name: "KindStepStarted", Wire: "step_started", Value: 1},
		{Name: "KindStepCompleted", Wire: "step_completed", Value: 2},
	}
	aligned := map[int]string{1: "step_started", 2: "step_completed"}
	assert.Empty(t, sfcDiagsLegendForward(kinds, aligned), "aligned legend must yield zero C3a diags")

	missing := map[int]string{1: "step_started"} // entry 2 dropped
	assert.NotEmpty(t, sfcDiagsLegendForward(kinds, missing), "missing legend entry must fire C3a")

	mismatch := map[int]string{1: "step_started", 2: "step_complete"} // typo'd label
	assert.NotEmpty(t, sfcDiagsLegendForward(kinds, mismatch), "mismatched legend label must fire C3a")

	unresolved := []sfcKindConst{{Name: "KindMystery", Wire: "", Value: 3}}
	assert.NotEmpty(t, sfcDiagsLegendForward(unresolved, map[int]string{3: "mystery"}),
		"unresolved wire form (String() switch gap) must fire C3a")
}

// TestSagaStatusFanoutCoverage_REDFixtureTerminalEventKind proves the C4
// detection path (sfcDiagsTerminalEventKind) fires on either-direction drift
// between the IsTerminal() set and the TerminalEventKind switch case set.
func TestSagaStatusFanoutCoverage_REDFixtureTerminalEventKind(t *testing.T) {
	t.Parallel()
	terminal := map[string]bool{"StatusSucceeded": true, "StatusFailed": true}
	assert.Empty(t, sfcDiagsTerminalEventKind(terminal, terminal), "matching sets must yield zero C4 diags")

	missingCase := map[string]bool{"StatusSucceeded": true} // StatusFailed absent from TerminalEventKind
	assert.NotEmpty(t, sfcDiagsTerminalEventKind(terminal, missingCase), "terminal status missing a TerminalEventKind case must fire C4")

	extraCase := map[string]bool{"StatusSucceeded": true, "StatusFailed": true, "StatusRunning": true}
	assert.NotEmpty(t, sfcDiagsTerminalEventKind(terminal, extraCase),
		"TerminalEventKind case not classified terminal by IsTerminal() must fire C4")
}

func TestSfcParseReadyzStatusTable(t *testing.T) {
	t.Parallel()
	md := []byte("intro\n\n" +
		"| Status | Value | Phase | Terminal? |\n" +
		"|---|---|---|---|\n" +
		"| `Pending` | 1 | Not yet started | No |\n" +
		"| `CompensationFailed` | 8 | rollback failed | Yes |\n" +
		"\nafter the table `Ignored` should not match\n")
	assert.Equal(t, []string{"Pending", "CompensationFailed"}, sfcParseReadyzStatusTable(md))

	// No header signature → empty (a stray backtick table elsewhere is ignored).
	assert.Empty(t, sfcParseReadyzStatusTable([]byte("| Foo | Bar |\n|---|---|\n| `x` | 1 |\n")))

	// Contract: a blank line inside the table body terminates parsing early
	// (rows after it are dropped). This is intentional — the real table has no
	// blank rows, and the B1 row-count lock catches any resulting truncation.
	truncated := sfcParseReadyzStatusTable([]byte(
		"| Status | Value | Phase | Terminal? |\n" +
			"|---|---|---|---|\n" +
			"| `Pending` | 1 | x | No |\n" +
			"\n" +
			"| `Failed` | 5 | y | Yes |\n"))
	assert.Equal(t, []string{"Pending"}, truncated, "blank line mid-table ends parsing (B1 count lock catches truncation)")
}

func TestSfcParseKindLegend(t *testing.T) {
	t.Parallel()
	md := []byte("preceding kind=10 mention is not the legend\n" +
		"`kind` 速查：1=step_started，2=step_completed，\n" +
		"3=step_failed。\n" +
		"trailing 9=should_be_ignored after terminator\n")
	assert.Equal(t, map[int]string{1: "step_started", 2: "step_completed", 3: "step_failed"},
		sfcParseKindLegend(md))

	assert.Nil(t, sfcParseKindLegend([]byte("no legend marker here\n")))

	// Contract: if the "。" terminator is ever removed by a doc edit, the blank
	// line still bounds the legend region so the parser does not run away over
	// the rest of the document (which could otherwise produce same-count-wrong-
	// entries drift the B3 count lock would not catch).
	noTerminator := []byte(
		"`kind` 速查：1=step_started，2=step_completed\n" +
			"\n" +
			"## Some later section with 9=stray_match text\n")
	assert.Equal(t, map[int]string{1: "step_started", 2: "step_completed"},
		sfcParseKindLegend(noTerminator), "blank line bounds the legend when 。 terminator is absent")
}
