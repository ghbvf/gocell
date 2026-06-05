// invariants:
//   - INVARIANT: PROJECTION-EVENT-CARRIER-TYPED-01
//
// PROJECTION-EVENT-CARRIER-TYPED-01 — the projection harness's public event
// carrier APIs MUST carry the minimal typed read-only interface
// `cellvocab.ProjectionEvent`, NOT the concrete `outbox.Entry`. This is the
// EPIC #1609 PR-01 deliverable (ADR
// docs/architecture/202606051200-1609-adr-saga-journal-projection-source.md
// §D2/§6): generalizing the carrier so saga-journal events and outbox events
// flow through one typed funnel, with todoorder migrated in the same PR and no
// dual `outbox.Entry`-typed path.
//
// # Two assertions
//
//   - A1 (targeted positive lock): the carrier parameter of the four public
//     carrier symbols resolves EXACTLY to cellvocab.ProjectionEvent —
//     projection.Apply (named func type) / projection.ReplaySource.Replay's fn
//     callback / projection.Cursor.Position / cell.ProjectionApply (the cell-local
//     mirror, an alias). Locking to the exact interface (not merely "not
//     outbox.Entry") prevents a silent swap to a different wrong carrier type.
//   - A2 (broad future-proof ban): no exported FuncDecl / interface method /
//     named func type in scope has any carrier parameter (incl. a one-level
//     func-typed parameter such as Replay's fn) resolving to outbox.Entry. This
//     catches a NEWLY-added projection API that reintroduces the concrete coupling,
//     even one A1 does not enumerate. Scope = kernel/projection + kernel/cellvocab
//     in full, plus kernel/cell gated to Projection*-prefixed symbols
//     (projCarrierSymbolInScope) — so kernel/cell's legitimate outbox.EntryHandler
//     bus/subscription APIs are not false-flagged while a future kernel/cell
//     Projection* carrier on outbox.Entry IS caught.
//
// # AI-robust rating — type-system Hard (API shape, single axis; NOT a funnel)
//
// The PRIMARY defense is the Go type system: the four carrier signatures ARE the
// interface, so a handler/source/cursor that took outbox.Entry would not satisfy
// them (compile error). This archtest is the backstop against drift —
// reintroducing an outbox.Entry-typed projection API. Per ADR §6 this is NOT a
// carrier-source-sealing funnel: ProjectionEvent has all-exported methods and any
// package may implement it; the carrier source is intentionally open (PR-03 saga
// carrier implements it too). Forge protection lives in the wiring layer (the
// Tailer feeding Apply solely from the sealed journal, ADR §5), not here. If the
// carrier source ever needs sealing, add an unexported marker method to
// ProjectionEvent and only then claim an upstream Hard.
//
// # Blind spots (tool: go/types via Run(t, Production/Fixture) + TypesInfo) +
// reverse self-check
//
//   - go/types resolution sees only what compiles; a carrier param typed as a
//     local alias of outbox.Entry is still resolved via types.Unalias (covered).
//   - Alias transparency (intended, not a gap): projection.ProjectionEvent /
//     projection.Apply / cell.ProjectionApply are Go type ALIASES of the
//     cellvocab.* types, so types.Unalias collapses them to one canonical. A1
//     resolves the carrier param to the same canonical regardless of which alias
//     spelling a callsite uses; an outbox.Entry smuggled in via any alias is still
//     caught at A1 / A2.
//   - A2 scope = kernel/projection + kernel/cellvocab (full) + kernel/cell (gated
//     to Projection*-prefixed exported symbols via projCarrierSymbolInScope). The
//     prefix gate on kernel/cell is required because that package legitimately
//     carries outbox.Entry via the NAMED outbox.EntryHandler bus/subscription APIs
//     (Subscribe, SubscriptionRequest, …); scanning only Projection*-named symbols
//     there catches a future kernel/cell Projection* carrier minted on outbox.Entry
//     without false-flagging the bus API. (The named-type detector already excludes
//     EntryHandler, but the prefix gate also protects against a future NON-projection
//     cell API that took a bare outbox.Entry directly.) The scope predicate itself
//     is reverse-self-checked by TestProjectionEventCarrierTyped01_SymbolScope.
//   - typeContainsOutboxEntry recurses ONLY into ANONYMOUS func-typed params
//     (Replay's fn), NOT named func types used as params (outbox.EntryHandler is
//     the legitimate bus-delivery contract). A future named projection-carrier func
//     type on outbox.Entry is caught at its own TypeSpec declaration (direct-param
//     shape), not at its use site. A carrier hidden two func levels deep
//     (func() func(outbox.Entry)) is out of the declared scan depth — no such shape
//     exists in the projection harness; the targeted A1 lock covers the real surface.
//   - Reverse self-check: TestProjectionEventCarrierTyped01_ScannerCatchesViolation
//     loads internal/projcarrierfixture (archtest_fixture build tag — real source
//     AST capture, not a hand-rolled string) and asserts the scanner reports the
//     three planted carrier violations (named func type / interface method /
//     func-typed param) and NOT the two interface-carrier controls.
//
// ref: docs/architecture/202606051200-1609-adr-saga-journal-projection-source.md
// ref: .claude/rules/gocell/eventbus.md §"Projection 载体接口"
package archtest

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Symbol/package paths are derived from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01;
	// bare "github.com/ghbvf/gocell" string literals are banned in archtest files).
	outboxEntryCanonical     = PlatformModulePath + "/kernel/outbox.Entry"
	projectionEventCanonical = PlatformModulePath + "/kernel/cellvocab.ProjectionEvent"

	projectionPkgPath = PlatformModulePath + "/kernel/projection"
	cellvocabPkgPath  = PlatformModulePath + "/kernel/cellvocab"
	// projectionCarrierCellPkgPath is this rule's own kernel/cell path constant
	// (rule self-contained — does not borrow prom_cell_label_funnel_test.go's
	// cellPkgPath, so the carrier archtest carries no implicit dependency on an
	// unrelated rule file's constant).
	projectionCarrierCellPkgPath = PlatformModulePath + "/kernel/cell"
)

// expectedProjCarrierFixtureViolations is the number of
// PROJECTION-EVENT-CARRIER-TYPED-01 carrier violations declared in
// tools/archtest/internal/projcarrierfixture/fixture.go (BadApply /
// BadSource.Replay-fn / BadCursor.Position). Update first when changing the fixture.
const expectedProjCarrierFixtureViolations = 3

// projCarrierBroadScanPkgs is the closed set of carrier-owning packages A2
// scans for bare outbox.Entry carrier params. kernel/projection + kernel/cellvocab
// are scanned in full; kernel/cell is scanned but gated to Projection*-prefixed
// exported symbols (see projCarrierSymbolInScope) so its legitimate
// outbox.EntryHandler bus/subscription APIs (Subscribe, SubscriptionRequest, …)
// are never false-flagged while a future kernel/cell Projection* carrier on
// outbox.Entry is still caught.
var projCarrierBroadScanPkgs = map[string]bool{
	projectionPkgPath:            true,
	cellvocabPkgPath:             true,
	projectionCarrierCellPkgPath: true,
}

// projCarrierSymbolInScope reports whether an exported symbol named symbolName in
// package pkgPath is within the A2 carrier scan scope. kernel/projection and
// kernel/cellvocab are carrier-only packages → every exported symbol is in scope.
// kernel/cell hosts both projection carriers AND the outbox.EntryHandler bus API,
// so only Projection*-prefixed symbols (ProjectionApply / ProjectionResetHook /
// any future Projection* carrier type) are in scope there. Used only in the
// real-repo (restrict) scan; the fixture scan (restrict=false) checks every symbol.
func projCarrierSymbolInScope(pkgPath, symbolName string) bool {
	switch pkgPath {
	case projectionPkgPath, cellvocabPkgPath:
		return true
	case projectionCarrierCellPkgPath:
		return strings.HasPrefix(symbolName, "Projection")
	default:
		return false
	}
}

type projCarrierViolation struct {
	File   string
	Line   int
	Symbol string
}

// canonicalNamed resolves t to its "<pkg-path>.<Name>" canonical, stripping
// pointers and materialized aliases (Go 1.23+). Returns "" for non-named types.
func canonicalNamed(t types.Type) string {
	for {
		ptr, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = ptr.Elem()
	}
	t = types.Unalias(t)
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	if obj.Pkg() == nil {
		return obj.Name()
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

// typeContainsOutboxEntry reports whether a parameter type is a bare outbox.Entry
// carrier. Two shapes count:
//
//  1. the param type IS outbox.Entry directly (Apply's event / Cursor.Position /
//     Append / a named func-type DECLARATION's event param, which A2 walks via the
//     TypeSpec FuncType so its event arrives here as the direct type); and
//  2. the param is an ANONYMOUS func type with an outbox.Entry parameter
//     (ReplaySource.Replay's `fn func(outbox.Entry) error` callback).
//
// It deliberately does NOT recurse into NAMED func types used as parameters —
// `handler outbox.EntryHandler` is the legitimate bus-delivery contract the
// projection Coordinator wires into ConsumerBase, not a projection carrier. A
// future named projection-carrier func type on outbox.Entry is still caught at
// its own TypeSpec declaration (shape 1), not at its use site.
func typeContainsOutboxEntry(t types.Type) bool {
	if canonicalNamed(t) == outboxEntryCanonical {
		return true
	}
	// Only an ANONYMOUS func type (types.Unalias(t) is *types.Signature, not a
	// *types.Named) is a raw carrier callback; named func types (EntryHandler) are
	// excluded by construction.
	sig, ok := types.Unalias(t).(*types.Signature)
	if !ok {
		return false
	}
	for i := 0; i < sig.Params().Len(); i++ {
		if canonicalNamed(sig.Params().At(i).Type()) == outboxEntryCanonical {
			return true
		}
	}
	return false
}

// checkParams records a violation for symbol when any param's resolved type
// contains outbox.Entry in a carrier position.
func checkParams(p *Pass, params *ast.FieldList, symbol, rel string, out *[]projCarrierViolation) {
	if params == nil {
		return
	}
	for _, field := range params.List {
		tv, ok := p.TypesInfo.Types[field.Type]
		if !ok || tv.Type == nil {
			continue
		}
		if typeContainsOutboxEntry(tv.Type) {
			*out = append(*out, projCarrierViolation{
				File:   rel,
				Line:   p.Fset.Position(field.Pos()).Line,
				Symbol: symbol,
			})
		}
	}
}

// scanProjectionCarrierViolations walks exported func decls, named func types,
// and interface methods for outbox.Entry carrier params. When restrict is true
// only packages in projCarrierBroadScanPkgs are scanned (real-repo A2); when
// false every package in the Pass is scanned (fixture detection).
func scanProjectionCarrierViolations(p *Pass, restrict bool) []projCarrierViolation {
	if p.Pkg == nil || p.TypesInfo == nil {
		return nil
	}
	if restrict && !projCarrierBroadScanPkgs[p.Pkg.Path()] {
		return nil
	}
	var out []projCarrierViolation
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, "_gen.go") {
			continue
		}
		EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if !fn.Name.IsExported() || fn.Type.Params == nil {
				return
			}
			if restrict && !projCarrierSymbolInScope(p.Pkg.Path(), fn.Name.Name) {
				return
			}
			checkParams(p, fn.Type.Params, fn.Name.Name, rel, &out)
		})
		EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
			if !ts.Name.IsExported() {
				return
			}
			if restrict && !projCarrierSymbolInScope(p.Pkg.Path(), ts.Name.Name) {
				return
			}
			switch t := ts.Type.(type) {
			case *ast.FuncType:
				checkParams(p, t.Params, ts.Name.Name, rel, &out)
			case *ast.InterfaceType:
				if t.Methods == nil {
					return
				}
				for _, m := range t.Methods.List {
					ft, ok := m.Type.(*ast.FuncType)
					if !ok || len(m.Names) == 0 || !m.Names[0].IsExported() {
						continue
					}
					checkParams(p, ft.Params, ts.Name.Name+"."+m.Names[0].Name, rel, &out)
				}
			}
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// carrierParamCanonicals resolves, for a typed Pass, the canonical type of each
// of the four public carrier symbols' carrier parameter. Keys are stable symbol
// labels; absent symbols (wrong package in this Pass) are simply not added.
func carrierParamCanonicals(p *Pass) map[string]string {
	if p.Pkg == nil {
		return nil
	}
	out := map[string]string{}
	scope := p.Pkg.Scope()
	switch p.Pkg.Path() {
	case projectionPkgPath:
		if c, ok := namedFuncCarrier(scope, "Apply"); ok {
			out["projection.Apply"] = c
		}
		if c, ok := ifaceMethodFnParamCarrier(scope, "ReplaySource", "Replay"); ok {
			out["projection.ReplaySource.Replay.fn"] = c
		}
		if c, ok := ifaceMethodCarrier(scope, "Cursor", "Position"); ok {
			out["projection.Cursor.Position"] = c
		}
	case projectionCarrierCellPkgPath:
		if c, ok := namedFuncCarrier(scope, "ProjectionApply"); ok {
			out["cell.ProjectionApply"] = c
		}
	}
	return out
}

// namedFuncCarrier returns the canonical of the last parameter of a named func
// type (the event carrier in `func(ctx, event) error`).
func namedFuncCarrier(scope *types.Scope, name string) (string, bool) {
	obj := scope.Lookup(name)
	if obj == nil {
		return "", false
	}
	sig, ok := obj.Type().Underlying().(*types.Signature)
	if !ok || sig.Params().Len() == 0 {
		return "", false
	}
	return canonicalNamed(sig.Params().At(sig.Params().Len() - 1).Type()), true
}

// ifaceMethodCarrier returns the canonical of the first parameter of an
// interface method (the carrier in `Position(event) (int64, error)`).
func ifaceMethodCarrier(scope *types.Scope, typeName, method string) (string, bool) {
	sig, ok := projcIfaceMethodSig(scope, typeName, method)
	if !ok || sig.Params().Len() == 0 {
		return "", false
	}
	return canonicalNamed(sig.Params().At(0).Type()), true
}

// ifaceMethodFnParamCarrier returns the canonical of the first parameter of the
// func-typed parameter of an interface method (the carrier in Replay's
// `fn func(event) error`).
func ifaceMethodFnParamCarrier(scope *types.Scope, typeName, method string) (string, bool) {
	sig, ok := projcIfaceMethodSig(scope, typeName, method)
	if !ok {
		return "", false
	}
	for i := 0; i < sig.Params().Len(); i++ {
		if fnSig, ok := sig.Params().At(i).Type().Underlying().(*types.Signature); ok && fnSig.Params().Len() > 0 {
			return canonicalNamed(fnSig.Params().At(0).Type()), true
		}
	}
	return "", false
}

func projcIfaceMethodSig(scope *types.Scope, typeName, method string) (*types.Signature, bool) {
	obj := scope.Lookup(typeName)
	if obj == nil {
		return nil, false
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		return nil, false
	}
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		if m.Name() != method {
			continue
		}
		if sig, ok := m.Type().(*types.Signature); ok {
			return sig, true
		}
	}
	return nil, false
}

// INVARIANT: PROJECTION-EVENT-CARRIER-TYPED-01
//
// A1 — the four public carrier symbols carry cellvocab.ProjectionEvent exactly.
func TestProjectionEventCarrierTyped01_CarriersAreProjectionEvent(t *testing.T) {
	t.Parallel()

	got := map[string]string{}
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		for k, v := range carrierParamCanonicals(p) {
			got[k] = v
		}
		return nil
	})

	wantSymbols := []string{
		"projection.Apply",
		"projection.ReplaySource.Replay.fn",
		"projection.Cursor.Position",
		"cell.ProjectionApply",
	}
	for _, sym := range wantSymbols {
		canon, found := got[sym]
		assert.Truef(t, found, "PROJECTION-EVENT-CARRIER-TYPED-01: carrier symbol %s not resolved", sym)
		assert.Equalf(t, projectionEventCanonical, canon,
			"PROJECTION-EVENT-CARRIER-TYPED-01: %s carrier must be %s, got %s — the projection "+
				"carrier must be the typed ProjectionEvent interface, not the concrete outbox.Entry",
			sym, projectionEventCanonical, canon)
	}
}

// INVARIANT: PROJECTION-EVENT-CARRIER-TYPED-01
//
// A2 — no exported projection/cellvocab carrier API takes outbox.Entry.
func TestProjectionEventCarrierTyped01_NoBareOutboxEntry(t *testing.T) {
	t.Parallel()

	var violations []projCarrierViolation
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		violations = append(violations, scanProjectionCarrierViolations(p, true)...)
		return nil
	})

	for _, v := range violations {
		t.Errorf("PROJECTION-EVENT-CARRIER-TYPED-01: %s:%d %s has an outbox.Entry carrier parameter — "+
			"projection carrier APIs must take cellvocab.ProjectionEvent (see ADR #1609 §D2).",
			v.File, v.Line, v.Symbol)
	}
}

// INVARIANT: PROJECTION-EVENT-CARRIER-TYPED-01
//
// A3 — reverse self-check: the broad scanner catches the fixture's planted
// carrier violations and not its interface-carrier controls.
func TestProjectionEventCarrierTyped01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()

	var violations []projCarrierViolation
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/projcarrierfixture"}),
		func(p *Pass) []Diagnostic {
			violations = append(violations, scanProjectionCarrierViolations(p, false)...)
			return nil
		})

	require.Len(t, violations, expectedProjCarrierFixtureViolations,
		"fixture must yield 3 carrier violations: BadApply (named func type) / "+
			"BadSource.Replay (func-typed param) / BadCursor.Position (interface method)")
	gotSymbols := map[string]bool{}
	for _, v := range violations {
		gotSymbols[v.Symbol] = true
	}
	assert.True(t, gotSymbols["BadApply"], "named-func-type carrier violation must be caught")
	assert.True(t, gotSymbols["BadSource.Replay"], "func-typed-param carrier violation must be caught")
	assert.True(t, gotSymbols["BadCursor.Position"], "interface-method carrier violation must be caught")
	assert.False(t, gotSymbols["GoodApply"], "interface-carrier control must NOT be flagged")
	assert.False(t, gotSymbols["GoodCursor.Position"], "interface-carrier control must NOT be flagged")
}

// INVARIANT: PROJECTION-EVENT-CARRIER-TYPED-01
//
// A2 scope reverse self-check: projCarrierSymbolInScope gates the broad scan to
// carrier-only packages in full and to Projection*-prefixed symbols in kernel/cell
// (so the bus API is not false-flagged, while a future cell Projection* carrier is).
func TestProjectionEventCarrierTyped01_SymbolScope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pkg, sym string
		want     bool
	}{
		{projectionPkgPath, "Apply", true},
		{projectionPkgPath, "AnythingExported", true},
		{cellvocabPkgPath, "ProjectionEvent", true},
		{cellvocabPkgPath, "AnythingExported", true},
		{projectionCarrierCellPkgPath, "ProjectionApply", true},
		{projectionCarrierCellPkgPath, "ProjectionResetHook", true},
		{projectionCarrierCellPkgPath, "ProjectionFutureCarrier", true},
		{projectionCarrierCellPkgPath, "Subscribe", false}, // bus API — out of scope
		{projectionCarrierCellPkgPath, "SubscriptionRequest", false},
		{projectionCarrierCellPkgPath, "RegisterProjection", false},              // not Projection*-prefixed (RegisterP…)
		{PlatformModulePath + "/runtime/bootstrap", "ProjectionAnything", false}, // outside scan set
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, projCarrierSymbolInScope(c.pkg, c.sym),
			"projCarrierSymbolInScope(%q, %q)", c.pkg, c.sym)
	}
}
