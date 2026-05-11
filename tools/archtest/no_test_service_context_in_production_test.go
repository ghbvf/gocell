// INVARIANT: NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01
//
// # NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01
//
// Invariant: auth.TestServiceContext(...) must only appear in _test.go files
// or files whose name contains "_test_" (i.e. files under test helper packages).
// Calling TestServiceContext in production code bypasses service-token
// authentication and would silently skip the HMAC guard in non-test builds.
//
// Detection: AST walk of all .go files under runtime/, cells/, cmd/, kernel/,
// adapters/, examples/, and tests/, scanning for auth.TestServiceContext(...)
// call expressions. Files ending in _test.go or containing "_test_" in their
// base name are excluded from the check.
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

const ruleNoTestServiceContextInProduction = "NO-TEST-SERVICE-CONTEXT-IN-PRODUCTION-01"

// TestNO_TEST_SERVICE_CONTEXT_IN_PRODUCTION_01 enforces that
// auth.TestServiceContext is never called from non-test production code.
func TestNO_TEST_SERVICE_CONTEXT_IN_PRODUCTION_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	// All production roots — non-test code anywhere in the repo must not
	// call auth.TestServiceContext. Direct walk is correct here because the
	// rule scope is wider than cell-managed code (e.g., examples/ssobff/
	// is non-cell example code, examples/iotdevice/{auth.go,main.go} are
	// non-cell wiring code). The scanner skips _test.go in-line so test
	// helpers can call TestServiceContext freely.
	searchDirs := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "cells"),
		filepath.Join(root, "cmd"),
		filepath.Join(root, "kernel"),
		filepath.Join(root, "adapters"),
		filepath.Join(root, "examples"),
		filepath.Join(root, "tests"),
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
			// Skip _test.go files and test helper files.
			if strings.HasSuffix(base, "_test.go") || strings.Contains(base, "_test_") {
				continue
			}

			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)

			hits, scanErr := scanTestServiceContextCalls(f, rel)
			require.NoError(t, scanErr)
			violations = append(violations, hits...)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	if len(violations) > 0 {
		t.Errorf("%s: %d auth.TestServiceContext calls found in non-test production files.\n"+
			"auth.TestServiceContext is a test helper that bypasses HMAC guard.\n"+
			"It must only appear in _test.go files.",
			ruleNoTestServiceContextInProduction, len(violations))
	}
}

// scanTestServiceContextCalls parses a single .go file and returns violation
// strings for every auth.TestServiceContext(...) call expression.
func scanTestServiceContextCalls(path, rel string) ([]string, error) {
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
		if sel.Sel.Name != "TestServiceContext" {
			return
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != "auth" {
			return
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: auth.TestServiceContext called in non-test file — move to _test.go",
			rel, pos.Line))
	})
	return violations, nil
}
