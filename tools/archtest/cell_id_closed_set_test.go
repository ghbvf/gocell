package archtest

// invariants:
//   - INVARIANT: CELL-ID-CLOSED-SET-01
//
// CELL-ID-CLOSED-SET-01 is the M12b (#1093) runtime defense-in-depth for the
// metric `cell` label: the per-request cell id must be validated against the
// assembly's closed cell-id set at the metric write point, degrading an absent
// or out-of-set cell to RuntimeCellSentinel instead of polluting the platform
// SLO series.
//
// The downstream "label must be validated" guarantee is type-system Hard: the
// metric collectors take a sealed metrics.CellLabel (unexported field) whose
// sole exported constructor is metrics.ResolveCellLabel, so a raw, unvalidated
// string cannot reach a collector — that is a compile error, no archtest needed.
// This file locks the remaining Medium concerns:
//
//   - SealedConstruction: the only place a CellLabel value is constructed
//     in-package is metrics.ResolveCellLabel (no in-package backdoor literal).
//     Same shape as OUTBOX-ENTRY-SEALED-CONSTRUCTION-01.
//   - FunnelBody: ResolveCellLabel reads ctxkeys.CellIDFrom, performs the
//     `valid[v]` closed-set membership check, and returns the zero CellLabel on
//     a miss — the membership check is load-bearing and cannot be silently
//     dropped (RED-fixture self-checked).
//   - StringSentinel (subsumes the former HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01):
//     CellLabel.String() renders the zero value as RuntimeCellSentinel, so an
//     absent/out-of-set/zero label can never emit an empty or forged cell.
//   - UpstreamSource: bootstrap threads the assembly closed set
//     (s.asm.CellIDs(), not a literal or the singular assembly id) into every
//     router via router.WithCellIDClosedSet, and buildMux passes
//     r.cellIDClosedSet to both the Metrics and BodyLimit middleware.
//
// Relationship to HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01: that rule forbids
// using the assembly's *name* (singular b.assemblyID / assemblyCore.ID()) as a
// cell label *value*. This rule uses the assembly's *cell-id set* (plural
// s.asm.CellIDs()) as a membership *filter*. Value-source vs membership-filter —
// the two are orthogonal and must not be conflated.
//
// AI-robust rating: Medium (AST form checks + RED-fixture self-check of the
// funnel-body collector). The downstream type-system seal is the Hard half;
// these are the Medium upstream/wiring net.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleCellIDClosedSet = "CELL-ID-CLOSED-SET-01"

// findFuncDeclNamed returns the first top-level/method FuncDecl named name, or
// nil. Receiver-agnostic; callers that need a specific receiver filter further.
func findFuncDeclNamed(file *ast.File, name string) *ast.FuncDecl {
	var found *ast.FuncDecl
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if found == nil && fn.Name != nil && fn.Name.Name == name {
			found = fn
		}
	})
	return found
}

// collectResolveCellLabelBodyViolations freezes the membership funnel body by
// CONTROL-FLOW LINKAGE, not mere presence: ResolveCellLabel must (a) read
// ctxkeys.CellIDFrom, and (b) contain an if-stmt whose `valid` closed-set
// membership check GATES a return of the zero CellLabel{} sentinel. Checking
// that `valid[...]` and `CellLabel{}` each appear *somewhere* is insufficient —
// a discarded membership result (`_ = valid[v]`) plus an unrelated `CellLabel{}`
// return would pass a presence check yet still let an out-of-set cell id reach
// the metric. The membership-miss branch must be the one that degrades to the
// sentinel; that linkage is the exact regression this rule exists to stop.
func collectResolveCellLabelBodyViolations(fn *ast.FuncDecl, label string) []string {
	var readsCtxCellID, membershipGatesSentinel bool
	scanner.EachInSubtree[ast.CallExpr](fn.Body, func(v *ast.CallExpr) {
		if isSelectorCall(v, "ctxkeys", "CellIDFrom") {
			readsCtxCellID = true
		}
	})
	// An if-stmt whose Init/Cond indexes `valid` AND whose body returns the zero
	// CellLabel{} — i.e. the membership check controls the sentinel return.
	scanner.EachInSubtree[ast.IfStmt](fn.Body, func(ifs *ast.IfStmt) {
		if membershipGatesSentinel {
			return
		}
		indexesValid := false
		for _, n := range []ast.Node{ifs.Init, ifs.Cond} {
			if n == nil {
				continue
			}
			scanner.EachInSubtree[ast.IndexExpr](n, func(ix *ast.IndexExpr) {
				if id, ok := ix.X.(*ast.Ident); ok && id.Name == "valid" {
					indexesValid = true
				}
			})
		}
		if !indexesValid {
			return
		}
		scanner.EachInSubtree[ast.CompositeLit](ifs.Body, func(cl *ast.CompositeLit) {
			if id, ok := cl.Type.(*ast.Ident); ok && id.Name == "CellLabel" && len(cl.Elts) == 0 {
				membershipGatesSentinel = true
			}
		})
	})

	var viol []string
	if !readsCtxCellID {
		viol = append(viol, label+": ResolveCellLabel must read ctxkeys.CellIDFrom")
	}
	if !membershipGatesSentinel {
		viol = append(viol, label+": ResolveCellLabel's `valid` closed-set membership check must GATE the zero "+
			"CellLabel{} sentinel return — an if-stmt that indexes `valid` and whose body returns CellLabel{}. A "+
			"membership check whose result does not control the sentinel return would let an out-of-set cell id "+
			"reach the metric (it renders as the sentinel via String()).")
	}
	return viol
}

// TestCellIDClosedSet01_FunnelBody locks the membership funnel body shape.
func TestCellIDClosedSet01_FunnelBody(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "observability", "metrics", "cell_label.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	fn := findFuncDeclNamed(file, "ResolveCellLabel")
	require.NotNilf(t, fn, "%s: ResolveCellLabel func not found", rel)
	for _, v := range collectResolveCellLabelBodyViolations(fn, rel) {
		t.Errorf("%s: %s", ruleCellIDClosedSet, v)
	}
}

// TestCellIDClosedSet01_FunnelBody_DetectorFixtures is the RED-fixture
// self-check for the funnel-body collector: a regression that stops detecting a
// dropped membership check / ctx read / sentinel return fails here.
func TestCellIDClosedSet01_FunnelBody_DetectorFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		src            string
		wantViolations bool
	}{
		{
			name: "green_canonical",
			src: `package fixture
func ResolveCellLabel(ctx context.Context, valid map[string]struct{}) CellLabel {
	v, ok := ctxkeys.CellIDFrom(ctx)
	if !ok || v == "" { return CellLabel{} }
	if _, member := valid[v]; !member { return CellLabel{} }
	return CellLabel{v: v}
}`,
		},
		{
			name: "red_membership_check_dropped",
			src: `package fixture
func ResolveCellLabel(ctx context.Context, valid map[string]struct{}) CellLabel {
	v, ok := ctxkeys.CellIDFrom(ctx)
	if !ok || v == "" { return CellLabel{} }
	return CellLabel{v: v}
}`,
			wantViolations: true,
		},
		{
			name: "red_ctx_read_dropped",
			src: `package fixture
func ResolveCellLabel(ctx context.Context, valid map[string]struct{}) CellLabel {
	if _, member := valid["x"]; !member { return CellLabel{} }
	return CellLabel{v: "x"}
}`,
			wantViolations: true,
		},
		{
			name: "red_sentinel_return_dropped",
			src: `package fixture
func ResolveCellLabel(ctx context.Context, valid map[string]struct{}) CellLabel {
	v, _ := ctxkeys.CellIDFrom(ctx)
	if _, member := valid[v]; member { return CellLabel{v: v} }
	return CellLabel{v: v}
}`,
			wantViolations: true,
		},
		{
			// Keeps the membership check and ctx read — isolates the returnsEmptyLit
			// branch: the sentinel return is replaced with a non-zero literal, so
			// the membership-miss path does not degrade to the zero CellLabel.
			name: "red_only_sentinel_drop",
			src: `package fixture
func ResolveCellLabel(ctx context.Context, valid map[string]struct{}) CellLabel {
	v, ok := ctxkeys.CellIDFrom(ctx)
	if !ok || v == "" { return CellLabel{v: "fallback"} }
	if _, member := valid[v]; !member { return CellLabel{v: "fallback"} }
	return CellLabel{v: v}
}`,
			wantViolations: true,
		},
		{
			// The presence-vs-linkage gap (F5): membership IS indexed and the zero
			// CellLabel{} IS returned, but the membership result is DISCARDED and the
			// sentinel return is gated on the ctx check, not membership — so an
			// out-of-set v still reaches the metric. A presence-only check passes
			// this; the linkage check must reject it.
			name: "red_membership_not_gating_sentinel",
			src: `package fixture
func ResolveCellLabel(ctx context.Context, valid map[string]struct{}) CellLabel {
	v, ok := ctxkeys.CellIDFrom(ctx)
	_ = valid[v]
	if !ok || v == "" { return CellLabel{} }
	return CellLabel{v: v}
}`,
			wantViolations: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err)
			fn := findFuncDeclNamed(f, "ResolveCellLabel")
			require.NotNil(t, fn)
			viol := collectResolveCellLabelBodyViolations(fn, "fixture.go")
			if got := len(viol) > 0; got != tc.wantViolations {
				t.Errorf("violations non-empty = %v (%v), want %v", got, viol, tc.wantViolations)
			}
		})
	}
}

// TestCellIDClosedSet01_SealedConstruction asserts CellLabel composite literals
// appear ONLY inside metrics.ResolveCellLabel — no in-package backdoor that
// would forge an unvalidated label (the seal's downstream type guarantee only
// blocks OTHER packages; this closes the in-package gap).
func TestCellIDClosedSet01_SealedConstruction(t *testing.T) {
	root := findModuleRoot(t)
	dir := filepath.Join(root, "runtime", "observability", "metrics")
	// findProductionGoFilesInDir wraps scanner.DirsScope(...).Files() (the
	// scanner framework, SCANNER-FRAMEWORK-USAGE-01) and excludes _test.go.
	files, err := findProductionGoFilesInDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	for _, path := range files {
		rel := slashRel(t, root, path)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoErrorf(t, err, "%s: parse failed", rel)

		isCellLabelFile := filepath.Base(path) == "cell_label.go"
		resolveFn := findFuncDeclNamed(file, "ResolveCellLabel")

		scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
			id, ok := cl.Type.(*ast.Ident)
			if !ok || id.Name != "CellLabel" {
				return
			}
			if !isCellLabelFile {
				t.Errorf("%s: %s — CellLabel composite literal outside cell_label.go (in-package backdoor "+
					"construction); the only sanctioned constructor is metrics.ResolveCellLabel", rel, ruleCellIDClosedSet)
				return
			}
			if resolveFn == nil || cl.Pos() < resolveFn.Pos() || cl.End() > resolveFn.End() {
				t.Errorf("%s: %s — CellLabel composite literal outside ResolveCellLabel; the funnel is the "+
					"sole constructor (an in-package backdoor would forge an unvalidated label)", rel, ruleCellIDClosedSet)
			}
		})
	}
}

// TestCellIDClosedSet01_StringSentinel subsumes the former
// HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01: CellLabel.String() must render the
// zero value as RuntimeCellSentinel, so an absent/out-of-set/zero label degrades
// to "_runtime" rather than emitting an empty or forged cell.
func TestCellIDClosedSet01_StringSentinel(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "observability", "metrics", "cell_label.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	var stringFn *ast.FuncDecl
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Name.Name != "String" {
			return
		}
		if base, ok := receiverBaseName(fn); ok && base == "CellLabel" {
			stringFn = fn
		}
	})
	require.NotNilf(t, stringFn, "%s: CellLabel.String() method not found", rel)

	var refsSentinel bool
	scanner.EachInSubtree[ast.Ident](stringFn.Body, func(id *ast.Ident) {
		if id.Name == "RuntimeCellSentinel" {
			refsSentinel = true
		}
	})
	assert.Truef(t, refsSentinel,
		"%s: %s — CellLabel.String() must return RuntimeCellSentinel for the zero value "+
			"(relocated %s)", rel, ruleCellIDClosedSet, ruleHTTPMetricsLabelRuntimeSentinel)
}

// TestCellIDClosedSet01_UpstreamSource locks the wiring: bootstrap threads the
// assembly closed set (s.asm.CellIDs(), not a literal / singular id) into every
// router via WithCellIDClosedSet, and buildMux passes r.cellIDClosedSet to both
// the Metrics and BodyLimit middleware.
//
// NOTE: gRPC has no closed-set wiring yet (ResolveCellLabel(ctx, nil) in UnaryMetrics);
// when gRPC attribution lands (#1383) this test must gain a gRPC interceptor wiring assertion.
func TestCellIDClosedSet01_UpstreamSource(t *testing.T) {
	root := findModuleRoot(t)

	// --- bootstrap source lock ---
	bsPath := filepath.Join(root, "runtime", "bootstrap", "phases_http.go")
	bsRel := slashRel(t, root, bsPath)
	fset := token.NewFileSet()
	bsFile, err := parser.ParseFile(fset, bsPath, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", bsRel)

	bsFn := findFuncDeclNamed(bsFile, "buildListenerRouterOpts")
	require.NotNilf(t, bsFn, "%s: buildListenerRouterOpts not found", bsRel)

	var wiresFromCellIDs, wiresFromBadSource bool
	scanner.EachInSubtree[ast.CallExpr](bsFn.Body, func(v *ast.CallExpr) {
		if !isSelectorCall(v, "router", "WithCellIDClosedSet") {
			return
		}
		if len(v.Args) == 1 {
			if call, ok := v.Args[0].(*ast.CallExpr); ok && selectorQualifier(call.Fun) == "s.asm.CellIDs" {
				wiresFromCellIDs = true
				return
			}
		}
		wiresFromBadSource = true
	})
	assert.Truef(t, wiresFromCellIDs,
		"%s: %s — buildListenerRouterOpts must wire router.WithCellIDClosedSet(s.asm.CellIDs())",
		bsRel, ruleCellIDClosedSet)
	assert.Falsef(t, wiresFromBadSource,
		"%s: %s — WithCellIDClosedSet arg must be s.asm.CellIDs() (the closed cell-id SET), not a literal "+
			"or the singular assembly id",
		bsRel, ruleCellIDClosedSet)

	// --- router buildMux pass-through lock ---
	rtPath := filepath.Join(root, "runtime", "http", "router", "router.go")
	rtRel := slashRel(t, root, rtPath)
	rtFile, err := parser.ParseFile(fset, rtPath, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rtRel)

	buildMux := findFuncDeclNamed(rtFile, "buildMux")
	require.NotNilf(t, buildMux, "%s: buildMux not found", rtRel)

	var metricsHasSet, bodyLimitHasSet bool
	scanner.EachInSubtree[ast.CallExpr](buildMux.Body, func(v *ast.CallExpr) {
		switch {
		case isSelectorCall(v, "middleware", "Metrics") && callHasClosedSetArg(v):
			metricsHasSet = true
		case isSelectorCall(v, "middleware", "BodyLimit") && callHasClosedSetArg(v):
			bodyLimitHasSet = true
		}
	})
	assert.Truef(t, metricsHasSet,
		"%s: %s — buildMux must pass r.cellIDClosedSet to middleware.Metrics", rtRel, ruleCellIDClosedSet)
	assert.Truef(t, bodyLimitHasSet,
		"%s: %s — buildMux must pass r.cellIDClosedSet to middleware.BodyLimit", rtRel, ruleCellIDClosedSet)
}

// callHasClosedSetArg reports whether any argument of call is the selector
// expression r.cellIDClosedSet.
func callHasClosedSetArg(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if selectorQualifier(arg) == "r.cellIDClosedSet" {
			return true
		}
	}
	return false
}
