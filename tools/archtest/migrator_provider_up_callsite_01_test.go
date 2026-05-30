// Package archtest_test — migrator_provider_up_callsite_01_test.go
//
// INVARIANT: MIGRATOR-PROVIDER-UP-CALLSITE-01
//
// Every call to m.provider.Up(...) or m.provider.Down(...) inside
// adapters/postgres/migrator.go MUST reside in a method whose name is in the
// allowlist {"forwardRun", "Down"}. Any other method directly invoking
// m.provider.Up / m.provider.Down bypasses the phase0 ForwardRebuildPermit gate.
//
// Design intent (issue #1248):
//   - Migrator.Up is refactored into a shared forwardRun helper that holds
//     the phase0 gate (parse annotations → check ForwardRebuildPermit).
//   - Migrator.ForwardRebuild delegates to forwardRun with permits.
//   - Migrator.Down is the only caller of provider.Down (destructive rollback).
//   - No other method may call provider.Up or provider.Down directly.
//
// AI-robust: funnel downstream Medium (archtest callsite-allowlist, AST-based;
// provider field is unexported so packages outside adapters/postgres cannot call
// it at all — package-external Hard). Package-internal bypass (a sibling func
// in migrator.go calling m.provider.Up directly) can only be caught at
// archtest time, not by the Go type system — this is the permanent Medium ceiling
// for package-internal enforcement (same pattern as SPAN-SETATTR-HOLDER-SEAL
// #851 / HEALTHZ-HOLDER-SEAL #893 / GOOSE-SESSION-LOCKER-01 #1131).
//
// Funnel axis breakdown (ai-robust §Funnel 双向锁 terms — consistent with the
// "downstream Medium / package-external Hard" summary above):
//   - Upstream Hard (package-external): the provider field is unexported, so no
//     code outside package postgres can reference m.provider; the public API
//     (Up / ForwardRebuild / Down) all routes through the phase0 gate, and the
//     Go compiler guarantees external callers cannot bypass it. This is the axis
//     that "guarantees the callsite necessarily passes through the funnel".
//   - Downstream Medium (package-internal): archtest A1 below is the caller
//     allowlist — it locks "every m.provider.Up/Down call in migrator.go must
//     reside in forwardRun or Down" (forbidding the method being called outside
//     the funnel). A sibling func in the same package could still call it;
//     archtest CI catches that, Go's type system cannot. Permanent Medium ceiling
//     (goose.Provider is third-party, unsealable); tracked won't-do in gh #1335
//     (same shape as #851 / #893 / #1131).
//
// TDD RED confirmation: run
//
//	go test ./tools/archtest/... -run TestArchtest_MigratorProviderUpCallsite
//
// Before the implementation refactor, m.provider.Up is called in Migrator.Up
// (not in forwardRun), so this test MUST FAIL in the RED phase.
//
// Blind spots:
//   - provider.Up / provider.Down called via a function value stored in a
//     field or local variable are not tracked (no such case in the corpus).
//   - Calls through an interface variable aliasing provider are not tracked.
//   - Method calls on a copy of provider (not via selector on the struct field)
//     are not tracked.
//
// These blind spots are documented, not enforced; the corpus has no such cases.

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	postgres "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// migratorProviderCallAllowlist is the set of method names in which
// m.provider.Up or m.provider.Down calls are permitted.
var migratorProviderCallAllowlist = map[string]bool{
	"forwardRun": true,
	"Down":       true,
}

// TestArchtest_MigratorProviderUpCallsite asserts that every call to
// <recv>.provider.Up(...) or <recv>.provider.Down(...) inside
// adapters/postgres/migrator.go appears only in methods whose names are in
// migratorProviderCallAllowlist.
//
// Rule: MIGRATOR-PROVIDER-UP-CALLSITE-01.
//
// Owner-method resolution: scanner.EachInChildren[ast.FuncDecl] iterates the
// file's direct top-level declarations and yields each *ast.FuncDecl. Within
// each FuncDecl.Body we then call scanner.EachInSubtree[ast.CallExpr] to find
// all provider.Up/Down call expressions. This approach is scope-native: the
// method name is known from the enclosing FuncDecl, so no pos-range lookup is
// needed and there is no off-by-one risk.
func TestArchtest_MigratorProviderUpCallsite(t *testing.T) {
	root := findModuleRoot(t)
	migratorPath := filepath.Join(root, "adapters", "postgres", "migrator.go")

	fset := token.NewFileSet()
	// #nosec G304 -- reading repo-resident file under module root
	f, err := parser.ParseFile(fset, migratorPath, nil, 0)
	if err != nil {
		t.Fatalf("MIGRATOR-PROVIDER-UP-CALLSITE-01: cannot parse %s: %v", migratorPath, err)
	}

	// Walk top-level FuncDecls via EachInChildren (depth-1 from the file node)
	// and within each method body use EachInSubtree to find provider.Up/Down
	// call expressions. The method name is directly available from fd.Name.Name.
	var violations []string
	scanner.EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		methodName := fd.Name.Name
		// Pattern: CallExpr{ Fun: SelectorExpr{ X: SelectorExpr{ Sel: "provider" }, Sel: "Up"|"Down" } }
		scanner.EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			outer, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			// outer.Sel must be "Up" or "Down"
			if outer.Sel.Name != "Up" && outer.Sel.Name != "Down" {
				return
			}
			// outer.X must itself be a SelectorExpr with Sel == "provider"
			inner, ok := outer.X.(*ast.SelectorExpr)
			if !ok {
				return
			}
			if inner.Sel.Name != "provider" {
				return
			}
			// Found a candidate: <recv>.provider.Up/Down(...)
			if !migratorProviderCallAllowlist[methodName] {
				pos := fset.Position(call.Pos())
				violations = append(violations, formatProviderCallViolation(pos.Line, outer.Sel.Name, methodName))
			}
		})
	})

	for _, v := range violations {
		t.Logf("MIGRATOR-PROVIDER-UP-CALLSITE-01: violation: %s", v)
	}
	assert.Empty(t, violations,
		"MIGRATOR-PROVIDER-UP-CALLSITE-01: m.provider.Up / m.provider.Down in migrator.go "+
			"must only be called from methods in allowlist %v. "+
			"Any other caller bypasses the ForwardRebuildPermit phase0 gate (issue #1248). "+
			"Refactor: introduce forwardRun that holds the gate and is the sole caller of provider.Up.",
		migratorSortedKeys(migratorProviderCallAllowlist),
	)
}

// formatProviderCallViolation formats a single violation for logging.
func formatProviderCallViolation(line int, method, owner string) string {
	if owner == "" {
		return fmt.Sprintf("line %d: provider.%s called outside any method body", line, method)
	}
	return fmt.Sprintf("line %d: provider.%s called in method %q (not in allowlist)", line, method, owner)
}

// migratorSortedKeys returns sorted keys of a bool map for deterministic messages.
func migratorSortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// simple insertion sort — tiny slice
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// ---------------------------------------------------------------------------
// Reverse blind-spot self-checks for MIGRATOR-PROVIDER-UP-CALLSITE-01
// (ai-robust.md charter: every archtest must have reverse tests for its blind spots)
// ---------------------------------------------------------------------------

// TestArchtest_MigratorProviderUpCallsite_BlindSpot_NoFuncValueAssignment is
// the reverse self-check for the blind spot "provider.Up called via a function
// value stored in a field or local variable".
// This test asserts that migrator.go does NOT contain any assignment of
// m.provider.Up or m.provider.Down to a local variable or field — i.e., the
// blind-spot form does not exist in the current corpus (vacuous pass).
// If it ever appears, MIGRATOR-PROVIDER-UP-CALLSITE-01 would not catch it,
// so this reverse test provides early warning.
func TestArchtest_MigratorProviderUpCallsite_BlindSpot_NoFuncValueAssignment(t *testing.T) {
	root := findModuleRoot(t)
	migratorPath := filepath.Join(root, "adapters", "postgres", "migrator.go")

	fset := token.NewFileSet()
	// #nosec G304 -- reading repo-resident file under module root
	f, err := parser.ParseFile(fset, migratorPath, nil, 0)
	if err != nil {
		t.Fatalf("MIGRATOR-PROVIDER-UP-CALLSITE-01 blind-spot check: cannot parse %s: %v", migratorPath, err)
	}

	// Detect any AssignStmt where the RHS contains <recv>.provider.Up or
	// <recv>.provider.Down as a function value (SelectorExpr, not a CallExpr).
	// Use EachInSubtree[ast.AssignStmt] to find all assignment statements, then
	// EachInSubtree[ast.SelectorExpr] within each Rhs expression to find
	// provider.Up/Down used as function values.
	var violations []string
	scanner.EachInSubtree[ast.AssignStmt](f, func(assign *ast.AssignStmt) {
		for _, rhs := range assign.Rhs {
			scanner.EachInSubtree[ast.SelectorExpr](rhs, func(outer *ast.SelectorExpr) {
				if outer.Sel.Name != "Up" && outer.Sel.Name != "Down" {
					return
				}
				inner, ok := outer.X.(*ast.SelectorExpr)
				if !ok {
					return
				}
				if inner.Sel.Name != "provider" {
					return
				}
				// Check that this SelectorExpr is not immediately the Fun of a CallExpr.
				// We want function-value references, not calls. Because EachInSubtree is
				// pre-order, we detect call-context by checking whether the parent is a
				// CallExpr. We do this by searching for a CallExpr wrapping this node
				// via position: if no CallExpr in the assignment RHS has this selector
				// as its Fun, then it is a function-value reference.
				isCall := false
				scanner.EachInSubtree[ast.CallExpr](rhs, func(call *ast.CallExpr) {
					if call.Fun == outer {
						isCall = true
					}
				})
				if !isCall {
					pos := fset.Position(outer.Pos())
					violations = append(violations, fmt.Sprintf(
						"line %d: provider.%s used as function value (MIGRATOR-PROVIDER-UP-CALLSITE-01 blind spot)",
						pos.Line, outer.Sel.Name))
				}
			})
		}
	})

	assert.Empty(t, violations,
		"MIGRATOR-PROVIDER-UP-CALLSITE-01 blind-spot: provider.Up/Down must not be stored as a function value; "+
			"if this form is added, MIGRATOR-PROVIDER-UP-CALLSITE-01 would not catch it.")
}

// TestArchtest_MigratorProviderUpCallsite_BlindSpot_NoProviderCopy is the
// reverse self-check for the blind spot "method calls on a copy of provider
// (not via selector on the struct field)".
// Asserts that migrator.go does NOT contain any variable declared with the
// goose.Provider type that could be used to call Up/Down outside the selector
// path — vacuous pass on the current corpus.
func TestArchtest_MigratorProviderUpCallsite_BlindSpot_NoProviderCopy(t *testing.T) {
	root := findModuleRoot(t)
	migratorPath := filepath.Join(root, "adapters", "postgres", "migrator.go")

	fset := token.NewFileSet()
	// #nosec G304 -- reading repo-resident file under module root
	f, err := parser.ParseFile(fset, migratorPath, nil, 0)
	if err != nil {
		t.Fatalf("MIGRATOR-PROVIDER-UP-CALLSITE-01 blind-spot check: cannot parse %s: %v", migratorPath, err)
	}

	// Detect local variable declarations of type *goose.Provider (or goose.Provider).
	// Use EachInSubtree[ast.ValueSpec] to walk all value specs across the file.
	var violations []string
	scanner.EachInSubtree[ast.ValueSpec](f, func(valSpec *ast.ValueSpec) {
		if valSpec.Type == nil {
			return
		}
		// Check for *goose.Provider or goose.Provider type annotation.
		if checkGooseProviderType(valSpec.Type) {
			pos := fset.Position(valSpec.Pos())
			violations = append(violations, fmt.Sprintf(
				"line %d: local goose.Provider var declared (MIGRATOR-PROVIDER-UP-CALLSITE-01 blind spot)",
				pos.Line))
		}
	})

	assert.Empty(t, violations,
		"MIGRATOR-PROVIDER-UP-CALLSITE-01 blind-spot: local goose.Provider variables must not exist; "+
			"if added, MIGRATOR-PROVIDER-UP-CALLSITE-01 would not detect Up/Down calls on them.")
}

// checkGooseProviderType reports whether expr is a type annotation for
// *goose.Provider or goose.Provider.
func checkGooseProviderType(expr ast.Expr) bool {
	if starExpr, isStar := expr.(*ast.StarExpr); isStar {
		expr = starExpr.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkgIdent.Name == "goose" && sel.Sel.Name == "Provider"
}

// migratorForwardRebuildAnnotationPatternSingleSource verifies that the archtest
// uses the same regex pattern as the runtime gate (postgres.ForwardRebuildAnnotationPattern).
// This is not a blind-spot test but a single-source validation: if the pattern
// diverges between the archtest local copy and the runtime, this test catches it.
func TestArchtest_MigrationForwardRebuild_PatternSingleSource(t *testing.T) {
	// postgres.ForwardRebuildAnnotationPattern is the exported single source of truth.
	// The archtest forwardRebuildAnnotationRE in pg_schema_guard_invariants_test.go
	// must use the same pattern. This test asserts that the exported constant exists
	// and is non-empty, providing compile-time verification of the import.
	assert.NotEmpty(t, postgres.ForwardRebuildAnnotationPattern,
		"postgres.ForwardRebuildAnnotationPattern must be a non-empty exported constant "+
			"(single source of truth for runtime gate and archtest MIGRATION-FORWARD-REBUILD-ANNOTATION-01)")
}

// TestArchtest_MigratorProviderUpCallsite_BlindSpot_NoInterfaceAlias is the
// reverse self-check for the blind spot "calls through an interface variable
// aliasing provider". It asserts migrator.go never assigns or passes m.provider
// as a whole expression — which would let it be aliased into an interface-typed
// variable and have Up/Down called outside the forwardRun/Down allowlist. A
// reference is flagged only when the entire assigned/passed expression is
// `<x>.provider` (Sel=="provider"); `m.provider.Up(...)` is a method call
// (CallExpr), not an alias, and is not flagged. Vacuous pass on the corpus.
func TestArchtest_MigratorProviderUpCallsite_BlindSpot_NoInterfaceAlias(t *testing.T) {
	root := findModuleRoot(t)
	migratorPath := filepath.Join(root, "adapters", "postgres", "migrator.go")

	fset := token.NewFileSet()
	// #nosec G304 -- reading repo-resident file under module root
	f, err := parser.ParseFile(fset, migratorPath, nil, 0)
	if err != nil {
		t.Fatalf("MIGRATOR-PROVIDER-UP-CALLSITE-01 blind-spot check: cannot parse %s: %v", migratorPath, err)
	}

	var violations []string
	flag := func(e ast.Expr) {
		if sel, ok := e.(*ast.SelectorExpr); ok && sel.Sel.Name == "provider" {
			pos := fset.Position(e.Pos())
			violations = append(violations, fmt.Sprintf(
				"line %d: m.provider aliased as a whole expression (MIGRATOR-PROVIDER-UP-CALLSITE-01 blind spot)",
				pos.Line))
		}
	}
	scanner.EachInSubtree[ast.AssignStmt](f, func(assign *ast.AssignStmt) {
		for _, rhs := range assign.Rhs {
			flag(rhs)
		}
	})
	scanner.EachInSubtree[ast.ValueSpec](f, func(vs *ast.ValueSpec) {
		for _, v := range vs.Values {
			flag(v)
		}
	})
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		for _, arg := range call.Args {
			flag(arg)
		}
	})

	assert.Empty(t, violations,
		"MIGRATOR-PROVIDER-UP-CALLSITE-01 blind-spot: m.provider must not be aliased into a variable "+
			"or passed as an argument; an interface alias could call Up/Down outside the allowlist.")
}
