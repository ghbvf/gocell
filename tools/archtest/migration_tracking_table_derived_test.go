//go:build archtest

// INVARIANT: MIGRATION-TRACKING-TABLE-DERIVED-01
package archtest

import (
	"go/ast"
	"testing"
)

// migrationTableHelpers are the unexported adapters/postgres construction cores
// that accept an explicit goose tracking-table string. In production the table
// MUST be derived from a migration.Namespace via trackingTableFor; only
// in-package TEST code may pass an arbitrary table string (to provision isolated
// per-test tracking tables). Production callers of these helpers = NewMigrator
// and VerifyExpectedVersion, both of which pass trackingTableFor(ns).
var migrationTableHelpers = map[string]struct{}{
	"newMigratorForTable":           {},
	"verifyExpectedVersionForTable": {},
}

// MIGRATION-TRACKING-TABLE-DERIVED-01: in NON-test adapters/postgres code, every
// call to newMigratorForTable / verifyExpectedVersionForTable must pass a
// trackingTableFor(...) call as its tracking-table argument. This pins the one
// in-package seam where a raw tracking-table string could enter production: the
// table is always a pure function of a validated migration.Namespace
// (schema_migrations_<namespace>), so no production path can re-introduce the
// bare global schema_migrations table (#1089 / root cause R3).
//
// AI-robust 评级：Medium (downstream caller-allowlist) — go/types resolution
// would be overkill: the two helper names are unexported and unique within
// package adapters/postgres, so an AST ident match within that single package is
// exact. The EXPORTED surface is already Hard (NewMigrator / VerifyExpectedVersion
// take a typed migration.Namespace — a bare string is a compile error), and goose
// provider construction is pinned to adapters/postgres by GOOSE-SESSION-LOCKER-01.
// This archtest is the package-internal backstop for the unexported test escape
// hatch. Tests (which legitimately pass literal tables to the helpers) are out of
// scope via Production{Tests:false}.
//
// Blind spot (false-positive direction): a production caller that assigns
// trackingTableFor(ns) to a local var and then passes the var would be reported
// as a violation even though it is legitimate — the rule only accepts a direct
// inlined trackingTableFor(...) call in the table-name slot. Both production
// sites inline the call (migrator.go NewMigrator, schema_guard.go
// VerifyExpectedVersion), which is the package idiom, so the rule does not
// misfire today. Tightening to data-flow tracking is not worth the brittleness
// at Medium.
//
// ref: adapters/postgres/migrator.go newMigratorForTable / NewMigrator
// ref: adapters/postgres/schema_guard.go verifyExpectedVersionForTable / VerifyExpectedVersion
func TestMigrationTrackingTableDerived01(t *testing.T) {
	t.Parallel()
	const pkgPath = PlatformModulePath + "/adapters/postgres"

	checked := 0
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != pkgPath {
			return nil
		}
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(ce *ast.CallExpr) {
				callee, ok := ce.Fun.(*ast.Ident)
				if !ok {
					return
				}
				if _, isHelper := migrationTableHelpers[callee.Name]; !isHelper {
					return
				}
				checked++
				if len(ce.Args) == 0 || !isTrackingTableForCall(ce.Args[len(ce.Args)-1]) {
					ds = append(ds, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(ce.Pos()).Line,
						Message: "MIGRATION-TRACKING-TABLE-DERIVED-01: production call to " + callee.Name +
							" must pass trackingTableFor(ns) as the tracking-table argument — the table must be " +
							"derived from a migration.Namespace, never a raw string (else the global-table " +
							"collision footgun #1089/R3 returns).",
					})
				}
			})
		}
		return ds
	})

	// Anti-vacuity: the production callers (NewMigrator, VerifyExpectedVersion)
	// must actually exist and be scanned, or the rule silently passes.
	if checked == 0 {
		t.Fatalf("MIGRATION-TRACKING-TABLE-DERIVED-01: found 0 production calls to %v — "+
			"the rule must observe NewMigrator / VerifyExpectedVersion delegating through the helpers",
			keysOf(migrationTableHelpers))
	}
}

// isTrackingTableForCall reports whether expr is a direct call to trackingTableFor(...).
func isTrackingTableForCall(expr ast.Expr) bool {
	ce, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := ce.Fun.(*ast.Ident)
	return ok && id.Name == "trackingTableFor"
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
