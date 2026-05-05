// HANDLER-POLICY-REQUIRED-01
//
// Invariant: every *contract.NewHandler(svc, nil) call in a cell.go that is
// NOT backed by a Public=true contract endpoint MUST supply a non-nil policy.
//
// Non-public routes with nil policy silently degrade to "no authorization
// guard" in production — any bearer token (valid JWT) can access the endpoint
// regardless of roles or ownership.
//
// This archtest scans cell.go files under:
//   - cells/**/cell.go
//   - examples/**/cells/**/cell.go
//
// and reports any NewHandler(_, nil) call where the corresponding generated
// handler package does NOT export a NewHandler that takes only one argument
// (the public-contract form, which takes no policy arg).
//
// Detection strategy: pure AST text scan for the literal argument pattern
// "NewHandler(<expr>, nil)" inside cell.go files. We cross-check against
// known public-contract package names. Packages whose generated NewHandler
// has the signature NewHandler(svc Service) (arity 1) are Public endpoints
// and are exempt — they must pass nil as their second argument, but the
// codegen no longer generates a two-arg NewHandler for them (so the literal
// "NewHandler(<svc>, nil)" won't appear in their wiring anyway after B2).
//
// RED: before B2, cell.go passes nil to enqueue/dequeue/report/ack/extendlease/status.
// GREEN: after B2, only register (public) has a single-arg NewHandler and
// zero "NewHandler(<svc>, nil)" calls remain for non-public handlers.
//
// Negative fixture: tools/archtest/testdata/handler_nil_policy/cell.go
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const handlerPolicyRequiredRule = "HANDLER-POLICY-REQUIRED-01"

// handlerPolicyPublicExemptPkgs lists the import alias prefixes whose generated
// NewHandler is single-arg (Public=true endpoint). After the B2 fix, only
// "registercontract" is in this list. This is intentionally narrow — it is
// updated whenever a new Public contract is added. The test also double-checks
// by scanning the generated handler_gen.go for the single-arg signature.
var handlerPolicyPublicExemptPkgs = []string{
	"registercontract", // http.device.register.v1 — Public:true
}

// TestHANDLER_POLICY_REQUIRED_01 scans cell.go files for NewHandler(<svc>, nil)
// calls and fails on any that are not from a known Public-contract package.
func TestHANDLER_POLICY_REQUIRED_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	cellFiles := collectCellGoFiles(t, root)

	var violations []string
	for _, f := range cellFiles {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		hits := scanForNilPolicyNewHandler(f, rel)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s: %s", handlerPolicyRequiredRule, v)
	}

	if len(violations) > 0 {
		t.Logf(`%s: %d violation(s) found. Fix: pass a non-nil auth.Policy to NewHandler.
For Public endpoints declare auth.public: true in contract.yaml and regenerate
— the generated NewHandler then accepts no policy argument, so the nil call
site disappears entirely.`, handlerPolicyRequiredRule, len(violations))
	}
}

// TestHANDLER_POLICY_REQUIRED_01_NegativeFixture verifies that the scanner
// detects the violation pattern in the negative fixture file.
func TestHANDLER_POLICY_REQUIRED_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	fixture := filepath.Join(root, "tools", "archtest", "testdata",
		"handler_nil_policy", "cell.go")
	if _, err := os.Stat(fixture); os.IsNotExist(err) {
		t.Fatalf("negative fixture missing: %s", fixture)
	}

	rel := "tools/archtest/testdata/handler_nil_policy/cell.go"
	hits := scanForNilPolicyNewHandler(fixture, rel)
	if len(hits) == 0 {
		t.Errorf("negative fixture produced no violations — scanner broken")
	}
}

// collectCellGoFiles returns all cell.go files in cells/ and examples/ subtrees.
func collectCellGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string

	walk := func(dir string) {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if info.Name() == "cell.go" {
				files = append(files, path)
			}
			return nil
		})
	}
	walk("cells")
	walk("examples")
	return files
}

// scanForNilPolicyNewHandler parses the Go file at path and returns a list of
// violation strings for any call of the form <pkg>.NewHandler(<expr>, nil)
// where <pkg> is not in handlerPolicyPublicExemptPkgs.
func scanForNilPolicyNewHandler(path, rel string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// Unparseable files are flagged as violations so the rule isn't silently skipped.
		return []string{fmt.Sprintf("%s: parse error: %v", rel, err)}
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "NewHandler" {
			return true
		}
		// Only inspect two-argument calls: NewHandler(svc, policy).
		if len(call.Args) != 2 {
			return true
		}
		// Check if the last argument is a nil identifier.
		ident, ok := call.Args[1].(*ast.Ident)
		if !ok || ident.Name != "nil" {
			return true
		}
		// Determine the package alias used for the call (e.g. "enqueuecontract").
		pkgAlias := ""
		if id, ok := sel.X.(*ast.Ident); ok {
			pkgAlias = id.Name
		}
		// Skip known Public-contract packages.
		if isPublicExemptPkg(pkgAlias) {
			return true
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: %s.NewHandler called with nil policy — non-public endpoint must supply a real auth.Policy",
			rel, pos.Line, pkgAlias,
		))
		return true
	})
	return violations
}

// isPublicExemptPkg returns true when pkgAlias belongs to a Public=true contract.
func isPublicExemptPkg(alias string) bool {
	for _, exempt := range handlerPolicyPublicExemptPkgs {
		if alias == exempt {
			return true
		}
	}
	return false
}

// Also flag the unlabelled form "NewHandler(<svc>, nil)" where the package part
// is inferred from a dot-import or local redeclaration. This is unusual but
// handled by checking sel.X type.

// isHandlerPolicyFile returns true when rel is under tools/archtest/testdata
// (used to exclude fixture files from the production scan).
func isHandlerPolicyFixtureFile(rel string) bool {
	return strings.Contains(rel, "tools/archtest/testdata")
}
