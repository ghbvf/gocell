package archtest

// INVARIANT: TEST-EVENTUALLY-FUNNEL-01
//
// test_eventually_funnel_test.go — Hard upstream funnel for
// pkg/testutil/testwait.External, paired with the Hard downstream
// TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
//
//   - Banned surface: every testify func/method whose Pkg().Path() ∈
//     {require, assert} AND whose Name() starts with "Eventually". As of
//     testify v1.11.1 this expands to 8 package-level funcs and 8
//     *Assertions methods (Eventually / EventuallyWithT / Eventuallyf /
//     EventuallyWithTf in each package, both as top-level funcs and as
//     *Assertions methods). Prefix matching auto-covers any future
//     EventuallyXyz variant; baseline drift is locked by
//     TestEventuallyFunnel_SymbolSentinel.
//   - Banned call shapes: qualified-ident form (`require.Eventually(...)`)
//     and method-selector form (`require.New(t).Eventually(...)`) are both
//     direct calls and rejected by the main rule. The callee resolver
//     consults both *types.Info.Uses (qualified) and *types.Info.Selections
//     (method) via resolveSelectorCalleeFunc.
//   - Banned reference shapes: indirect references (var assignment,
//     function-pointer pass-through, reflect.ValueOf, struct-field binding,
//     method value `require.New(t).Eventually`, method expression
//     `(*require.Assertions).EventuallyWithT`) are caught by the reverse
//     blind-spot self-test (TestEventuallyFunnel_NoIndirectReferences),
//     which walks both info.Uses and info.Selections. Inside the reverse
//     self-test, direct-call Sel idents (collected by
//     collectDirectEventuallyCallIdents) are excluded so the two rules
//     don't double-report the same line — direct calls are not allowed,
//     just attributed to the main rule for diagnostic clarity.
//
// Callers MUST go through testwait.External (synchronous wall-clock
// polling with const-literal reason) or testwait.Deterministic
// (channel-blocking wait — preferred).
//
// Together with TEST-POLLING-EXTERNAL-REASON-LITERAL-01 this closes the
// testwait funnel as a Hard 范本: "typed marker funnel for unbounded ops"
// per .claude/rules/gocell/ai-robust.md §"Hard 范本目录" — sibling of
// panicregister.Approved. Note: per the parent 范本 entry, enforcement is
// archtest-bound (not Go compile-time); the (callee, arg) form-uniqueness
// is the highest tier reachable for this rule shape in Go, but a future
// removal of require/assert.Eventually from testify would make this
// archtest a redundant guard rather than a load-bearing lock.
// See pkg/testutil/testwait/testwait.go godoc and
// docs/plans/202605181600-042-archtest.md §1.1 PR3.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const ruleTestEventuallyFunnel01 = "TEST-EVENTUALLY-FUNNEL-01"

// Banned testify symbol identities. Resolution is via *types.Info.Uses
// (qualified-ident: `require.Eventually(...)`) OR *types.Info.Selections
// (method-selector: `require.New(t).Eventually(...)`), both producing a
// *types.Func whose Pkg().Path() ∈ bannedEventuallyPkgPaths and whose
// Name() satisfies isBannedEventuallyFuncName. Import aliases (e.g.
// `import req "…/require"; req.Eventually(...)`) are handled correctly by
// both resolvers. Pure-AST fallback (fixture mode without type info)
// matches package-ident "require" / "assert" — fixture files always use
// the canonical names.
//
// The banned func-name set is identified by the `Eventually` prefix rather
// than an enumerated allowlist: testify v1.11.1 ships exactly four such
// names (`Eventually`, `EventuallyWithT`, `Eventuallyf`, `EventuallyWithTf`),
// all polling/wait variants of the same semantics. Prefix matching closes
// the "AI co-author adds a new const to the enumeration" bypass (an
// ai-robust §"input-struct field exclusion" anti-pattern) and auto-covers
// any future `EventuallyXyz` testify upstream may add. Drift in the
// testify surface that would invalidate this assumption is caught by
// TestEventuallyFunnel_SymbolSentinel.
const (
	testifyRequirePkgPath = "github.com/stretchr/testify/require"
	testifyAssertPkgPath  = "github.com/stretchr/testify/assert"

	testifyFuncEventuallyPrefix = "Eventually"
)

// bannedEventuallyPkgPaths is the closed set of testify package paths whose
// `Eventually*` functions / methods are banned. Closed by enumeration:
// require + assert are the only two packages testify exposes Eventually
// variants from.
var bannedEventuallyPkgPaths = map[string]struct{}{
	testifyRequirePkgPath: {},
	testifyAssertPkgPath:  {},
}

// bannedEventuallySymbol is the (pkg path, func name) tuple identifying a
// matched banned symbol. Named (not anonymous) so it appears identically
// in the matcher return type and the indirect scanner — repeating the
// field set inline at 6+ sites obscured the equivalence relation.
type bannedEventuallySymbol struct {
	PkgPath string
	Name    string
}

type eventuallyFunnelViolation struct {
	File   string
	Line   int
	Reason string
}

// scanFileForEventuallyFunnelViolations walks one AST file and returns
// violations of TEST-EVENTUALLY-FUNNEL-01 (direct bare callsites of any
// banned symbol). info must be the *types.Info bound to the same
// packages.Load that produced the file. info == nil falls back to pure-AST
// selector-name matching (used only in fixture mode).
//
// Signature mirrors the sibling scanFileForTestwaitExternalViolations: takes
// *token.FileSet rather than *Pass so the function dependency is limited to
// what it actually reads.
func scanFileForEventuallyFunnelViolations(
	fset *token.FileSet,
	file *ast.File,
	info *types.Info,
	rel string,
) []eventuallyFunnelViolation {
	var violations []eventuallyFunnelViolation

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sym, ok := bannedEventuallyCallee(call.Fun, info)
		if !ok {
			return
		}
		violations = append(violations, eventuallyFunnelViolation{
			File: rel,
			Line: fset.Position(call.Pos()).Line,
			Reason: fmt.Sprintf(
				"bare %s.%s call — use testwait.External(t, reason, ...) for "+
					"synchronous polling or testwait.Deterministic(t, ch, ...) for "+
					"channel waits (see github.com/ghbvf/gocell/pkg/testutil/testwait "+
					"or run: go doc ./pkg/testutil/testwait)",
				lastPathSegment(sym.PkgPath), sym.Name,
			),
		})
	})

	return violations
}

// bannedEventuallyCallee reports whether funExpr is a call to one of the
// banned testify Eventually symbols (qualified-ident OR method-selector on
// `*Assertions`). Returns the matched symbol identity on hit.
//
// When info is non-nil, resolution is via resolveSelectorCalleeFunc, which checks
// *types.Info.Uses (qualified ident: `require.Eventually(...)`) then
// *types.Info.Selections (method selector: `require.New(t).Eventually(...)`).
// Both paths surface the underlying *types.Func, so import aliases and
// method-selector forms are handled uniformly.
//
// When info is nil (fixture mode without type resolution), falls back to
// pure-AST matching of `pkgIdent.Sel` where pkgIdent.Name is "require" or
// "assert" — fixture files always import testify under its canonical name.
func bannedEventuallyCallee(funExpr ast.Expr, info *types.Info) (bannedEventuallySymbol, bool) {
	var zero bannedEventuallySymbol
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return zero, false
	}
	if !isBannedEventuallyFuncName(sel.Sel.Name) {
		return zero, false
	}
	if info != nil {
		fn := resolveSelectorCalleeFunc(sel, info)
		if fn == nil {
			return zero, false
		}
		return bannedEventuallyFuncObj(fn)
	}
	// Pure-AST fallback (fixture mode): match `<pkg>.<Eventually*>` where
	// pkg ident is "require" or "assert".
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return zero, false
	}
	switch xIdent.Name {
	case "require":
		return bannedEventuallySymbol{PkgPath: testifyRequirePkgPath, Name: sel.Sel.Name}, true
	case "assert":
		return bannedEventuallySymbol{PkgPath: testifyAssertPkgPath, Name: sel.Sel.Name}, true
	}
	return zero, false
}

// resolveSelectorCalleeFunc returns the *types.Func denoted by sel, consulting both
// *types.Info.Uses (qualified-ident: pkg.Func) and *types.Info.Selections
// (method-selector: receiver.Method). Exactly one is populated for any
// given selector in well-typed source; nil result means sel is not a
// function/method reference.
func resolveSelectorCalleeFunc(sel *ast.SelectorExpr, info *types.Info) *types.Func {
	if obj := info.Uses[sel.Sel]; obj != nil {
		if fn, _ := obj.(*types.Func); fn != nil {
			return fn
		}
	}
	if selObj := info.Selections[sel]; selObj != nil {
		if fn, _ := selObj.Obj().(*types.Func); fn != nil {
			return fn
		}
	}
	return nil
}

func isBannedEventuallyFuncName(name string) bool {
	return strings.HasPrefix(name, testifyFuncEventuallyPrefix)
}

// shouldSkipForEventuallyFunnel returns true for paths excluded from the
// scan. Mirrors shouldSkipForTestwaitExternal: generated code, vendored
// libraries, archtest's own fixture testdata, and worktree/.git/node_modules
// out-of-tree directories. No carve-out for pkg/testutil/testwait/ —
// testwait.go uses time.NewTimer/Ticker directly and never calls any
// testify Eventually symbol; testwait_test.go tests testwait's own API and
// does not call Eventually either.
func shouldSkipForEventuallyFunnel(rel string) bool {
	switch {
	case strings.HasPrefix(rel, "vendor/"):
		return true
	case strings.HasPrefix(rel, "generated/"):
		return true
	case strings.HasPrefix(rel, "tools/archtest/testdata/"):
		return true
	case strings.HasPrefix(rel, "worktrees/"):
		return true
	case strings.HasPrefix(rel, ".git/"):
		return true
	case strings.HasPrefix(rel, "node_modules/"):
		return true
	case strings.Contains(rel, "/testdata/") || strings.HasPrefix(rel, "testdata/"):
		return true
	}
	return false
}

// TestEventuallyFunnel enforces TEST-EVENTUALLY-FUNNEL-01 module-wide.
// Scans both production and test sources (Tests: true) because testify's
// require/assert Eventually variants are test-only APIs whose callers live
// in *_test.go.
//
// Tool: archtest.RunTyped + resolveSelectorCalleeFunc (info.Uses ∪ info.Selections)
// + go/ast SelectorExpr matching. This is the typed-marker funnel upstream
// lock; the downstream lock (testwait.External callee+arg form-uniqueness)
// is TEST-POLLING-EXTERNAL-REASON-LITERAL-01.
//
// Blind spots of the chosen tool (per AI-robust §"工具选定后强制盲区自检"):
//
//   - Indirect call via function variable: var f = require.Eventually; f(t, cond, ...).
//   - Function-pointer pass-through: helper(require.Eventually).
//   - Reflect call: reflect.ValueOf(require.Eventually).Call(...).
//   - Struct-field binding: wrapper{F: require.Eventually}.
//   - Method value/expression of *Assertions: var f = require.New(t).Eventually
//     or var f = (*require.Assertions).EventuallyWithT (MethodVal /
//     MethodExpr selections, not CallExpr.Fun).
//
// All five shapes silently bypass the CallExpr.Fun-driven main scan.
// TestEventuallyFunnel_NoIndirectReferences below is the reverse self-test:
// it walks both *types.Info.Uses and *types.Info.Selections for every
// reference to a banned symbol and asserts none appear outside
// *ast.CallExpr.Fun position — and even then, since direct call IS the
// violation, the union of the main rule + reverse self-test forms the
// (callee) form-uniqueness Hard lock: outside testwait.External /
// testwait.Deterministic no syntactic shape can reach the testify
// (require|assert).Eventually* surface.
func TestEventuallyFunnel(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var violations []eventuallyFunnelViolation

	scan := func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if shouldSkipForEventuallyFunnel(rel) {
				continue
			}
			for _, v := range scanFileForEventuallyFunnelViolations(p.Fset, file, p.TypesInfo, rel) {
				key := fmt.Sprintf("%s:%d:%s", v.File, v.Line, v.Reason)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				violations = append(violations, v)
			}
		}
		return nil
	}

	// Two-load coverage (mirror PANIC-REGISTERED-01 / TEST-POLLING-EXTERNAL-REASON-LITERAL-01):
	// Load 1 (tags=nil) catches reverse build directives; Load 2 (ProductionFlatTags) catches all
	// forward-tagged files in one union. Tests:true in both so *_test.go callers are scanned.
	_ = RunTyped(t, TypedOpts{Tests: true}, []string{"./..."}, scan)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: ProductionFlatTags()}, []string{"./..."}, scan)

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleTestEventuallyFunnel01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s: bare (require|assert).Eventually / *WithT is banned; route polling through "+
			"pkg/testutil/testwait.External (const-literal reason) or testwait.Deterministic "+
			"(channel wait). See docs/plans/202605181600-042-archtest.md §1.1 PR3. "+
			"Run: go test -count=1 ./tools/archtest/... -run TestEventuallyFunnel$ for local repro.",
		ruleTestEventuallyFunnel01)
}

// TestEventuallyFunnelFixtures verifies the rule logic against static
// fixture packages under tools/archtest/testdata/eventually_funnel_fixtures/.
// Each fixture dir owns a diag.golden capturing the rule's real output.
//
// To regenerate golden files: go test ./tools/archtest/... -run TestEventuallyFunnelFixtures$ -update.
func TestEventuallyFunnelFixtures(t *testing.T) {
	t.Parallel()

	dirs := []string{
		// GREEN — expect 0 violations (empty golden).
		"positive_green",
		// RED cases — one direct callsite each.
		"require_eventually_red",
		"require_eventually_collect_red",
		"assert_eventually_red",
		"assert_eventually_collect_red",
		// RED cases — *f formatted variants (prefix predicate coverage).
		"qualified_f_variants_red",
		// RED cases — *Assertions method-selector calls (Selections path).
		"assertions_method_call_red",
	}

	root := findModuleRoot(t)
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/eventually_funnel_fixtures/" + dir

			diags := RunTypedFixture(t, FixtureOpts{}, []string{fixturePattern},
				func(p *Pass) []Diagnostic {
					if p.TypesInfo == nil || p.Fset == nil {
						return nil
					}
					var out []Diagnostic
					for _, file := range p.Files {
						rel := p.Rel(file)
						for _, v := range scanFileForEventuallyFunnelViolations(p.Fset, file, p.TypesInfo, rel) {
							out = append(out, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
						}
					}
					return out
				})

			goldenPath := filepath.Join(root, "tools", "archtest", "testdata",
				"eventually_funnel_fixtures", dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}

// TestEventuallyFunnel_NoIndirectReferences is the blind-spot reverse
// self-test required by AI-robust §"工具选定后强制盲区自检".
//
// It walks every reference to a banned symbol via both *types.Info.Uses
// (qualified-ident references like `var f = require.Eventually`) and
// *types.Info.Selections (method-selector references like
// `var f = require.New(t).Eventually` or
// `(*require.Assertions).EventuallyWithT`), then rejects ALL such references
// (since direct call is itself banned, indirect references are also
// banned). Any non-CallExpr.Fun reference would prove the symbol is
// "reachable" from code outside testwait — the rule's intent is that the
// surface (require|assert).Eventually* is unreachable except through the
// testwait funnel.
func TestEventuallyFunnel_NoIndirectReferences(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var violations []eventuallyFunnelViolation

	scan := func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if shouldSkipForEventuallyFunnel(rel) {
				continue
			}
			for _, v := range scanFileForIndirectEventuallyReferences(p, file, rel) {
				key := fmt.Sprintf("%s:%d:%s", v.File, v.Line, v.Reason)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				violations = append(violations, v)
			}
		}
		return nil
	}

	_ = RunTyped(t, TypedOpts{Tests: true}, []string{"./..."}, scan)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: ProductionFlatTags()}, []string{"./..."}, scan)

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})

	if len(violations) > 0 {
		t.Logf("%s blind-spot self-test: %d indirect reference(s):",
			ruleTestEventuallyFunnel01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s blind-spot reverse self-test: (require|assert).Eventually / *WithT must not be "+
			"reachable via indirect references (function value, pointer pass-through, "+
			"reflect, struct field); route through testwait.External / testwait.Deterministic. "+
			"Run: go test -count=1 ./tools/archtest/... -run TestEventuallyFunnel_NoIndirectReferences$ for local repro.",
		ruleTestEventuallyFunnel01)
}

// TestEventuallyFunnel_NoIndirectReferences_Fixtures proves the indirect
// reference scanner is wired correctly by loading each of the four RED
// fixture packages and asserting the diagnostics match the expected golden
// output. Without these RED fixtures, TestEventuallyFunnel_NoIndirectReferences
// would pass even if the scanner logic regressed (current tree has no
// indirect references, so the module-wide test is always green regardless
// of scanner health).
//
// Fixture categories:
//   - indirect_var_red:          var f = require.Eventually
//   - indirect_funcarg_red:      helper(require.Eventually)
//   - indirect_reflect_red:      reflect.ValueOf(require.Eventually)
//   - indirect_struct_field_red: wrapper{F: require.Eventually}
//   - indirect_var_f_red:        var f = require.Eventuallyf
//     (covers prefix predicate on Uses path)
//   - indirect_method_red:       var f = require.New(t).Eventually +
//     var f = (*assert.Assertions).EventuallyWithT
//     (covers MethodVal + MethodExpr on Selections path)
func TestEventuallyFunnel_NoIndirectReferences_Fixtures(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	fixtures := []string{
		"indirect_var_red",
		"indirect_funcarg_red",
		"indirect_reflect_red",
		"indirect_struct_field_red",
		"indirect_var_f_red",
		"indirect_method_red",
	}

	for _, dir := range fixtures {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/eventually_funnel_fixtures/" + dir

			diags := RunTypedFixture(t, FixtureOpts{}, []string{fixturePattern},
				func(p *Pass) []Diagnostic {
					if p.TypesInfo == nil || p.Fset == nil {
						return nil
					}
					var out []Diagnostic
					for _, file := range p.Files {
						rel := p.Rel(file)
						for _, v := range scanFileForIndirectEventuallyReferences(p, file, rel) {
							out = append(out, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
						}
					}
					sort.Slice(out, func(i, j int) bool {
						if out[i].Rel != out[j].Rel {
							return out[i].Rel < out[j].Rel
						}
						return out[i].Line < out[j].Line
					})
					return out
				})

			goldenPath := filepath.Join(root, "tools", "archtest", "testdata",
				"eventually_funnel_fixtures", dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}

// TestEventuallyFunnel_SymbolSentinel locks the testify Eventually surface
// against rename/removal drift. The prefix predicate
// (isBannedEventuallyFuncName) auto-covers any future EventuallyXyz testify
// upstream may add, so additions don't fail this test — but a rename like
// `Eventually` → `WaitUntil` would silently disable the entire archtest in
// production code (callers would migrate to the new name without tripping a
// ban). This sentinel walks the loaded testify require/assert package
// scopes via *types.Package.Imports() and asserts the v1.11.1 baseline of
// 4 package-level Eventually* funcs is still present per package — any
// drop fails the test and points the reviewer at this comment to update
// the baseline alongside any prefix predicate adjustment.
//
// Methods on *Assertions (require.Assertions.Eventually, etc.) are NOT
// walked here; they are anchored at compile time by the assertions_method_call_red
// fixture, which fails to load if testify renames a method forwarder.
//
// Tool: RunTyped + *types.Package.Imports() + Scope().Lookup. No new
// archtest entry point; reuses the existing module-wide typed load.
func TestEventuallyFunnel_SymbolSentinel(t *testing.T) {
	t.Parallel()

	observed := make(map[bannedEventuallySymbol]struct{})
	logged := make(map[bannedEventuallySymbol]struct{})

	scan := func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		for _, imp := range p.Pkg.Imports() {
			if _, ok := bannedEventuallyPkgPaths[imp.Path()]; !ok {
				continue
			}
			scope := imp.Scope()
			for _, name := range scope.Names() {
				fn, ok := scope.Lookup(name).(*types.Func)
				if !ok {
					continue
				}
				if !isBannedEventuallyFuncName(fn.Name()) {
					continue
				}
				sym := bannedEventuallySymbol{PkgPath: imp.Path(), Name: fn.Name()}
				observed[sym] = struct{}{}
			}
		}
		return nil
	}

	_ = RunTyped(t, TypedOpts{Tests: true}, []string{"./..."}, scan)

	// v1.11.1 baseline. The four names per package are testify's complete
	// Eventually* surface as of this writing; if testify removes/renames any,
	// the archtest stops catching those callsites in production code. Adding
	// a new EventuallyXyz is auto-covered by the prefix predicate and only
	// logged here (not failed).
	baselineNames := []string{
		testifyFuncEventuallyPrefix,            // "Eventually"
		testifyFuncEventuallyPrefix + "WithT",  // "EventuallyWithT"
		testifyFuncEventuallyPrefix + "f",      // "Eventuallyf"
		testifyFuncEventuallyPrefix + "WithTf", // "EventuallyWithTf"
	}
	for _, pkgPath := range []string{testifyRequirePkgPath, testifyAssertPkgPath} {
		for _, name := range baselineNames {
			sym := bannedEventuallySymbol{PkgPath: pkgPath, Name: name}
			_, ok := observed[sym]
			assert.True(t, ok,
				"%s sentinel: baseline testify symbol %s.%s not found in loaded surface — "+
					"upstream rename/removal? Update baselineNames in TestEventuallyFunnel_SymbolSentinel "+
					"AND verify isBannedEventuallyFuncName still covers the replacement name.",
				ruleTestEventuallyFunnel01, pkgPath, name)
			logged[sym] = struct{}{}
		}
	}

	// Log (don't fail) any observed Eventually* symbol not in the baseline —
	// alerts reviewer that testify added a new variant and prefix predicate
	// is silently extending coverage.
	for sym := range observed {
		if _, known := logged[sym]; known {
			continue
		}
		t.Logf("%s sentinel: testify surface added %s.%s — prefix predicate covers it; "+
			"consider adding to baselineNames once stable.",
			ruleTestEventuallyFunnel01, sym.PkgPath, sym.Name)
	}
}

// bannedEventuallyIdentObj reports whether obj is a banned testify
// Eventually function/method; on hit returns the matched symbol identity.
func bannedEventuallyIdentObj(obj types.Object) (bannedEventuallySymbol, bool) {
	var zero bannedEventuallySymbol
	if obj == nil {
		return zero, false
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return zero, false
	}
	return bannedEventuallyFuncObj(fn)
}

// bannedEventuallyFuncObj is the single membership check: fn belongs to
// testify require/assert AND its name starts with `Eventually`. Used by
// both the callee resolver (direct calls) and the ident-object check
// (indirect references) to keep "what counts as banned" single-sourced.
func bannedEventuallyFuncObj(fn *types.Func) (bannedEventuallySymbol, bool) {
	var zero bannedEventuallySymbol
	if fn.Pkg() == nil {
		return zero, false
	}
	pkgPath := fn.Pkg().Path()
	if _, ok := bannedEventuallyPkgPaths[pkgPath]; !ok {
		return zero, false
	}
	if !isBannedEventuallyFuncName(fn.Name()) {
		return zero, false
	}
	return bannedEventuallySymbol{PkgPath: pkgPath, Name: fn.Name()}, true
}

// collectDirectEventuallyCallIdents returns the set of SelectorExpr.Sel
// Idents in file whose callee resolves to a banned symbol (via either
// qualified-ident Uses or method-selector Selections). These are the
// direct-call sites caught by the main rule; the reverse self-test
// excludes them so the two rules don't double-report the same line.
func collectDirectEventuallyCallIdents(file *ast.File, info *types.Info) map[*ast.Ident]struct{} {
	directCallFun := make(map[*ast.Ident]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		fn := resolveSelectorCalleeFunc(sel, info)
		if fn == nil {
			return
		}
		if _, ok := bannedEventuallyFuncObj(fn); !ok {
			return
		}
		directCallFun[sel.Sel] = struct{}{}
	})
	return directCallFun
}

// scanFileForIndirectEventuallyReferences returns all Idents in file that
// reference a banned symbol AND are not in the direct-call set. Walks two
// maps for full coverage:
//
//   - pass.TypesInfo.Uses for qualified-ident references
//     (e.g. `var f = require.Eventuallyf`).
//   - pass.TypesInfo.Selections for method-selector references
//     (e.g. `var f = require.New(t).Eventually` MethodVal or
//     `(*require.Assertions).EventuallyWithT` MethodExpr).
//
// Deduplication is by ident pointer: a single Sel never appears in both
// maps for a well-typed selector, but the dedup is cheap insurance.
func scanFileForIndirectEventuallyReferences(
	pass *Pass,
	file *ast.File,
	rel string,
) []eventuallyFunnelViolation {
	directCallFun := collectDirectEventuallyCallIdents(file, pass.TypesInfo)
	absFile := pass.Abs(file)
	seenIdent := make(map[*ast.Ident]struct{})
	var out []eventuallyFunnelViolation

	emit := func(ident *ast.Ident, sym bannedEventuallySymbol) {
		if ident == nil {
			return
		}
		if pass.Fset.Position(ident.Pos()).Filename != absFile {
			return
		}
		if _, ok := directCallFun[ident]; ok {
			return
		}
		if _, ok := seenIdent[ident]; ok {
			return
		}
		seenIdent[ident] = struct{}{}
		out = append(out, eventuallyFunnelViolation{
			File: rel,
			Line: pass.Fset.Position(ident.Pos()).Line,
			Reason: fmt.Sprintf(
				"indirect reference to %s.%s (function value, pointer pass-through, "+
					"method binding, or reflect); use testwait.External / "+
					"testwait.Deterministic at the callsite instead",
				lastPathSegment(sym.PkgPath), sym.Name,
			),
		})
	}

	// Pass 1: qualified-ident references (info.Uses).
	for ident, obj := range pass.TypesInfo.Uses {
		if ident == nil || obj == nil {
			continue
		}
		sym, ok := bannedEventuallyIdentObj(obj)
		if !ok {
			continue
		}
		emit(ident, sym)
	}

	// Pass 2: method-selector references (info.Selections). Covers MethodVal
	// (receiver.Method as a function value) and MethodExpr
	// ((*Receiver).Method as a function value). FieldVal selections are
	// skipped — Eventually is never a struct field in testify.
	for sel, selObj := range pass.TypesInfo.Selections {
		if sel == nil || sel.Sel == nil || selObj == nil {
			continue
		}
		switch selObj.Kind() {
		case types.MethodVal, types.MethodExpr:
		default:
			continue
		}
		fn, ok := selObj.Obj().(*types.Func)
		if !ok {
			continue
		}
		sym, ok := bannedEventuallyFuncObj(fn)
		if !ok {
			continue
		}
		emit(sel.Sel, sym)
	}

	return out
}
