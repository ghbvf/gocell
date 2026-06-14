package archtest

// distlock_lock_not_context.go — importable distlock rule logic (#1640 M3 PR-9).
//
// Non-test home for DISTLOCK-LOCK-NOT-CONTEXT-01 scanner logic, so it can be
// compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go, so rule logic external repos must run cannot live in
// a _test.go file). GoCell's own TestDistlockLockNotContext01 in
// distlock_lock_not_context_test.go dogfoods the same CheckDistlockLockNotContext01
// — single source, no parallel rule body.
//
// One rule is implemented here:
//
//   - DISTLOCK-LOCK-NOT-CONTEXT-01: runtime/distlock.Lock MUST NOT implement
//     context.Context. Anti-vacuity requires that both the Lock type and the
//     context.Context interface be resolvable.
//
// Dogfood test: TestDistlockLockNotContext01 (distlock_lock_not_context_test.go).
//
// # Register status
//
// Not registered in StandardCellRules: scan scope is gocell-hardcoded
// (runtime/distlock layout) with no ConfigForExternalCell consumer-extension →
// vacuous/false-red externally; kept importable + module-path-agnostic + fork-safe.

import (
	"go/types"
	"testing"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// ---------------------------------------------------------------------------
// Package-path constants (module-path-agnostic)
// ---------------------------------------------------------------------------

// distlockPkgPath is the import path of runtime/distlock.
// Derived from PlatformModulePath so a module rename / /v2 bump updates exactly
// one place.
const distlockPkgPath = PlatformFrameworkModulePath + "/runtime/distlock"

// contextPkgPath is the stdlib context package import path.
const contextPkgPath = "context"

// lockTypeName is the exported type name checked by DISTLOCK-LOCK-NOT-CONTEXT-01.
const lockTypeName = "Lock"

// contextTypeName is the interface name within the context package.
const contextTypeName = "Context"

// ---------------------------------------------------------------------------
// DISTLOCK-LOCK-NOT-CONTEXT-01
// ---------------------------------------------------------------------------

// distlockCheckResult holds intermediate scan results for CheckDistlockLockNotContext01.
type distlockCheckResult struct {
	foundLockType bool
	foundCtxIface bool
	implements    bool
	violationLine int
}

// CheckDistlockLockNotContext01 verifies that *runtime/distlock.Lock does not
// implement context.Context. Returns anti-vacuity diagnostics when the Lock
// type or context.Context interface cannot be found, and a violation diagnostic
// when Lock implements the interface.
//
// Not registered in StandardCellRules: see file godoc.
func CheckDistlockLockNotContext01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	result := scanDistlockPkg(t)
	return buildDistlockDiags(result)
}

// scanDistlockPkg loads runtime/distlock and collects the type-check result.
func scanDistlockPkg(t *testing.T) distlockCheckResult {
	t.Helper()
	var res distlockCheckResult
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./framework/runtime/distlock/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != distlockPkgPath {
				return nil
			}
			obj := p.Pkg.Scope().Lookup(lockTypeName)
			if obj == nil {
				return nil
			}
			lockType, ok := obj.Type().(*types.Named)
			if !ok {
				return nil
			}
			res.foundLockType = true
			if lockType.Obj() != nil {
				res.violationLine = p.Fset.Position(lockType.Obj().Pos()).Line
			}
			ctxIface := resolveContextInterface(p.Pkg.Imports())
			if ctxIface == nil {
				return nil
			}
			res.foundCtxIface = true
			res.implements = typesutil.ImplementsInterface(lockType, ctxIface)
			return nil
		})
	return res
}

// buildDistlockDiags converts a distlockCheckResult into diagnostics.
func buildDistlockDiags(res distlockCheckResult) []Diagnostic {
	if !res.foundLockType {
		return []Diagnostic{{
			Rel:  "runtime/distlock",
			Line: 1,
			Message: "DISTLOCK-LOCK-NOT-CONTEXT-01 anti-vacuity: runtime/distlock.Lock type not found; " +
				"rule cannot enforce (type was renamed or removed)",
		}}
	}
	if !res.foundCtxIface {
		return []Diagnostic{{
			Rel:  "runtime/distlock",
			Line: 1,
			Message: "DISTLOCK-LOCK-NOT-CONTEXT-01 anti-vacuity: context.Context interface not resolvable " +
				"via distlock imports; rule cannot enforce",
		}}
	}
	if !res.implements {
		return nil
	}
	line := res.violationLine
	if line == 0 {
		line = 1
	}
	return []Diagnostic{{
		Rel:  "runtime/distlock",
		Line: line,
		Message: "*runtime/distlock.Lock must NOT implement context.Context; " +
			"adding Deadline()/Err() methods would let callers pass *Lock to " +
			"db.QueryContext / http.NewRequestWithContext / etc., reintroducing " +
			"the misuse class identified in GH #20. See ADR " +
			"docs/architecture/202605200000-adr-distlock-lock-as-resource.md",
	}}
}

// resolveContextInterface walks the supplied import set and returns the
// context.Context interface type, or nil if not reachable.
func resolveContextInterface(imports []*types.Package) *types.Interface {
	for _, imp := range imports {
		if imp.Path() != contextPkgPath {
			continue
		}
		ctxObj := imp.Scope().Lookup(contextTypeName)
		if ctxObj == nil {
			continue
		}
		named, ok := ctxObj.Type().(*types.Named)
		if !ok {
			continue
		}
		iface, ok := named.Underlying().(*types.Interface)
		if !ok {
			continue
		}
		return iface
	}
	return nil
}
