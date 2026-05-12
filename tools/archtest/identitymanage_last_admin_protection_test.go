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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

const (
	identityManageNewServiceFn = "github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage.NewService"
	withLastAdminProtectionFn  = "github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage.WithLastAdminProtection"
)

// identitymanageNewServiceCallSite records a production-file occurrence of
// identitymanage.NewService(...). The companion WithLastAdminProtection
// presence is computed at the file granularity (Boolean).
type identitymanageNewServiceCallSite struct {
	File                string
	Line                int
	HasProtectionInFile bool
}

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

func scanLastAdminProtectionViolations(root string, pkgs []*packages.Package) []identitymanageNewServiceCallSite {
	var out []identitymanageNewServiceCallSite
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			rel, err := filepath.Rel(root, absPath)
			if err != nil {
				continue
			}
			relSlash := filepath.ToSlash(rel)
			// Skip test files + the identitymanage package itself (the
			// rule does not apply to the implementation of NewService or
			// its in-package tests).
			if strings.HasSuffix(relSlash, "_test.go") {
				continue
			}
			if strings.Contains(relSlash, "cells/accesscore/slices/identitymanage/") {
				continue
			}

			// First pass: locate identitymanage.NewService(...) calls in this file.
			var newServiceCalls []*ast.CallExpr
			// Second pass: detect identitymanage.WithLastAdminProtection(...) anywhere in the file.
			hasProtection := false
			scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				canon := canonicalCalledFuncForLastAdmin(pkg.TypesInfo, call)
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
			for _, call := range newServiceCalls {
				out = append(out, identitymanageNewServiceCallSite{
					File:                relSlash,
					Line:                pkg.Fset.Position(call.Pos()).Line,
					HasProtectionInFile: hasProtection,
				})
			}
		}
	}
	return out
}

// INVARIANT: IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01
//
// TestIdentitymanageLastAdminProtectionWiring01_RealRepoClean verifies that
// no production wiring of identitymanage.NewService omits
// WithLastAdminProtection (file-scoped co-existence check).
func TestIdentitymanageLastAdminProtectionWiring01_RealRepoClean(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)
	resolver, err := typeseval.LoadProductionPackages(root, modulePath, false, nil)
	require.NoError(t, err)

	for _, site := range scanLastAdminProtectionViolations(root, resolver.Production()) {
		if site.HasProtectionInFile {
			continue
		}
		t.Errorf("IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01: %s:%d calls identitymanage.NewService without "+
			"identitymanage.WithLastAdminProtection(roleRepo) anywhere in the same file — every production wiring "+
			"MUST install the at-least-one-effective-admin guard (S4.0; see "+
			"docs/architecture/202605101400-adr-admin-invariant.md).",
			site.File, site.Line)
	}
}
