//go:build archtest

// INVARIANT: NO-TEST-DEVICE-PRINCIPAL-IN-PRODUCTION-01
//
// # NO-TEST-DEVICE-PRINCIPAL-IN-PRODUCTION-01
//
// Invariant: auth.MustNewTestDevicePrincipal(...) must only appear in _test.go
// files or files whose name contains "_test_" (i.e. files under test helper
// packages). It is an EXPORTED helper that delegates to the unexported
// mintDevicePrincipal and therefore produces a fully sealed PrincipalDevice
// WITHOUT going through JWT verification. Calling it in production code would
// forge a verified device identity and bypass the device-token authn boundary.
//
// Why this guard is the closure for DEVICE-PRINCIPAL-MINT-CALLER-01: the seal is
// Hard (the unexported deviceSeal type + Principal.device field cannot be set
// from outside runtime/auth), so package-external code cannot FABRICATE a seal by
// struct literal. But the test helper is exported precisely so external test
// packages (corecells, tests/integration) can build a sealed device principal
// without the full Issue→AuthenticateBearer chain — which means external code
// CAN obtain a seal by CALLING it. This rule pins that exported escape hatch to
// non-production code, so the only production path to a sealed device principal
// remains mintDevicePrincipal via the verified bearer path. Same posture +
// permanent Go-language ceiling as NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01
// (TestServiceContext bypasses the service-token HMAC guard identically); the
// helper cannot live outside package auth (it needs the unexported minter) nor
// in _test.go (external packages must see it), so a Medium archtest is the
// ceiling and the seal stays the Hard control.
//
// Detection: AST walk of all .go files under runtime/, cmd/, kernel/, adapters/,
// examples/, tests/, and the platform-cell roots, scanning for
// auth.MustNewTestDevicePrincipal(...) call expressions. Files ending in _test.go
// or containing "_test_" in their base name are excluded.
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

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleNoTestDevicePrincipalInProduction = "NO-TEST-DEVICE-PRINCIPAL-IN-PRODUCTION-01"

// TestNO_TEST_DEVICE_PRINCIPAL_IN_PRODUCTION_01 enforces that
// auth.MustNewTestDevicePrincipal is never called from non-test production code.
func TestNO_TEST_DEVICE_PRINCIPAL_IN_PRODUCTION_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	// All production roots — non-test code anywhere in the repo must not call
	// auth.MustNewTestDevicePrincipal. Mirrors the search scope of
	// NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01 (the scanner skips _test.go in-line
	// so test helpers call it freely). Platform cells route through
	// platformCellScanDirs (single source for the on-disk scan root).
	searchDirs := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "cmd"),
		filepath.Join(root, "kernel"),
		filepath.Join(root, "adapters"),
		filepath.Join(root, "examples"),
		filepath.Join(root, "tests"),
	}
	for _, d := range platformCellScanDirs() {
		searchDirs = append(searchDirs, filepath.Join(root, d))
	}

	var violations []string
	for _, dir := range searchDirs {
		allFiles, err := findAllGoFilesInDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
		for _, f := range allFiles {
			base := filepath.Base(f)
			if strings.HasSuffix(base, "_test.go") || strings.Contains(base, "_test_") {
				continue
			}

			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)

			hits, scanErr := scanTestDevicePrincipalCalls(f, rel)
			require.NoError(t, scanErr)
			violations = append(violations, hits...)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	if len(violations) > 0 {
		t.Errorf("%s: %d auth.MustNewTestDevicePrincipal calls found in non-test production files.\n"+
			"auth.MustNewTestDevicePrincipal is a test helper that forges a sealed device principal\n"+
			"without JWT verification. It must only appear in _test.go files.",
			ruleNoTestDevicePrincipalInProduction, len(violations))
	}
}

// scanTestDevicePrincipalCalls parses a single .go file and returns violation
// strings for every auth.MustNewTestDevicePrincipal(...) call expression.
func scanTestDevicePrincipalCalls(path, rel string) ([]string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var violations []string
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name != "MustNewTestDevicePrincipal" {
			return
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != "auth" {
			return
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: auth.MustNewTestDevicePrincipal called in non-test file — move to _test.go",
			rel, pos.Line,
		))
	})
	return violations, nil
}
