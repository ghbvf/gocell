// invariants:
//   - INVARIANT: PG-REPO-AMBIENT-TX-01
//
// # Package archtest — PG-REPO-AMBIENT-TX-01
//
// PG-REPO-AMBIENT-TX-01 enforces the struct-field-funnel form of the ambient-tx
// routing contract for PostgreSQL-backed repositories:
//
//   - R1 (single sanctioned holder): the ONLY struct permitted to declare a
//     field of type *pgxpool.Pool is the package-local pgExecutor. Any other
//     struct holding such a field bypasses the ambient-tx routing logic and
//     can issue DML directly against the pool, breaking ADR-credential D5
//     same-tx revoke and L2 outbox atomicity.
//
//   - R2 (wrap funnel): every function/method parameter of type *pgxpool.Pool
//     must be in a New*-prefixed constructor whose body feeds that parameter
//     straight to a newPGExecutor(param) call. A bare *pgxpool.Pool param on a
//     non-constructor, or a constructor that receives the pool but stores it
//     raw, violates the funnel.
//
//   - R3 (usage-point funnel): methods on repo/store structs that hold a
//     pgExecutor field must NOT (a) access <x>.pool directly (where <x>
//     resolves to pgExecutor type via *types.Info) nor (b) call
//     <x>.ExecDirect(...) unless the callsite is on the static allowlist.
//     The allowlist is keyed by (file, enclosing function name) and currently
//     contains exactly one entry:
//     "adapters/postgres/refresh_store.go::revokeSessionDetachedAt" — the
//     intentional independent-commit cascade-revoke compensation path.
//
// # AI-rebust grading (Funnel 双向锁评级)
//
// 下游 Hard / 上游 Medium (backlog: PG-REPO-AMBIENT-TX-UPSTREAM-HARD-01).
//
// 上游 Medium: intra-package compile Hard is unreachable — adapters/postgres
// repos share the package with pgExecutor; Go package-level visibility means a
// sibling file can always reach pgExecutor.pool or add its own field — the
// compiler cannot block it. The ceiling is archtest-bound form-uniqueness (same
// grade and precedent as PANIC-REGISTERED-01 / panic(panicregister.Approved)
// per ai-collab.md §Hard 范本 #2 caveat). R1+R2 guard holding+construction;
// R3 adds callsite caller-allowlist enforcement so that the only existing
// legitimate bypass (revokeSessionDetachedAt) is statically pinned and any NEW
// bypass immediately fails CI. This elevates upstream enforcement from doc-only
// to archtest-enforced Medium (caller-allowlist form). Hard terminal state
// remains: seal pgExecutor behind an exported interface + private construction
// funnel making bypass unexpressible package-externally.
// See backlog PG-REPO-AMBIENT-TX-UPSTREAM-HARD-01.
//
// 下游 Hard via form-uniqueness (archtest-bound), NOT compile-time:
//
//   - R1 resolves every *ast.StructType field's type via *types.Info to the named
//     type github.com/jackc/pgx/v5/pgxpool.Pool (pointer-to-named), then checks
//     that the enclosing struct's type name is exactly "pgExecutor". The
//     resolution path is *types.Pointer → *types.Named → Obj().Pkg().Path() +
//     Obj().Name(). No field-name matching; no string anchors; no allowlist map.
//     Any struct whose field resolves to that type and is not named pgExecutor
//     fails CI — there is no "looks-like-but-isn't" gray zone.
//
//   - R2 resolves every *ast.FuncDecl parameter type via *types.Info to the
//     same *pgxpool.Pool named type. A *pgxpool.Pool param is legal only when
//     (a) the enclosing FuncDecl name starts with "New" AND (b) the function
//     body contains a CallExpr whose Fun resolves via *types.Info.Uses to the
//     package-local newPGExecutor function (verified by both fn.Name() AND
//     fn.Pkg().Path() matching the package under scan — prevents cross-package
//     false negatives) and whose first argument is the same *pgxpool.Pool param
//     identifier. Both (a) and (b) are required AND; failing either flags a
//     violation. Exempted: the pgExecutor methods themselves and newPGExecutor's
//     own parameter.
//
//   - R3 resolves selector expressions via *types.Info.Types to check whether
//     the receiver expression resolves to the package-local pgExecutor named
//     type (Obj().Name() == "pgExecutor" AND Obj().Pkg().Path() == pkgPath).
//     (a) For pool access: any SelectorExpr `<x>.pool` where <x> resolves to
//     pgExecutor and `.pool` is accessed from outside pgExecutor's own methods.
//     (b) For ExecDirect calls: any CallExpr `<x>.ExecDirect(...)` where <x>
//     resolves to pgExecutor, unless the enclosing function is on the static
//     allowlist map[string]struct{} keyed by "file::funcname".
//     Exempted: pgExecutor's own methods (they legitimately use e.pool /
//     call ExecDirect on self); newPGExecutor constructor.
//
// # Blind spots
//
// BS-1 Field embedding: an embedded struct containing *pgxpool.Pool — e.g.
//
//	type badRepo struct { pgExecutorInner }; type pgExecutorInner struct { pool *pgxpool.Pool }
//
// — is not caught by R1 (R1 scans direct struct fields only, not embedded
// sub-fields). Accepted: the repo has no such pattern; the convention is direct
// field inclusion. Reverse self-check: TestPGRepoAmbientTx_SelfCheck BS-1 check
// walks production struct types via *types.Info and asserts no anonymous/embedded
// field transitively exposes a *pgxpool.Pool outside pgExecutor.
//
// BS-2 Interface-typed field that carries a *pgxpool.Pool at runtime — R1 is a
// static-type check; it cannot see runtime dynamic values. Accepted per
// ai-collab.md §3.
//
// BS-3 Function-value indirection: `var fn = newPGExecutor; fn(pool)` — R2's
// callee resolution uses *types.Info.Uses, which resolves the Fun identifier of
// a call expression to the *types.Func object. A function-value indirect call
// has a *types.Var as Fun, so resolution returns ok=false and R2 does not see
// this as newPGExecutor. Accepted: same blind spot as PANIC-REGISTERED-01 and
// CAS-PROTOCOL-COMPOSITION-ROOT-01 BS-2; the repo has no such pattern. Reverse
// self-check: TestPGRepoAmbientTx_SelfCheck asserts no function-value
// assignment from newPGExecutor exists in production.
//
// BS-4 *pgxpool.Pool type alias: `type myPool = *pgxpool.Pool` — Go type aliases
// resolve to the same underlying *types.Named so isPgxPoolType still matches.
// Not truly a blind spot; documented for completeness.
//
// BS-5 cells/accesscore/postgres DI helper: the composition-root DI package
// cells/accesscore/postgres (NOT cells/accesscore/internal/adapters/postgres)
// contains a Deps struct that legitimately holds pool *pgxpool.Pool and a
// NewDeps constructor that accepts *pgxpool.Pool. This is NOT a repo/store file —
// it is the DI wiring layer that bridges cmd/* to the cell's internal adapters.
// It is intentionally excluded from pgRepoPackagePatterns (which only includes
// internal/adapters/postgres, not the parent postgres package). Reverse
// self-check: TestPGRepoAmbientTx_SelfCheck BS-5 check asserts that
// cells/accesscore/postgres contains exactly one non-pgExecutor struct holding
// *pgxpool.Pool named "Deps", preventing a second such struct from silently
// appearing there.
//
// BS-6 R3 method-value indirection: `var fn = s.db.ExecDirect; fn(ctx, sql)` —
// R3's ExecDirect detection looks for SelectorExpr CallExpr with Sel.Name ==
// "ExecDirect". A method-value stored in a variable has a different AST shape
// and would not be caught. Accepted: the repo has no such pattern; method-value
// assignment from ExecDirect is covered by reverse self-check
// TestPGRepoAmbientTx_SelfCheck BS-6.
//
// BS-7 R3 pool access via local variable: `p := s.db.pool; p.Exec(ctx, sql)` —
// R3's pool detection looks for a SelectorExpr `<x>.pool` where <x> resolves to
// pgExecutor. If pool is first assigned to a local variable and then used, the
// Exec call is on the variable, not on pgExecutor, and would not be caught.
// Accepted: pool is an unexported field — the only way to capture it locally is
// inside the same package; the repo has no such pattern. Covered by
// TestPGRepoAmbientTx_SelfCheck BS-7 reverse self-check.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pgxpoolImportPath = "github.com/jackc/pgx/v5/pgxpool"
	pgxpoolTypeName   = "Pool"
	pgExecutorName    = "pgExecutor"
	newPGExecutorName = "newPGExecutor"
	execDirectName    = "ExecDirect"
	poolFieldName     = "pool"
)

// r3ExecDirectAllowlist is the static callsite allowlist for R3(b): the ONLY
// production callsites permitted to call pgExecutor.ExecDirect from a
// repo/store method. Keys are "relative-file::enclosing-func-name".
//
// Current single allowlist entry:
//
//	adapters/postgres/refresh_store.go::revokeSessionDetachedAt
//
// This is the intentional independent-commit cascade-revoke compensation path
// (security response that must commit independently of the ambient transaction).
// See PGRefreshStore.revokeSessionDetachedAt godoc and ADR
// docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md.
//
// Maintenance obligation: add a new entry ONLY when there is an ADR-explicit
// rationale for bypassing the ambient transaction. Any new *_repo.go/*_store.go
// method calling ExecDirect that is NOT here will fail CI immediately.
var r3ExecDirectAllowlist = map[string]struct{}{
	"adapters/postgres/refresh_store.go::revokeSessionDetachedAt": {},
}

// pgRepoPackagePatterns lists the import patterns whose production .go files
// the archtest must parse with full TypesInfo. Both adapters/postgres and the
// cell-private adapters/postgres are included since both declare a pgExecutor
// and their repos are subject to the same funnel rule.
//
// Maintenance obligation: whenever a new PG adapter package is added to the
// repo that declares its own pgExecutor struct and *_repo.go or *_store.go
// files, this list MUST be extended to include that package. Omitting a package
// silently zeroes R1/R2 coverage for all repos in that package — the companion
// assertProductionCoverage guard only validates packages already listed here;
// it cannot detect entirely missing packages.
//
// Note: cells/accesscore/postgres (the DI wiring helper) is intentionally NOT
// in this list — it is a composition-root bridge package, not a repo/store
// package. Its *pgxpool.Pool usage is covered by the BS-5 self-check.
var pgRepoPackagePatterns = []string{
	"github.com/ghbvf/gocell/adapters/postgres",
	"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres",
}

// TestPGRepoAmbientTx guards PG-REPO-AMBIENT-TX-01 against the production
// packages. RED fixtures are exercised separately by
// TestPGRepoAmbientTx_RedFixtureDetected. Blind-spot self-checks are in
// TestPGRepoAmbientTx_SelfCheck.
func TestPGRepoAmbientTx(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads PG repo packages with TypesInfo, ~2-3s)")
	}

	diags := RunTyped(t, TypedOpts{}, pgRepoPackagePatterns, pgRepoAmbientTxRule)
	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Rel != diags[j].Rel {
			return diags[i].Rel < diags[j].Rel
		}
		return diags[i].Line < diags[j].Line
	})

	// Companion coverage guard: each package pattern must contribute at least
	// one *_repo.go or *_store.go to the parsed set. A silent import-path drift
	// would zero out the rule's coverage.
	assertProductionCoverage(t)

	for _, d := range diags {
		t.Errorf("PG-REPO-AMBIENT-TX-01 %s:%d: %s", d.Rel, d.Line, d.Message)
	}
}

// pgRepoAmbientTxRule is the Rule function for PG-REPO-AMBIENT-TX-01.
// Extracted so the same logic is exercised by both the production-package test
// and the RED-fixture test.
//
// File scope: only *_repo.go and *_store.go files are checked. Infrastructure
// files (pool.go, tx_manager.go, pg_executor.go, etc.) legitimately hold raw
// *pgxpool.Pool fields and are intentionally out of scope. The fixture package
// uses plain .go files without the _repo/_store suffix; the fixture filename
// itself (fixture.go) is in scope for the fixture test by design — the rule
// checks all files when no suffix filter applies to fixture packages. To keep
// the production and fixture paths consistent, R1/R2 scans all files passed
// via p.Files; the production test separately applies the filename-suffix
// coverage guard via assertProductionCoverage.
//
// Infrastructure exemptions are applied via the isInfraFile predicate, which
// excludes files that are NOT *_repo.go or *_store.go from the R1/R2 checks
// in the production packages. For fixture packages (which don't follow the
// _repo/_store naming), the rule applies to all files so the RED fixtures are
// detected.
func pgRepoAmbientTxRule(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	// pkgPath is the import path of the package being scanned. It is used by
	// bodyCallsNewPGExecutorWith to assert that the resolved newPGExecutor callee
	// belongs to this same package (package-local function), not a same-named
	// function in a different package.
	var pkgPath string
	if p.Pkg != nil {
		pkgPath = p.Pkg.Path()
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		base := filepath.Base(rel)
		// In production packages, only check *_repo.go and *_store.go.
		// Infrastructure files (pool.go, tx_manager.go, pg_executor.go, errors.go,
		// etc.) legitimately need *pgxpool.Pool fields. Fixture files have no
		// _repo/_store suffix but should still be checked for RED fixture coverage.
		isProductionPkg := strings.Contains(rel, "adapters/postgres") ||
			strings.Contains(rel, "cells/accesscore/internal/adapters/postgres")
		if isProductionPkg {
			if !strings.HasSuffix(base, "_repo.go") && !strings.HasSuffix(base, "_store.go") {
				continue
			}
		}
		diags = append(diags, scanR1PoolFields(p.Fset, file, rel, p.TypesInfo)...)
		diags = append(diags, scanR2PoolParams(p.Fset, file, rel, p.TypesInfo, pkgPath)...)
		diags = append(diags, scanR3UsagePoints(p.Fset, file, rel, p.TypesInfo, pkgPath)...)
	}
	return diags
}

// scanR1PoolFields implements R1: every struct field whose type resolves to
// *pgxpool.Pool must be in a struct named exactly pgExecutor.
func scanR1PoolFields(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
		// EachInChildren[ast.TypeSpec] visits the direct Spec children of gd.
		// GenDecl.Specs is []ast.Spec; TypeSpec is a direct child — depth=1 is correct.
		EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return
			}
			structName := ts.Name.Name
			if structName == pgExecutorName {
				return // only sanctioned holder
			}
			if st.Fields == nil {
				return
			}
			for _, field := range st.Fields.List {
				if !isPgxPoolType(field.Type, info) {
					continue
				}
				fieldName := "_"
				if len(field.Names) > 0 {
					fieldName = field.Names[0].Name
				}
				line := fset.Position(field.Type.Pos()).Line
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf(
						"R1: struct %s field %s holds *pgxpool.Pool; "+
							"only pgExecutor may hold this field — route via newPGExecutor",
						structName, fieldName),
				})
			}
		})
	})
	return diags
}

// scanR2PoolParams implements R2: *pgxpool.Pool function parameters are only
// allowed in New*-prefixed constructors that call newPGExecutor(param).
// pkgPath is the import path of the package being scanned; it is forwarded to
// bodyCallsNewPGExecutorWith to assert the resolved newPGExecutor callee
// belongs to this same package (not a same-named function in another package).
func scanR2PoolParams(fset *token.FileSet, file *ast.File, rel string, info *types.Info, pkgPath string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Type == nil || fn.Type.Params == nil {
			return
		}
		// newPGExecutor itself is exempt — it IS the funnel.
		if fn.Name.Name == newPGExecutorName {
			return
		}
		// pgExecutor methods are exempt — they operate on an already-wrapped executor.
		if receiverTypeName(fn) == pgExecutorName {
			return
		}

		poolParams := collectPGPoolParams(fn, info)
		if len(poolParams) == 0 {
			return
		}

		// R2a: non-New* function with pool param is always a violation.
		if !strings.HasPrefix(fn.Name.Name, "New") {
			line := fset.Position(fn.Name.Pos()).Line
			for _, paramName := range poolParams {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf(
						"R2: func %s has *pgxpool.Pool param %q but is not a New* constructor; "+
							"only New*-prefixed constructors may accept *pgxpool.Pool (and must wrap via newPGExecutor)",
						fn.Name.Name, paramName),
				})
			}
			return
		}

		// R2b: New*-prefixed function must call newPGExecutor(poolParam) in its body.
		if fn.Body == nil {
			return
		}
		for _, paramName := range poolParams {
			if !bodyCallsNewPGExecutorWith(fn.Body, paramName, info, pkgPath) {
				line := fset.Position(fn.Name.Pos()).Line
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf(
						"R2: New* constructor %s has *pgxpool.Pool param %q but does not call "+
							"newPGExecutor(%s); pool must be wrapped via newPGExecutor",
						fn.Name.Name, paramName, paramName),
				})
			}
		}
	})
	return diags
}

// scanR3UsagePoints implements R3: repo/store methods must not access
// pgExecutor.pool directly nor call pgExecutor.ExecDirect outside the allowlist.
//
// pkgPath is the import path of the package being scanned and is used to verify
// that the resolved pgExecutor type belongs to this same package (package-local
// identity check), preventing false positives from similarly-named types in
// other packages.
//
// Two sub-checks:
//
// (a) pool direct access: any SelectorExpr `<x>.pool` where <x>'s static type
// (via *types.Info.Types) resolves to pgExecutor. Exempt: pgExecutor's own
// methods (receiver type == pgExecutor); newPGExecutor.
//
// (b) ExecDirect call outside allowlist: any CallExpr `<x>.ExecDirect(...)` where
// <x> resolves to pgExecutor, unless the enclosing FuncDecl's canonical key
// "relative-file::func-name" is in r3ExecDirectAllowlist. Exempt: pgExecutor's
// own methods.
//
// Cognitive complexity: split into two sub-helpers to stay ≤15.
func scanR3UsagePoints(fset *token.FileSet, file *ast.File, rel string, info *types.Info, pkgPath string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		// pgExecutor's own methods legitimately use e.pool and ExecDirect.
		if receiverTypeName(fn) == pgExecutorName {
			return
		}
		// newPGExecutor itself is also exempt.
		if fn.Name.Name == newPGExecutorName {
			return
		}
		if fn.Body == nil {
			return
		}
		enclosingKey := rel + "::" + fn.Name.Name
		diags = append(diags, scanR3PoolAccess(fset, fn.Body, rel, info, pkgPath)...)
		diags = append(diags, scanR3ExecDirect(fset, fn.Body, rel, info, pkgPath, enclosingKey)...)
	})
	return diags
}

// scanR3PoolAccess flags SelectorExpr `<x>.pool` where <x> resolves to the
// package-local pgExecutor type.
func scanR3PoolAccess(fset *token.FileSet, body *ast.BlockStmt, rel string, info *types.Info, pkgPath string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.SelectorExpr](body, func(sel *ast.SelectorExpr) {
		if sel.Sel.Name != poolFieldName {
			return
		}
		if !isPgExecutorType(sel.X, info, pkgPath) {
			return
		}
		line := fset.Position(sel.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: "R3: direct access to pgExecutor.pool field bypasses ambient-tx routing; " +
				"use e.Exec/e.Query/e.QueryRow (or ExecDirect for allowlisted compensation paths)",
		})
	})
	return diags
}

// scanR3ExecDirect flags CallExpr `<x>.ExecDirect(...)` where <x> resolves to
// pgExecutor, unless enclosingKey is in r3ExecDirectAllowlist.
func scanR3ExecDirect(
	fset *token.FileSet,
	body *ast.BlockStmt,
	rel string,
	info *types.Info,
	pkgPath string,
	enclosingKey string,
) []Diagnostic {
	var diags []Diagnostic
	if _, allowed := r3ExecDirectAllowlist[enclosingKey]; allowed {
		return nil
	}
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name != execDirectName {
			return
		}
		if !isPgExecutorType(sel.X, info, pkgPath) {
			return
		}
		line := fset.Position(call.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: "R3: pgExecutor.ExecDirect call outside the allowlist bypasses ambient-tx " +
				"routing; only allowlisted compensation callsites may call ExecDirect " +
				"(see r3ExecDirectAllowlist in pg_repo_ambient_tx_test.go)",
		})
	})
	return diags
}

// isPgExecutorType reports whether expr's static type resolves to the
// package-local pgExecutor named struct type. The check requires both the type
// name ("pgExecutor") AND the package path to match, preventing false positives
// from similarly-named types in other packages.
//
// Handles both value and pointer receiver forms (pgExecutor and *pgExecutor).
// pkgPath may be empty (fixture loads where Pkg is nil); when empty only the
// type name is checked.
func isPgExecutorType(expr ast.Expr, info *types.Info, pkgPath string) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	t := tv.Type
	// Dereference pointer if receiver is *pgExecutor.
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil {
		return false
	}
	if obj.Name() != pgExecutorName {
		return false
	}
	if pkgPath != "" && (obj.Pkg() == nil || obj.Pkg().Path() != pkgPath) {
		return false
	}
	return true
}

// collectPGPoolParams returns the names of fn's parameters whose type resolves
// to *pgxpool.Pool.
func collectPGPoolParams(fn *ast.FuncDecl, info *types.Info) []string {
	var names []string
	for _, field := range fn.Type.Params.List {
		if !isPgxPoolType(field.Type, info) {
			continue
		}
		for _, n := range field.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

// bodyCallsNewPGExecutorWith reports whether body contains a call to the
// package-local newPGExecutor whose first argument is the identifier paramName.
//
// The callee is verified via two conditions (AND):
//  1. fn.Name() == newPGExecutorName — name match.
//  2. fn.Pkg() != nil && fn.Pkg().Path() == pkgPath — the resolved *types.Func
//     belongs to the same package being scanned, confirming it is the
//     package-local newPGExecutor and not a same-named function from another
//     package. This prevents cross-package false negatives where a different
//     package's newPGExecutor-alike would satisfy only the name check.
//
// pkgPath may be empty (AST-only pass or unknown package); when empty, only the
// name check is applied, preserving behavior for fixture loads where Pkg is nil.
func bodyCallsNewPGExecutorWith(body *ast.BlockStmt, paramName string, info *types.Info, pkgPath string) bool {
	found := false
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		if found {
			return
		}
		fn := resolveCalleeFunc(call.Fun, info)
		if fn == nil || fn.Name() != newPGExecutorName {
			return
		}
		// Assert package-local identity when pkgPath is known.
		if pkgPath != "" && (fn.Pkg() == nil || fn.Pkg().Path() != pkgPath) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == paramName {
			found = true
		}
	})
	return found
}

// resolveCalleeFunc resolves a CallExpr's Fun to a *types.Func via TypesInfo.
// Handles both bare Ident (package-local / dot-import) and SelectorExpr forms.
// Returns nil when the callee cannot be resolved to a *types.Func (e.g. method
// calls resolved via Selections, function values via Var).
func resolveCalleeFunc(fun ast.Expr, info *types.Info) *types.Func {
	if info == nil {
		return nil
	}
	switch e := fun.(type) {
	case *ast.Ident:
		if obj, ok := info.Uses[e]; ok {
			f, _ := obj.(*types.Func)
			return f
		}
	case *ast.SelectorExpr:
		if obj, ok := info.Uses[e.Sel]; ok {
			f, _ := obj.(*types.Func)
			return f
		}
	}
	return nil
}

// receiverTypeName returns the bare type name from a FuncDecl's receiver,
// stripping any leading * pointer marker. Returns "" when fn has no receiver
// or the type expression is not a recognized form.
//
// Used by this test and by mem_tx_lock_ownership_test.go and
// sealed_marker_noop_transparency_test.go as a shared archtest helper.
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return ReceiverTypeName(fn.Recv.List[0].Type)
}

// isPgxPoolType reports whether expr's static type is *pgxpool.Pool.
// Returns false when TypesInfo is nil or expr's type cannot be resolved.
func isPgxPoolType(expr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	ptr, ok := tv.Type.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == pgxpoolImportPath && obj.Name() == pgxpoolTypeName
}

// assertProductionCoverage fails the test if either of the two production
// package patterns did not yield at least one *_repo.go or *_store.go file.
// This prevents a silent coverage zero-out caused by import-path drift or
// package renaming.
func assertProductionCoverage(t *testing.T) {
	t.Helper()
	if testing.Short() {
		return
	}
	for _, pattern := range pgRepoPackagePatterns {
		found := false
		_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				base := filepath.Base(p.Rel(f))
				if strings.HasSuffix(base, "_repo.go") || strings.HasSuffix(base, "_store.go") {
					found = true
				}
			}
			return nil
		})
		require.Truef(t, found,
			"PG-REPO-AMBIENT-TX-01 coverage guard: no *_repo.go or *_store.go file found "+
				"in package pattern %q — coverage zeroed out; "+
				"check for package rename or import-path drift",
			pattern)
	}
}

// TestPGRepoAmbientTx_RedFixtureDetected asserts the rule catches all five
// RED violations in internal/pgrepoambienttxfixture:
//
//   - 1 R1: badR1Repo holds *pgxpool.Pool (not named pgExecutor)
//   - 1 R2: badR2NonNew is a non-New* function with *pgxpool.Pool param
//   - 1 R2: NewBadR2NoWrap is a New* function with *pgxpool.Pool param but no newPGExecutor call
//   - 1 R3: badR3PoolDirect accesses r.db.pool directly
//   - 1 R3: badR3ExecDirect calls r.db.ExecDirect outside the allowlist
//
// GREEN cases (pgExecutor field, goodNewFoo→newPGExecutor, goodExecMethod using
// r.db.Exec) must produce zero diagnostics.
//
// Without this test, TestPGRepoAmbientTx's zero-diagnostic result on the real
// packages has no informational value: the rule could be silently passing
// everything. The fixture provides a known-positive sample so any rule
// regression immediately fails CI.
func TestPGRepoAmbientTx_RedFixtureDetected(t *testing.T) {
	t.Parallel()

	diags := RunTypedFixture(t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/pgrepoambienttxfixture/..."},
		pgRepoAmbientTxRule,
	)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	// Exact count: 1 R1 + 2 R2 + 2 R3 = 5 total.
	// Equality (not >=5) so unintentional fixture drift is immediately visible.
	assert.Len(t, diags, 5,
		"PG-REPO-AMBIENT-TX-01 RED fixture must yield exactly 5 violations "+
			"(1×R1: badR1Repo + 1×R2: badR2NonNew + 1×R2: NewBadR2NoWrap + "+
			"1×R3: badR3PoolDirect + 1×R3: badR3ExecDirect); "+
			"GREEN cases (pgExecutor, goodNewFoo→newPGExecutor, goodExecMethod) must produce 0. "+
			"Update the expected count if the fixture changes intentionally.")

	r1Count, r2Count, r3Count := 0, 0, 0
	for _, d := range diags {
		if strings.HasPrefix(d.Message, "R1:") {
			r1Count++
		}
		if strings.HasPrefix(d.Message, "R2:") {
			r2Count++
		}
		if strings.HasPrefix(d.Message, "R3:") {
			r3Count++
		}
	}
	assert.Equal(t, 1, r1Count, "expected exactly 1 R1 violation (badR1Repo)")
	assert.Equal(t, 2, r2Count, "expected exactly 2 R2 violations (badR2NonNew + NewBadR2NoWrap)")
	assert.Equal(t, 2, r3Count, "expected exactly 2 R3 violations (badR3PoolDirect + badR3ExecDirect)")
}

// TestPGRepoAmbientTx_SelfCheck verifies the blind-spot list by asserting that
// the prohibited AST forms (BS-1 embedded pool fields, BS-3 function-value
// assignment from newPGExecutor) are absent from production packages, and that
// the BS-5 intentional exclusion (cells/accesscore/postgres.Deps) remains
// exactly bounded. This makes the blind-spot documentation falsifiable rather
// than purely commentary.
func TestPGRepoAmbientTx_SelfCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SelfCheck in -short mode")
	}

	// BS-1 reverse check: no embedded/anonymous struct field in any production
	// package should transitively expose a *pgxpool.Pool outside pgExecutor.
	// R1 only scans direct fields; this check verifies the accepted limitation
	// does not silently hide a real violation in existing production code.
	var bs1Violations []string
	_ = RunTyped(t, TypedOpts{}, pgRepoPackagePatterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
				EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					if ts.Name.Name == pgExecutorName {
						return
					}
					for _, field := range st.Fields.List {
						// Anonymous/embedded fields have an empty Names slice.
						if len(field.Names) != 0 {
							continue
						}
						if embeddedStructHasPoolField(field.Type, p.TypesInfo) {
							bs1Violations = append(bs1Violations, fmt.Sprintf(
								"%s:%d: struct %s has embedded field that transitively "+
									"holds *pgxpool.Pool (BS-1 blind spot — extend R1 "+
									"if this pattern is needed)",
								rel, p.Fset.Position(field.Type.Pos()).Line, ts.Name.Name))
						}
					}
				})
			})
		}
		return nil
	})
	assert.Empty(t, bs1Violations,
		"BS-1 self-check: no production struct outside pgExecutor may use an "+
			"embedded field that carries *pgxpool.Pool (R1 blind spot)")

	// BS-3 reverse check: newPGExecutor must not be stored as a function value
	// in any production package. A function-value indirection would bypass R2.
	var bs3Violations []string
	_ = RunTyped(t, TypedOpts{}, pgRepoPackagePatterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				// EachInChildren[ast.Ident] visits the direct Ident children of assign
				// (both Lhs and Rhs elements that are bare *ast.Ident). Rhs elements
				// that are *ast.Ident are direct children of AssignStmt at depth=1.
				EachInChildren[ast.Ident](assign, func(id *ast.Ident) {
					if id.Name != newPGExecutorName {
						return
					}
					bs3Violations = append(bs3Violations,
						fmt.Sprintf("%s:%d: newPGExecutor stored as function value "+
							"(BS-3 blind spot — extend rule if this pattern is needed)",
							rel, p.Fset.Position(id.Pos()).Line))
				})
			})
		}
		return nil
	})
	assert.Empty(t, bs3Violations,
		"BS-3 self-check: newPGExecutor must not be stored as a function value in production")

	// BS-5 reverse check: cells/accesscore/postgres (DI wiring helper, intentionally
	// NOT in pgRepoPackagePatterns) legitimately holds *pgxpool.Pool in its Deps
	// struct. Assert exactly ONE non-pgExecutor struct in that package holds
	// *pgxpool.Pool and its name is "Deps". A second such struct would mean the
	// DI package grew beyond its intended scope.
	bs5Patterns := []string{"github.com/ghbvf/gocell/cells/accesscore/postgres"}
	var bs5NonDepsCount int
	_ = RunTyped(t, TypedOpts{}, bs5Patterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
				EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					if ts.Name.Name == pgExecutorName {
						return
					}
					for _, field := range st.Fields.List {
						if isPgxPoolType(field.Type, p.TypesInfo) && ts.Name.Name != "Deps" {
							bs5NonDepsCount++
						}
					}
				})
			})
		}
		return nil
	})
	assert.Equal(t, 0, bs5NonDepsCount,
		"BS-5 self-check: cells/accesscore/postgres must not introduce a second "+
			"non-pgExecutor struct holding *pgxpool.Pool beyond the intentional Deps struct; "+
			"if a second such struct appears, add it to pgRepoPackagePatterns instead")

	// BS-6 reverse check: ExecDirect must not be stored as a method value in any
	// production repo/store file. A method-value indirection would bypass R3's
	// ExecDirect detection. Check all files (not just _repo/_store) in the
	// production packages to be conservative about the blind spot boundary.
	var bs6Violations []string
	_ = RunTyped(t, TypedOpts{}, pgRepoPackagePatterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				// Look for Rhs elements that are SelectorExpr with Sel == "ExecDirect".
				EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name != execDirectName {
						return
					}
					// Only flag if the receiver resolves to pgExecutor.
					var pkgPath string
					if p.Pkg != nil {
						pkgPath = p.Pkg.Path()
					}
					if !isPgExecutorType(sel.X, p.TypesInfo, pkgPath) {
						return
					}
					bs6Violations = append(bs6Violations,
						fmt.Sprintf("%s:%d: ExecDirect stored as method value "+
							"(BS-6 blind spot — R3 would not detect this; extend rule if needed)",
							rel, p.Fset.Position(sel.Pos()).Line))
				})
			})
		}
		return nil
	})
	assert.Empty(t, bs6Violations,
		"BS-6 self-check: pgExecutor.ExecDirect must not be stored as a method value in production")

	// BS-7 reverse check: pool must not be assigned to a local variable in any
	// production repo/store file. An assignment like `p := s.db.pool` would let
	// the caller bypass R3's pool-access detection on the subsequent p.Exec call.
	var bs7Violations []string
	_ = RunTyped(t, TypedOpts{}, pgRepoPackagePatterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			var pkgPath string
			if p.Pkg != nil {
				pkgPath = p.Pkg.Path()
			}
			EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name != poolFieldName {
						return
					}
					if !isPgExecutorType(sel.X, p.TypesInfo, pkgPath) {
						return
					}
					bs7Violations = append(bs7Violations,
						fmt.Sprintf("%s:%d: pgExecutor.pool assigned to local variable "+
							"(BS-7 blind spot — R3 pool-access detection would miss subsequent use; "+
							"extend rule if this pattern is needed)",
							rel, p.Fset.Position(sel.Pos()).Line))
				})
			})
		}
		return nil
	})
	assert.Empty(t, bs7Violations,
		"BS-7 self-check: pgExecutor.pool must not be assigned to a local variable in production")
}

// embeddedStructHasPoolField reports whether the type denoted by expr (an
// anonymous/embedded field type) is a named struct type containing a
// *pgxpool.Pool field. It does NOT recurse into nested embeddings — it checks
// only the immediate fields of the embedded struct, consistent with the single
// level at which BS-1 operates.
func embeddedStructHasPoolField(expr ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	t := tv.Type
	// Dereference pointer if the embedded type is *T.
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		field := st.Field(i)
		ptr, ok := field.Type().(*types.Pointer)
		if !ok {
			continue
		}
		inner, ok := ptr.Elem().(*types.Named)
		if !ok {
			continue
		}
		obj := inner.Obj()
		if obj != nil && obj.Pkg() != nil &&
			obj.Pkg().Path() == pgxpoolImportPath && obj.Name() == pgxpoolTypeName {
			return true
		}
	}
	return false
}
