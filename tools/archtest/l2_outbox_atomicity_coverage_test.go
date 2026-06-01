// INVARIANT: L2-OUTBOX-ATOMICITY-COVERAGE-01
//
// l2_outbox_atomicity_coverage_test.go — every L2 unit (cell-level or
// slice-level) must have an atomicity test whose name is derived from the
// unit's Service package (typed, via go/types + types.Unalias) and whose
// file carries //go:build integration.
//
// # L2-OUTBOX-ATOMICITY-COVERAGE-01
//
// Invariant: for each L2 unit (a cell or slice whose consistencyLevel==L2),
// a test named TestL2Atomicity_<P>_RollsBack MUST exist as a top-level
// func TestXxx(*testing.T) in some *_test.go file across the module.
// Hybrid L2 units (Service has HandleEvent(ctx context.Context, entry
// outbox.Entry) outbox.HandleResult) additionally require
// TestL2Atomicity_<P>_ReplayIdempotent.
//
// Derivation logic (three-phase):
//
//  1. YAML scan (upstream Hard, YAML-derived): scan cells/*/cell.yaml and
//     cells/*/slices/*/slice.yaml to enumerate L2 units.
//     Floor assert: >= 12 L2 units (fail-closed against broken scan).
//
//  2. Typed derivation (Hard, via go/types): RunTypedProduction loads every
//     production package; for L2 slice packages the Service symbol is
//     resolved via pass.Pkg.Scope().Lookup("Service") + types.Unalias to
//     pierce aliases. Alias-to-appender folding: all 4 auditappend* slices
//     resolve to package "appender" via `type Service = appender.Service`,
//     so the expected set for those 4 collapses to a single pair
//     TestL2Atomicity_appender_{RollsBack,ReplayIdempotent}.
//     Hybrid detection: does the named type's method set include HandleEvent
//     with signature (context.Context, outbox.Entry) outbox.HandleResult?
//
//  3. Downstream Hard — exact-name match: scan ALL *_test.go in the module
//     (ModuleScope + IncludeTests). Collect top-level FuncDecl names.
//     For each expected name: absent → Diagnostic. Found → assert
//     //go:build integration constraint.
//
// Cell-level L2 units (auditcore): no Service symbol to derive from; use
// the cell ID directly: TestL2Atomicity_auditcore_RollsBack.
//
// AI-robust grade:
//   - Upstream (enumeration): Hard — YAML is the canonical SoR for
//     consistencyLevel; scan is self-contained (no metadata.NewParser).
//   - Typed derivation: Hard — types.Unalias + method-set walk; alias
//     folding is correctness-by-construction (not a hand-maintained list).
//   - Downstream (name match): Hard — exact FuncDecl name in AST.
//
// Tools used + blind spots:
//
//   - RunTypedProduction: does not see *_test.go files (Tests:false);
//     guard: all expected test funcs live in test-only files, the
//     downstream AST scan uses DirsScope+IncludeTests.
//   - types.Unalias: Go 1.22+; must be applied before (*types.Named) assert.
//   - Blind spot B1 (Medium): service.go Service type renamed away from
//     "Service", OR Service present but non-Named / nil defining pkg → all
//     such degraded resolutions set serviceFound=false and are caught by
//     TestL2OutboxAtomicityCoverage_ServiceLookupTotal.
//   - Blind spot B2 (Medium): auditappend* de-aliases (removes `type Service
//     = appender.Service`) → TestL2OutboxAtomicityCoverage_AliasFoldsToSinglePackage.
//   - Blind spot B3 (Hard, via TestL2OutboxAtomicityCoverage_DetectsMissingName):
//     matcher fails-open → synthetic non-existent name proves it fails-closed.
//   - Blind spot B4 (Medium, TestL2OutboxAtomicityCoverage_BodyAssertsRollback):
//     "does the body truly assert rollback" is semantic-equivalence (undecidable,
//     Rice's theorem) — no sound low-cost Hard path exists, so this stays Medium
//     by nature (NOT deferred tech debt; no backlog issue). Scans idents AND
//     string-literal messages for rollback-specific vocabulary.
//   - Blind spot B5 (Medium): if typed resolution misses a slice entirely
//     (package load failure), l2BuildExpectedNames falls back to the sliceDir
//     string for the expected name; ServiceLookupTotal surfaces the miss as a
//     visible failure rather than silently degrading.
//
// ref: seed_role_iface_test.go (alias detection form precedent)
// ref: assembly_invariants_test.go (YAML + typed multi-phase structure)
// ref: ai-robust.md "string-typed concept funnel" + "single sanctioned holder"
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const ruleL2AtomicityCoverage = "L2-OUTBOX-ATOMICITY-COVERAGE-01"

// outboxPkgImportPath is the canonical import path of kernel/outbox.
const outboxPkgImportPath = "github.com/ghbvf/gocell/kernel/outbox"

// l2MinUnits is the floor assertion: scan must find at least this many L2
// units. Fail-closed against a broken YAML scan silently zeroing coverage.
const l2MinUnits = 12

// l2UnitKind distinguishes cell-level from slice-level L2 units.
type l2UnitKind int

const (
	l2UnitCell  l2UnitKind = iota // whole cell declared L2
	l2UnitSlice                   // slice declared L2
)

// l2Unit records one L2 unit as discovered from YAML metadata.
type l2Unit struct {
	kind     l2UnitKind
	cellID   string // always set
	sliceDir string // set only for l2UnitSlice
}

// l2SliceInfo records typed resolution for one L2 slice package.
type l2SliceInfo struct {
	defPkgName   string // package name of the defining (Unalias'd) type
	isHybrid     bool   // Service has HandleEvent method matching the L2 hybrid signature
	serviceFound bool   // true ONLY when Service resolved fully (obj→Named→defPkg); false on any fallback
}

// TestL2OutboxAtomicityCoverage is the primary enforcement test.
// It fails (RED) until Wave 2 backfills the missing atomicity tests.
func TestL2OutboxAtomicityCoverage(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Phase 1: enumerate L2 units from YAML.
	units := l2EnumerateUnits(t, root)

	// Phase 2: typed derivation — resolve Service defPkg + hybrid for slices.
	sliceInfos := l2ResolveSliceInfos(t, root, units)

	// Phase 3: build expected-name set.
	expected := l2BuildExpectedNames(units, sliceInfos)

	// Phase 4: collect all *_test.go top-level Test* FuncDecl names + files.
	testFuncs := l2CollectTestFuncs(t, root)

	// Phase 5: match + report.
	var diags []Diagnostic
	for name := range expected {
		info, found := testFuncs[name]
		if !found {
			diags = append(diags, Diagnostic{
				Rel: "cells/",
				Message: "L2 unit missing atomicity test " + name +
					"; add it with //go:build integration",
			})
			continue
		}
		if !info.hasIntegrationTag {
			diags = append(diags, Diagnostic{
				Rel:     info.file,
				Message: name + " found but its file lacks //go:build integration constraint",
			})
		}
	}
	Report(t, ruleL2AtomicityCoverage, diags)
}

// l2TestFuncInfo holds info about a discovered test function.
type l2TestFuncInfo struct {
	file              string // module-relative slash path
	hasIntegrationTag bool
}

// l2EnumerateUnits scans cells/*/cell.yaml and cells/*/slices/*/slice.yaml
// to collect all L2 units. Uses raw YAML only (no metadata.NewParser) for
// self-containment.
func l2EnumerateUnits(t *testing.T, root string) []l2Unit {
	t.Helper()
	var units []l2Unit

	// Scan cell.yaml files: cells/<id>/cell.yaml (depth-2 from cells/).
	cellScope := DirsScope(
		root, []string{"cells"},
		MatchRels(func(rel string) bool {
			rel = filepath.ToSlash(rel)
			return strings.HasPrefix(rel, "cells/") &&
				strings.Count(rel, "/") == depth2SlashCount &&
				filepath.Base(rel) == "cell.yaml"
		}),
	)

	EachContentFile(t, cellScope, []string{".yaml"}, func(_ *testing.T, fc ContentContext) {
		var doc struct {
			ID               string `yaml:"id"`
			ConsistencyLevel string `yaml:"consistencyLevel"`
		}
		require.NoError(t, yaml.Unmarshal(fc.Bytes, &doc),
			"%s: parse %s", ruleL2AtomicityCoverage, fc.Rel)
		if doc.ConsistencyLevel == "L2" {
			units = append(units, l2Unit{kind: l2UnitCell, cellID: doc.ID})
		}
	})

	// Scan slice.yaml files: cells/<cell-id>/slices/<slice-id>/slice.yaml.
	// depth from cells/ is 4: cells/<cell>  /slices/<slice>/slice.yaml = 4 slashes.
	const sliceDepthSlashCount = 4
	sliceScope := DirsScope(
		root, []string{"cells"},
		MatchRels(func(rel string) bool {
			rel = filepath.ToSlash(rel)
			return strings.HasPrefix(rel, "cells/") &&
				strings.Count(rel, "/") == sliceDepthSlashCount &&
				filepath.Base(rel) == "slice.yaml"
		}),
	)

	EachContentFile(t, sliceScope, []string{".yaml"}, func(_ *testing.T, fc ContentContext) {
		var doc struct {
			ID               string `yaml:"id"`
			BelongsToCell    string `yaml:"belongsToCell"`
			ConsistencyLevel string `yaml:"consistencyLevel"`
		}
		require.NoError(t, yaml.Unmarshal(fc.Bytes, &doc),
			"%s: parse %s", ruleL2AtomicityCoverage, fc.Rel)
		if doc.ConsistencyLevel != "L2" {
			return
		}
		// sliceDir is the directory name (e.g. "sessionlogin")
		sliceDir := filepath.Base(filepath.Dir(filepath.FromSlash(fc.Rel)))
		units = append(units, l2Unit{
			kind:     l2UnitSlice,
			cellID:   doc.BelongsToCell,
			sliceDir: sliceDir,
		})
	})

	require.GreaterOrEqual(t, len(units), l2MinUnits,
		"%s: found only %d L2 units (want >= %d); YAML scan may be broken — fail-closed",
		ruleL2AtomicityCoverage, len(units), l2MinUnits)

	return units
}

// l2SliceKey is the composite global identity for an L2 slice. Two cells can
// declare a same-named slice dir (e.g. cells/a/slices/foo and cells/b/slices/foo),
// so the bare basename is NOT a unique key — keying typed infos by it would let
// one cell's slice silently overwrite the other's. cellID/sliceDir is unique
// (mirrors K8s namespace/name identity; bare basename is never a global id).
func l2SliceKey(cellID, sliceDir string) string {
	return cellID + "/" + sliceDir
}

// l2ResolveSliceInfos uses RunTypedProduction to resolve Service type + hybrid
// flag for every L2 slice unit. Returns a map from l2SliceKey(cellID,sliceDir)
// → l2SliceInfo (composite key avoids cross-cell same-name collisions).
func l2ResolveSliceInfos(t *testing.T, root string, units []l2Unit) map[string]l2SliceInfo {
	t.Helper()
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "%s: read module path", ruleL2AtomicityCoverage)

	// wantMap maps each L2 slice's import path → its composite identity key.
	// Each slice lives at cells/<cellID>/slices/<sliceDir>; the import path is
	// globally unique, and we record results under the composite key so two
	// cells with a same-named slice dir cannot collide.
	wantMap := make(map[string]string)
	for _, u := range units {
		if u.kind != l2UnitSlice {
			continue
		}
		importPath := modPath + "/cells/" + u.cellID + "/slices/" + u.sliceDir
		wantMap[importPath] = l2SliceKey(u.cellID, u.sliceDir)
	}

	infos := make(map[string]l2SliceInfo, len(wantMap))

	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		key, ok := wantMap[p.Pkg.Path()]
		if !ok {
			return nil
		}
		infos[key] = l2ResolveServiceInfo(p.Pkg, modPath)
		return nil
	})

	return infos
}

// l2ResolveServiceInfo resolves the Service symbol in pkg and returns its
// defPkgName (after Unalias) and whether it is hybrid (has HandleEvent).
func l2ResolveServiceInfo(pkg *types.Package, modPath string) l2SliceInfo {
	obj := pkg.Scope().Lookup("Service")
	if obj == nil {
		// No Service symbol; fall back to the package's own name.
		return l2SliceInfo{defPkgName: pkg.Name()}
	}

	// Pierce through alias chain to reach the defining Named type.
	raw := types.Unalias(obj.Type())
	named, ok := raw.(*types.Named)
	if !ok {
		return l2SliceInfo{defPkgName: pkg.Name()}
	}
	defPkg := named.Obj().Pkg()
	if defPkg == nil {
		return l2SliceInfo{defPkgName: pkg.Name()}
	}

	isHybrid := l2ServiceIsHybrid(named, modPath)
	return l2SliceInfo{
		defPkgName:   defPkg.Name(),
		isHybrid:     isHybrid,
		serviceFound: true, // full resolution: obj → Named → defPkg all succeeded
	}
}

// l2ServiceIsHybrid reports whether named (a *types.Named from Unalias) has a
// method HandleEvent with signature (context.Context, outbox.Entry) outbox.HandleResult.
func l2ServiceIsHybrid(named *types.Named, modPath string) bool {
	outboxPath := outboxPkgImportPath

	for i := range named.NumMethods() {
		m := named.Method(i)
		if m.Name() != "HandleEvent" {
			continue
		}
		sig, ok := m.Type().(*types.Signature)
		if !ok {
			continue
		}
		params := sig.Params()
		results := sig.Results()
		if params.Len() != 2 || results.Len() != 1 {
			continue
		}
		// param[0]: context.Context
		if !l2IsContextContext(params.At(0).Type()) {
			continue
		}
		// param[1]: outbox.Entry
		if !l2IsNamedFrom(params.At(1).Type(), outboxPath, "Entry") {
			continue
		}
		// result[0]: outbox.HandleResult
		if !l2IsNamedFrom(results.At(0).Type(), outboxPath, "HandleResult") {
			continue
		}
		_ = modPath // kept for future cross-package path checks
		return true
	}
	return false
}

// l2IsContextContext reports whether t is context.Context (interface).
func l2IsContextContext(t types.Type) bool {
	t = types.Unalias(t)
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

// l2IsNamedFrom reports whether t (after Unalias) is a named type from pkgPath
// with name typeName.
func l2IsNamedFrom(t types.Type, pkgPath, typeName string) bool {
	t = types.Unalias(t)
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == pkgPath && obj.Name() == typeName
}

// l2BuildExpectedNames constructs the complete deduplicated expected-name set.
//
// Granularity is per-UNIT: one canonical TestL2Atomicity_<pkg>_RollsBack per L2
// slice (+ _ReplayIdempotent for hybrid), matching issue #876's stated scope.
// A slice's individual outbox-mutation paths (e.g. configwrite Create/Update/
// Delete) are NOT separately required here — their *_RollsBack_<Mutation> tests
// are intentional defense-in-depth, documented in the ADR as outside this gate.
// per-mutation Hard derivation (from RunInTx-emitting Service methods) is tracked
// in backlog #957; do NOT hand-list mutation suffixes here (Medium regression).
func l2BuildExpectedNames(units []l2Unit, sliceInfos map[string]l2SliceInfo) map[string]struct{} {
	expected := make(map[string]struct{})

	for _, u := range units {
		switch u.kind {
		case l2UnitCell:
			// Cell-level: TestL2Atomicity_<cellID>_RollsBack.
			expected["TestL2Atomicity_"+u.cellID+"_RollsBack"] = struct{}{}

		case l2UnitSlice:
			info, ok := sliceInfos[l2SliceKey(u.cellID, u.sliceDir)]
			if !ok {
				// Typed resolution missed this slice; fall back to sliceDir.
				expected["TestL2Atomicity_"+u.sliceDir+"_RollsBack"] = struct{}{}
				continue
			}
			p := info.defPkgName
			expected["TestL2Atomicity_"+p+"_RollsBack"] = struct{}{}
			if info.isHybrid {
				expected["TestL2Atomicity_"+p+"_ReplayIdempotent"] = struct{}{}
			}
		}
	}
	return expected
}

// l2CollectTestFuncs scans every *_test.go file in the module and returns a
// map from Test* function name to l2TestFuncInfo.
func l2CollectTestFuncs(t *testing.T, root string) map[string]l2TestFuncInfo {
	t.Helper()
	scope := ModuleScope(root, IncludeTests())
	result := make(map[string]l2TestFuncInfo)

	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			hasTag := l2FileHasIntegrationTag(p.Abs(f))
			EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
				if fd.Recv != nil {
					return
				}
				if fd.Name == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
					return
				}
				if !l2IsStandardTestFunc(fd.Type) {
					return
				}
				result[fd.Name.Name] = l2TestFuncInfo{
					file:              rel,
					hasIntegrationTag: hasTag,
				}
			})
		}
		return nil
	})

	return result
}

// l2FileHasIntegrationTag reports whether the file at absPath is exclusively
// gated on the integration build tag (builds with -tags=integration on top of
// toolchain defaults, and NOT without it). Delegates to the package-canonical
// fileHasExclusivelyTag so build-constraint evaluation flows through
// BuildContextPredicate (TYPESEVAL-EVAL-PREDICATE-CENTRALIZED-01) and inherits
// the drifting toolchain-default tag set rather than a hand-written predicate.
func l2FileHasIntegrationTag(absPath string) bool {
	if absPath == "" {
		return false
	}
	has, err := fileHasExclusivelyTag(absPath, "integration")
	if err != nil {
		return false
	}
	return has
}

// l2IsStandardTestFunc reports whether ft matches func(*testing.T).
func l2IsStandardTestFunc(ft *ast.FuncType) bool {
	if ft == nil || ft.Params == nil || len(ft.Params.List) != 1 {
		return false
	}
	field := ft.Params.List[0]
	ptr, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := ptr.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "testing" && sel.Sel.Name == "T"
}

// l2MatchName reports whether name exists in testFuncs (for self-checks).
func l2MatchName(name string, testFuncs map[string]l2TestFuncInfo) bool {
	_, found := testFuncs[name]
	return found
}

// ---- Self-check tests ----

// TestL2OutboxAtomicityCoverage_ServiceLookupTotal verifies that every L2 slice
// package successfully resolves a Service symbol via go/types. A refactor that
// removes or renames the Service type would shrink the expected-name set
// silently without this guard.
//
// AI-robust: Medium (archtest-bound; a rename makes it fail visibly).
// Checks info.serviceFound (set true ONLY on the full obj→Named→defPkg path),
// not defPkgName != "" — the fallback returns also set a non-empty pkg.Name(),
// so a "Service present but non-Named / nil defPkg / absent" degradation would
// pass a defPkgName check while silently bypassing the typed-derivation Hard
// guarantee. serviceFound closes that blind spot.
func TestL2OutboxAtomicityCoverage_ServiceLookupTotal(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err)

	units := l2EnumerateUnits(t, root)
	infos := l2ResolveSliceInfos(t, root, units)

	var missing []string
	for _, u := range units {
		if u.kind != l2UnitSlice {
			continue
		}
		info, found := infos[l2SliceKey(u.cellID, u.sliceDir)]
		if !found || !info.serviceFound {
			missing = append(missing, u.cellID+"/"+u.sliceDir)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%s ServiceLookupTotal: %d L2 slice(s) failed to resolve Service; "+
			"check that each slice package exports a type named 'Service'. "+
			"Missing: %v (module: %s)",
			ruleL2AtomicityCoverage, len(missing), missing, modPath)
	}
}

// TestL2OutboxAtomicityCoverage_AliasFoldsToSinglePackage verifies that all 4
// auditappend* slices' Service types Unalias to package "appender". If someone
// de-aliases them, the expected set would expand from 1 to 4 names, which
// would violate the current folded contract.
//
// AI-robust: Medium (validates current alias structure; breaks loudly on change).
func TestL2OutboxAtomicityCoverage_AliasFoldsToSinglePackage(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	units := l2EnumerateUnits(t, root)
	infos := l2ResolveSliceInfos(t, root, units)

	auditappendSlices := []string{
		"auditappendconfig",
		"auditappendrole",
		"auditappendsession",
		"auditappenduser",
	}

	for _, sliceDir := range auditappendSlices {
		// All four auditappend* slices live under the auditcore cell.
		info, found := infos[l2SliceKey("auditcore", sliceDir)]
		if !found {
			t.Errorf("%s AliasFoldsToSinglePackage: slice %s not found in typed infos",
				ruleL2AtomicityCoverage, sliceDir)
			continue
		}
		assert.Equal(t, "appender", info.defPkgName,
			"%s AliasFoldsToSinglePackage: slice %s should Unalias to package 'appender'; "+
				"got %q. If the alias was removed, update the expected folding and open a "+
				"backlog item for the naming impact.",
			ruleL2AtomicityCoverage, sliceDir, info.defPkgName)
		assert.True(t, info.isHybrid,
			"%s AliasFoldsToSinglePackage: slice %s (appender.Service) should be detected "+
				"as hybrid (has HandleEvent); check that HandleEvent signature matches "+
				"(context.Context, outbox.Entry) outbox.HandleResult",
			ruleL2AtomicityCoverage, sliceDir)
	}
}

// TestL2OutboxAtomicityCoverage_DetectsMissingName verifies that the matcher
// correctly reports a missing name (fails-closed, not silently passes).
// Feeds a synthetic name that cannot exist in the real test suite.
//
// AI-robust: Hard — directly exercises l2MatchName with a known-absent name.
func TestL2OutboxAtomicityCoverage_DetectsMissingName(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	testFuncs := l2CollectTestFuncs(t, root)

	const syntheticName = "TestL2Atomicity_zzznonexistent_RollsBack"
	if l2MatchName(syntheticName, testFuncs) {
		t.Errorf("%s DetectsMissingName: matcher reported %q as found; "+
			"that test must not exist in the module — remove it or choose a different synthetic name",
			ruleL2AtomicityCoverage, syntheticName)
	}
}

// TestL2OutboxAtomicityCoverage_BodyAssertsRollback verifies that each matched
// producer/hybrid test FuncDecl body contains at least one assert/require
// selector call AND some rollback-shaped expression (a reference to
// rollback-related identifiers). This is a semantic best-effort guard.
//
// AI-robust: Medium (inherent blind spot — cannot reliably verify test
// semantics from AST; documented as the one Medium gap in this archtest).
// Passes vacuously when no expected names are found yet (RED phase).
func TestL2OutboxAtomicityCoverage_BodyAssertsRollback(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	units := l2EnumerateUnits(t, root)
	sliceInfos := l2ResolveSliceInfos(t, root, units)
	expected := l2BuildExpectedNames(units, sliceInfos)

	// Scan *_test.go files collecting FuncDecl AST nodes for matched names.
	scope := ModuleScope(root, IncludeTests())
	type funcEntry struct {
		decl *ast.FuncDecl
		file *ast.File
		rel  string
	}
	matched := make(map[string]funcEntry)

	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
				if fd.Recv != nil || fd.Name == nil {
					return
				}
				if _, want := expected[fd.Name.Name]; !want {
					return
				}
				if !l2IsStandardTestFunc(fd.Type) {
					return
				}
				matched[fd.Name.Name] = funcEntry{decl: fd, file: f, rel: rel}
			})
		}
		return nil
	})

	// Vacuous pass when nothing is matched yet (RED phase: tests don't exist).
	for name, entry := range matched {
		l2AssertBodyHasAssertion(t, name, entry.decl, entry.rel)
	}
}

// l2AssertBodyHasAssertion checks that a test function body contains at least
// one assert/require call (best-effort semantic check).
func l2AssertBodyHasAssertion(t *testing.T, name string, fd *ast.FuncDecl, rel string) {
	t.Helper()
	if fd.Body == nil {
		return
	}
	_, hasAssertion := FindFirstInSubtree[ast.SelectorExpr](fd.Body, func(sel *ast.SelectorExpr) bool {
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return false
		}
		return pkg.Name == "assert" || pkg.Name == "require"
	})
	// Check for rollback-specific vocabulary in BOTH identifiers and
	// string-literal assertion messages. The rollback intent in these tests
	// lives predominantly in assert/require message strings ("MUST NOT persist",
	// "rolled back") rather than identifiers, so scanning ast.BasicLit is what
	// gives this check signal beyond hasAssertion. Generic tokens (equal/count)
	// are deliberately excluded: nearly every test references assert.Equal or a
	// count helper, so including them would collapse hasRollbackShape into
	// hasAssertion (the trivially-true degenerate the reviewer flagged).
	isRollbackToken := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "rollback") || strings.Contains(s, "roll back") ||
			strings.Contains(s, "persist") || strings.Contains(s, "unchanged") ||
			strings.Contains(s, "notexist") || strings.Contains(s, "notfound") ||
			strings.Contains(s, "must not")
	}
	_, hasRollbackShape := FindFirstInSubtree[ast.Ident](fd.Body, func(id *ast.Ident) bool {
		return isRollbackToken(id.Name)
	})
	if !hasRollbackShape {
		_, hasRollbackShape = FindFirstInSubtree[ast.BasicLit](fd.Body, func(lit *ast.BasicLit) bool {
			return lit.Kind == token.STRING && isRollbackToken(lit.Value)
		})
	}

	if !hasAssertion {
		t.Errorf("%s BodyAssertsRollback: %s:%s has no assert/require call (Medium — "+
			"best-effort semantic check; may need manual inspection)",
			ruleL2AtomicityCoverage, rel, name)
	}
	if !hasRollbackShape {
		t.Errorf("%s BodyAssertsRollback: %s:%s has no rollback-shaped assertion "+
			"(rollback/persist/unchanged/must-not reference in ident or message; "+
			"Medium — best-effort)",
			ruleL2AtomicityCoverage, rel, name)
	}
}

// Build-tag detection is delegated to the package-canonical
// fileHasExclusivelyTag (see l2FileHasIntegrationTag) so constraint evaluation
// flows through BuildContextPredicate; that helper carries its own parser
// self-checks in ci_integration_discovery_invariants_test.go, so no duplicate
// parser/self-check is maintained here.

// ---- Inline fixture for AST-scan correctness ----

// TestL2OutboxAtomicityCoverage_ASTScanCollectsTestFunc verifies that
// l2CollectTestFuncs recognizes a standard Test*(*testing.T) declaration
// and ignores helper functions with different signatures.
func TestL2OutboxAtomicityCoverage_ASTScanCollectsTestFunc(t *testing.T) {
	t.Parallel()

	src := `package foo

import "testing"

func TestFoo(t *testing.T) {}
func TestBar(t *testing.T, extra int) {}
func helperFoo(t *testing.T) {}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err)

	var found []string
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Recv != nil || fd.Name == nil {
			return
		}
		if !strings.HasPrefix(fd.Name.Name, "Test") {
			return
		}
		if !l2IsStandardTestFunc(fd.Type) {
			return
		}
		found = append(found, fd.Name.Name)
	})

	assert.Equal(t, []string{"TestFoo"}, found,
		"AST scan should collect only TestFoo (standard signature)")
}
