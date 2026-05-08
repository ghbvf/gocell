package archtest

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
var authPlanConstructorNames = []string{
	"AuthJWT",
	"AuthJWTFromAssembly",
	"AuthMTLS",
	"AuthNone",
	"AuthServiceToken",
}

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
	"kernel/cell/auth_plan.go",
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
		ast.Inspect(af, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
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
			return true
		})
	}

	if len(hits) > 0 {
		for _, h := range hits {
			t.Logf("%s: %s:%d: forbidden policy string literal %s", authPlanRule01, h.file, h.line, h.val)
		}
	}
	assert.Empty(t, hits,
		"AUTH-PLAN-01: string literals %v are old cell.Policy.Name discriminators; "+
			"use typed AuthPlan (cell.NewAuthJWT / cell.AuthMTLS{} / …) instead. "+
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
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if id.Name != "bootstrap" {
				return true
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
			return true
		})
	}

	if len(hits) > 0 {
		for _, h := range hits {
			t.Logf("%s: %s:%d: forbidden selector %s", authPlanRule02, h.file, h.line, h.sel)
		}
	}
	assert.Empty(t, hits,
		"AUTH-PLAN-02: bootstrap.Policy* factory functions have been deleted; "+
			"use []cell.ListenerAuth{cell.NewAuthJWT(v)} / cell.NewAuthJWTFromAssembly(asm) / … instead")
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
		ast.Inspect(af, func(n ast.Node) bool {
			// Composite literal: cell.Policy{...}
			if lit, ok := n.(*ast.CompositeLit); ok {
				if sel, ok := lit.Type.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "cell" && sel.Sel.Name == "Policy" {
						hits = append(hits, hit{
							file: rel,
							line: fset.Position(lit.Pos()).Line,
							kind: "composite literal",
						})
					}
				}
				return true
			}
			// Selector expression used as a type: cell.Policy (non-composite, e.g. in var decl or param)
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "cell" && sel.Sel.Name == "Policy" {
					hits = append(hits, hit{
						file: rel,
						line: fset.Position(sel.Pos()).Line,
						kind: "type reference",
					})
				}
			}
			return true
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
		"AUTH-PLAN-03: cell.Policy was deleted in PR262; use []cell.ListenerAuth instead")
}

// ---------------------------------------------------------------------------
// AUTH-PLAN-04 (LAYER-09): cells/ must not construct AuthPlan values
// ---------------------------------------------------------------------------

// TestAuthPlan_CellsMustNotConstructAuthPlans enforces AUTH-PLAN-04 (LAYER-09):
// AuthPlan values (AuthJWT, AuthJWTFromAssembly, AuthMTLS, etc.) are composition-
// root concerns and must only be constructed in cmd/ and examples/. Cells and
// runtime (except runtime/bootstrap/ which is the wiring layer) must not
// instantiate the concrete types — that would couple business logic to listener
// topology decisions.
//
// Scanned directories:
//   - cells/       — business cell implementations
//   - runtime/     — shared runtime (except runtime/bootstrap/ which is the
//     composition wiring layer and is explicitly allowed)
//
// The scan covers both composite literals (cell.AuthJWT{}) and constructor
// function calls (cell.NewAuthJWT(...), cell.NewAuthMTLS(), etc.).
func TestAuthPlan_CellsMustNotConstructAuthPlans(t *testing.T) {
	root := findModuleRoot(t)

	// Collect files from cells/ and runtime/ (excluding runtime/bootstrap/).
	cellsDir := filepath.Join(root, "cells")
	runtimeDir := filepath.Join(root, "runtime")

	var files []string
	for _, dir := range []string{cellsDir, runtimeDir} {
		ff, err := findProductionGoFilesInDir(dir)
		require.NoError(t, err)
		files = append(files, ff...)
	}

	// runtime/bootstrap/ is the authorized composition-wiring layer.
	bootstrapPrefix := filepath.ToSlash(filepath.Join(root, "runtime", "bootstrap")) + "/"

	type hit struct {
		file string
		line int
		name string
		kind string // "composite literal" or "constructor call"
	}
	var hits []hit

	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)

		// Skip runtime/bootstrap/ — it is the wiring layer and is allowed.
		if strings.HasPrefix(filepath.ToSlash(f), bootstrapPrefix) {
			continue
		}

		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				// Composite literal: cell.AuthJWT{} / AuthJWT{...}
				typeName := ""
				switch t := node.Type.(type) {
				case *ast.SelectorExpr:
					if id, ok := t.X.(*ast.Ident); ok && id.Name == "cell" {
						typeName = t.Sel.Name
					}
				case *ast.Ident:
					typeName = t.Name
				}
				for _, forbidden := range authPlanConstructorNames {
					if typeName == forbidden {
						hits = append(hits, hit{
							file: rel,
							line: fset.Position(node.Pos()).Line,
							name: typeName,
							kind: "composite literal",
						})
					}
				}

			case *ast.CallExpr:
				// Constructor calls: cell.NewAuthJWT(...) / cell.NewAuthJWTFromAssembly(...)
				// etc. These are SelectorExpr call expressions where the package is "cell".
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "cell" {
					return true
				}
				// Constructor naming convention: New<TypeName> or <TypeName>{} literal.
				// Check for NewAuth* constructors.
				name := sel.Sel.Name
				if strings.HasPrefix(name, "NewAuth") {
					hits = append(hits, hit{
						file: rel,
						line: fset.Position(node.Pos()).Line,
						name: name,
						kind: "constructor call",
					})
				}
			}
			return true
		})
	}

	if len(hits) > 0 {
		for _, h := range hits {
			t.Logf("%s: %s:%d: %s constructs %s via %s (LAYER-09 violation)",
				authPlanRule04, h.file, h.line,
				func() string {
					if strings.HasPrefix(h.file, "cells/") {
						return "cells/"
					}
					return "runtime/ (non-bootstrap)"
				}(),
				h.name, h.kind)
		}
	}
	assert.Empty(t, hits,
		"AUTH-PLAN-04 (LAYER-09): AuthPlan construction belongs in composition roots (cmd/, examples/, runtime/bootstrap/); "+
			"cells/ and runtime/ (non-bootstrap) must not instantiate AuthJWT / AuthJWTFromAssembly / AuthMTLS / etc.")
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
	ast.Inspect(af, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		if bl.Value == `"jwt"` {
			found = true
		}
		return true
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
	ast.Inspect(af, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && id.Name == "bootstrap" && sel.Sel.Name == "PolicyJWT" {
			found = true
		}
		return true
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
	ast.Inspect(af, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && id.Name == "cell" && sel.Sel.Name == "Policy" {
			found = true
		}
		return true
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
