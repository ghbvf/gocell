// Package archtest_test — migrator_permit_funnel_test.go
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
// Funnel upstream Hard / downstream Medium breakdown:
//   - Upstream Hard (package-external): provider field is unexported (lowercase),
//     so no code outside package postgres can reference m.provider at all;
//     Go compiler is the gate.
//   - Upstream Medium (package-internal): archtest A1 below locks "every
//     provider.Up/Down call inside migrator.go must be in an allowlisted method";
//     a new func inside the same package can still call it — archtest CI catches
//     this, but Go's type system cannot.
//   - Downstream: no separate downstream axis (provider.Up is a third-party
//     method, not a callsite we can seal).
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
// Rule: MIGRATOR-PROVIDER-UP-CALLSITE-01
func TestArchtest_MigratorProviderUpCallsite(t *testing.T) {
	root := findModuleRoot(t)
	migratorPath := filepath.Join(root, "adapters", "postgres", "migrator.go")

	fset := token.NewFileSet()
	// #nosec G304 -- reading repo-resident file under module root
	f, err := parser.ParseFile(fset, migratorPath, nil, 0)
	if err != nil {
		t.Fatalf("MIGRATOR-PROVIDER-UP-CALLSITE-01: cannot parse %s: %v", migratorPath, err)
	}

	// Collect all top-level FuncDecls (methods) for position-based lookup.
	type methodRange struct {
		name  string
		start token.Pos
		end   token.Pos
	}
	var methods []methodRange
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		methods = append(methods, methodRange{
			name:  fd.Name.Name,
			start: fd.Body.Lbrace,
			end:   fd.Body.Rbrace,
		})
	}

	// ownerMethod returns the name of the FuncDecl whose body contains pos,
	// or "" if pos is not inside any method body.
	ownerMethod := func(pos token.Pos) string {
		for _, m := range methods {
			if pos >= m.start && pos <= m.end {
				return m.name
			}
		}
		return ""
	}

	// Walk the AST looking for <expr>.provider.Up(...) and
	// <expr>.provider.Down(...) call expressions.
	// Pattern: CallExpr{ Fun: SelectorExpr{ X: SelectorExpr{ Sel: "provider" }, Sel: "Up"|"Down" } }
	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		outer, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// outer.Sel must be "Up" or "Down"
		if outer.Sel.Name != "Up" && outer.Sel.Name != "Down" {
			return true
		}
		// outer.X must itself be a SelectorExpr with Sel == "provider"
		inner, ok := outer.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if inner.Sel.Name != "provider" {
			return true
		}
		// Found a candidate: <recv>.provider.Up/Down(...)
		owner := ownerMethod(call.Pos())
		if !migratorProviderCallAllowlist[owner] {
			pos := fset.Position(call.Pos())
			violations = append(violations, formatProviderCallViolation(pos.Line, outer.Sel.Name, owner))
		}
		return true
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
