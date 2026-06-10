package archtest

// reconstitute_user_caller_allowlist.go — importable detection logic for
// RECONSTITUTE-USER-CALLER-01 (#1302 M3 Batch D).
//
// Non-test home of the detector so it can be compiled by an external
// Cell repository (Go never compiles a dependency's _test.go).
// GoCell's own Test* functions in reconstitute_user_caller_allowlist_test.go
// call the same Check* — single source, no parallel rule body.
//
// Platform-symbol paths are anchored to [PlatformModulePath].

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

// ─── rule ID constant ──────────────────────────────────────────────────────

const ruleReconstituteUserCaller01 = "RECONSTITUTE-USER-CALLER-01"

// ─── platform-symbol path constants (no bare literals) ────────────────────

const (
	reconstituteUserPkg = PlatformModulePath + "/corecells/accesscore/internal/domain"
)

// ─── symbol name constant ──────────────────────────────────────────────────

const reconstituteUserName = "ReconstituteUser"

// ─── caller allowlist ─────────────────────────────────────────────────────

// reconstituteUserCallerAllowlistPrefixes lists module-relative path prefixes
// whose production code is permitted to call domain.ReconstituteUser directly.
//
// Rationale:
//   - corecells/accesscore/internal/mem/: mem store implementations rebuild User
//     aggregates from stored values; ReconstituteUser is the correct rehydration
//     path for an in-memory store.
//   - corecells/accesscore/internal/adapters/postgres/: PG store implementations
//     use scanUser → ReconstituteUser to rehydrate from DB rows; this is the
//     canonical persistence boundary.
//   - corecells/accesscore/internal/domain/: the function is defined here; tests and
//     internal helpers in the same package are allowed.
//   - corecells/accesscore/internal/ports/conformance/: the UserRepository
//     conformance test suite (RunUserRepoConformance) uses ReconstituteUser to
//     seed live fixtures for repository acceptance tests. The conformance package
//     is a test infrastructure package, not a slice; it tests the persistence
//     boundary and therefore belongs in the allowlist.
//
// _test.go files are always allowed (see isReconstituteCallerAllowlisted).
var reconstituteUserCallerAllowlistPrefixes = []string{
	"corecells/accesscore/internal/mem/",
	"corecells/accesscore/internal/adapters/postgres/",
	"corecells/accesscore/internal/domain/",
	"corecells/accesscore/internal/ports/conformance/",
}

// isReconstituteCallerAllowlisted reports whether a module-relative path is
// in the ReconstituteUser caller allowlist. Test files (*_test.go) always pass.
func isReconstituteCallerAllowlisted(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, prefix := range reconstituteUserCallerAllowlistPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// scanReconstituteViolationsPass walks a single file's AST for CallExpr nodes
// where the callee resolves to domain.ReconstituteUser via
// ResolvePackageRef (facade over typeseval.ResolvePackageRef). Returns one
// Diagnostic per disallowed call site.
//
// AST form covered: `domain.ReconstituteUser(...)` where `domain` is the
// package alias resolving to reconstituteUserPkg via info.Uses[*ast.Ident]
// → *types.PkgName. See ResolvePackageRef godoc for the exact lookup.
func scanReconstituteViolationsPass(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok {
			return
		}
		if pkgPath != reconstituteUserPkg || name != reconstituteUserName {
			return
		}
		line := p.Fset.Position(call.Pos()).Line
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"RECONSTITUTE-USER-CALLER-01: disallowed caller of domain.ReconstituteUser; "+
					"allowed prefixes: %v",
				reconstituteUserCallerAllowlistPrefixes,
			),
		})
	})
	return out
}

// ─── CheckReconstituteUserCallerAllowlist01 ─────────────────────────────────

// CheckReconstituteUserCallerAllowlist01 runs RECONSTITUTE-USER-CALLER-01 over
// the running module and returns its diagnostics.
//
// This is the importable CellRule body. GoCell's own Test* functions in
// reconstitute_user_caller_allowlist_test.go call the same detectors —
// single source, no parallel rule body.
//
// The rule is intentionally NOT registered in StandardCellRules: it reasons
// about GoCell's own internal package layout (accesscore domain/mem/postgres),
// making it vacuous-green or false-red for an external module.
func CheckReconstituteUserCallerAllowlist01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: cfg.BuildTags},
		[]string{"./corecells/accesscore/...", "./cmd/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if isReconstituteCallerAllowlisted(rel) {
					continue
				}
				diags = append(diags, scanReconstituteViolationsPass(p, file, rel)...)
			}
			return nil
		})

	return diags
}
