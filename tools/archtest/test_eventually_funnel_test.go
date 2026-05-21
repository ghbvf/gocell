package archtest

// INVARIANT: TEST-EVENTUALLY-FUNNEL-01
//
// test_eventually_funnel_test.go — Hard upstream funnel for
// pkg/testutil/testwait.External, paired with the Hard downstream
// TEST-POLLING-EXTERNAL-REASON-LITERAL-01:
//
//   - Bare callsites of (require|assert).Eventually and
//     (require|assert).EventuallyWithT are rejected anywhere in the module
//     (production + test code). Callers MUST go through testwait.External
//     (synchronous wall-clock polling with const-literal reason) or
//     testwait.Deterministic (channel-blocking wait — preferred).
//   - Every reference to one of those four testify symbols MUST appear in
//     *ast.CallExpr.Fun position only if the call itself is also forbidden
//     (it is). The reverse blind-spot self-test asserts no indirect
//     references reach the symbols (function-variable assignment,
//     function-pointer pass-through, reflect.ValueOf, struct-field binding)
//     — those four shapes would otherwise bypass the main rule's
//     CallExpr-driven scan.
//
// Together with TEST-POLLING-EXTERNAL-REASON-LITERAL-01 this closes the
// testwait funnel as a Hard 范本: "typed marker funnel for unbounded ops"
// per .claude/rules/gocell/ai-collab.md §"Hard 范本目录" — sibling of
// panicregister.Approved. See pkg/testutil/testwait/testwait.go godoc and
// docs/plans/202605181600-042-archtest.md §1.1 PR3.

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const ruleTestEventuallyFunnel01 = "TEST-EVENTUALLY-FUNNEL-01"

// Banned testify symbol identities. Resolution is via *types.Info.Uses →
// *types.Func, so import aliases (e.g. `import req "…/require"; req.Eventually(...)`)
// are handled correctly. Pure-AST fallback (fixture mode without type info)
// matches package-ident "require" / "assert" — fixture files always use the
// canonical names.
const (
	testifyRequirePkgPath = "github.com/stretchr/testify/require"
	testifyAssertPkgPath  = "github.com/stretchr/testify/assert"

	testifyFuncEventually      = "Eventually"
	testifyFuncEventuallyWithT = "EventuallyWithT"
)

// bannedEventuallySymbols enumerates the four (pkg, func) pairs whose
// callsites + references are rejected. Listed explicitly (not derived) to
// keep the banned set obvious at the rule callsite and to make "add a new
// banned symbol" an explicit code change.
var bannedEventuallySymbols = []struct {
	PkgPath string
	Name    string
}{
	{testifyRequirePkgPath, testifyFuncEventually},
	{testifyRequirePkgPath, testifyFuncEventuallyWithT},
	{testifyAssertPkgPath, testifyFuncEventually},
	{testifyAssertPkgPath, testifyFuncEventuallyWithT},
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
func scanFileForEventuallyFunnelViolations(
	pass *Pass,
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
			Line: pass.Fset.Position(call.Pos()).Line,
			Reason: fmt.Sprintf(
				"bare %s.%s call — use testwait.External(t, reason, ...) for "+
					"synchronous polling or testwait.Deterministic(t, ch, ...) for "+
					"channel waits (see pkg/testutil/testwait)",
				lastPathSegment(sym.PkgPath), sym.Name,
			),
		})
	})

	return violations
}

// bannedEventuallyCallee reports whether funExpr is one of the four banned
// testify callees. Returns the matched symbol identity on hit.
//
// When info is non-nil, resolution is via *types.Info.Uses on the SelectorExpr's
// Sel, which handles import aliases correctly. When info is nil (fixture mode
// without type resolution), falls back to pure-AST matching of `pkgIdent.Sel`
// where pkgIdent.Name is "require" or "assert".
func bannedEventuallyCallee(funExpr ast.Expr, info *types.Info) (struct {
	PkgPath string
	Name    string
}, bool,
) {
	var zero struct {
		PkgPath string
		Name    string
	}
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return zero, false
	}
	if !isBannedEventuallyFuncName(sel.Sel.Name) {
		return zero, false
	}
	if info != nil {
		obj := info.Uses[sel.Sel]
		if obj == nil {
			return zero, false
		}
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			return zero, false
		}
		for _, sym := range bannedEventuallySymbols {
			if fn.Pkg().Path() == sym.PkgPath && fn.Name() == sym.Name {
				return sym, true
			}
		}
		return zero, false
	}
	// Pure-AST fallback (fixture mode): match `<pkg>.Eventually` /
	// `<pkg>.EventuallyWithT` where pkg ident is "require" or "assert".
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return zero, false
	}
	switch xIdent.Name {
	case "require":
		return struct {
			PkgPath string
			Name    string
		}{PkgPath: testifyRequirePkgPath, Name: sel.Sel.Name}, true
	case "assert":
		return struct {
			PkgPath string
			Name    string
		}{PkgPath: testifyAssertPkgPath, Name: sel.Sel.Name}, true
	}
	return zero, false
}

func isBannedEventuallyFuncName(name string) bool {
	return name == testifyFuncEventually || name == testifyFuncEventuallyWithT
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
// Tool: archtest.RunTyped + *types.Info callee resolution + go/ast
// SelectorExpr matching. This is the typed-marker funnel upstream lock; the
// downstream lock (testwait.External callee+arg form-uniqueness) is
// TEST-POLLING-EXTERNAL-REASON-LITERAL-01.
//
// Blind spots of the chosen tool (per AI-rebust §"工具选定后强制盲区自检"):
//
//   - Indirect call via function variable: var f = require.Eventually; f(t, cond, ...).
//   - Function-pointer pass-through: helper(require.Eventually).
//   - Reflect call: reflect.ValueOf(require.Eventually).Call(...).
//   - Struct-field binding: wrapper{F: require.Eventually}.
//
// All four shapes silently bypass the CallExpr.Fun-driven main scan.
// TestEventuallyFunnel_NoIndirectReferences below is the reverse self-test:
// it scans every Ident that *types.Info.Uses resolves to one of the four
// banned symbols and asserts the Ident appears in *ast.CallExpr.Fun position
// — and even then, since direct call IS the violation, the union of the
// main rule + reverse self-test forms the (callee) form-uniqueness Hard
// lock: outside testwait.External / testwait.Deterministic no syntactic
// shape can reach require.Eventually / assert.Eventually / *WithT.
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
			for _, v := range scanFileForEventuallyFunnelViolations(p, file, p.TypesInfo, rel) {
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
			"Run: go test ./tools/archtest/... -run TestEventuallyFunnel$ for local repro.",
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
						for _, v := range scanFileForEventuallyFunnelViolations(p, file, p.TypesInfo, rel) {
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
// self-test required by AI-rebust §"工具选定后强制盲区自检".
//
// It scans every *ast.Ident in production + test code whose
// *types.Info.Uses entry resolves to one of the four banned symbols, then
// rejects ALL such references (since direct call is itself banned, indirect
// references are also banned). Any non-CallExpr.Fun reference would prove
// the symbol is "reachable" from code outside testwait — the rule's intent
// is that the symbol set is unreachable except through the testwait funnel.
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
			// Pass 1: gather CallExpr.Fun positions whose Fun resolves to a
			// banned symbol. These are the direct-call sites already caught
			// by TestEventuallyFunnel; we exclude them here so the reverse
			// self-test only reports new (indirect) shapes and the two
			// rules don't double-count the same line.
			directCallFun := make(map[*ast.Ident]struct{})
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return
				}
				if !isBannedEventuallyIdentUse(sel.Sel, p.TypesInfo) {
					return
				}
				directCallFun[sel.Sel] = struct{}{}
			})

			// Pass 2: every Ident in types.Info.Uses pointing at a banned
			// symbol must be in directCallFun; otherwise it's an indirect
			// reference (var assignment, funcarg, reflect, struct field).
			for ident, obj := range p.TypesInfo.Uses {
				if ident == nil || obj == nil {
					continue
				}
				sym, ok := bannedEventuallyIdentObj(obj)
				if !ok {
					continue
				}
				identFile := p.Fset.Position(ident.Pos()).Filename
				absFile := p.Abs(file)
				if identFile != absFile {
					continue
				}
				if _, ok := directCallFun[ident]; ok {
					continue
				}
				v := eventuallyFunnelViolation{
					File: rel,
					Line: p.Fset.Position(ident.Pos()).Line,
					Reason: fmt.Sprintf(
						"indirect reference to %s.%s (function value, pointer pass-through, "+
							"method binding, or reflect); use testwait.External / "+
							"testwait.Deterministic at the callsite instead",
						lastPathSegment(sym.PkgPath), sym.Name,
					),
				}
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
			"reflect, struct field); route through testwait.External / testwait.Deterministic.",
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
func TestEventuallyFunnel_NoIndirectReferences_Fixtures(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	fixtures := []string{
		"indirect_var_red",
		"indirect_funcarg_red",
		"indirect_reflect_red",
		"indirect_struct_field_red",
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

						// Pass 1: gather direct-call CallExpr.Fun Idents.
						directCallFun := make(map[*ast.Ident]struct{})
						EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
							sel, ok := call.Fun.(*ast.SelectorExpr)
							if !ok || sel.Sel == nil {
								return
							}
							if !isBannedEventuallyIdentUse(sel.Sel, p.TypesInfo) {
								return
							}
							directCallFun[sel.Sel] = struct{}{}
						})

						// Pass 2: every Ident in Uses pointing at a banned
						// symbol must be in directCallFun; otherwise indirect.
						for ident, obj := range p.TypesInfo.Uses {
							if ident == nil || obj == nil {
								continue
							}
							sym, ok := bannedEventuallyIdentObj(obj)
							if !ok {
								continue
							}
							identFile := p.Fset.Position(ident.Pos()).Filename
							absFile := p.Abs(file)
							if identFile != absFile {
								continue
							}
							if _, ok := directCallFun[ident]; ok {
								continue
							}
							out = append(out, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(ident.Pos()).Line,
								Message: fmt.Sprintf(
									"indirect reference to %s.%s (function value, pointer pass-through, "+
										"method binding, or reflect); use testwait.External / "+
										"testwait.Deterministic at the callsite instead",
									lastPathSegment(sym.PkgPath), sym.Name,
								),
							})
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

// isBannedEventuallyIdentUse reports whether ident's *types.Info.Uses entry
// resolves to one of the four banned symbols.
func isBannedEventuallyIdentUse(ident *ast.Ident, info *types.Info) bool {
	if ident == nil || info == nil {
		return false
	}
	_, ok := bannedEventuallyIdentObj(info.Uses[ident])
	return ok
}

// bannedEventuallyIdentObj reports whether obj is one of the four banned
// testify functions; on hit returns the matched symbol identity.
func bannedEventuallyIdentObj(obj types.Object) (struct {
	PkgPath string
	Name    string
}, bool,
) {
	var zero struct {
		PkgPath string
		Name    string
	}
	if obj == nil {
		return zero, false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return zero, false
	}
	for _, sym := range bannedEventuallySymbols {
		if fn.Pkg().Path() == sym.PkgPath && fn.Name() == sym.Name {
			return sym, true
		}
	}
	return zero, false
}
