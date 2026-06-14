package archtest

// audit_hash_input_frozen.go — importable audit hash input rule logic (#1640 M3 PR-9).
//
// Non-test home for AUDIT-HASH-INPUT-FROZEN-01 scanner logic, so it can be
// compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go, so rule logic external repos must run cannot live in
// a _test.go file). GoCell's own TestAuditHashInputFrozen_A2_HmacCallsite in
// audit_hash_input_frozen_test.go dogfoods the same CheckAuditHashInputFrozenA2
// — single source, no parallel rule body.
//
// One rule sub-check is implemented here:
//
//   - AUDIT-HASH-INPUT-FROZEN-01/A2: within runtime/audit/ledger production
//     source, any call to crypto/hmac.New must be located inside the body of
//     Protocol.ComputeHash. Import aliases are resolved via go/types, so
//     `import h "crypto/hmac"; h.New(...)` is caught identically.
//
// A1 (struct shape freeze via AST reflection) and B (reverse self-check with
// synthetic sources) remain in audit_hash_input_frozen_test.go because they
// rely on test-only helpers or are test-infrastructure checks.
//
// Dogfood test: TestAuditHashInputFrozen_A2_HmacCallsite
// (audit_hash_input_frozen_test.go).
//
// # Register status
//
// Not registered in StandardCellRules: scan scope is gocell-hardcoded
// (runtime/audit/ledger layout) with no ConfigForExternalCell
// consumer-extension → vacuous/false-red externally; kept importable +
// module-path-agnostic + fork-safe.

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Package-path constants (module-path-agnostic)
// ---------------------------------------------------------------------------

// auditLedgerPkgPath is the import path of runtime/audit/ledger.
// Derived from PlatformModulePath so a module rename / /v2 bump updates exactly
// one place.
const auditLedgerPkgPath = PlatformFrameworkModulePath + "/runtime/audit/ledger"

// ---------------------------------------------------------------------------
// AUDIT-HASH-INPUT-FROZEN-01 / A2
// ---------------------------------------------------------------------------

// CheckAuditHashInputFrozenA2 verifies that within runtime/audit/ledger
// production source any call to crypto/hmac.New is located inside the body of
// the Protocol.ComputeHash method. The callee identification uses go/types via
// IsCallToPkgFunc, so import aliases cannot bypass the lock.
//
// Not registered in StandardCellRules: see file godoc.
func CheckAuditHashInputFrozenA2(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return Run(t, Typed(TypedOpts{}, []string{
		"./framework/runtime/audit/ledger/...",
	}), runAuditHashInputA2Rule)
}

// runAuditHashInputA2Rule walks a typed Pass and emits diagnostics for any
// hmac.New call outside the sanctioned Protocol.ComputeHash body.
//
// Scope: only the canonical package runtime/audit/ledger itself. The storetest
// subpackage (runtime/audit/ledger/storetest) is intentionally excluded — its
// referenceComputeHash is an INDEPENDENT mirror used by hash-parity tests to
// detect drift in Protocol.ComputeHash. Forcing it through the same funnel
// would devolve into production-versus-production circular verification.
func runAuditHashInputA2Rule(p *Pass) []Diagnostic {
	if !p.Typed() {
		return nil
	}
	if p.Pkg != nil && p.Pkg.Path() != auditLedgerPkgPath {
		return nil
	}
	var diags []Diagnostic
	WalkFuncDecls(p, func(ctx FuncDeclContext) {
		if strings.HasSuffix(ctx.Rel, "_test.go") {
			return
		}
		diags = append(diags, scanFuncBodyForHmacViolations(ctx)...)
	})
	return diags
}

// scanFuncBodyForHmacViolations walks the FuncDecl body, emitting a Diagnostic
// for each crypto/hmac.New call; suppresses them when the enclosing func is the
// sanctioned `func (*Protocol) ComputeHash` (HasReceiver pins the receiver, so
// a same-named method on another type is not exempt). IsCallToPkgFunc is
// alias-proof: handles `hmac.New`, `h.New` after `import h "crypto/hmac"`, and
// `New` after `import . "crypto/hmac"`.
//
// HasReceiver (via scanner.ReceiverTypeName) also matches generic receivers
// `Protocol[T]`. Protocol is non-generic today, so this is a no-op; if Protocol
// ever becomes generic, re-confirm the exemption still scopes to the intended
// ComputeHash method (the broadening only ever *exempts* more, never detects
// less, so it cannot cause a missed hmac.New violation on a non-Protocol type).
func scanFuncBodyForHmacViolations(ctx FuncDeclContext) []Diagnostic {
	sanctioned := ctx.Func.Name != nil && ctx.Func.Name.Name == "ComputeHash" &&
		HasReceiver(ctx.Func, "Protocol")
	if sanctioned {
		return nil
	}
	var diags []Diagnostic
	EachInSubtree[ast.CallExpr](ctx.Func.Body, func(call *ast.CallExpr) {
		if !IsCallToPkgFunc(ctx.Info, call, "crypto/hmac", "New") {
			return
		}
		pos := ctx.Fset.Position(call.Pos())
		diags = append(diags, Diagnostic{
			Rel:  ctx.Rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"hmac.New called outside Protocol.ComputeHash (inside %s) — "+
					"all audit HMAC must flow through Protocol.ComputeHash with the canonical auditHashInput marshaling",
				ctx.Func.Name.Name,
			),
		})
	})
	return diags
}

// scanSyntheticSource type-checks src against the real crypto/hmac stdlib
// package and runs the A2 rule logic. Used by the reverse self-check to
// validate that the alias-proof typed scanner detects violations in all
// import shapes.
func scanSyntheticSource(t *testing.T, src string) []Diagnostic {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.Default()}
	if _, err := conf.Check("synthetic", fset, []*ast.File{file}, info); err != nil {
		t.Fatalf("type-check synthetic source: %v", err)
	}
	var diags []Diagnostic
	// Standalone load (own info/fset, no Pass) — build the FuncDeclContext the
	// same way the WalkFuncDecls engine does so the shared per-func scanner runs
	// against the synthetic typed info. EachInChildren keeps this depth-1
	// FuncDecl walk SCANNER-FRAMEWORK-USAGE compliant.
	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		diags = append(diags, scanFuncBodyForHmacViolations(FuncDeclContext{
			File: file, Func: fn, Info: info, Fset: fset, Rel: "synthetic.go",
		})...)
	})
	return diags
}
