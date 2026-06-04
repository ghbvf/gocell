package archtest

// invariants:
//   - INVARIANT: AUTH-PLAN-01
//   - INVARIANT: AUTH-PLAN-02
//   - INVARIANT: AUTH-PLAN-03
//   - INVARIANT: AUTH-PLAN-04
//
// auth_plan_test.go — AST guards for the typed AuthPlan migration (PR262).
//
// Four rules prevent regression to the old string-based cell.Policy dispatch:
//
//   AUTH-PLAN-01  No literal strings "jwt", "mtls", "service-token", "stack["
//                 in production .go files (these were the Policy.Name values).
//   AUTH-PLAN-02  No selector expression bootstrap.Policy{None,JWT,…} —
//                 all seven legacy factory names are deleted.
//   AUTH-PLAN-03  No cell.Policy composite literal or type reference (deleted type).
//   AUTH-PLAN-04  LAYER-09: cells/ code must not construct AuthPlan structs
//                 (AuthJWT, AuthJWTFromAssembly, AuthMTLS, etc.) — composition
//                 root responsibility only.
//
// ref: kubernetes/apiserver pkg/authentication/authenticator/interfaces.go@master
//      — typed authenticator interface, no string-keyed dispatch.
// ref: go-kratos/kratos transport/http/server.go@main
//      — middleware assembled at composition root, not inside application code.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ---------------------------------------------------------------------------
// Rule constants
// ---------------------------------------------------------------------------

const (
	authPlanRule01 = "AUTH-PLAN-01"
	authPlanRule02 = "AUTH-PLAN-02"
	authPlanRule03 = "AUTH-PLAN-03"
	authPlanRule04 = "AUTH-PLAN-04"
)

// forbiddenPolicyStrings are the old cell.Policy.Name values that must no
// longer appear as string literals in production code. The canonical source of
// truth is now the Describe() method of each typed plan.
var forbiddenPolicyStrings = []string{
	`"jwt"`,
	`"mtls"`,
	`"service-token"`,
	`"stack["`,
}

// forbiddenPolicySelectors are selector names that existed on the now-deleted
// bootstrap.Policy* factory functions.
var forbiddenPolicySelectors = []string{
	"PolicyNone",
	"PolicyJWT",
	"PolicyJWTFromAssembly",
	"PolicyMTLS",
	"PolicyServiceToken",
	"PolicyVerboseToken",
	"PolicyStack",
}

// authPlanConstructorNames are the AuthPlan struct type names that cells/ must
// not construct directly (LAYER-09). Composition roots (cmd/, examples/) are
// exempt.
var authPlanConstructorNames = map[string]struct{}{
	"AuthJWT":             {},
	"AuthJWTFromAssembly": {},
	"AuthMTLS":            {},
	"AuthNone":            {},
	"AuthServiceToken":    {},
	"AuthOperator":        {}, // #1505 operator control-plane listener gate
}

// authPlanPkgPath is the canonical import path of the package that owns the
// AuthPlan constructor names and types. Resolved via go/types — the package
// alias chosen at any given import site (`auth`, `kauth`, `myauth`, …) is
// irrelevant; only the import path identifies the type's origin.
const authPlanPkgPath = "github.com/ghbvf/gocell/kernel/auth"

// ---------------------------------------------------------------------------
// AUTH-PLAN-01: no forbidden policy string literals
// ---------------------------------------------------------------------------

// authPlanStringAllowlist contains files that legitimately return or use the
// old policy-name strings as Describe() return values, observability labels,
// or unrelated map keys. These are not dispatch discriminators.
//
// Rule: any file in this list may contain the strings in forbiddenPolicyStrings.
// The rule still catches new callers that add the strings outside this list.
var authPlanStringAllowlist = []string{
	// Canonical Describe() return values live in auth_plan.go.
	"kernel/auth/auth_plan.go",
	// describeAuthChain — the single file allowed to assemble describe strings.
	"runtime/bootstrap/auth_plan_describe.go",
	// Observability labels (AuthMethod="jwt") — not dispatch.
	"runtime/auth/authenticator.go",
	// Vault policy map — "jwt" is a Vault policy name key, not an auth discriminator.
	"adapters/vault/auth.go",
}

// TestAuthPlan_NoLegacyPolicyStringLiterals enforces AUTH-PLAN-01:
// the string values that were used as cell.Policy.Name discriminators
// ("jwt", "mtls", "service-token", "stack[") must not appear as bare string
// literals in production .go files outside the canonical allowlist.
//
// Allowlisted files (see authPlanStringAllowlist) may contain these strings
// because they own the canonical Describe() definitions or use them as
// observability labels rather than dispatch keys.
func TestAuthPlan_NoLegacyPolicyStringLiterals(t *testing.T) {
	root := findModuleRoot(t)

	files, err := findAllProductionGoFiles(root)
	require.NoError(t, err)

	type hit struct {
		file string
		line int
		val  string
	}
	var hits []hit

	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)

		// Skip allowlisted files — they own these strings legitimately.
		skip := false
		for _, allowed := range authPlanStringAllowlist {
			if strings.HasSuffix(rel, allowed) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			continue // unparseable; other tools will catch syntax errors
		}
		scanner.EachInSubtree[ast.BasicLit](af, func(bl *ast.BasicLit) {
			if bl.Kind != token.STRING {
				return
			}
			for _, forbidden := range forbiddenPolicyStrings {
				// bl.Value is the raw quoted string (e.g. `"jwt"`).
				if bl.Value == forbidden {
					hits = append(hits, hit{
						file: rel,
						line: fset.Position(bl.Pos()).Line,
						val:  bl.Value,
					})
				}
			}
		})
	}

	if len(hits) > 0 {
		for _, h := range hits {
			t.Logf("%s: %s:%d: forbidden policy string literal %s", authPlanRule01, h.file, h.line, h.val)
		}
	}
	assert.Empty(t, hits,
		"AUTH-PLAN-01: string literals %v are old cell.Policy.Name discriminators; "+
			"use typed AuthPlan (auth.NewAuthJWT / auth.AuthMTLS{} / …) instead. "+
			"If a new file legitimately needs these strings (e.g. a Describe() impl), "+
			"add it to authPlanStringAllowlist in tools/archtest/auth_plan_test.go:91.",
		forbiddenPolicyStrings)
}

// ---------------------------------------------------------------------------
// AUTH-PLAN-02: no deleted bootstrap.Policy* selector expressions
// ---------------------------------------------------------------------------

// TestAuthPlan_NoLegacyPolicySelectorExpressions enforces AUTH-PLAN-02:
// the seven deleted bootstrap.Policy* factory functions must not be referenced
// anywhere in the codebase. This catches accidental re-introduction of the
// old API.
func TestAuthPlan_NoLegacyPolicySelectorExpressions(t *testing.T) {
	root := findModuleRoot(t)

	files, err := findAllProductionGoFiles(root)
	require.NoError(t, err)

	type hit struct {
		file string
		line int
		sel  string
	}
	var hits []hit

	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		scanner.EachInSubtree[ast.SelectorExpr](af, func(sel *ast.SelectorExpr) {
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != "bootstrap" {
				return
			}
			for _, forbidden := range forbiddenPolicySelectors {
				if sel.Sel.Name == forbidden {
					hits = append(hits, hit{
						file: rel,
						line: fset.Position(sel.Pos()).Line,
						sel:  "bootstrap." + forbidden,
					})
				}
			}
		})
	}

	if len(hits) > 0 {
		for _, h := range hits {
			t.Logf("%s: %s:%d: forbidden selector %s", authPlanRule02, h.file, h.line, h.sel)
		}
	}
	assert.Empty(t, hits,
		"AUTH-PLAN-02: bootstrap.Policy* factory functions have been deleted; "+
			"use []auth.ListenerAuth{auth.NewAuthJWT(v)} / auth.NewAuthJWTFromAssembly(asm) / … instead")
}

// ---------------------------------------------------------------------------
// AUTH-PLAN-03: no cell.Policy composite literals or type references
// ---------------------------------------------------------------------------

// TestAuthPlan_NoCellPolicyTypeUsage enforces AUTH-PLAN-03:
// cell.Policy is a deleted type. Neither `cell.Policy{…}` composite literals
// nor `cell.Policy` identifier references should appear in any .go file.
func TestAuthPlan_NoCellPolicyTypeUsage(t *testing.T) {
	root := findModuleRoot(t)

	files, err := findAllProductionGoFiles(root)
	require.NoError(t, err)

	type hit struct {
		file string
		line int
		kind string
	}
	var hits []hit

	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		scanner.EachInSubtree[ast.CompositeLit](af, func(lit *ast.CompositeLit) {
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok {
				return
			}
			id, ok := sel.X.(*ast.Ident)
			if ok && id.Name == "cell" && sel.Sel.Name == "Policy" {
				hits = append(hits, hit{
					file: rel,
					line: fset.Position(lit.Pos()).Line,
					kind: "composite literal",
				})
			}
		})
		scanner.EachInSubtree[ast.SelectorExpr](af, func(sel *ast.SelectorExpr) {
			id, ok := sel.X.(*ast.Ident)
			if ok && id.Name == "cell" && sel.Sel.Name == "Policy" {
				hits = append(hits, hit{
					file: rel,
					line: fset.Position(sel.Pos()).Line,
					kind: "type reference",
				})
			}
		})
	}

	// De-duplicate (composite lit will also match the selector inside it)
	seen := map[string]bool{}
	deduped := hits[:0]
	for _, h := range hits {
		key := fmt.Sprintf("%s:%d:%s", h.file, h.line, h.kind)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, h)
		}
	}
	hits = deduped

	if len(hits) > 0 {
		for _, h := range hits {
			t.Logf("%s: %s:%d: cell.Policy %s (type was deleted in PR262)", authPlanRule03, h.file, h.line, h.kind)
		}
	}
	assert.Empty(t, hits,
		"AUTH-PLAN-03: cell.Policy was deleted in PR262; use []auth.ListenerAuth instead")
}

// ---------------------------------------------------------------------------
// AUTH-PLAN-04 (LAYER-09): cells/ must not construct AuthPlan values
// ---------------------------------------------------------------------------

// TestAuthPlan_CellsMustNotConstructAuthPlans enforces AUTH-PLAN-04 (LAYER-09):
// AuthPlan values (AuthJWT, AuthJWTFromAssembly, AuthMTLS, etc.) are
// composition-root concerns and must only be constructed in cmd/ and
// examples/. Cells and runtime (except runtime/bootstrap/ which is the wiring
// layer) must not instantiate the concrete types — that would couple business
// logic to listener topology decisions.
//
// Scanned packages:
//   - all production packages under cells/ and runtime/ (the Production scope
//     already excludes generated/)
//   - runtime/bootstrap/... is the authorized wiring layer and is skipped
//
// Resolution model — typed (Hard upgrade, PR #615 review fix):
//
// Both branches resolve the symbol's defining package via go/types
// (*types.Info.Uses / TypeOf), NOT via the AST package-qualifier identifier:
//
//   - Constructor calls (`foo.NewAuthJWT(...)`): Uses[sel.Sel] returns a
//     *types.Func; we check fn.Pkg().Path() == authPlanPkgPath and
//     fn.Name() in {NewAuth*}.
//
//   - Composite literals (`foo.AuthMTLS{}`, `AuthMTLS{}` inside the auth
//     package itself): we walk the type-AST (SelectorExpr or Ident) to its
//     *ast.Ident, look up Uses[ident] to get a *types.TypeName, and check
//     tn.Pkg().Path() == authPlanPkgPath and tn.Name() in
//     authPlanConstructorNames.
//
// Why typed: parser-only + import-alias allowlist (the previous form) went
// vacuously empty after the kernel/cell → kernel/auth move because the check
// was `pkg.Name == "cell"`. Maintaining an explicit alias allow-list ({auth,
// kauth, …}) makes any new alias a silent bypass — the AI-robust gap the
// reviewer flagged. Resolving through go/types is alias-agnostic by
// construction: `import myauth "github.com/ghbvf/gocell/kernel/auth"` still
// resolves Uses[*ast.Ident{Name:"myauth"}] back to the kernel/auth package
// object whose Path() is authPlanPkgPath.
//
// Blind spots (documented per ai-robust.md "工具选定后强制盲区自检"):
//
//   - reflective construction via reflect.New(reflect.TypeOf(auth.AuthMTLS{}))
//     — the typed walk catches the auth.AuthMTLS{} composite literal, so this
//     form is covered transitively.
//   - method values / function pointers (e.g. `f := auth.NewAuthJWT`) — the
//     Uses[*ast.Ident] resolution still fires on the SelectorExpr.Sel of the
//     RHS, so the rule still flags the assignment.
//   - dot-import (`import . "github.com/ghbvf/gocell/kernel/auth"` then
//     `_ = AuthMTLS{}`) — the type-AST is then a bare *ast.Ident, and
//     Uses[ident] still resolves to the kernel/auth *types.TypeName. Covered.
//   - cgo / unsafe-pointer construction — not expressible for sealed
//     interface implementations like AuthPlan. Out of scope.
func TestAuthPlan_CellsMustNotConstructAuthPlans(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	diags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		relPkg := strings.TrimPrefix(p.Pkg.Path(), modPath+"/")
		if !isAuthPlanScannedPkg(relPkg) {
			return nil
		}
		origin := authPlanPkgOrigin(relPkg)

		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)

			scanner.EachInSubtree[ast.CompositeLit](file, func(node *ast.CompositeLit) {
				tn := resolveTypeNameForComposite(p.TypesInfo, node.Type)
				if tn == nil || tn.Pkg() == nil {
					return
				}
				if tn.Pkg().Path() != authPlanPkgPath {
					return
				}
				if _, forbidden := authPlanConstructorNames[tn.Name()]; !forbidden {
					return
				}
				pos := p.Fset.Position(node.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf("%s: %s constructs %s via composite literal (LAYER-09 violation)",
						authPlanRule04, origin, tn.Name()),
				})
			})

			scanner.EachInSubtree[ast.CallExpr](file, func(node *ast.CallExpr) {
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				fn, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
				if !ok || fn.Pkg() == nil {
					return
				}
				if fn.Pkg().Path() != authPlanPkgPath {
					return
				}
				if !strings.HasPrefix(fn.Name(), "NewAuth") {
					return
				}
				pos := p.Fset.Position(node.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf("%s: %s constructs %s via constructor call (LAYER-09 violation)",
						authPlanRule04, origin, fn.Name()),
				})
			})
		}
		return out
	})

	if len(diags) > 0 {
		for _, d := range diags {
			t.Logf("%s:%d: %s", d.Rel, d.Line, d.Message)
		}
	}
	assert.Empty(t, diags,
		"AUTH-PLAN-04 (LAYER-09): AuthPlan construction belongs in composition roots (cmd/, examples/, runtime/bootstrap/); "+
			"cells/ and runtime/ (non-bootstrap) must not instantiate AuthJWT / AuthJWTFromAssembly / AuthMTLS / etc.")
}

// isAuthPlanScannedPkg reports whether the package at relPkg (module-relative
// slash-path, e.g. "cells/accesscore/slices/setup") is in scope for AUTH-PLAN-04
// (i.e. under cells/ or runtime/ but not runtime/bootstrap/).
func isAuthPlanScannedPkg(relPkg string) bool {
	if strings.HasPrefix(relPkg, "runtime/bootstrap") {
		return false
	}
	return strings.HasPrefix(relPkg, "cells/") || strings.HasPrefix(relPkg, "runtime/")
}

// authPlanPkgOrigin returns the human-readable origin label used in
// AUTH-PLAN-04 diagnostics ("cells/" or "runtime/ (non-bootstrap)").
func authPlanPkgOrigin(relPkg string) string {
	if strings.HasPrefix(relPkg, "cells/") {
		return "cells/"
	}
	return "runtime/ (non-bootstrap)"
}

// resolveTypeNameForComposite walks the type-AST of a CompositeLit and returns
// the *types.TypeName it resolves to (or nil for built-in container literals
// like []T{}, map[K]V{}, struct literals with anonymous type). Handles both
// the qualified form (`pkg.Foo{}`) and the bare form (`Foo{}` — inside the
// owning package or under a dot-import).
func resolveTypeNameForComposite(info *types.Info, typ ast.Expr) *types.TypeName {
	if info == nil || typ == nil {
		return nil
	}
	var ident *ast.Ident
	switch t := typ.(type) {
	case *ast.SelectorExpr:
		ident = t.Sel
	case *ast.Ident:
		ident = t
	default:
		return nil
	}
	tn, _ := info.Uses[ident].(*types.TypeName)
	return tn
}

// ---------------------------------------------------------------------------
// Regression fixtures — prove the scanners have teeth
// ---------------------------------------------------------------------------

// TestAuthPlan_Fixtures_Rule01 ensures AUTH-PLAN-01 scanner fires on known bad input.
func TestAuthPlan_Fixtures_Rule01(t *testing.T) {
	src := `package fixture
var x = "jwt"
`
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)

	var found bool
	scanner.EachInSubtree[ast.BasicLit](af, func(bl *ast.BasicLit) {
		if bl.Kind == token.STRING && bl.Value == `"jwt"` {
			found = true
		}
	})
	assert.True(t, found, "fixture scanner must detect the literal \"jwt\"")
}

// TestAuthPlan_Fixtures_Rule02 ensures AUTH-PLAN-02 scanner fires on known bad input.
func TestAuthPlan_Fixtures_Rule02(t *testing.T) {
	src := `package fixture
import "example.com/bootstrap"
var _ = bootstrap.PolicyJWT(nil)
`
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)

	var found bool
	scanner.EachInSubtree[ast.SelectorExpr](af, func(sel *ast.SelectorExpr) {
		id, ok := sel.X.(*ast.Ident)
		if ok && id.Name == "bootstrap" && sel.Sel.Name == "PolicyJWT" {
			found = true
		}
	})
	assert.True(t, found, "fixture scanner must detect bootstrap.PolicyJWT")
}

// TestAuthPlan_Rule04_TypedResolverContract probes the AUTH-PLAN-04 typed
// resolver directly — it builds an in-memory go/types program with an
// arbitrarily-aliased kernel/auth import and asserts that both the composite-
// literal path and the constructor-call path resolve the symbol's owning
// package to authPlanPkgPath, regardless of the alias the caller chose.
//
// This is the smoking-gun regression for PR #615's reviewer finding: the old
// rule encoded a hand-maintained alias allow-list (`pkg.Name in {auth, kauth}`),
// so a future `import myauth "..."` would silently bypass LAYER-09. After the
// typed upgrade, alias choice is provably irrelevant — the assertions here run
// against an exotic alias (`myauth`) that the old form would never have caught.
func TestAuthPlan_Rule04_TypedResolverContract(t *testing.T) {
	t.Parallel()

	// Build a tiny in-memory program with two packages:
	//   1) a stand-in for kernel/auth declaring AuthMTLS + NewAuthJWT
	//   2) a consumer importing it under an arbitrary alias `myauth`
	const authSrc = `package auth

type IntentTokenVerifier interface{}

type AuthMTLS struct{}

func NewAuthJWT(v IntentTokenVerifier) (struct{}, error) { _ = v; return struct{}{}, nil }
`
	const consumerSrc = `package consumer

import myauth "stand-in/kernel/auth"

var _ = myauth.AuthMTLS{}
var _, _ = myauth.NewAuthJWT(nil)
`

	fset := token.NewFileSet()
	authFile, err := parser.ParseFile(fset, "stand-in/kernel/auth/auth.go", authSrc, parser.SkipObjectResolution)
	require.NoError(t, err)
	consumerFile, err := parser.ParseFile(fset, "consumer/consumer.go", consumerSrc, parser.SkipObjectResolution)
	require.NoError(t, err)

	importer := &fixtureImporter{
		pkgs: map[string]*types.Package{},
		files: map[string][]*ast.File{
			"stand-in/kernel/auth": {authFile},
		},
		fset: fset,
	}
	authPkg, err := importer.checkPackage("stand-in/kernel/auth")
	require.NoError(t, err)

	consumerInfo := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	conf := &types.Config{Importer: importer}
	_, err = conf.Check("consumer", fset, []*ast.File{consumerFile}, consumerInfo)
	require.NoError(t, err)

	// Composite-literal path: resolveTypeNameForComposite must return the
	// stand-in package's AuthMTLS, regardless of the alias used.
	var (
		mtlsTN  *types.TypeName
		jwtFunc *types.Func
	)
	scanner.EachInSubtree[ast.CompositeLit](consumerFile, func(node *ast.CompositeLit) {
		if tn := resolveTypeNameForComposite(consumerInfo, node.Type); tn != nil {
			mtlsTN = tn
		}
	})
	require.NotNil(t, mtlsTN, "typed resolver must surface AuthMTLS composite literal under alias 'myauth'")
	assert.Equal(t, "AuthMTLS", mtlsTN.Name())
	assert.Equal(t, authPkg.Path(), mtlsTN.Pkg().Path(),
		"composite literal type must resolve to the kernel/auth stand-in package regardless of alias")

	// Constructor-call path: TypesInfo.Uses[sel.Sel] must be the *types.Func
	// from the stand-in package.
	scanner.EachInSubtree[ast.CallExpr](consumerFile, func(node *ast.CallExpr) {
		sel, ok := node.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if fn, ok := consumerInfo.Uses[sel.Sel].(*types.Func); ok {
			jwtFunc = fn
		}
	})
	require.NotNil(t, jwtFunc, "typed resolver must surface NewAuthJWT call under alias 'myauth'")
	assert.Equal(t, "NewAuthJWT", jwtFunc.Name())
	assert.Equal(t, authPkg.Path(), jwtFunc.Pkg().Path(),
		"constructor call must resolve to the kernel/auth stand-in package regardless of alias")
}

// fixtureImporter is a minimal types.Importer that checks named packages from
// in-memory ast files. Only used by TestAuthPlan_Rule04_TypedResolverContract;
// the path namespace is rooted at "stand-in/" so it never collides with the
// real module's packages.
type fixtureImporter struct {
	pkgs  map[string]*types.Package
	files map[string][]*ast.File
	fset  *token.FileSet
}

func (i *fixtureImporter) Import(path string) (*types.Package, error) {
	if p, ok := i.pkgs[path]; ok {
		return p, nil
	}
	return i.checkPackage(path)
}

func (i *fixtureImporter) checkPackage(path string) (*types.Package, error) {
	files, ok := i.files[path]
	if !ok {
		return nil, fmt.Errorf("fixtureImporter: no source for %q", path)
	}
	conf := &types.Config{Importer: i}
	pkg, err := conf.Check(path, i.fset, files, nil)
	if err != nil {
		return nil, err
	}
	i.pkgs[path] = pkg
	return pkg, nil
}

// TestAuthPlan_Fixtures_Rule03 ensures AUTH-PLAN-03 scanner fires on known bad input.
func TestAuthPlan_Fixtures_Rule03(t *testing.T) {
	src := `package fixture
import "example.com/cell"
var _ = cell.Policy{}
`
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)

	var found bool
	scanner.EachInSubtree[ast.CompositeLit](af, func(lit *ast.CompositeLit) {
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && id.Name == "cell" && sel.Sel.Name == "Policy" {
			found = true
		}
	})
	assert.True(t, found, "fixture scanner must detect cell.Policy{} composite literal")
}

// ---------------------------------------------------------------------------
// File-finding helpers
// ---------------------------------------------------------------------------

// findAllProductionGoFiles returns all non-test .go files under root,
// excluding vendor/, .git/, generated/, testdata/, worktrees/ directories and *_test.go.
func findAllProductionGoFiles(root string) ([]string, error) {
	return scanner.ModuleScope(root).Files()
}

// findProductionGoFilesInDir returns production .go files under a specific dir.
// dir must be an absolute path under root (module root).
func findProductionGoFilesInDir(dir string) ([]string, error) {
	root := moduleRootOf(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return nil, err
	}
	return scanner.DirsScope(root, []string{filepath.ToSlash(rel)}).Files()
}

// moduleRootOf walks up from dir to find the nearest go.mod file and returns
// the directory containing it. This avoids threading root through every caller.
func moduleRootOf(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := filepath.EvalSymlinks(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			panic("moduleRootOf: go.mod not found above " + dir)
		}
	}
}
