// INVARIANT: SAGA-STATUS-FANOUT-COVERAGE-01
//
// saga_status_fanout_coverage_test.go — enforces the contract-fanout closure for
// the saga.Status / journal.EventKind enums: when a constant is added, its fanout
// carriers must stay in lockstep, or the build / CI goes red.
//
// Motivation: PR #1210 C6 added saga.StatusCompensationFailed (=8) plus
// journal.KindStepCompensationFailed (=10) / KindSagaCompensationFailed (=11),
// but every fanout carrier (the readyz status table, the conformance terminal
// coverage, the alerting kind legend, the terminal→kind mapping) silently
// drifted and was only fixed after N rounds of review. .claude/rules/gocell/
// contract-fanout.md mandates the fanout; this is its machine guard.
//
// # Mechanism (codegen funnel + compile gate — Hard)
//
// The single source of truth is the saga.Status / journal.EventKind const set.
// `gocell generate saga-coverage` (tools/codegen/sagacoveragegen) renders three
// byte-locked artifacts from it:
//
//   - kernel/saga/sagajournaltest/terminal_coverage_gen.go — a struct with
//     exactly one field per TERMINAL saga.Status. conformance.go populates it
//     with a KEYLESS composite literal (terminalHappyPaths), so adding a terminal
//     status const → regen adds a field → the keyless literal fails to compile
//     ("too few values in struct literal") until a happy-path driver is supplied.
//     This is a COMPILE-TIME exhaustiveness gate — the Hard half. The conformance
//     harness (runTerminalCoverage) then runs each driver and asserts the
//     happy-path outcome (got==want, ok, terminal kind, lease released), so a
//     negative or mis-mapped driver is caught at runtime.
//   - the readyz.md saga lifecycle status table (Status / Value / Phase /
//     Terminal? rows).
//   - the alerting-rules.md "kind 速查" legend (value=wire entries).
//
// # Sub-rule index
//
//   - GOLDEN: each committed artifact (the gen file + the two marker-delimited
//     doc regions) is byte-identical to a fresh sagacoveragegen.Render(). A drift
//     surfaces as a red test naming the file and the regen command. This single
//     check subsumes the former C1 (conformance coverage), C2 (readyz table
//     forward/reverse + Value/Terminal? columns) and C3 (alerting legend) — none
//     can drift from the const set without the golden lock firing.
//   - C4: journal.TerminalEventKind switch exhaustiveness — its case set equals
//     the Status.IsTerminal() terminal set (no missing, no extra). TerminalEventKind
//     is production logic (event.go), not a generated artifact, so it keeps a
//     dedicated type-aware archtest. The runtime harness's got-kind assertion is a
//     second line of defense for the "missing case" direction; C4 also catches the
//     "extra case" direction.
//   - C5 (const-set ⇄ Valid()-range bijection): the go/types declared const set
//     of saga.Status / journal.EventKind MUST equal the value set Render()
//     enumerates via `for v := <start>; v.Valid(); v++`. This is what makes the
//     "const set is the single source of truth" claim STRICT: Render() (and hence
//     the golden) enumerates by the Valid()-loop, so without C5 a const added
//     without extending Valid() would be silently invisible to the whole funnel.
//     C5 enumerates the const set independently (go/types, compiler-derived) and
//     fails if it diverges from the loop in either direction. Type-aware archtest
//     (Medium) — closes blind-spot B2 below.
//
// # Blind-spot catalog (forms the chosen tools cannot see) + reverse self-checks
//
//   - B1 (switch-form coupling): C4 parses the case clauses of
//     journal.TerminalEventKind and Status.IsTerminal(). If either is refactored
//     away from a switch (map lookup, slices.Contains), sfcCollectSwitchStatusCases
//     yields an empty set. NOT a vacuous pass: the require.NotEmpty floor guards in
//     TestSagaStatusFanoutCoverageC4 fire and name both root causes (switch-form
//     change OR Status rename).
//   - B2 (const-set ⇄ Valid()-range coupling — NOW MACHINE-CHECKED BY C5): the
//     generator enumerates via `for v := <start>; v.Valid(); v++`. A const added
//     without extending Valid() (the loop stops before it), or a Valid() range that
//     exceeds / is non-contiguous with the declared const set, would make Render()
//     and the golden lock blind to part of the const set. Formerly dismissed as "a
//     self-contradiction the const author would not create"; that dismissal is
//     retired — C5 (TestSagaStatusFanoutCoverageC5) enumerates the const set
//     independently via go/types and fails on any divergence from the loop, in
//     either direction. C5's own residual blind spots are compile-gated: the loop
//     start sentinels (saga.StatusPending / journal.KindStepStarted) are referenced
//     by name, so renaming/removing them breaks the archtest build; renaming Valid()
//     drops the method-location floor guard (require.NotZero) rather than passing
//     vacuously.
//   - RED-fixtures: TestSagaStatusFanoutCoverageC4_REDFixture exercises
//     sfcDiagsTerminalEventKind on synthetic mismatched sets;
//     TestSagaStatusFanoutCoverageC5_REDFixture exercises sfcDiagsConstSetValidRange
//     on synthetic const-set/loop divergence (including the user-reported
//     "added a const but forgot Valid()" scenario); TestSagaCoverageGolden_REDFixture
//     exercises sfcGoldenDiags on synthetic drifted artifacts; all prove the live
//     checks emit diagnostics on drift even though they are green on aligned source.
//     TestSagaCoverageDiagnosticLocations asserts every emitted Diagnostic (C4, C5,
//     and golden) carries a real module-relative Rel and a non-zero Line (no
//     import-path Rel, no :0:).
//
// # AI-robust grading: Hard (codegen funnel + type-system compile gate)
//
// SUPERSEDES the prior "permanent Medium ceiling / Hard infeasible" grade. That
// grade conflated two codegen routes: codegen-ing the conformance DRIVE bodies
// (genuinely defeated by the non-mechanical legal-source-phase setup) versus
// codegen-ing the exhaustiveness SKELETON (NOT defeated). Go's keyless struct
// literal is a compile-time exhaustiveness primitive: a generated
// field-per-terminal struct + a hand-written keyless literal makes "added a
// terminal without coverage" a compile error. The drives stay hand-written; only
// the coverage skeleton + the doc fanout fragments are generated and golden-locked.
//
//   - Downstream Hard: the keyless literal in conformance.go cannot omit a
//     terminal once the generated struct gains its field (compile error). The doc
//     regions and the gen file cannot drift from the const set without the golden
//     lock firing.
//   - Upstream Hard: the generated artifacts are regenerate-and-diff byte-locked
//     against sagacoveragegen.Render(), which derives solely from the
//     saga.Status / journal.EventKind type sets (a rename breaks the generator
//     build). There is no hand-maintained golden list.
//
// ref: tools/codegen/sagacoveragegen (the single-source generator)
// ref: tools/codegen/requireddepsgen + tools/archtest/required_dep_nil_guard_test.go (generator-golden pattern)
// ref: .claude/rules/gocell/contract-fanout.md (the fanout obligation this guards)
package archtest

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/tools/codegen/sagacoveragegen"
)

const (
	sfcStatusPkgPath  = "github.com/ghbvf/gocell/kernel/saga"
	sfcJournalPkgPath = "github.com/ghbvf/gocell/kernel/saga/journal"
	sfcStatusTypeName = "Status"
	sfcKindTypeName   = "EventKind"
	sfcReadyzDocRel   = "docs/ops/readyz.md"
	sfcAlertingDocRel = "docs/ops/alerting-rules.md"
	sfcGenFileRel     = "kernel/saga/sagajournaltest/terminal_coverage_gen.go"
)

// ─── type-aware helpers (C4) ────────────────────────────────────────────────

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

// sfcCollectSwitchStatusCases parses a func/method named fnName (receiver
// matching sfcStatusPkgPath.recvType, or recvType=="" for a package func) and
// returns the set of saga.Status const names appearing in its non-default case
// clauses, plus the func's module-relative file and 1-based line for diagnostics.
func sfcCollectSwitchStatusCases(p *Pass, fnName, recvType string) (set map[string]bool, rel string, line int) {
	set = map[string]bool{}
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
			rel = p.Rel(f)
			line = p.Fset.Position(fd.Pos()).Line
			EachInSubtree[ast.CaseClause](fd, func(cc *ast.CaseClause) {
				for _, e := range cc.List {
					if name, ok := sfcConstName(e, p.TypesInfo, sfcStatusPkgPath, sfcStatusTypeName); ok {
						set[name] = true
					}
				}
			})
		})
	}
	return set, rel, line
}

// sfcDiagsTerminalEventKind builds C4 diagnostics: the journal.TerminalEventKind
// switch case set must equal the Status.IsTerminal() terminal set (both
// directions). All diagnostics point at the TerminalEventKind decl (rel:line).
func sfcDiagsTerminalEventKind(isTerminal, tekCases map[string]bool, rel string, line int) []Diagnostic {
	var diags []Diagnostic
	for name := range isTerminal {
		if !tekCases[name] {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: fmt.Sprintf(
				"terminal saga.%s missing a case in journal.TerminalEventKind switch", name)})
		}
	}
	for name := range tekCases {
		if !isTerminal[name] {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: fmt.Sprintf(
				"journal.TerminalEventKind has case saga.%s which Status.IsTerminal() does not classify terminal", name)})
		}
	}
	return diags
}

// ─── const-set ⇄ Valid()-range cross-check (C5) ──────────────────────────────

// sfcCollectDeclaredConsts enumerates, via go/types, the integer values of every
// package-scope const whose named type is pkgPath.typeName, and locates the
// type's Valid() method — the site to fix when the const set and the Valid()
// range diverge. Returns the value→constName map plus the Valid() decl's
// module-relative file and 1-based line (for C5 diagnostics).
func sfcCollectDeclaredConsts(p *Pass, pkgPath, typeName string) (values map[int64]string, validRel string, validLine int) {
	values = map[int64]string{}
	if p.Pkg == nil || p.TypesInfo == nil {
		return values, "", 0
	}
	scope := p.Pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !sfcIsTypedConst(obj, pkgPath, typeName) {
			continue
		}
		v, exact := constant.Int64Val(obj.(*types.Const).Val())
		if !exact {
			continue
		}
		values[v] = name
	}
	for _, f := range p.Files {
		if strings.HasSuffix(p.Rel(f), "_test.go") {
			continue
		}
		EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Name.Name != "Valid" || !sfcReceiverIsType(fd, p.TypesInfo, pkgPath, typeName) {
				return
			}
			validRel = p.Rel(f)
			validLine = p.Fset.Position(fd.Pos()).Line
		})
	}
	return values, validRel, validLine
}

// sfcStatusLoopValues returns the saga.Status value set enumerated EXACTLY as
// sagacoveragegen.collectStatuses does — `for s := StatusPending; s.Valid(); s++`
// — so C5 compares the declared const set against the values Render() actually
// sees, not a re-derived range (which would miss a non-contiguous Valid() that
// truncates the loop early). The iteration is capped at the uint8 domain: a
// Valid() that admits the whole domain never terminates the loop, which is
// itself the kind of bug C5 exists to surface, so the cap fails loudly rather
// than hanging CI.
func sfcStatusLoopValues(t *testing.T) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	n := 0
	for s := saga.StatusPending; s.Valid(); s++ {
		out[int64(s)] = true
		if n++; n > 256 {
			t.Fatalf("saga.Status.Valid() admits >256 values — Valid() never terminates the enumeration loop")
		}
	}
	return out
}

// sfcKindLoopValues mirrors sfcStatusLoopValues for journal.EventKind
// (sagacoveragegen.collectKinds enumeration).
func sfcKindLoopValues(t *testing.T) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	n := 0
	for k := journal.KindStepStarted; k.Valid(); k++ {
		out[int64(k)] = true
		if n++; n > 256 {
			t.Fatalf("journal.EventKind.Valid() admits >256 values — Valid() never terminates the enumeration loop")
		}
	}
	return out
}

// sfcDiagsConstSetValidRange builds C5 diagnostics: the go/types declared const
// value set MUST equal the value set Render() enumerates via
// `for v := <start>; v.Valid(); v++`. Any divergence means Render() (and thus
// the fanout golden) cannot see the full declared const set — the gap where a
// new const is added but Valid() is not extended. Both directions are reported;
// every diagnostic points at the type's Valid() method (the fix site).
func sfcDiagsConstSetValidRange(typeLabel string, declared map[int64]string, loop map[int64]bool, rel string, line int) []Diagnostic {
	var diags []Diagnostic
	for v, name := range declared {
		if !loop[v] {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: fmt.Sprintf(
				"%s const %s (value %d) is declared but %s.Valid() excludes it from the `for v := …; v.Valid(); v++` enumeration — "+
					"Render() and the fanout golden cannot cover it; extend Valid() (and IsTerminal()/String()/the fanout carriers) to admit it",
				typeLabel, name, v, typeLabel)})
		}
	}
	for v := range loop {
		if _, ok := declared[v]; !ok {
			diags = append(diags, Diagnostic{Rel: rel, Line: line, Message: fmt.Sprintf(
				"%s.Valid() admits value %d which no declared const carries — Valid()'s range exceeds the "+
					"const set; tighten Valid() or declare the missing const",
				typeLabel, v)})
		}
	}
	return diags
}

// ─── golden lock (GOLDEN) ─────────────────────────────────────────────────────

// sfcGoldenDiags compares the committed fanout artifacts against a fresh
// Render(). It is the pure detection core shared by the live golden test and the
// RED fixture. Every diagnostic carries a real module-relative Rel and non-zero
// Line (F3).
func sfcGoldenDiags(art sagacoveragegen.Artifacts, gen, readyz, alerting []byte) []Diagnostic {
	var diags []Diagnostic

	if !bytes.Equal(gen, art.TerminalCoverageGo) {
		diags = append(diags, Diagnostic{Rel: sfcGenFileRel, Line: 1, Message: sfcRegenMsg(
			"terminal_coverage_gen.go drifted from the saga.Status const set")})
	}

	diags = append(diags, sfcRegionDiag(readyz, sfcReadyzDocRel, art.ReadyzTable,
		sagacoveragegen.ReadyzTableStartMarker, sagacoveragegen.ReadyzTableEndMarker,
		"readyz.md saga status table region")...)
	diags = append(diags, sfcRegionDiag(alerting, sfcAlertingDocRel, art.KindLegend,
		sagacoveragegen.KindLegendStartMarker, sagacoveragegen.KindLegendEndMarker,
		"alerting-rules.md kind legend region")...)

	return diags
}

// sfcRegionDiag compares one marker-delimited doc region against want, pointing
// at the start-marker line on drift (or line 1 if the marker is missing).
func sfcRegionDiag(content []byte, rel, want, start, end, label string) []Diagnostic {
	region, err := sagacoveragegen.ExtractRegion(string(content), start, end)
	if err != nil {
		return []Diagnostic{{Rel: rel, Line: 1, Message: fmt.Sprintf("%s markers not found: %v", label, err)}}
	}
	if region != want {
		return []Diagnostic{{Rel: rel, Line: sfcMarkerLine(content, start), Message: sfcRegenMsg(label + " drifted from source")}}
	}
	return nil
}

func sfcRegenMsg(what string) string {
	return what + " — run `gocell generate saga-coverage` and commit the result"
}

// sfcMarkerLine returns the 1-based line of marker in content (1 if absent).
func sfcMarkerLine(content []byte, marker string) int {
	idx := bytes.Index(content, []byte(marker))
	if idx < 0 {
		return 1
	}
	return bytes.Count(content[:idx], []byte("\n")) + 1
}

// ─── file readers (sanctioned content reader; no os.ReadFile) ─────────────────

func sfcReadFile(t *testing.T, root, dir, rel, ext string) []byte {
	t.Helper()
	sc := DirsScope(root, []string{dir}, MatchRels(func(r string) bool { return r == rel }))
	files, err := LoadContentFiles(sc, []string{ext})
	require.NoError(t, err, "load %s", rel)
	require.Len(t, files, 1, "expected exactly one file at %s", rel)
	return files[0].Bytes
}

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

// ─── live tests ───────────────────────────────────────────────────────────────

// TestSagaStatusFanoutCoverageC4 enforces the TerminalEventKind ↔ IsTerminal
// exhaustiveness invariant against the live source.
func TestSagaStatusFanoutCoverageC4(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	isTerminal := map[string]bool{}
	tekCases := map[string]bool{}
	var tekRel string
	var tekLine int

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/saga/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			switch p.Pkg.Path() {
			case sfcStatusPkgPath:
				set, _, _ := sfcCollectSwitchStatusCases(p, "IsTerminal", sfcStatusTypeName)
				for k := range set {
					isTerminal[k] = true
				}
			case sfcJournalPkgPath:
				set, rel, line := sfcCollectSwitchStatusCases(p, "TerminalEventKind", "")
				for k := range set {
					tekCases[k] = true
				}
				if rel != "" {
					tekRel, tekLine = rel, line
				}
			}
			return nil
		})

	require.NotEmpty(t, isTerminal, "SAGA-STATUS-FANOUT-COVERAGE-01/C4: Status.IsTerminal() terminal set resolved empty — "+
		"either IsTerminal() was refactored away from a switch (see blind-spot B1) or saga.Status was renamed/moved")
	require.NotEmpty(t, tekCases, "SAGA-STATUS-FANOUT-COVERAGE-01/C4: journal.TerminalEventKind case set resolved empty — "+
		"either TerminalEventKind was refactored away from a switch (see blind-spot B1) or saga.Status was renamed/moved")

	Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C4", sfcDiagsTerminalEventKind(isTerminal, tekCases, tekRel, tekLine))
}

// TestSagaStatusFanoutCoverageC5 enforces that the go/types declared const set
// of saga.Status / journal.EventKind is identical to the value set Render()
// enumerates via `for v := <start>; v.Valid(); v++`. This closes the formerly
// dismissed blind-spot B2: a const added without extending Valid() is invisible
// to Render() and the golden lock, but C5 turns that divergence into a red test —
// making the "saga.Status / journal.EventKind const set is the single source of
// truth" claim strictly true rather than "the Valid()-admitted range is".
func TestSagaStatusFanoutCoverageC5(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	statusDeclared := map[int64]string{}
	kindDeclared := map[int64]string{}
	var statusValidRel, kindValidRel string
	var statusValidLine, kindValidLine int

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/saga/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			switch p.Pkg.Path() {
			case sfcStatusPkgPath:
				statusDeclared, statusValidRel, statusValidLine = sfcCollectDeclaredConsts(p, sfcStatusPkgPath, sfcStatusTypeName)
			case sfcJournalPkgPath:
				kindDeclared, kindValidRel, kindValidLine = sfcCollectDeclaredConsts(p, sfcJournalPkgPath, sfcKindTypeName)
			}
			return nil
		})

	// Floor guards (not vacuous passes): an empty declared set means the type was
	// renamed/moved or the package failed to load; a zero Valid() line means
	// Valid() was renamed or refactored away from a method.
	require.NotEmpty(t, statusDeclared, "SAGA-STATUS-FANOUT-COVERAGE-01/C5: saga.Status declared const set "+
		"resolved empty — type renamed/moved or package load failed")
	require.NotEmpty(t, kindDeclared, "SAGA-STATUS-FANOUT-COVERAGE-01/C5: journal.EventKind declared const set "+
		"resolved empty — type renamed/moved or package load failed")
	require.NotZero(t, statusValidLine, "SAGA-STATUS-FANOUT-COVERAGE-01/C5: saga.Status.Valid() method not found "+
		"— renamed or refactored away")
	require.NotZero(t, kindValidLine, "SAGA-STATUS-FANOUT-COVERAGE-01/C5: journal.EventKind.Valid() method not found "+
		"— renamed or refactored away")

	var diags []Diagnostic
	diags = append(diags,
		sfcDiagsConstSetValidRange("saga.Status", statusDeclared, sfcStatusLoopValues(t), statusValidRel, statusValidLine)...)
	diags = append(diags,
		sfcDiagsConstSetValidRange("journal.EventKind", kindDeclared, sfcKindLoopValues(t), kindValidRel, kindValidLine)...)
	Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/C5", diags)
}

// TestSagaCoverageGolden enforces that the three generated fanout artifacts are
// byte-identical to a fresh Render() of the const set.
func TestSagaCoverageGolden(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping content-load archtest in -short mode")
	}
	root := findModuleRoot(t)

	art, err := sagacoveragegen.Render()
	require.NoError(t, err, "render saga coverage artifacts")

	gen := sfcReadFile(t, root, "kernel/saga/sagajournaltest", sfcGenFileRel, ".go")
	docs := sfcLoadDocs(t, root)
	require.NotEmpty(t, docs[sfcReadyzDocRel], "load %s", sfcReadyzDocRel)
	require.NotEmpty(t, docs[sfcAlertingDocRel], "load %s", sfcAlertingDocRel)

	Report(t, "SAGA-STATUS-FANOUT-COVERAGE-01/GOLDEN",
		sfcGoldenDiags(art, gen, docs[sfcReadyzDocRel], docs[sfcAlertingDocRel]))
}

// ─── RED-fixture + diagnostic-location self-checks ────────────────────────────

// TestSagaStatusFanoutCoverageC4_REDFixture proves the C4 detection path fires on
// either-direction drift between the IsTerminal() set and the TerminalEventKind
// switch case set.
func TestSagaStatusFanoutCoverageC4_REDFixture(t *testing.T) {
	t.Parallel()
	terminal := map[string]bool{"StatusSucceeded": true, "StatusFailed": true}
	assert.Empty(t, sfcDiagsTerminalEventKind(terminal, terminal, sfcJournalPkgPath, 1),
		"matching sets must yield zero C4 diags")

	missingCase := map[string]bool{"StatusSucceeded": true} // StatusFailed absent from TerminalEventKind
	assert.NotEmpty(t, sfcDiagsTerminalEventKind(terminal, missingCase, sfcJournalPkgPath, 1),
		"terminal status missing a TerminalEventKind case must fire C4")

	extraCase := map[string]bool{"StatusSucceeded": true, "StatusFailed": true, "StatusRunning": true}
	assert.NotEmpty(t, sfcDiagsTerminalEventKind(terminal, extraCase, sfcJournalPkgPath, 1),
		"TerminalEventKind case not classified terminal by IsTerminal() must fire C4")
}

// TestSagaStatusFanoutCoverageC5_REDFixture proves the C5 detection path fires on
// either-direction drift between the declared const set and the Valid() loop, and
// is silent when they agree.
func TestSagaStatusFanoutCoverageC5_REDFixture(t *testing.T) {
	t.Parallel()
	declared := map[int64]string{1: "StatusPending", 2: "StatusRunning"}
	loop := map[int64]bool{1: true, 2: true}
	assert.Empty(t, sfcDiagsConstSetValidRange("saga.Status", declared, loop, "kernel/saga/status.go", 42),
		"aligned const set and Valid() loop must yield zero C5 diags")

	// The user-reported gap: a const is added (value 3) but Valid() is not
	// extended, so the `s.Valid()` loop stops at 2 and never sees value 3.
	declaredExtra := map[int64]string{1: "StatusPending", 2: "StatusRunning", 3: "StatusAborted"}
	assert.NotEmpty(t, sfcDiagsConstSetValidRange("saga.Status", declaredExtra, loop, "kernel/saga/status.go", 42),
		"a declared const outside the Valid() loop range must fire C5")

	// The inverse: Valid() admits a value that no declared const carries.
	loopExtra := map[int64]bool{1: true, 2: true, 3: true}
	assert.NotEmpty(t, sfcDiagsConstSetValidRange("saga.Status", declared, loopExtra, "kernel/saga/status.go", 42),
		"a Valid()-admitted value with no declared const must fire C5")
}

// TestSagaCoverageGolden_REDFixture proves the golden detection path fires when
// any of the three artifacts drifts, and is silent when all align.
func TestSagaCoverageGolden_REDFixture(t *testing.T) {
	t.Parallel()
	art, err := sagacoveragegen.Render()
	require.NoError(t, err)

	readyz := []byte(sagacoveragegen.ReadyzTableStartMarker + "\n" + art.ReadyzTable + sagacoveragegen.ReadyzTableEndMarker + "\n")
	alerting := []byte(sagacoveragegen.KindLegendStartMarker + "\n" + art.KindLegend + sagacoveragegen.KindLegendEndMarker + "\n")

	assert.Empty(t, sfcGoldenDiags(art, art.TerminalCoverageGo, readyz, alerting),
		"aligned artifacts must yield zero golden diags")

	assert.NotEmpty(t, sfcGoldenDiags(art, []byte("// drifted\n"), readyz, alerting),
		"a drifted gen file must fire golden")
	driftedReadyz := []byte(sagacoveragegen.ReadyzTableStartMarker + "\n| drift |\n" + sagacoveragegen.ReadyzTableEndMarker + "\n")
	assert.NotEmpty(t, sfcGoldenDiags(art, art.TerminalCoverageGo, driftedReadyz, alerting),
		"a drifted readyz region must fire golden")
	assert.NotEmpty(t, sfcGoldenDiags(art, art.TerminalCoverageGo, []byte("no markers here\n"), alerting),
		"a readyz doc missing its markers must fire golden")
	driftedLegend := []byte(sagacoveragegen.KindLegendStartMarker + "\n drift \n" + sagacoveragegen.KindLegendEndMarker + "\n")
	assert.NotEmpty(t, sfcGoldenDiags(art, art.TerminalCoverageGo, readyz, driftedLegend),
		"a drifted legend region must fire golden")
}

// TestSagaCoverageDiagnosticLocations asserts every emitted diagnostic carries a
// real module-relative Rel and a non-zero Line — no import-path Rel, no :0:
// (the F3 fix; guards against regressing to un-navigable locations).
func TestSagaCoverageDiagnosticLocations(t *testing.T) {
	t.Parallel()
	art, err := sagacoveragegen.Render()
	require.NoError(t, err)

	var all []Diagnostic
	all = append(all, sfcDiagsTerminalEventKind(
		map[string]bool{"StatusSucceeded": true},
		map[string]bool{"StatusFailed": true},
		"kernel/saga/journal/event.go", 135)...)
	all = append(all, sfcGoldenDiags(art, []byte("drift\n"),
		[]byte("no markers"), []byte("no markers"))...)
	all = append(all, sfcDiagsConstSetValidRange("saga.Status",
		map[int64]string{9: "StatusAborted"}, map[int64]bool{1: true},
		"kernel/saga/status.go", 42)...)

	require.NotEmpty(t, all, "self-check must exercise at least one diagnostic")
	for _, d := range all {
		assert.NotZero(t, d.Line, "diagnostic %q must carry a non-zero Line", d.Message)
		assert.False(t, strings.Contains(d.Rel, "github.com/"),
			"diagnostic Rel %q must be a module-relative path, not an import path", d.Rel)
		assert.NotEmpty(t, d.Rel, "diagnostic must carry a Rel")
	}
}
