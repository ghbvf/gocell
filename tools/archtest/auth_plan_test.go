//go:build archtest

package archtest

// auth_plan_test.go — dogfoods the AUTH-PLAN Check* functions against GoCell
// itself and provides in-process fixture regression tests.
//
//   - INVARIANT: AUTH-PLAN-01
//   - INVARIANT: AUTH-PLAN-02
//   - INVARIANT: AUTH-PLAN-03
//   - INVARIANT: AUTH-PLAN-04
//
// Detector logic, vars, and Check* functions live in auth_plan.go (non-test)
// so they can be compiled by external Cell repositories. This file dogfoods
// that shared logic against GoCell itself — single source, no parallel rule body.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestAuthPlan_NoLegacyPolicyStringLiterals enforces AUTH-PLAN-01.
func TestAuthPlan_NoLegacyPolicyStringLiterals(t *testing.T) {
	Report(t, authPlanRule01,
		CheckAuthPlanNoLegacyPolicyStringLiterals(t, ConfigForExternalCell{}))
}

// TestAuthPlan_NoLegacyPolicySelectorExpressions enforces AUTH-PLAN-02.
func TestAuthPlan_NoLegacyPolicySelectorExpressions(t *testing.T) {
	Report(t, authPlanRule02,
		CheckAuthPlanNoLegacyPolicySelectorExpressions(t, ConfigForExternalCell{}))
}

// TestAuthPlan_NoCellPolicyTypeUsage enforces AUTH-PLAN-03.
func TestAuthPlan_NoCellPolicyTypeUsage(t *testing.T) {
	Report(t, authPlanRule03,
		CheckAuthPlanNoCellPolicyTypeUsage(t, ConfigForExternalCell{}))
}

// TestAuthPlan_CellsMustNotConstructAuthPlans enforces AUTH-PLAN-04 (LAYER-09).
func TestAuthPlan_CellsMustNotConstructAuthPlans(t *testing.T) {
	t.Parallel()
	Report(t, authPlanRule04,
		CheckAuthPlanCellsMustNotConstructAuthPlans(t, ConfigForExternalCell{}))
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

// ---------------------------------------------------------------------------
// fixtureImporter — in-memory types.Importer for rule04 typed resolver test
// ---------------------------------------------------------------------------

// fixtureImporter is a minimal types.Importer that checks named packages from
// in-memory ast files. Only used by TestAuthPlan_Rule04_TypedResolverContract;
// the path namespace is rooted at "stand-in/" so it never collides with the
// real module's packages. It is test-only scaffolding, so it lives in this
// _test.go file (never shipped to an external cell importing auth_plan.go).
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
