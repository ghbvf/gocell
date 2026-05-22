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
// 下游 Hard / 上游 Medium (PG-REPO-AMBIENT-TX-UPSTREAM-HARD-01).
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
// # RED fixtures
//
// The authoritative expected-violation set is defined as expectedFixtureViolations
// (a []fixtureViolation slice in this file) and asserted by
// TestPGRepoAmbientTx_RedFixtureDetected. Each entry is a (ruleID_prefix, source
// line) pair pinned to tools/archtest/internal/pgrepoambienttxfixture/fixture.go.
// The fixture file's package godoc enumerates each RED case and its GREEN controls.
// Do not duplicate the list here or in ADR §4.5.1 — update expectedFixtureViolations
// and fixture.go together when the fixture changes intentionally.
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
// self-check: TestPGRepoAmbientTx_SelfCheck BS-3 uses identity-based detection
// (EachInSubtree[ast.Ident] + *types.Info.Uses, excluding direct-call callee
// positions) to assert no function-value use of newPGExecutor exists in any
// form (ValueSpec, call argument, return, composite-literal) in production.
//
// BS-4 *pgxpool.Pool type alias: `type myPool = *pgxpool.Pool` — Go type aliases
// resolve to the same underlying *types.Named so isPgxPoolType still matches.
// Not truly a blind spot; documented for completeness.
//
// BS-5 cells/accesscore PGBundle helper: after the Bundle funnel
// (PR #595 follow-up), the composition-root PG wiring for accesscore lives
// in cells/accesscore/pg_bundle.go (package accesscore). NewPGBundle accepts
// *pgxpool.Pool as a constructor parameter but the PGBundle struct does NOT
// retain it — only derived primitives (userRepo / roleRepo / setupLock /
// txRunner). cells/accesscore is intentionally excluded from
// pgRepoPackagePatterns. Reverse self-check: TestPGRepoAmbientTx_SelfCheck
// BS-5 now asserts no struct in cells/accesscore (outside pgExecutor and
// internal/) carries *pgxpool.Pool as a field — function-signature usage in
// NewPGBundle is permitted because it does not persist the pool.
//
// BS-6 R3 method-value indirection: `var fn = s.db.ExecDirect; fn(ctx, sql)` —
// R3's ExecDirect detection looks for a CallExpr whose Fun is a SelectorExpr with
// Sel.Name == "ExecDirect". A method-value stored in a variable has a different
// AST shape (SelectorExpr not directly enclosed by CallExpr.Fun) and would not be
// caught by R3. Accepted: the repo has no such pattern. Reverse self-check:
// TestPGRepoAmbientTx_SelfCheck BS-6 uses identity-based detection
// (EachInSubtree[ast.Ident] + *types.Info.Uses on Sel idents, excluding
// direct-call callee positions) to assert no method-value use of ExecDirect on
// pgExecutor exists in any form in production.
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

// modulePathPrefix is the go.mod module path plus trailing slash. Trimming it
// from a pgRepoPackagePatterns entry yields the module-relative package
// directory, which is the form Pass.Rel() / Diagnostic.Rel use. Deriving the
// production-scope predicate from this single source (see isProductionRepoPkg)
// removes the former hand-maintained literal list that silently drifted out of
// sync with pgRepoPackagePatterns (the iotdevice pattern was missing from it).
const modulePathPrefix = "github.com/ghbvf/gocell/"

// pgRepoPackagePatterns lists the import patterns whose production .go files
// the archtest must parse with full TypesInfo. All three PG adapter packages
// (adapters/postgres, the accesscore cell-private adapter, and the iotdevice
// example cell-private adapter) are included since each declares a pgExecutor
// and their repos are subject to the same funnel rule.
//
// Maintenance obligation: whenever a new PG adapter package is added to the
// repo that declares its own pgExecutor struct and *_repo.go or *_store.go
// files, this list MUST be extended to include that package. Omitting a package
// silently zeroes R1/R2 coverage for all repos in that package — the companion
// assertProductionCoverage guard only validates packages already listed here;
// it cannot detect entirely missing packages.
//
// Note: cells/accesscore (the PGBundle host) is intentionally NOT in this
// list — it is a composition-root bridge package, not a repo/store package.
// NewPGBundle's *pgxpool.Pool function-signature usage is covered by the
// BS-5 self-check, which asserts no struct in cells/accesscore (outside the
// internal/ tree) carries *pgxpool.Pool as a field.
var pgRepoPackagePatterns = []string{
	"github.com/ghbvf/gocell/adapters/postgres",
	"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres",
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/adapters/postgres",
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
		// The production-scope predicate is DERIVED from pgRepoPackagePatterns
		// (single source) so adding a fourth PG adapter package cannot leave the
		// suffix filter silently disabled for it.
		if isProductionRepoPkg(rel) {
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

// isProductionRepoPkg reports whether the module-relative file path rel lives
// in one of the production PG adapter packages listed in pgRepoPackagePatterns.
// It is the single-source replacement for the former hand-maintained substring
// disjunction; the iotdevice package pattern was absent from that literal list,
// so its files escaped the *_repo.go/*_store.go suffix filter. Fixture packages
// (loaded under a separate tools/archtest/internal/... pattern) are never under
// these prefixes, so they correctly return false and remain fully scanned.
//
// rel is always a *.go file path (from Pass.Rel), never the package directory,
// so only HasPrefix("<dir>/") matches — equality against a bare directory is
// not a reachable case and intentionally not checked.
func isProductionRepoPkg(rel string) bool {
	for _, pattern := range pgRepoPackagePatterns {
		relDir := strings.TrimPrefix(pattern, modulePathPrefix)
		if relDir == pattern {
			continue // not a module-local pattern; defensive, should not happen
		}
		if strings.HasPrefix(rel, relDir+"/") {
			return true
		}
	}
	return false
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
						structName, fieldName,
					),
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
						fn.Name.Name, paramName,
					),
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
						fn.Name.Name, paramName, paramName,
					),
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

// assertProductionCoverage fails the test if any of the production package
// patterns in pgRepoPackagePatterns did not yield at least one *_repo.go or
// *_store.go file. This prevents a silent coverage zero-out caused by
// import-path drift or package renaming.
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

// fixtureViolation is a (ruleID_prefix, line) pair identifying one expected
// RED fixture diagnostic. The ruleID_prefix matches the start of Diagnostic.Message
// ("R1:", "R2:", "R3:"). Line is the 1-based source line from the fixture file
// as returned by fset.Position. Both fields must match exactly.
type fixtureViolation struct {
	rulePrefix string
	line       int
}

// expectedFixtureViolations is the authoritative expected set for
// TestPGRepoAmbientTx_RedFixtureDetected. Each entry must correspond 1:1 with
// exactly one diagnostic produced by pgRepoAmbientTxRule on the fixture package.
//
// Lines are pinned to the fixture source at
// tools/archtest/internal/pgrepoambienttxfixture/fixture.go. When the fixture
// changes intentionally, update both the fixture and this set together —
// a mismatch is always a bug.
//
// Mask check: losing badR1Repo while gaining a different spurious R1 at a
// different line changes the line, so the set still fails (two different lines
// for the same rulePrefix). A substitution at the SAME line is caught by the
// test because the set sizes must match AND every actual entry must be in the
// expected set.
var expectedFixtureViolations = []fixtureViolation{
	// R1: badR1Repo — struct field pool *pgxpool.Pool at line 88 of fixture.go
	{"R1:", 88},
	// R2: badR2NonNew — non-New* func with *pgxpool.Pool param, func name at line 93
	{"R2:", 93},
	// R2: NewBadR2NoWrap — New* func without newPGExecutor call, func name at line 99
	{"R2:", 99},
	// R3: badR3PoolDirect — r.db.pool direct access at line 117
	{"R3:", 117},
	// R3: badR3ExecDirect — r.db.ExecDirect outside allowlist at line 123
	{"R3:", 123},
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
// The assertion is an exact-set match on (ruleID_prefix, line) pairs from
// expectedFixtureViolations. This prevents a masked substitution where losing
// badR1Repo but gaining a different spurious R1 at a different line would still
// yield r1Count==1 under the old category-count form.
//
// Without this test, TestPGRepoAmbientTx's zero-diagnostic result on the real
// packages has no informational value: the rule could be silently passing
// everything. The fixture provides a known-positive sample so any rule
// regression immediately fails CI.
func TestPGRepoAmbientTx_RedFixtureDetected(t *testing.T) {
	t.Parallel()

	diags := RunTypedFixture(
		t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/pgrepoambienttxfixture/..."},
		pgRepoAmbientTxRule,
	)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	// Build actual (rulePrefix, line) set from produced diagnostics.
	type diagKey struct {
		rulePrefix string
		line       int
	}
	actualSet := make(map[diagKey]struct{}, len(diags))
	for _, d := range diags {
		var prefix string
		switch {
		case strings.HasPrefix(d.Message, "R1:"):
			prefix = "R1:"
		case strings.HasPrefix(d.Message, "R2:"):
			prefix = "R2:"
		case strings.HasPrefix(d.Message, "R3:"):
			prefix = "R3:"
		default:
			t.Errorf("unexpected diagnostic with unrecognized rulePrefix at %s:%d: %s",
				d.Rel, d.Line, d.Message)
			continue
		}
		actualSet[diagKey{prefix, d.Line}] = struct{}{}
	}

	// Build expected set.
	expectedSet := make(map[diagKey]struct{}, len(expectedFixtureViolations))
	for _, v := range expectedFixtureViolations {
		expectedSet[diagKey(v)] = struct{}{}
	}

	// Assert exact match: every expected entry must be in actual, and vice versa.
	// Using two-sided diff so the error message names the missing/extra entries.
	for k := range expectedSet {
		if _, ok := actualSet[k]; !ok {
			t.Errorf("PG-REPO-AMBIENT-TX-01 RED fixture: expected %s violation at line %d "+
				"but it was NOT produced — rule may have regressed or fixture line shifted; "+
				"update expectedFixtureViolations if fixture changed intentionally",
				k.rulePrefix, k.line)
		}
	}
	for k := range actualSet {
		if _, ok := expectedSet[k]; !ok {
			t.Errorf("PG-REPO-AMBIENT-TX-01 RED fixture: unexpected %s violation at line %d "+
				"— rule produced an extra diagnostic not in expectedFixtureViolations; "+
				"update expectedFixtureViolations if fixture changed intentionally",
				k.rulePrefix, k.line)
		}
	}
	if len(actualSet) != len(expectedSet) {
		t.Errorf("PG-REPO-AMBIENT-TX-01 RED fixture: got %d unique (rulePrefix, line) entries, "+
			"want %d; GREEN cases (pgExecutor, goodNewFoo→newPGExecutor, goodExecMethod) must produce 0; "+
			"see expectedFixtureViolations for the authoritative list",
			len(actualSet), len(expectedSet))
	}
}

// TestPGRepoAmbientTx_SelfCheck verifies the blind-spot list by asserting that
// prohibited AST forms are absent from production packages and that the BS-5
// intentional exclusion (cells/accesscore) remains exactly bounded.
// This makes the blind-spot documentation falsifiable rather than purely commentary.
//
// BS-3: identity-based detection via EachInSubtree[ast.Ident]+*types.Info.Uses
// covers all function-value forms of newPGExecutor (ValueSpec, call argument,
// return, composite-literal), not just AssignStmt. Any Ident resolving to
// newPGExecutor that is NOT in the callee position of a direct call is flagged.
//
// BS-5: after the Bundle funnel collapse (PR #595), no struct in
// cells/accesscore (outside internal/) may carry *pgxpool.Pool as a field —
// NewPGBundle is the sole sanctioned consumer and uses pool only as a
// function parameter without persisting it.
//
// BS-6: identity-based detection via EachInSubtree[ast.Ident]+*types.Info.Uses
// covers all method-value forms of ExecDirect on pgExecutor (ValueSpec, call
// argument, return, composite-literal), not just AssignStmt. Receiver type is
// verified via *types.Func.Type().(*types.Signature).Recv() rather than by
// AST receiver expression type, making it independent of AST statement shape.
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
								rel, p.Fset.Position(field.Type.Pos()).Line, ts.Name.Name,
							))
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

	// BS-3 reverse check: newPGExecutor must not appear as a function value (i.e.,
	// used as a value rather than directly called) in any production package.
	// A function-value indirection bypasses R2 because R2's callee resolution uses
	// *types.Info.Uses on the Fun ident of a direct CallExpr; an indirect call via
	// a stored function value has a *types.Var as Fun, so resolution returns ok=false.
	//
	// Detection (identity-based, covers all AST forms uniformly):
	//   1. Walk EachInSubtree[ast.Ident] over all production files.
	//   2. Resolve each Ident via *types.Info.Uses to a *types.Func.
	//   3. If the resolved func is the package-local newPGExecutor (name + pkg path),
	//      check whether the Ident is in the callee position of a direct call:
	//      collect all such callee Ident positions from EachInSubtree[ast.CallExpr]
	//      in a pre-pass, then exclude them.
	//   4. Any remaining Ident that resolves to newPGExecutor is a function-value use
	//      (ValueSpec, call argument, return value, composite-literal element, etc.).
	//
	// This is strictly broader than the former AssignStmt-only scan and matches the
	// godoc claim "asserts no function-value assignment from newPGExecutor".
	var bs3Violations []string
	_ = RunTyped(t, TypedOpts{}, pgRepoPackagePatterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		pkgPath := ""
		if p.Pkg != nil {
			pkgPath = p.Pkg.Path()
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			bs3Violations = append(bs3Violations,
				findNewPGExecutorValueUses(p.Fset, file, rel, p.TypesInfo, pkgPath)...)
		}
		return nil
	})
	assert.Empty(t, bs3Violations,
		"BS-3 self-check: newPGExecutor must not be used as a function value in production; "+
			"all non-callee Ident references to newPGExecutor are covered by identity resolution")

	// BS-5 reverse check: after Bundle funnel collapse (PR #595), no struct
	// in cells/accesscore (outside the internal/ tree, intentionally NOT in
	// pgRepoPackagePatterns) may carry *pgxpool.Pool as a field. NewPGBundle
	// accepts *pgxpool.Pool as a function parameter to derive repos/setupLock,
	// but PGBundle itself stores only derived primitives — never the pool.
	bs5Patterns := []string{"github.com/ghbvf/gocell/cells/accesscore"}
	var bs5PoolFieldCount int
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
						if isPgxPoolType(field.Type, p.TypesInfo) {
							bs5PoolFieldCount++
						}
					}
				})
			})
		}
		return nil
	})
	// Bundle funnel anchor: PGBundle holds only derived primitives, never the pool.
	assert.Equal(t, 0, bs5PoolFieldCount,
		"BS-5 self-check: no struct in cells/accesscore (outside the internal/ tree) "+
			"may carry *pgxpool.Pool as a field — PGBundle must hold only derived "+
			"primitives (userRepo/roleRepo/setupLock/txRunner). If a new struct "+
			"legitimately needs to hold the pool, add it to pgRepoPackagePatterns instead.")

	// BS-6 reverse check: ExecDirect must not appear as a method value (i.e., used
	// as a value rather than directly called) in any production file. A method-value
	// indirection bypasses R3's ExecDirect detection because R3 looks for a CallExpr
	// whose Fun is a SelectorExpr; a method value stored in a variable has a
	// different AST shape (SelectorExpr without enclosing CallExpr.Fun).
	//
	// Detection (identity-based, covers all AST forms uniformly):
	//   1. Pre-pass: collect Ident positions that are the Sel of a SelectorExpr that
	//      is directly used as CallExpr.Fun (direct call to ExecDirect on pgExecutor).
	//   2. Walk EachInSubtree[ast.Ident] over all production files.
	//   3. Resolve each Ident via *types.Info.Uses to its object; filter to those
	//      whose Sel.Name == "ExecDirect" AND whose receiver resolves to pgExecutor.
	//   4. Exclude Idents in direct-call callee position (step 1). Remaining Idents
	//      are method-value uses (ValueSpec, call argument, return, composite-lit).
	//
	// Check all files (not just _repo/_store) in the production packages to be
	// conservative about the blind spot boundary.
	var bs6Violations []string
	_ = RunTyped(t, TypedOpts{}, pgRepoPackagePatterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		pkgPath := ""
		if p.Pkg != nil {
			pkgPath = p.Pkg.Path()
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			bs6Violations = append(bs6Violations,
				findExecDirectValueUses(p.Fset, file, rel, p.TypesInfo, pkgPath)...)
		}
		return nil
	})
	assert.Empty(t, bs6Violations,
		"BS-6 self-check: pgExecutor.ExecDirect must not be used as a method value in production; "+
			"all non-callee Sel references to ExecDirect on pgExecutor are covered by identity resolution")

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

// findNewPGExecutorValueUses returns violations for BS-3: any Ident in the file
// that resolves (via *types.Info.Uses) to the package-local newPGExecutor function
// AND is NOT in the callee position of a direct call. This covers all value-use
// forms — ValueSpec, call argument, return value, composite-literal element, etc.
// — that the former AssignStmt-only scan missed.
//
// Two-pass approach:
//  1. Collect all Ident positions that serve as the direct-call callee of newPGExecutor
//     (these are legitimate call sites, not function-value uses).
//  2. Walk all Idents via EachInSubtree; flag those resolving to newPGExecutor that
//     are NOT in the callee set.
func findNewPGExecutorValueUses(fset *token.FileSet, file *ast.File, rel string, info *types.Info, pkgPath string) []string {
	// Pass 1: collect callee-ident positions for direct newPGExecutor calls.
	calleePos := make(map[token.Pos]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		fn := resolveCalleeFunc(call.Fun, info)
		if fn == nil || fn.Name() != newPGExecutorName {
			return
		}
		if pkgPath != "" && (fn.Pkg() == nil || fn.Pkg().Path() != pkgPath) {
			return
		}
		// Record the Fun identifier position as a known-callee.
		switch e := call.Fun.(type) {
		case *ast.Ident:
			calleePos[e.Pos()] = struct{}{}
		case *ast.SelectorExpr:
			calleePos[e.Sel.Pos()] = struct{}{}
		}
	})

	// Pass 2: flag any Ident resolving to newPGExecutor that is NOT a direct-call callee.
	var violations []string
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		obj, ok := info.Uses[id]
		if !ok {
			return
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return
		}
		if fn.Name() != newPGExecutorName {
			return
		}
		if pkgPath != "" && (fn.Pkg() == nil || fn.Pkg().Path() != pkgPath) {
			return
		}
		if _, isCallee := calleePos[id.Pos()]; isCallee {
			return // legitimate direct call — not a function-value use
		}
		violations = append(violations,
			fmt.Sprintf("%s:%d: newPGExecutor used as function value (not a direct call) "+
				"(BS-3 blind spot — R2 would not detect indirect calls; extend rule if needed)",
				rel, fset.Position(id.Pos()).Line))
	})
	return violations
}

// findExecDirectValueUses returns violations for BS-6: any Ident in the file that
// resolves (via *types.Info.Uses) to the ExecDirect method on a pgExecutor receiver
// AND is NOT in the callee position of a direct call. This covers all method-value
// forms — ValueSpec, call argument, return value, composite-literal element, etc.
// — that the former AssignStmt+EachInChildren[SelectorExpr] scan missed.
//
// Note: for methods, *types.Info.Uses maps the Sel ident of a SelectorExpr to the
// method's *types.Func object. We check that the Sel's object is a *types.Func whose
// receiver is pgExecutor (via *types.Func.Type().(*types.Signature).Recv()).
//
// Two-pass approach:
//  1. Collect Sel-ident positions for direct ExecDirect calls on pgExecutor receivers.
//  2. Walk all Idents; flag those resolving to ExecDirect on pgExecutor that are NOT
//     in the callee set.
func findExecDirectValueUses(fset *token.FileSet, file *ast.File, rel string, info *types.Info, pkgPath string) []string {
	// Pass 1: collect callee-ident positions for direct ExecDirect calls.
	calleePos := make(map[token.Pos]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != execDirectName {
			return
		}
		if !isPgExecutorType(sel.X, info, pkgPath) {
			return
		}
		calleePos[sel.Sel.Pos()] = struct{}{}
	})

	// Pass 2: flag any Ident named "ExecDirect" resolving to a method on pgExecutor
	// that is NOT a direct-call callee.
	var violations []string
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		if id.Name != execDirectName {
			return
		}
		obj, ok := info.Uses[id]
		if !ok {
			return
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return
		}
		// Check that the method receiver type is pgExecutor.
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Recv() == nil {
			return
		}
		recv := sig.Recv().Type()
		if ptr, ok := recv.(*types.Pointer); ok {
			recv = ptr.Elem()
		}
		named, ok := recv.(*types.Named)
		if !ok || named.Obj() == nil || named.Obj().Name() != pgExecutorName {
			return
		}
		if pkgPath != "" && (named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != pkgPath) {
			return
		}
		if _, isCallee := calleePos[id.Pos()]; isCallee {
			return // legitimate direct call — not a method-value use
		}
		violations = append(violations,
			fmt.Sprintf("%s:%d: pgExecutor.ExecDirect used as method value (not a direct call) "+
				"(BS-6 blind spot — R3 would not detect this; extend rule if needed)",
				rel, fset.Position(id.Pos()).Line))
	})
	return violations
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
