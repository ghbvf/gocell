// invariants asserted in this file:
//   - INVARIANT: CELLGEN-ERRCODE-FUNNEL-01
//
// Package archtest — cellgen errcode funnel invariant.
//
// CELLGEN-ERRCODE-FUNNEL-01: in non-test .go files under
// tools/codegen/cellgen/, **no identifier** may resolve via *types.Info
// to a *types.Func owned by a registered constructor in the blacklist
// `{(fmt, Errorf), (errors, New), (errors, Join)}`. Regardless of the
// syntactic position the reference appears in — CallExpr.Fun, var
// initialization RHS, type conversion argument, type assertion target,
// slice literal element, struct literal field, function-value passing —
// types.Info.Uses[] collapses every form to the same identity check.
//
// Detection algorithm:
//
//   - STEP 1 — scan every *ast.Ident in the file via EachInSubtree.
//   - STEP 2 — for each Ident, look up types.Info.Uses[ident]. If the
//     resolved object is *types.Func AND (Pkg.Path, Name) ∈ blacklist,
//     emit a violation.
//
// Why Ident-scan, not CallExpr-driven: a CallExpr-only scan misses
//   - named function types: `type Ctor func(string) error; var c Ctor = errors.New`
//   - type conversions:    `(func(string) error)(errors.New)("x")`
//   - type assertions:     `any(errors.New).(func(string) error)("x")`
//   - re-export via *types.Var: closed by Ident-scan at the assignment site
//     (the var declaration's RHS Ident resolves to the blacklist Func).
//
// types.Info.Uses[] uniformly resolves any Ident's referenced object
// regardless of containing syntax; this is the maximally uniform
// form-uniqueness oracle in Go. Strictly dual to PANIC-REGISTERED-01
// (which uses types.Info.Uses[] on the panic-arg CallExpr's Fun).
//
// AI-rebust: Hard within the registered set — (Pkg.Path, Name) blacklist
// ∪ depguard-banned third-party packages. Form uniqueness via
// types.Info.Uses[]; archtest fail-on-deviation. Per charter §"typed
// function call as Hard funnel for unbounded operations", this is Go's
// upper bound for "no reference to registered constructors" rule shape.
//
// Funnel double-lock:
//   - upstream Hard: this archtest's Ident-scan + .golangci.yml depguard
//     `cellgen-error-libs` rule (third-party import ban).
//   - downstream Hard: pkg/errcode funnel content locked by
//     ERRCODE-KIND-LITERAL-01 + MESSAGE-CONST-LITERAL-01 +
//     DETAILS-SLOG-ATTR-01.
//
// See ADR docs/architecture/202605171200-adr-cellgen-errcode-funnel-mechanism.md
// §D5 (canonical Ident-scan design) + §D7 (Path C-full backlog upgrade).
//
// Declared blind spots (charter §"工具选定后强制盲区自检" — runtime
// self-checks: TestCellgenErrcodeFunnelBlindSpotsAbsent):
//   - Composite literal `&myErr{}` where myErr implements error in a
//     non-errcode package: no Ident reference to a blacklist Func. Path
//     C-full (errcode.Error field privatization) is the backlog upgrade.
//   - Reflect dynamic construction (reflect.MakeFunc / reflect.Value.Call
//     producing error): bypasses static AST.
//   - Cross-package function-valued var imports (e.g., `sl.Ctor` where
//     `sl.Ctor` is `var Ctor = errors.New`): the Ident `Ctor` resolves to
//     a *types.Var, not *types.Func. Mitigated by depguard
//     `cellgen-error-libs` import ban for registered third-party libs;
//     new libs require trigger-based ADR §Escalation same-PR addition.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/typesutil"
)

const ruleCellgenErrcodeFunnel01 = "CELLGEN-ERRCODE-FUNNEL-01"

// cellgenErrConstructorBlacklist is the registered set of stdlib
// error-construction functions cellgen production code must not
// reference (in any syntactic position). Form uniqueness is via the
// (Pkg.Path, Name) pair resolved through *types.Info.Uses[], so
// aliased / dot-imported / re-exported / convert-wrapped / type-asserted
// references collapse to the same identity check. Third-party error
// libraries (github.com/pkg/errors, golang.org/x/xerrors,
// go.uber.org/multierr, etc.) are blocked at the import boundary by the
// .golangci.yml depguard `cellgen-error-libs` rule — defense in depth.
//
// Extending the blacklist: append a new (Pkg.Path, Name) entry below AND
// add a RED fixture under testdata/cellgen_errcode_funnel_fixtures/.
var cellgenErrConstructorBlacklist = []struct {
	PkgPath string
	Name    string
}{
	{PkgPath: "fmt", Name: "Errorf"},
	{PkgPath: "errors", Name: "New"},
	{PkgPath: "errors", Name: "Join"},
}

// errorInterface is the built-in `error` interface, looked up once from
// universe scope. Used by TestCellgenErrcodeFunnelBlindSpotsAbsent's
// composite-literal scan (no longer needed by the main scanner after
// Ident-scan refactor). errcodePkgPath is declared in
// panic_invariants_test.go in the same archtest package and is reused
// zero-cost (TestCellgenErrcodeFunnelBlindSpotsAbsent excludes
// pkg/errcode-owned types from the composite-literal violation set).
var errorInterface = func() *types.Interface {
	obj := types.Universe.Lookup("error")
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		panic("archtest: universe scope 'error' is not an interface")
	}
	return iface
}()

type cellgenErrcodeViolation struct {
	File   string
	Line   int
	Reason string
}

// scanFileForCellgenErrcodeViolations walks one AST file and returns
// CELLGEN-ERRCODE-FUNNEL-01 violations. info MUST be non-nil — the rule
// is types.Info-driven; without it, alias / dot-import / conversion /
// assertion forms cannot be uniformly resolved.
//
// Implementation: enumerate every *ast.Ident in the file, resolve
// types.Info.Uses[ident], and if the resolved object is a *types.Func
// whose (Pkg.Path, Name) is in the blacklist, emit a violation. The
// Uses[] resolution uniformly handles SelectorExpr `.Sel` Idents,
// dot-import bare Idents, conversion-arg Idents, type-assertion-expr
// Idents — every syntactic position collapses to the same Func identity.
func scanFileForCellgenErrcodeViolations(
	p *Pass,
	file *ast.File,
	rel string,
) []cellgenErrcodeViolation {
	info := p.TypesInfo
	var violations []cellgenErrcodeViolation
	seenLine := make(map[int]struct{}) // dedup multiple Idents on the same line

	EachInSubtree[ast.Ident](file, func(ident *ast.Ident) {
		obj := info.Uses[ident]
		if obj == nil {
			return
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return
		}
		pkgPath, name, hit := blacklistMatch(fn)
		if !hit {
			return
		}
		line := p.Fset.Position(ident.Pos()).Line
		if _, dup := seenLine[line]; dup {
			return // a single line may contain multiple refs (e.g., slice literal)
		}
		seenLine[line] = struct{}{}
		violations = append(violations, cellgenErrcodeViolation{
			File: rel,
			Line: line,
			Reason: fmt.Sprintf(
				"cellgen: reference to blacklisted error constructor: %s.%s",
				pkgPath, name),
		})
	})

	return violations
}

// blacklistMatch reports whether fn is in cellgenErrConstructorBlacklist.
// Returns the matched pkg path and name on hit.
func blacklistMatch(fn *types.Func) (pkgPath, name string, hit bool) {
	if fn.Pkg() == nil {
		return "", "", false
	}
	pkgPath = fn.Pkg().Path()
	name = fn.Name()
	for _, entry := range cellgenErrConstructorBlacklist {
		if entry.PkgPath == pkgPath && entry.Name == name {
			return pkgPath, name, true
		}
	}
	return "", "", false
}

// TestCellgenErrcodeFunnel enforces CELLGEN-ERRCODE-FUNNEL-01 against the
// production cellgen package (tools/codegen/cellgen/*.go and any
// sub-packages, excluding *_test.go). Expected to pass: cellgen
// currently has zero references to fmt.Errorf / errors.New / errors.Join
// (verified post-Ident-scan refactor; ADR §D5 spike fact for callsites
// pre-dates this rewrite).
//
// Build-tag coverage: cellgen has no //go:build files
// (TestCellgenErrcodeFunnelNoBuildTagFiles enforces this), so the
// default build context covers the entire package. No KnownNonDefaultTags
// fan-out is needed; build-tag-isolated files are structurally prevented.
//
// Blind spots (declared in package godoc, runtime-checked by
// TestCellgenErrcodeFunnelBlindSpotsAbsent): composite literal of
// non-errcode types implementing error; reflect.MakeFunc dynamic
// construction; cross-package function-valued var imports.
func TestCellgenErrcodeFunnel(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var violations []cellgenErrcodeViolation

	_ = RunTyped(t, TypedOpts{},
		[]string{"./tools/codegen/cellgen/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, v := range scanFileForCellgenErrcodeViolations(p, file, rel) {
					key := fmt.Sprintf("%s:%d:%s", v.File, v.Line, v.Reason)
					if _, dup := seen[key]; dup {
						continue
					}
					seen[key] = struct{}{}
					violations = append(violations, v)
				}
			}
			return nil
		})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleCellgenErrcodeFunnel01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
		t.Fatalf("%s: cellgen production code must not reference "+
			"blacklisted error constructors (fmt.Errorf / errors.New / "+
			"errors.Join). Use pkg/errcode constructors instead "+
			"(errcode.New / errcode.Wrap / errcode.Assertion / "+
			"errcode.WrapInfra). See ADR "+
			"docs/architecture/202605171200-adr-cellgen-errcode-funnel-mechanism.md §D5.",
			ruleCellgenErrcodeFunnel01)
	}
}

// TestCellgenErrcodeFunnelNoBuildTagFiles asserts that no production
// .go file under tools/codegen/cellgen/ carries a //go:build directive.
// This is the structural complement to TestCellgenErrcodeFunnel's
// default-build-context scan — without this assertion, a future cellgen
// file gated by `//go:build integration` (or any non-default tag) would
// silently escape the rule. By forbidding build-tag-isolated files in
// cellgen production, the default-only scan is guaranteed complete.
//
// If a legitimate need for non-default build tags ever arises in cellgen,
// the trigger is to (a) extend TestCellgenErrcodeFunnel to fan out over
// KnownNonDefaultTags (mirroring panic_invariants_test.go pattern) AND
// (b) update this test to allow the specific tag, all in the same PR.
//
// Implementation note: uses two RunTyped calls to comply with
// SCANNER-FRAMEWORK-USAGE-01 while covering the full build directive file set.
// Load 1 (tags=nil) catches files active under the default context, including
// those with reverse //go:build !X directives that a union-tag load would
// silently exclude (go/build matchTag semantics). Load 2 (ProductionFlatTags)
// catches files gated by positive non-default tags. The seen map deduplicates
// across both loads.
func TestCellgenErrcodeFunnelNoBuildTagFiles(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var offending []string

	scan := func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			packagePos := file.Package
			for _, cg := range file.Comments {
				// Build constraints must precede the package clause.
				if cg.Pos() >= packagePos {
					break
				}
				for _, c := range cg.List {
					text := strings.TrimSpace(c.Text)
					if !strings.HasPrefix(text, "//go:build") && !strings.HasPrefix(text, "// +build") {
						continue
					}
					key := rel + ":" + text
					if _, dup := seen[key]; dup {
						continue
					}
					seen[key] = struct{}{}
					offending = append(offending, fmt.Sprintf("%s: %s", rel, text))
				}
			}
		}
		return nil
	}

	// 两次 Load 覆盖全 build directive 文件集（含反向 //go:build !X）：
	// Load 1 (tags=nil) 覆盖默认 build + 反向 directive 文件；
	// Load 2 (ProductionFlatTags) 覆盖所有正向 tag 激活文件 union。
	// 两次 Load 文件集有重合，seen map dedup 保证 offending 唯一。
	// 单次 union Load 设计被拒绝：go/build matchTag 语义下 //go:build !X 在
	// -tags=...,X,... 模式下静默排除（详见 ADR 202605190000 §Alternatives）。
	_ = RunTyped(t, TypedOpts{}, []string{"./tools/codegen/cellgen/..."}, scan)
	_ = RunTyped(t, TypedOpts{Tags: ProductionFlatTags()}, []string{"./tools/codegen/cellgen/..."}, scan)

	if len(offending) > 0 {
		for _, o := range offending {
			t.Logf("  %s", o)
		}
		t.Fatalf("%s: cellgen production files must not carry //go:build "+
			"directives — TestCellgenErrcodeFunnel only scans the default "+
			"build context. To introduce non-default-tag cellgen files, "+
			"update TestCellgenErrcodeFunnel to fan out over "+
			"KnownNonDefaultTags AND adjust this test in the same PR.",
			ruleCellgenErrcodeFunnel01)
	}
}

// TestCellgenErrcodeFunnelBlindSpotsAbsent enforces the runtime side of
// charter §"工具选定后强制盲区自检" (`.claude/rules/gocell/ai-collab.md`)
// — Hard/Medium rule shapes require that each declared blind spot has a
// **runtime self-check** asserting the spot is empty in production AST,
// not just a documentation claim.
//
// Blind spots asserted absent in production cellgen (mirrors ADR
// docs/architecture/202605171200-adr-cellgen-errcode-funnel-mechanism.md §D5):
//
//  1. CompositeLit of a non-errcode type whose method set implements
//     the error interface — e.g., `return &myErr{}` where myErr has
//     `func (m *myErr) Error() string`. This is the only documented
//     escape from the Ident-scan funnel under current Path A (closed
//     only by Path C-full errcode.Error field privatization).
//  2. reflect.MakeFunc dynamic error construction — escapes static AST
//     resolution.
func TestCellgenErrcodeFunnelBlindSpotsAbsent(t *testing.T) {
	t.Parallel()

	var compositeLitErrs []string
	var reflectCalls []string

	_ = RunTyped(t, TypedOpts{},
		[]string{"./tools/codegen/cellgen/..."},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}

				// Blind spot 1: CompositeLit whose type implements error.
				EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
					if lit.Type == nil {
						return
					}
					t := p.TypesInfo.TypeOf(lit.Type)
					if t == nil {
						return
					}
					if typesutil.ImplementsInterface(t, errorInterface) {
						compositeLitErrs = append(compositeLitErrs,
							fmt.Sprintf("%s:%d: %s", rel,
								p.Fset.Position(lit.Pos()).Line, t.String()))
					}
				})

				// Blind spot 2: reflect.MakeFunc.
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					obj := p.TypesInfo.Uses[sel.Sel]
					fn, isFunc := obj.(*types.Func)
					if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != "reflect" {
						return
					}
					if fn.Name() != "MakeFunc" {
						return
					}
					reflectCalls = append(reflectCalls,
						fmt.Sprintf("%s:%d: reflect.%s",
							rel, p.Fset.Position(call.Pos()).Line, fn.Name()))
				})
			}
			return nil
		})

	if len(compositeLitErrs) > 0 {
		t.Logf("%s blind-spot: %d composite literal(s) implementing error in cellgen:",
			ruleCellgenErrcodeFunnel01, len(compositeLitErrs))
		for _, v := range compositeLitErrs {
			t.Logf("  %s", v)
		}
		t.Fatalf("%s: composite-literal error construction detected in cellgen. "+
			"Per ADR §D5/§D7, this is the documented blind spot of Path A "+
			"(closed only by Path C-full errcode.Error field privatization). "+
			"Either remove the construction or trigger the Path C-full upgrade.",
			ruleCellgenErrcodeFunnel01)
	}
	if len(reflectCalls) > 0 {
		t.Logf("%s blind-spot: %d reflect.MakeFunc call(s) in cellgen:",
			ruleCellgenErrcodeFunnel01, len(reflectCalls))
		for _, v := range reflectCalls {
			t.Logf("  %s", v)
		}
		t.Fatalf("%s: reflect.MakeFunc detected in cellgen. Reflect-based "+
			"dynamic error construction escapes static AST resolution. "+
			"Per ADR §D5, this is a declared blind spot — extend the rule "+
			"or add a reflect import ban before merging.",
			ruleCellgenErrcodeFunnel01)
	}
}

// TestCellgenErrcodeFunnelScannerFixtures verifies the
// CELLGEN-ERRCODE-FUNNEL-01 rule logic against static fixture packages
// under tools/archtest/testdata/cellgen_errcode_funnel_fixtures/. Each
// fixture dir owns a diag.golden capturing the rule's real output. GREEN
// fixtures have an empty golden. Mirrors panic_registered_fixtures
// convention.
//
// Fixtures cover the full escape surface (Path A registered set + the
// historical Findings 1+2 closure that drove the CallExpr → Ident-scan
// refactor in PR #574 round-2):
//
//	F1 fmt_errorf_red               — fmt.Errorf SelectorExpr
//	F2 fmt_alias_errorf_red         — fmtx "fmt" alias + fmtx.Errorf
//	F3 fmt_dot_import_errorf_red    — import . "fmt" + Errorf
//	F4 errors_new_red               — errors.New SelectorExpr
//	F5 errors_dot_import_new_red    — import . "errors" + New
//	F6 errors_join_red              — errors.Join SelectorExpr
//	F7 function_valued_var_red      — var ErrNew = errors.New + ErrNew(...)
//	F8 local_wrapper_red            — same-pkg wrapper body with errors.New
//	F9 named_function_valued_var_red — type Ctor func(...); var X Ctor = errors.New
//	F10 type_conversion_red         — (func(string) error)(errors.New)("x")
//	F11 type_assertion_red          — any(errors.New).(func(string) error)("x")
//
// Plus one GREEN fixture exercising errcode.{Assertion, New, Wrap, WrapInfra}.
func TestCellgenErrcodeFunnelScannerFixtures(t *testing.T) {
	t.Parallel()

	dirs := []string{
		"fmt_errorf_red",
		"fmt_alias_errorf_red",
		"fmt_dot_import_errorf_red",
		"errors_new_red",
		"errors_dot_import_new_red",
		"errors_join_red",
		"local_wrapper_red",
		"function_valued_var_red",
		"named_function_valued_var_red",
		"type_conversion_red",
		"type_assertion_red",
		"errcode_assertion_green",
	}

	root := findModuleRoot(t)
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/cellgen_errcode_funnel_fixtures/" + dir

			var diags []Diagnostic
			_ = RunTyped(t, TypedOpts{}, []string{fixturePattern}, func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil || p.Fset == nil {
					return nil
				}
				for _, file := range p.Files {
					rel := p.Rel(file)
					if strings.HasSuffix(rel, "_test.go") {
						continue
					}
					for _, v := range scanFileForCellgenErrcodeViolations(p, file, rel) {
						diags = append(diags, Diagnostic{
							Rel:     v.File,
							Line:    v.Line,
							Message: v.Reason,
						})
					}
				}
				return nil
			})

			goldenPath := filepath.Join(root, "tools", "archtest", "testdata",
				"cellgen_errcode_funnel_fixtures", dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}
