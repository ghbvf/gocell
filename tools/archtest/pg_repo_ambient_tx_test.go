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
//     <x>.ExecDirect(...) without a sibling pgrepoapproved.ApprovedExecDirect
//     (const-literal-reason) marker call in the same FuncDecl body. The marker
//     is a typed funnel from pkg/pgrepoapproved; (callee, arg) form-uniqueness
//     plus same-body co-location pin the allowed bypass to its source location.
//     Currently exactly one production callsite holds a marker:
//     adapters/postgres/refresh_store.go::revokeSessionDetachedAt
//     (the intentional independent-commit cascade-revoke compensation path).
//
// # Discovery
//
// The set of in-scope packages is auto-derived at test time by
// discoverPGAdapterPackages: a production package is in scope iff its scope
// declares a type named "pgExecutor". This replaces the former hand-maintained
// pgRepoPackagePatterns list — a structural property the rule already relies
// on becomes the discovery signal, so adding a new PG adapter package that
// follows the pgExecutor convention is automatically covered, and renaming
// pgExecutor out of scope is caught by TestPGRepoAmbientTx_DiscoveryCoverage.
// Packages that use a different funnel pattern (e.g., configcore's
// Session/DBTX) are correctly excluded; they require a separate archtest if
// equivalent governance is desired.
//
// # AI-robust grading (Funnel 双向锁评级)
//
// 下游 Hard / 上游 Medium (Hard terminal state tracked: gh issue #916 PGEXECUTOR-SEAL-INTERFACE-01).
//
// 上游 Medium: intra-package compile Hard is unreachable — adapters/postgres
// repos share the package with pgExecutor; Go package-level visibility means a
// sibling file can always reach pgExecutor.pool or add its own field — the
// compiler cannot block it. The ceiling is archtest-bound form-uniqueness (same
// grade and precedent as PANIC-REGISTERED-01 / panic(panicregister.Approved)
// per ai-robust.md §Hard 范本 #2 caveat). R1+R2 guard holding+construction;
// R3(a) pins pool field access; R3(b) elevates the former hand-maintained
// string allowlist to a Hard typed marker funnel
// (pkg/pgrepoapproved.ApprovedExecDirect — sibling deployment of
// panicregister.Approved). Discovery itself moved from Soft hand-maintained
// list to Medium type-aware auto-derivation in PR #502 (issue #823).
// Hard terminal state for the upstream remains: seal pgExecutor behind an
// exported interface + private construction funnel making bypass unexpressible
// package-externally — tracked at gh issue #916 (PGEXECUTOR-SEAL-INTERFACE-01).
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
//     resolves to pgExecutor, unless the same FuncDecl body contains a sibling
//     CallExpr to pkg/pgrepoapproved.ApprovedExecDirect whose first argument is
//     a string-typed constant (const literal). Callee identity and arg
//     constness are both verified via *types.Info; no string anchors / no
//     hand-maintained map. Exempted: pgExecutor's own methods (they
//     legitimately use e.pool / call ExecDirect on self); newPGExecutor
//     constructor.
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
// ai-robust.md §3.
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
// txRunner). cells/accesscore is intentionally excluded from discovery: its
// package scope does not declare pgExecutor, so discoverPGAdapterPackages
// does not place it in scope. Reverse self-check:
// TestPGRepoAmbientTx_SelfCheck BS-5 asserts no struct in cells/accesscore
// (outside pgExecutor and internal/) carries *pgxpool.Pool as a field —
// function-signature usage in NewPGBundle is permitted because it does not
// persist the pool.
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
//
// BS-8 R3(b) spurious marker: pgrepoapproved.ApprovedExecDirect marker placed
// in a FuncDecl body that does NOT actually call pgExecutor.ExecDirect — a
// review hazard because it documents an ADR-approved bypass that never
// happens, decaying the audit trail. R3(b) itself does not check this (it
// only fires when ExecDirect is called without a marker). Covered by
// TestPGRepoAmbientTx_SelfCheck BS-8: scans every production FuncDecl body
// holding a marker and requires a co-located pgExecutor.ExecDirect call.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const (
	pgxpoolImportPath           = "github.com/jackc/pgx/v5/pgxpool"
	pgxpoolTypeName             = "Pool"
	pgExecutorName              = "pgExecutor"
	newPGExecutorName           = "newPGExecutor"
	execDirectName              = "ExecDirect"
	poolFieldName               = "pool"
	approvedMarkerImportPath    = "github.com/ghbvf/gocell/pkg/pgrepoapproved"
	approvedMarkerFuncName      = "ApprovedExecDirect"
	expectedPGAdapterPackageMin = 3 // discovery coverage floor; see ADR §3.
)

// pgAdapterPackagesCache holds the once-computed result of
// discoverPGAdapterPackages. Three parallel tests (TestPGRepoAmbientTx,
// TestPGRepoAmbientTx_DiscoveryCoverage, TestPGRepoAmbientTx_SelfCheck) each
// call discoverPGAdapterPackages; without caching each would execute a full
// RunTyped/packages.Load sweep. The cached slice is a deduped sorted read-only
// value — safe for concurrent readers once populated by sync.Once.
var (
	pgAdapterPackagesOnce  sync.Once
	pgAdapterPackagesCache []string
)

// pgrepoApprovedReasonFormat is the required format for the reason argument to
// pgrepoapproved.ApprovedExecDirect: kebab-case identifier (lowercase letters,
// digits, hyphens; starting with lowercase letter; length ≥ 2). Snake_case,
// PascalCase, single-char, and leading-hyphen strings all fail. Aligned with
// panicregister precedent (panic_invariants_test.go::panicRegisteredReasonFormat).
var pgrepoApprovedReasonFormat = regexp.MustCompile(`^[a-z][a-z0-9-]+$`)

// pgrepoApprovedReasonPlaceholder matches reason literals that are placeholder
// identifiers (todo / fixme / tbd / xxx / placeholder / wip) optionally followed
// by a hyphen and more text. Rejected because they provide no descriptive
// information about the bypass site. Aligned with panicregister precedent.
var pgrepoApprovedReasonPlaceholder = regexp.MustCompile(`^(todo|fixme|tbd|xxx|placeholder|wip)(-|$)`)

// inspectStopAtFuncLit walks body's AST invoking visit on every non-FuncLit
// node, but stops descending at *ast.FuncLit boundaries. R3 uses this to bound
// the "approval scope" to a single FuncDecl/FuncLit body — markers and
// ExecDirect calls must co-locate in the SAME scope, not the entire subtree.
// Without this scope bound, a marker in a nested closure could batch-approve
// outer-scope ExecDirect calls (and vice versa), defeating the audit-trail
// intent. See F1 in PR #917 round-2 review.
func inspectStopAtFuncLit(body ast.Node, visit func(ast.Node)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		visit(n)
		return true
	})
}

// discoverPGAdapterPackages auto-discovers all production packages that
// declare a package-local type named pgExecutor. The discovery signal replaces
// the former hand-maintained pgRepoPackagePatterns list: a package is in
// scope of PG-REPO-AMBIENT-TX-01 iff its package scope declares pgExecutor,
// which is itself the structural funnel the rule already depends on.
//
// Returned slice is the set of import paths (deduped, sorted) and is suitable
// for direct use with RunTyped. Packages whose scope does not contain
// pgExecutor (including configcore's Session/DBTX alternative funnel) are
// intentionally excluded — they need a separate archtest if/when they want
// equivalent governance.
//
// The result is computed once per test binary execution via sync.Once and
// cached for concurrent reuse. The returned slice is read-only; callers must
// not mutate it. Three parallel tests share this cache: TestPGRepoAmbientTx,
// TestPGRepoAmbientTx_DiscoveryCoverage, and TestPGRepoAmbientTx_SelfCheck.
//
// See package godoc §Discovery and ADR
// docs/architecture/202605241400-003-pg-repo-ambient-tx-discovery-hard.md.
func discoverPGAdapterPackages(t *testing.T) []string {
	t.Helper()
	pgAdapterPackagesOnce.Do(func() {
		root := findModuleRoot(t)
		var paths []string
		_ = RunTyped(t, TypedOpts{Tests: false}, prodscan.Patterns(root), func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Scope() == nil {
				return nil
			}
			obj := p.Pkg.Scope().Lookup(pgExecutorName)
			if obj == nil {
				return nil
			}
			// F3 (PR #917 round-2): require obj to be a *types.TypeName whose
			// type is a named struct. A var/const/func/alias named pgExecutor
			// does not declare the funnel and must not put the package in scope.
			tn, ok := obj.(*types.TypeName)
			if !ok {
				return nil
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				return nil
			}
			if _, ok := named.Underlying().(*types.Struct); !ok {
				return nil
			}
			paths = append(paths, p.Pkg.Path())
			return nil
		})
		sort.Strings(paths)
		pgAdapterPackagesCache = paths
	})
	return pgAdapterPackagesCache
}

// TestPGRepoAmbientTx guards PG-REPO-AMBIENT-TX-01 against the production
// packages. Discovery is type-aware (see discoverPGAdapterPackages): the set
// of in-scope packages is derived from "package scope declares pgExecutor",
// not from a hand-maintained list. RED fixtures are exercised separately by
// TestPGRepoAmbientTx_RedFixtureDetected. Blind-spot self-checks are in
// TestPGRepoAmbientTx_SelfCheck.
func TestPGRepoAmbientTx(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads PG repo packages with TypesInfo, ~2-3s)")
	}

	packages := discoverPGAdapterPackages(t)
	assert.GreaterOrEqual(t, len(packages), expectedPGAdapterPackageMin,
		"PG-REPO-AMBIENT-TX-01 discovery coverage floor: expected at least %d packages "+
			"declaring pgExecutor; got %d (%v). A refactor may have broken the discovery "+
			"signal (e.g., renamed pgExecutor or moved its declaration out of package scope).",
		expectedPGAdapterPackageMin, len(packages), packages)

	diags := RunTyped(t, TypedOpts{}, packages, pgRepoAmbientTxRule)
	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Rel != diags[j].Rel {
			return diags[i].Rel < diags[j].Rel
		}
		return diags[i].Line < diags[j].Line
	})

	for _, d := range diags {
		t.Errorf("PG-REPO-AMBIENT-TX-01 %s:%d: %s", d.Rel, d.Line, d.Message)
	}
}

// pgRepoAmbientTxRule is the Rule function for PG-REPO-AMBIENT-TX-01.
// Extracted so the same logic is exercised by both the production-package test
// and the RED-fixture test.
//
// File scope: in production packages (those whose package scope contains
// pgExecutor), only *_repo.go and *_store.go files are checked. Infrastructure
// files (pool.go, tx_manager.go, pg_executor.go, etc.) legitimately hold raw
// *pgxpool.Pool fields and are intentionally out of scope. For fixture packages
// (loaded under tools/archtest/internal/..., which prodscan.Patterns does not
// reach), the rule checks all files so RED fixtures named fixture.go are still
// detected — the production-vs-fixture distinction is structural: discovery
// only ever hands production packages here, so the suffix filter is always
// correct for them; fixture packages reach this function via RunTypedFixture
// in a separate test path where no discovery filter applies.
//
// The production/fixture branch uses the same heuristic as discovery: a
// production PG adapter package has its files under a directory whose go.mod
// module-relative path is NOT below tools/archtest/internal/. We detect this
// by looking at p.Rel(file) prefix: fixture rels start with "tools/archtest/"
// while production rels start with cells/, adapters/, examples/, etc.
func pgRepoAmbientTxRule(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var pkgPath string
	if p.Pkg != nil {
		pkgPath = p.Pkg.Path()
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		base := filepath.Base(rel)
		// Fixture packages reside under tools/archtest/internal/ and are loaded
		// via RunTypedFixture (separate test path), never through production
		// discovery. The tools/archtest/ prefix check is a Soft convention: fixture
		// packages MUST be placed under tools/archtest/internal/ as documented by
		// RunTypedFixture and the fixture naming convention; misplacing a fixture
		// package outside that prefix would cause R1/R2/R3 to apply the
		// _repo.go/_store.go suffix filter and miss the fixture's intentional
		// violations. Production packages always start with cells/, adapters/,
		// examples/, etc., so the prefix boundary is structurally stable but not
		// mechanically enforced beyond this string check.
		if !strings.HasPrefix(rel, "tools/archtest/") {
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
// pgExecutor.pool directly nor call pgExecutor.ExecDirect without a sibling
// pgrepoapproved.ApprovedExecDirect(literal) marker in the SAME approval
// scope (= same FuncDecl body OR same FuncLit body — nested closures are
// independent scopes).
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
// (b) ExecDirect call without same-scope marker: any CallExpr
// `<x>.ExecDirect(...)` where <x> resolves to pgExecutor is rejected unless
// the SAME approval scope contains a CallExpr resolving to
// pkg/pgrepoapproved.ApprovedExecDirect whose first argument is a kebab-case
// const string literal. See bodyHasApprovedExecDirectMarker godoc for the full
// form-uniqueness chain. Exempt: pgExecutor's own methods.
//
// Approval scope handling (F1 in PR #917 round-2 review): we visit each
// FuncDecl body once, then recursively visit every nested *ast.FuncLit body
// as its own independent scope. inspectStopAtFuncLit bounds each per-scope
// scan so a marker in scope X cannot approve ExecDirect calls in scope Y.
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
		// Scope 1: the FuncDecl body itself.
		diags = append(diags, scanR3PoolAccess(fset, fn.Body, rel, info, pkgPath)...)
		diags = append(diags, scanR3ExecDirect(fset, fn.Body, rel, info, pkgPath)...)
		// Scope 2..N: every nested FuncLit body inside this FuncDecl is its
		// own independent approval scope. Use EachInSubtree on the unmodified
		// body to find FuncLits; the per-scope helpers themselves stop at
		// FuncLit boundaries via inspectStopAtFuncLit, so they only see the
		// scope-local AST of whichever body they are called on.
		EachInSubtree[ast.FuncLit](fn.Body, func(fl *ast.FuncLit) {
			if fl.Body == nil {
				return
			}
			diags = append(diags, scanR3PoolAccess(fset, fl.Body, rel, info, pkgPath)...)
			diags = append(diags, scanR3ExecDirect(fset, fl.Body, rel, info, pkgPath)...)
		})
	})
	return diags
}

// scanR3PoolAccess flags SelectorExpr `<x>.pool` where <x> resolves to the
// package-local pgExecutor type. Scope-bounded: walk stops at nested FuncLit
// boundaries (each FuncLit is scanned separately by scanR3UsagePoints).
func scanR3PoolAccess(fset *token.FileSet, body *ast.BlockStmt, rel string, info *types.Info, pkgPath string) []Diagnostic {
	var diags []Diagnostic
	inspectStopAtFuncLit(body, func(n ast.Node) {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return
		}
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
				"use e.Exec/e.Query/e.QueryRow (or ExecDirect with sibling " +
				"pgrepoapproved.ApprovedExecDirect marker for ADR-approved compensation paths)",
		})
	})
	return diags
}

// scanR3ExecDirect flags CallExpr `<x>.ExecDirect(...)` where <x> resolves to
// pgExecutor, unless the same FuncDecl body contains a sibling
// pgrepoapproved.ApprovedExecDirect(literal) marker call. Marker form is
// verified via (callee, arg) form-uniqueness:
//
//  1. callee resolves via *types.Info.Uses to the *types.Func for
//     ApprovedExecDirect in pkg/pgrepoapproved (name + pkg path match);
//  2. first argument's static type-and-value resolves via *types.Info.Types
//     to a string constant (const literal) — fmt.Sprintf / concatenation /
//     variables yield non-constant TypeAndValue and are rejected.
//
// The marker is checked once per FuncDecl body; the same marker covers every
// ExecDirect call inside that body. Multiple markers in one body are not
// rejected (they are harmless documentation), but archtest BS-8 reverse self-
// check pins that only the sanctioned site holds a marker.
//
// Marker order relative to ExecDirect calls is NOT enforced by this check —
// co-location (same FuncDecl body) is the only structural requirement. By
// convention the marker is placed before the ExecDirect call for readability,
// but the archtest passes regardless of order.
func scanR3ExecDirect(
	fset *token.FileSet,
	body *ast.BlockStmt,
	rel string,
	info *types.Info,
	pkgPath string,
) []Diagnostic {
	if bodyHasApprovedExecDirectMarker(body, info) {
		return nil
	}
	var diags []Diagnostic
	inspectStopAtFuncLit(body, func(n ast.Node) {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
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
			Message: "R3: pgExecutor.ExecDirect call requires sibling " +
				"pgrepoapproved.ApprovedExecDirect(<kebab-case-literal>) marker in the same " +
				"approval scope (FuncDecl/FuncLit body, not nested closure) to document the " +
				"ADR-approved bypass of ambient tx; " +
				"add 'pgrepoapproved.ApprovedExecDirect(\"your-adr-reason\")' before the " +
				"ExecDirect call; see pkg/pgrepoapproved and ADR " +
				"docs/architecture/202605241400-003-pg-repo-ambient-tx-discovery-hard.md",
		})
	})
	return diags
}

// bodyHasApprovedExecDirectMarker reports whether body contains a CallExpr
// whose callee resolves to pkg/pgrepoapproved.ApprovedExecDirect AND whose
// first argument is a kebab-case const string literal. Form-uniqueness chain
// (each rule a separate REJECT branch):
//
//  1. callee resolves via *types.Info.Uses to *types.Func with name
//     "ApprovedExecDirect" AND Pkg().Path() == pkg/pgrepoapproved
//  2. arg[0] is a *ast.BasicLit with Kind == token.STRING (rejects const
//     identifiers, constant-folded "a"+"b" concatenation, variable references,
//     fmt.Sprintf, and anything else that would erase the source-level reason)
//  3. strconv.Unquote(arg[0]) matches pgrepoApprovedReasonFormat
//     (^[a-z][a-z0-9-]+$ — kebab-case identifier, length ≥ 2)
//  4. unquoted value is NOT a placeholder identifier (todo/fixme/tbd/xxx/
//     placeholder/wip per pgrepoApprovedReasonPlaceholder)
//
// Scope bound (F1 in PR #917 round-2 review): scan stops at *ast.FuncLit
// boundaries via inspectStopAtFuncLit. A marker in a nested closure does NOT
// approve outer-scope ExecDirect calls; conversely, an outer-scope marker does
// NOT approve ExecDirect calls inside nested closures. Each FuncDecl /
// FuncLit body is an independent approval scope (handled by scanR3UsagePoints
// recursing into FuncLits with this same per-scope check).
//
// Order is not enforced: marker may appear before, after, or between
// ExecDirect calls in the same scope — convention is before for readability.
// One marker satisfies the check for all ExecDirect calls in the same scope;
// multiple markers in one scope are allowed (harmless redundancy).
func bodyHasApprovedExecDirectMarker(body *ast.BlockStmt, info *types.Info) bool {
	found := false
	inspectStopAtFuncLit(body, func(n ast.Node) {
		if found {
			return
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		fn := resolveCalleeFunc(call.Fun, info)
		if fn == nil || fn.Name() != approvedMarkerFuncName {
			return
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != approvedMarkerImportPath {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		// F2 rule 2: arg[0] must be a *ast.BasicLit + token.STRING.
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		// F2 rule 3: kebab-case format.
		val, err := strconv.Unquote(lit.Value)
		if err != nil || !pgrepoApprovedReasonFormat.MatchString(val) {
			return
		}
		// F2 rule 4: not a placeholder identifier.
		if pgrepoApprovedReasonPlaceholder.MatchString(val) {
			return
		}
		found = true
	})
	return found
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
	// R1: badR1Repo — struct field pool *pgxpool.Pool type position at line 114
	{"R1:", 114},
	// R2: badR2NonNew — non-New* func with *pgxpool.Pool param, func name at line 119
	{"R2:", 119},
	// R2: NewBadR2NoWrap — New* func without newPGExecutor call, func name at line 125
	{"R2:", 125},
	// R3: badR3PoolDirect — r.db.pool direct access selector at line 143
	{"R3:", 143},
	// R3: badR3ExecDirect — r.db.ExecDirect call without sibling marker at line 149
	{"R3:", 149},
	// F1 RED (PR #917 round-2):
	// R3: badR3MarkerInNestedClosure — outer ExecDirect, marker in nested closure at line 174
	{"R3:", 174},
	// R3: badR3MarkerOuterExecInNestedClosure — inner ExecDirect, marker in outer scope at line 187
	{"R3:", 187},
	// F2 RED (PR #917 round-2):
	// R3: badR3ApprovedConstIdent — marker reason is const ident (not BasicLit), at line 198
	{"R3:", 198},
	// R3: badR3ApprovedConcat — marker reason is "a"+"b" BinaryExpr at line 206
	{"R3:", 206},
	// R3: badR3ApprovedEmpty — marker reason is "" (fails kebab regex) at line 214
	{"R3:", 214},
	// R3: badR3ApprovedPlaceholder — marker reason is "todo" (placeholder) at line 223
	{"R3:", 223},
}

// TestPGRepoAmbientTx_RedFixtureDetected asserts the rule catches all eleven
// RED violations in internal/pgrepoambienttxfixture:
//
//   - 1 R1: badR1Repo holds *pgxpool.Pool (not named pgExecutor)
//   - 1 R2: badR2NonNew is a non-New* function with *pgxpool.Pool param
//   - 1 R2: NewBadR2NoWrap is a New* function with *pgxpool.Pool param but no newPGExecutor call
//   - 1 R3: badR3PoolDirect accesses r.db.pool directly
//   - 1 R3: badR3ExecDirect calls r.db.ExecDirect without sibling pgrepoapproved.ApprovedExecDirect marker
//
// F1 scope-bounded marker checks (PR #917 round-2):
//
//   - 1 R3: badR3MarkerInNestedClosure — outer ExecDirect, marker in nested closure
//   - 1 R3: badR3MarkerOuterExecInNestedClosure — outer marker, inner ExecDirect in closure
//
// F2 reason form-uniqueness checks (PR #917 round-2):
//
//   - 1 R3: badR3ApprovedConstIdent — marker reason is *ast.Ident, not BasicLit
//   - 1 R3: badR3ApprovedConcat — marker reason is "a"+"b" *ast.BinaryExpr
//   - 1 R3: badR3ApprovedEmpty — marker reason "" fails kebab regex (len ≥ 2)
//   - 1 R3: badR3ApprovedPlaceholder — marker reason "todo" matches placeholder regex
//
// GREEN cases (pgExecutor field, goodNewFoo→newPGExecutor, goodExecMethod using
// r.db.Exec, goodApprovedSingleExecDirect with marker+1 ExecDirect,
// goodApprovedMultiExecDirect with 1 marker+2 ExecDirect calls) must produce
// zero diagnostics.
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

	packages := discoverPGAdapterPackages(t)

	// BS-1 reverse check: no embedded/anonymous struct field in any production
	// package should transitively expose a *pgxpool.Pool outside pgExecutor.
	// R1 only scans direct fields; this check verifies the accepted limitation
	// does not silently hide a real violation in existing production code.
	var bs1Violations []string
	_ = RunTyped(t, TypedOpts{}, packages, func(p *Pass) []Diagnostic {
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
	_ = RunTyped(t, TypedOpts{}, packages, func(p *Pass) []Diagnostic {
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
	// the discovered PG adapter set since cells/accesscore itself does not
	// declare pgExecutor) may carry *pgxpool.Pool as a field. NewPGBundle
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
			"legitimately needs to hold the pool, declare a package-local pgExecutor "+
			"in that package so discovery auto-includes it.")

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
	_ = RunTyped(t, TypedOpts{}, packages, func(p *Pass) []Diagnostic {
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
	_ = RunTyped(t, TypedOpts{}, packages, func(p *Pass) []Diagnostic {
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

	// BS-8 reverse check: pgrepoapproved.ApprovedExecDirect markers must only
	// appear in FuncDecl bodies that themselves contain a sibling
	// pgExecutor.ExecDirect call. A spurious marker in any other body is a
	// review violation — it documents an ADR-approved bypass that never
	// happens, which decays the audit trail.
	var bs8Violations []string
	_ = RunTyped(t, TypedOpts{}, packages, func(p *Pass) []Diagnostic {
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
			// Visit every approval scope (FuncDecl body + every nested FuncLit
			// body); for each scope holding a marker, require a same-scope
			// pgExecutor.ExecDirect call. This mirrors the F1 scope-bound
			// fix in scanR3UsagePoints — marker and ExecDirect must co-locate
			// in the SAME approval scope, so spurious-marker detection must
			// also be scope-bounded.
			visitScope := func(body *ast.BlockStmt, label string, pos token.Pos) {
				if body == nil {
					return
				}
				if !bodyHasApprovedExecDirectMarker(body, p.TypesInfo) {
					return
				}
				if !bodyCallsPGExecutorExecDirect(body, p.TypesInfo, pkgPath) {
					bs8Violations = append(bs8Violations, fmt.Sprintf(
						"%s:%d: %s has pgrepoapproved.ApprovedExecDirect marker but "+
							"no pgExecutor.ExecDirect call in same approval scope — marker is "+
							"a spurious audit-trail entry; remove it or add the corresponding "+
							"ExecDirect call",
						rel, p.Fset.Position(pos).Line, label,
					))
				}
			}
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				visitScope(fn.Body, "func "+fn.Name.Name, fn.Pos())
				EachInSubtree[ast.FuncLit](fn.Body, func(fl *ast.FuncLit) {
					visitScope(fl.Body, "nested closure in "+fn.Name.Name, fl.Pos())
				})
			})
		}
		return nil
	})
	assert.Empty(t, bs8Violations,
		"BS-8 self-check: pgrepoapproved.ApprovedExecDirect marker must only co-locate with "+
			"an actual pgExecutor.ExecDirect call in the SAME approval scope (FuncDecl/FuncLit body)")
}

// bodyCallsPGExecutorExecDirect reports whether body contains a CallExpr
// `<x>.ExecDirect(...)` whose receiver `<x>` resolves via *types.Info.Types to
// the package-local pgExecutor named type. Scope-bounded: walk stops at nested
// FuncLit boundaries so the marker/ExecDirect co-location check mirrors the
// per-scope approval semantics of scanR3ExecDirect.
func bodyCallsPGExecutorExecDirect(body *ast.BlockStmt, info *types.Info, pkgPath string) bool {
	found := false
	inspectStopAtFuncLit(body, func(n ast.Node) {
		if found {
			return
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != execDirectName {
			return
		}
		if isPgExecutorType(sel.X, info, pkgPath) {
			found = true
		}
	})
	return found
}

// TestPGRepoAmbientTx_DiscoveryCoverage asserts the auto-discovery returns the
// expected set of PG adapter packages. Failure mode is a clear diff against
// the named set, replacing the former silent zero-coverage failure mode where
// a hand-maintained list could drop a package without any visible signal.
//
// The expected set is intentionally exhaustive (every currently-known PG
// adapter package) so that:
//   - removing pgExecutor from any existing package fails this test loudly
//   - adding a new PG adapter package requires updating this expected set
//     (the update is the same act as declaring the package in-scope, but in a
//     visible diff rather than a silent omission)
//
// This is a Hard 范本 "single sanctioned holder" applied to discovery: the
// discovery signal IS the funnel symbol (pgExecutor type declaration), so any
// package that the rule must scan and any package whose scope contains
// pgExecutor are the same set by construction.
func TestPGRepoAmbientTx_DiscoveryCoverage(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based discovery coverage in -short mode")
	}

	expected := []string{
		"github.com/ghbvf/gocell/adapters/postgres",
		"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres",
		"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/adapters/postgres",
	}
	assert.Equal(t, expectedPGAdapterPackageMin, len(expected),
		"expectedPGAdapterPackageMin floor constant must equal expected set size; "+
			"update both if a package is added or removed")
	got := discoverPGAdapterPackages(t)

	expectedSet := make(map[string]struct{}, len(expected))
	for _, p := range expected {
		expectedSet[p] = struct{}{}
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, p := range got {
		gotSet[p] = struct{}{}
	}
	// F4 (PR #917 round-2): collect both sides into sorted slices before
	// reporting so CI output is deterministic across runs (Go map iteration
	// is randomized).
	var missing, extra []string
	for p := range expectedSet {
		if _, ok := gotSet[p]; !ok {
			missing = append(missing, p)
		}
	}
	for p := range gotSet {
		if _, ok := expectedSet[p]; !ok {
			extra = append(extra, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, p := range missing {
		t.Errorf("PG-REPO-AMBIENT-TX-01 DiscoveryCoverage: expected package %q "+
			"not discovered — was pgExecutor renamed/moved out of package scope?", p)
	}
	for _, p := range extra {
		t.Errorf("PG-REPO-AMBIENT-TX-01 DiscoveryCoverage: discovered new package %q "+
			"not in expected set — if intentional, add it to expected; "+
			"if accidental, this package declares pgExecutor unexpectedly; "+
			"update the 'expected []string' slice in TestPGRepoAmbientTx_DiscoveryCoverage "+
			"and bump expectedPGAdapterPackageMin to match", p)
	}
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
