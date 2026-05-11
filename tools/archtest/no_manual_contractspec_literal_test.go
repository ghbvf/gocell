// INVARIANT: NO-MANUAL-CONTRACTSPEC-LITERAL-01
//   - INVARIANT: CONTRACTSPEC-FRAMEWORK-BUILDERS-EXIST-01
//
// # NO-MANUAL-CONTRACTSPEC-LITERAL-01
//
// Invariant: contractspec.ContractSpec{…} composite literals and
// contractspec.EventSpec(…) call expressions must only appear in:
//   - generated/contracts/**/*_gen.go — business contract specs (codegen output)
//   - kernel/contractspec/** itself  — type definition + typed funnels
//     (NewFrameworkHTTP / NewEventDerivation, see framework.go)
//
// Hand-written production code under cells/, examples/**/cells/, and runtime/
// must NOT define ContractSpec literals. Framework-owned HTTP infra (health
// probes, devtools catalog) and event-tracing derivations use the typed
// funnels in kernel/contractspec/framework.go — these are the only legitimate
// runtime-side construction paths and have no allowlist exception.
//
// Exclusions:
//   - generated/contracts/**/*_gen.go  — the authoritative home for business contracts
//   - tools/codegen/**/testdata/**     — codegen fixture files
//   - **/fixtures/**                   — test fixture trees
//   - kernel/contractspec/** itself    — defines ContractSpec and the typed funnels
//   - *_test.go                        — test helpers may reference specs for assertions
//
// AI-rebust: Hard — composite literal under cells/ + examples/ + runtime/
// is unrepresentable (archtest fails CI); the typed funnels are the only
// form that survives. Aligns with the "typed function call as Hard funnel
// for unbounded operations" charter pattern (PANIC-REGISTERED-01 same path).
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#PR4 W3 + G-04
// ref: kernel/contractspec/spec.go construction-site catalog
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

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestNO_MANUAL_CONTRACTSPEC_LITERAL_01 scans production .go files under
// cells/, examples/*/cells/, and runtime/ for contractspec.ContractSpec{…}
// composite literals and contractspec.EventSpec(…) call expressions,
// failing on any found. The typed funnels in kernel/contractspec/framework.go
// are the only legitimate runtime-side construction paths.
func TestNO_MANUAL_CONTRACTSPEC_LITERAL_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files := collectContractSpecScanFiles(t, root)

	var violations []string
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		hits := scanForContractSpecLiterals(token.NewFileSet(), f, rel)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("NO-MANUAL-CONTRACTSPEC-LITERAL-01: %s", v)
	}
}

// TestCONTRACTSPEC_FRAMEWORK_BUILDERS_EXIST_01 locks the typed funnel API.
// If kernel/contractspec.NewFrameworkHTTP or NewEventDerivation are renamed
// or removed, this test fails to compile — signaling that the Hard gate has
// lost its only legitimate runtime-side construction path and must be
// revisited (either redirect callers to a new funnel or update this archtest).
func TestCONTRACTSPEC_FRAMEWORK_BUILDERS_EXIST_01(t *testing.T) {
	t.Parallel()
	_ = contractspec.NewFrameworkHTTP("http.framework.test.v1", "GET", "/test")
	_ = contractspec.NewEventDerivation("event.test.v1", cellvocab.ContractEvent, "amqp", "test.topic")
}

// collectContractSpecScanFiles returns production .go files to scan.
// Scope: cells (top-level cells/ + examples/*/cells/) discovered via
// findCellProductionGoFiles (metadata-driven), plus runtime/ via DirsScope
// directory walk. kernel/contractspec itself owns the ContractSpec type
// definition and the typed funnels (NewFrameworkHTTP / NewEventDerivation),
// so it is intentionally outside this scope. *_gen.go files are excluded
// from the unioned set.
func collectContractSpecScanFiles(t *testing.T, root string) []string {
	t.Helper()
	cellFiles, err := findCellProductionGoFiles(root)
	if err != nil {
		t.Fatalf("metadata.NewParser: %v", err)
	}
	runtimeFiles, err := scanner.DirsScope(root, []string{"runtime"}).Files()
	if err != nil {
		t.Fatalf("scanner.DirsScope(runtime): %v", err)
	}
	seen := make(map[string]struct{}, len(cellFiles)+len(runtimeFiles))
	out := make([]string, 0, len(cellFiles)+len(runtimeFiles))
	for _, f := range append(append([]string{}, cellFiles...), runtimeFiles...) {
		if strings.HasSuffix(f, "_gen.go") {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_NegativeFixture verifies that the
// scanner correctly identifies a contractspec.ContractSpec{} literal in a
// hand-written file. The fixture in testdata/no_manual_contractspec_literal/
// contains a deliberate violation.
func TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "no_manual_contractspec_literal", "violates", "handler.go"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	// Simulate the relative path as it would appear under cells/.
	rel := "cells/fake/slices/bad/handler.go"
	violations := scanForContractSpecLiterals(token.NewFileSet(), fixturePath, rel)
	if len(violations) == 0 {
		t.Errorf("expected at least 1 violation for fixture with manual ContractSpec literal, got 0")
	}
	// Verify the violation message is informative.
	for _, v := range violations {
		if !strings.Contains(v, "ContractSpec") {
			t.Errorf("violation message should mention ContractSpec: %q", v)
		}
	}
}

// scanForContractSpecLiterals AST-scans f for:
//  1. contractspec.ContractSpec{…} composite literals
//  2. contractspec.EventSpec(…) call expressions
//
// where "contractspec" is the local alias for kernel/contractspec.
func scanForContractSpecLiterals(fset *token.FileSet, path, rel string) []string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil // syntax errors handled by build
	}

	alias := contractspecLocalAlias(f)
	if alias == "" {
		return nil // file does not import kernel/contractspec
	}

	var violations []string
	// Match contractspec.ContractSpec{…} composite literals.
	scanner.EachInSubtree[ast.CompositeLit](f, func(node *ast.CompositeLit) {
		sel, ok := node.Type.(*ast.SelectorExpr)
		if !ok {
			return
		}
		ident, ok2 := sel.X.(*ast.Ident)
		if !ok2 || ident.Name != alias || sel.Sel.Name != "ContractSpec" {
			return
		}
		pos := fset.Position(node.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: manual %s.ContractSpec{} literal — must be in generated/contracts/**/*_gen.go only",
			rel, pos.Line, alias,
		))
	})
	// Match contractspec.EventSpec(…) call expressions.
	scanner.EachInSubtree[ast.CallExpr](f, func(node *ast.CallExpr) {
		sel, ok := node.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		ident, ok2 := sel.X.(*ast.Ident)
		if !ok2 || ident.Name != alias || sel.Sel.Name != "EventSpec" {
			return
		}
		pos := fset.Position(node.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: manual %s.EventSpec() call — must be in generated/contracts/**/*_gen.go only",
			rel, pos.Line, alias,
		))
	})
	return violations
}
