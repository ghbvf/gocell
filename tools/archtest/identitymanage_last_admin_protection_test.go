// invariants:
//   - INVARIANT: IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01
//
// IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01 — every production caller
// of `cells/accesscore/slices/identitymanage.NewService` MUST pass
// `identitymanage.WithLastAdminProtection(roleRepo)` so the
// at-least-one-effective-admin invariant is enforced at the application
// layer (S4.0). Without the option the service constructs without a
// LastAdminGuard, and Lock/Delete/Update fall through to the DB trigger
// (migration 024) only — losing the precise 403 application-layer 错误
// codepath, and breaking the layered protection contract in
// `docs/architecture/202605101400-adr-admin-invariant.md`.
//
// AI-rebust 评级：Medium (archtest type-aware via typeseval.SharedResolver
// + file-scoped co-existence check). The file-scoped check matches the
// realistic wiring style — composition uses `identityOpts := []Option{...}`
// slice spread, and rule walks the calling file looking for both
// `identitymanage.NewService(...)` and `identitymanage.WithLastAdminProtection(...)`
// somewhere in the same file. A file constructing identitymanage.Service
// without naming WithLastAdminProtection anywhere is a Soft → Hard
// upgrade candidate, but file-scoped suffices for the current
// single-caller (cells/accesscore/cell_init.go) wiring shape.
//
// ref: PR #476 round-2 deferred #3 (closed in PR)
// ref: ai-collab.md §"Soft → Hard 改造方向" 名字 convention → archtest 类型化
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const (
	identityManageNewServiceFn = "github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage.NewService"
	withLastAdminProtectionFn  = "github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage.WithLastAdminProtection"
)

// canonicalCalledFuncForLastAdmin resolves a CallExpr Fun ident to its
// canonical "<pkg-path>.<func-name>" string via *types.Info, with the same
// shape as wrapper_location_test.canonicalCalledFunc. Duplicated here to
// keep this rule's scanner self-contained — typeseval helpers do not yet
// vend a shared canonical-callee helper.
func canonicalCalledFuncForLastAdmin(info *types.Info, call *ast.CallExpr) string {
	if info == nil || call == nil {
		return ""
	}
	var ident *ast.Ident
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		ident = fn.Sel
	case *ast.Ident:
		ident = fn
	default:
		return ""
	}
	obj := info.Uses[ident]
	if obj == nil {
		return ""
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return ""
	}
	pkg := fn.Pkg()
	if pkg == nil {
		return ""
	}
	return pkg.Path() + "." + fn.Name()
}

// INVARIANT: IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01
//
// TestIdentitymanageLastAdminProtectionWiring01_RealRepoClean verifies that
// no production wiring of identitymanage.NewService omits
// WithLastAdminProtection (file-scoped co-existence check).
func TestIdentitymanageLastAdminProtectionWiring01_RealRepoClean(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			// Skip test files + the identitymanage package itself (the
			// rule does not apply to the implementation of NewService or
			// its in-package tests).
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if strings.Contains(rel, "cells/accesscore/slices/identitymanage/") {
				continue
			}

			// First pass: locate identitymanage.NewService(...) calls in this file.
			var newServiceCalls []*ast.CallExpr
			// Second pass: detect identitymanage.WithLastAdminProtection(...) anywhere in the file.
			hasProtection := false
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				canon := canonicalCalledFuncForLastAdmin(p.TypesInfo, call)
				switch canon {
				case identityManageNewServiceFn:
					newServiceCalls = append(newServiceCalls, call)
				case withLastAdminProtectionFn:
					hasProtection = true
				}
			})
			if len(newServiceCalls) == 0 {
				continue
			}
			if hasProtection {
				continue
			}
			for _, call := range newServiceCalls {
				line := p.Fset.Position(call.Pos()).Line
				ds = append(ds, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: "IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01: calls identitymanage.NewService without " +
						"identitymanage.WithLastAdminProtection(roleRepo) anywhere in the same file — every production wiring " +
						"MUST install the at-least-one-effective-admin guard (S4.0; see " +
						"docs/architecture/202605101400-adr-admin-invariant.md).",
				})
			}
		}
		return ds
	})

	Report(t, "IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01", diags)
}
