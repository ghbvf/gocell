// invariants:
//   - INVARIANT: PG-REPO-AMBIENT-TX-01
//
// # Package archtest — PG-REPO-AMBIENT-TX-01 (Hard upstream + Hard downstream)
//
// PG-REPO-AMBIENT-TX-01 enforces the ambient-tx routing contract for
// PostgreSQL-backed repositories. The funnel is sealed at the Go visibility
// level: the sanctioned holder type pgExecutor lives in a per-adapter
// /internal/pgexec/ sub-package and is unexported there; parent packages can
// only obtain a value via pgexec.New(pool) and can only invoke methods through
// the exported pgexec.PGExecutor interface, which does not expose the raw
// *pgxpool.Pool field.
//
//   - R1 (no raw pool in repo/store layer): every *_repo.go / *_store.go file
//     in any package — repos must not declare a *pgxpool.Pool field. Pool
//     access is funneled through the exported pgexec.PGExecutor interface
//     (held as a field). Infrastructure files (pool.go, tx_manager.go) and
//     the sub-package's own pgexec.go legitimately hold the raw pool and are
//     out of scope by file-extension filter.
//
//   - R2 (cross-package wrap funnel): every *_repo.go / *_store.go file's
//     function parameter of type *pgxpool.Pool must satisfy BOTH (a) function
//     name starts with "New" AND (b) function body contains a CallExpr whose
//     Fun resolves via *types.Info.Uses to a *types.Func with Pkg().Path()
//     ending /internal/pgexec AND Name() == "New", with the pool param as
//     first argument. Exempt: any FuncDecl living inside a package whose
//     import path ends /internal/pgexec (the sub-package's own New factory
//     constructs &pgExecutor{pool: param} directly).
//
//   - R3 (ExecDirect typed marker funnel): every *_repo.go / *_store.go
//     method calling `<x>.ExecDirect(...)` where <x> resolves to a named type
//     pgexec.PGExecutor declared in a package whose import path ends
//     /internal/pgexec MUST have a sibling pgrepoapproved.ApprovedExecDirect
//     marker call in the SAME approval scope (FuncDecl body OR enclosing
//     FuncLit body — nested closures are independent scopes). The marker
//     is a typed funnel from pkg/pgrepoapproved; 5-门 form-uniqueness (callee
//     resolves to the funnel, arg[0] is *ast.BasicLit + token.STRING +
//     kebab-case regex + non-placeholder). Currently exactly one production
//     callsite holds a marker:
//     adapters/postgres/refresh_store.go::revokeSessionDetachedAt (the
//     intentional independent-commit cascade-revoke compensation path).
//
// # AI-robust grading (Funnel 双向锁评级)
//
// 下游 Hard / 上游 Hard.
//
// 上游 Hard via Go type-system sealing (closes gh #738 / #916, retired the
// upstream Medium ceiling documented in ADR 202605241400-003 prior amendment):
//
//   - pgExecutor struct is unexported in a per-adapter internal/pgexec/
//     sub-package. Parent-package files CANNOT reference the concrete type
//     name to declare a field, accept a parameter, or perform a type assertion
//     — the symbol is package-private to /internal/pgexec/. This is the same
//     "sealed construction" Hard pattern as HEALTH-REDACTED-ERROR-MSG-FUNNEL-01
//     (SlogDependencyEntry unexported fields) per ai-robust.md §Hard 范本 #6.
//
//   - The pool field is unreachable from the PGExecutor interface (no method
//     exposes it). Parent-package files holding pgexec.PGExecutor as a field
//     can only invoke interface methods. Direct access to .pool from the
//     parent package is a compile error.
//
//   - R1 / R2 archtest defense-in-depth: R1 catches any new repo/store file
//     that accidentally re-introduces *pgxpool.Pool as a field; R2 catches
//     any New-prefixed constructor that bypasses pgexec.New. Both rules are
//     global predicates over the production module (no discovery, no
//     hand-maintained allowlist) using *types.Info resolution.
//
// 下游 Hard via form-uniqueness (archtest-bound):
//
//   - R3 ExecDirect marker funnel: typed function call
//     pkg/pgrepoapproved.ApprovedExecDirect with arg-form uniqueness
//     (BasicLit + token.STRING + kebab-case regex + non-placeholder). Sibling
//     deployment of panic(panicregister.Approved(...)) per ai-robust.md
//     §Hard 范本 #2 "typed marker funnel for unbounded ops".
//
// # RED fixtures
//
// The authoritative expected-violation set is defined as
// expectedFixtureViolations and asserted by
// TestPGRepoAmbientTx_RedFixtureDetected. The fixture package at
// tools/archtest/internal/pgrepoambienttxfixture/ exercises:
//
//   - R1 violations in fixture_repo.go (the _repo.go file extension triggers
//     R1 scope; fixture.go is exempt by filename)
//   - R2 violations in fixture_repo.go (file-extension filter applies to all
//     three rules including R2)
//   - R3(b) violations in fixture_repo.go (parent-pkg holds the fixture's own
//     /internal/pgexec/PGExecutor interface; methods call ExecDirect through it)
//
// # Blind spots
//
// BS-1 Field embedding: an embedded struct that transitively carries
// *pgxpool.Pool — TestPGRepoAmbientTx_SelfCheck walks production
// repo/store struct types via *types.Info and asserts no anonymous/embedded
// field transitively exposes a *pgxpool.Pool.
//
// BS-2 Interface-typed field that carries a *pgxpool.Pool at runtime — R1 is
// a static-type check; it cannot see runtime dynamic values. Accepted per
// ai-robust.md §3.
//
// BS-3 Function-value indirection of pgexec.New: `var fn = pgexec.New; fn(pool)`
// — R2's callee resolution uses *types.Info.Uses, which only resolves direct
// calls. Identity-based reverse check via EachInSubtree[ast.Ident] +
// *types.Info.Uses asserts no function-value reference to pgexec.New exists in
// any production package.
//
// BS-4 *pgxpool.Pool type alias: `type myPool = *pgxpool.Pool` — Go type
// aliases resolve to the same underlying *types.Named so isPgxPoolType still
// matches. Documented for completeness.
//
// BS-9 Interface type alias re-export: `type MyExec = pgexec.PGExecutor` in
// the parent package is permitted (same method set, same identity to
// archtest's type resolver). The underlying *pgxpool.Pool is still
// unreachable because the interface does not expose it; the alias adds no
// bypass surface. Documented for completeness; no archtest cost.
//
// BS-5 cells/accesscore PGBundle helper: the cells/accesscore root package
// owns NewPGBundle which accepts *pgxpool.Pool as a constructor parameter
// but the PGBundle struct does NOT retain it — only derived primitives
// (userRepo / roleRepo / setupLock / txRunner). The reverse check asserts no
// struct in cells/accesscore (outside the internal/ tree) carries
// *pgxpool.Pool as a field — function-signature usage in NewPGBundle is
// permitted because the body does not persist the pool.
//
// BS-6 ExecDirect method-value indirection: `var fn = s.db.ExecDirect; fn(...)`
// — R3's detection requires a direct CallExpr. Identity-based reverse check
// asserts no method-value use of ExecDirect on pgexec.PGExecutor exists in
// any production file.
//
// BS-8 Spurious marker: pgrepoapproved.ApprovedExecDirect markers placed in
// FuncDecl/FuncLit bodies that do NOT actually call ExecDirect — review
// hazard because it documents an ADR-approved bypass that never happens. The
// reverse check scans every approval scope holding a marker and requires a
// same-scope ExecDirect call.
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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const (
	pgxpoolImportPath        = "github.com/jackc/pgx/v5/pgxpool"
	pgxpoolTypeName          = "Pool"
	pgexecPkgSuffix          = "/internal/pgexec"
	pgexecFactoryName        = "New"
	pgExecutorInterfaceName  = "PGExecutor"
	execDirectName           = "ExecDirect"
	approvedMarkerImportPath = "github.com/ghbvf/gocell/pkg/pgrepoapproved"
	approvedMarkerFuncName   = "ApprovedExecDirect"
)

// pgrepoApprovedReasonFormat is the required format for the reason argument to
// pgrepoapproved.ApprovedExecDirect: kebab-case identifier (lowercase letters,
// digits, hyphens; starting with lowercase letter; length ≥ 2). Aligned with
// panicregister precedent (panic_invariants_test.go::panicRegisteredReasonFormat).
var pgrepoApprovedReasonFormat = regexp.MustCompile(`^[a-z][a-z0-9-]+$`)

// pgrepoApprovedReasonPlaceholder matches reason literals that are placeholder
// identifiers (todo / fixme / tbd / xxx / placeholder / wip) optionally followed
// by a hyphen and more text. Rejected because they provide no descriptive
// information about the bypass site.
var pgrepoApprovedReasonPlaceholder = regexp.MustCompile(`^(todo|fixme|tbd|xxx|placeholder|wip)(-|$)`)

// stopAtFuncLit halts descent at *ast.FuncLit boundaries. R3 uses this to
// bound the "approval scope" to a single FuncDecl/FuncLit body — markers and
// ExecDirect calls must co-locate in the SAME scope, not the entire subtree.
func stopAtFuncLit(n ast.Node) bool {
	_, ok := n.(*ast.FuncLit)
	return ok
}

// isRepoOrStoreFile reports whether rel's basename ends with _repo.go or
// _store.go. This is the file-extension scope filter for R1 / R3, separating
// the repo layer (where pool access must be funneled) from the infrastructure
// layer (pool.go / tx_manager.go / internal/pgexec/pgexec.go — these
// legitimately hold the raw pool and are out of scope).
func isRepoOrStoreFile(rel string) bool {
	base := filepath.Base(rel)
	return strings.HasSuffix(base, "_repo.go") || strings.HasSuffix(base, "_store.go")
}

// isPgexecSubpackage reports whether pkgPath ends with /internal/pgexec. The
// sub-package is the sealed holder location: its own New factory constructs
// &pgExecutor{pool: param} directly and is exempt from R2's "must call
// pgexec.New" requirement.
func isPgexecSubpackage(pkgPath string) bool {
	return strings.HasSuffix(pkgPath, pgexecPkgSuffix)
}

// TestPGRepoAmbientTx guards PG-REPO-AMBIENT-TX-01 against the production
// module. Discovery is removed — rules are global predicates over all
// production packages (filtered by file extension where applicable). RED
// fixtures are exercised separately by TestPGRepoAmbientTx_RedFixtureDetected.
// Blind-spot self-checks are in TestPGRepoAmbientTx_SelfCheck.
func TestPGRepoAmbientTx(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production module with TypesInfo, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.Patterns(root)
	diags := RunTyped(t, TypedOpts{}, patterns, pgRepoAmbientTxRule)
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
//
// File scope: all three rules apply ONLY to *_repo.go and *_store.go files.
// Infrastructure files (pool.go, tx_manager.go), the sub-package's own
// internal/pgexec/pgexec.go, composition-root helpers (cmd/corebundle/*.go),
// and pass-through composition helpers (bundle.go, options.go) legitimately
// pass *pgxpool.Pool around without wrapping — pool wrapping is the
// responsibility of the leaf constructor (New<Repo>Repo / NewSessionStore /
// etc.) which lives in *_repo.go / *_store.go. The file-extension filter is
// the structurally correct layer boundary: it carves the repo layer (where
// pool MUST be wrapped) from the infra/composition layer (which legitimately
// hands the raw pool to repo constructors).
func pgRepoAmbientTxRule(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var pkgPath string
	if p.Pkg != nil {
		pkgPath = p.Pkg.Path()
	}
	// The sub-package itself owns the New factory and constructs the impl
	// directly. Exempt all FuncDecls in /internal/pgexec/ from R2.
	if isPgexecSubpackage(pkgPath) {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if !isRepoOrStoreFile(rel) {
			continue
		}
		diags = append(diags, scanR1PoolFields(p.Fset, file, rel, p.TypesInfo)...)
		diags = append(diags, scanR2PoolParams(p.Fset, file, rel, p.TypesInfo)...)
		diags = append(diags, scanR3ExecDirect(p.Fset, file, rel, p.TypesInfo)...)
	}
	return diags
}

// scanR1PoolFields implements R1: in *_repo.go / *_store.go files, no struct
// may declare a field of type *pgxpool.Pool. Pool access must be funneled
// through pgexec.PGExecutor (held as an interface field).
func scanR1PoolFields(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
		EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return
			}
			structName := ts.Name.Name
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
						"R1: struct %s field %s holds *pgxpool.Pool in a repo/store file; "+
							"repos must hold pgexec.PGExecutor (interface) and call pgexec.New(pool) "+
							"in the constructor — the raw pool stays sealed inside the "+
							"adapter's internal/pgexec/ sub-package",
						structName, fieldName,
					),
				})
			}
		})
	})
	return diags
}

// scanR2PoolParams implements R2: in *_repo.go / *_store.go files, any
// function parameter of type *pgxpool.Pool must be in a New*-prefixed function
// whose body calls pgexec.New(param) where pgexec is the adapter's
// /internal/pgexec/ sub-package. Pass-through helpers and composition-root
// wiring live in non-repo/store files and are out of scope.
func scanR2PoolParams(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Type == nil || fn.Type.Params == nil {
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
							"only New*-prefixed constructors may accept *pgxpool.Pool (and must wrap via pgexec.New)",
						fn.Name.Name, paramName,
					),
				})
			}
			return
		}
		// R2b: New*-prefixed function must call pgexec.New(poolParam) in its body.
		if fn.Body == nil {
			return
		}
		for _, paramName := range poolParams {
			if !bodyCallsPgexecNewWith(fn.Body, paramName, info) {
				line := fset.Position(fn.Name.Pos()).Line
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf(
						"R2: New* constructor %s has *pgxpool.Pool param %q but does not call "+
							"pgexec.New(%s); pool must be wrapped via the adapter's "+
							"internal/pgexec/.New factory",
						fn.Name.Name, paramName, paramName,
					),
				})
			}
		}
	})
	return diags
}

// scanR3ExecDirect implements R3: in *_repo.go / *_store.go files, any
// CallExpr `<x>.ExecDirect(...)` where <x>'s static type resolves to the named
// interface pgexec.PGExecutor (declared in a package whose import path ends
// /internal/pgexec) must be co-located in the SAME approval scope with a
// sibling pgrepoapproved.ApprovedExecDirect(literal) marker call.
//
// Approval scope handling: each FuncDecl body is visited once, then every
// nested *ast.FuncLit body as its own independent scope. inspectStopAtFuncLit
// bounds each per-scope scan so a marker in scope X cannot approve ExecDirect
// calls in scope Y.
func scanR3ExecDirect(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		diags = append(diags, scanR3ExecDirectInScope(fset, fn.Body, rel, info)...)
		EachInSubtree[ast.FuncLit](fn.Body, func(fl *ast.FuncLit) {
			if fl.Body == nil {
				return
			}
			diags = append(diags, scanR3ExecDirectInScope(fset, fl.Body, rel, info)...)
		})
	})
	return diags
}

// scanR3ExecDirectInScope flags CallExpr `<x>.ExecDirect(...)` in body where
// <x> resolves to the sealed pgexec.PGExecutor interface, unless the SAME
// approval scope contains a sibling ApprovedExecDirect marker call.
func scanR3ExecDirectInScope(fset *token.FileSet, body *ast.BlockStmt, rel string, info *types.Info) []Diagnostic {
	if bodyHasApprovedExecDirectMarker(body, info) {
		return nil
	}
	var diags []Diagnostic
	EachInSubtreeStopAt[ast.CallExpr](body, stopAtFuncLit, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != execDirectName {
			return
		}
		if !isPGExecutorInterfaceType(sel.X, info) {
			return
		}
		line := fset.Position(call.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: "R3: pgexec.PGExecutor.ExecDirect call requires sibling " +
				"pgrepoapproved.ApprovedExecDirect(<kebab-case-literal>) marker in the same " +
				"approval scope (FuncDecl/FuncLit body, not nested closure) to document the " +
				"ADR-approved bypass of ambient tx; " +
				"add 'pgrepoapproved.ApprovedExecDirect(\"your-adr-reason\")' before the " +
				"ExecDirect call; see pkg/pgrepoapproved; " +
				"example: pgrepoapproved.ApprovedExecDirect(\"revoke-session-cascade\") in same func body before the ExecDirect call; only one production callsite exists at adapters/postgres/refresh_store.go::revokeSessionDetachedAt. " +
				"ADR docs/architecture/202605241400-003-pg-repo-ambient-tx-discovery-hard.md",
		})
	})
	return diags
}

// bodyHasApprovedExecDirectMarker reports whether body contains a CallExpr
// whose callee resolves to pkg/pgrepoapproved.ApprovedExecDirect AND whose
// first argument is a kebab-case const string literal. Scope bound via
// stopAtFuncLit: a marker in a nested closure does NOT approve outer-scope
// ExecDirect calls; conversely, an outer-scope marker does NOT approve
// ExecDirect calls inside nested closures.
//
// Form-uniqueness chain (each rule a separate REJECT branch):
//  1. callee resolves via *types.Info.Uses to *types.Func with name
//     "ApprovedExecDirect" AND Pkg().Path() == pkg/pgrepoapproved
//  2. arg[0] is a *ast.BasicLit with Kind == token.STRING
//  3. strconv.Unquote(arg[0]) matches pgrepoApprovedReasonFormat
//  4. unquoted value is NOT a placeholder identifier per pgrepoApprovedReasonPlaceholder
func bodyHasApprovedExecDirectMarker(body *ast.BlockStmt, info *types.Info) bool {
	found := false
	EachInSubtreeStopAt[ast.CallExpr](body, stopAtFuncLit, func(call *ast.CallExpr) {
		if found {
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
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil || !pgrepoApprovedReasonFormat.MatchString(val) {
			return
		}
		if pgrepoApprovedReasonPlaceholder.MatchString(val) {
			return
		}
		found = true
	})
	return found
}

// isPGExecutorInterfaceType reports whether expr's static type resolves to a
// named interface called "PGExecutor" declared in a package whose import path
// ends /internal/pgexec. This is the cross-package receiver-identity check
// for R3's ExecDirect marker requirement after the sealed-sub-package upgrade.
func isPGExecutorInterfaceType(expr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	t := tv.Type
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	if obj.Name() != pgExecutorInterfaceName {
		return false
	}
	return strings.HasSuffix(obj.Pkg().Path(), pgexecPkgSuffix)
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

// bodyCallsPgexecNewWith reports whether body contains a call to pgexec.New
// whose first argument is the identifier paramName. The callee is verified via
// *types.Info.Uses resolving to a *types.Func with Pkg().Path() ending
// /internal/pgexec AND Name() == "New".
func bodyCallsPgexecNewWith(body *ast.BlockStmt, paramName string, info *types.Info) bool {
	found := false
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		if found {
			return
		}
		fn := resolveCalleeFunc(call.Fun, info)
		if fn == nil || fn.Name() != pgexecFactoryName {
			return
		}
		if fn.Pkg() == nil || !strings.HasSuffix(fn.Pkg().Path(), pgexecPkgSuffix) {
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

// isPgxPoolType reports whether expr's static type is *pgxpool.Pool.
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
// RED fixture diagnostic.
type fixtureViolation struct {
	rulePrefix string
	line       int
}

// expectedFixtureViolations is the authoritative expected set for
// TestPGRepoAmbientTx_RedFixtureDetected. Lines are pinned to the fixture
// sources. When the fixture changes intentionally, update both the fixture
// and this set together.
//
// File layout:
//   - fixture.go: package godoc only (no rules apply — not _repo.go)
//   - fixture_repo.go: ALL RED cases (R1 + R2 + R3) + GREEN repo controls
//   - internal/pgexec/pgexec.go: sealed sub-package mirroring production form
var expectedFixtureViolations = []fixtureViolation{
	// R1 RED (fixture_repo.go) — pointer to field type pos.
	{"R1:", 20}, // badR1Repo.pool
	// R2 RED (fixture_repo.go) — pointer to FuncDecl name pos.
	{"R2:", 31}, // badR2NonNew
	{"R2:", 37}, // NewBadR2NoWrap
	// R3 RED (fixture_repo.go) — pointer to call pos.
	{"R3:", 51}, // badR3ExecDirect
	{"R3:", 58}, // badR3MarkerInNestedClosure (outer call, marker in nested closure)
	{"R3:", 69}, // badR3MarkerOuterExecInNestedClosure (inner call, marker in outer)
	{"R3:", 78}, // badR3ApprovedConstIdent (marker reason is *ast.Ident)
	{"R3:", 84}, // badR3ApprovedConcat (marker reason is BinaryExpr)
	{"R3:", 90}, // badR3ApprovedEmpty (marker reason "" fails kebab regex)
	{"R3:", 96}, // badR3ApprovedPlaceholder (marker reason "todo")
}

// TestPGRepoAmbientTx_RedFixtureDetected asserts the rule catches every RED
// violation in tools/archtest/internal/pgrepoambienttxfixture. The assertion
// is an exact-set match on (ruleID_prefix, line) pairs.
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

	expectedSet := make(map[diagKey]struct{}, len(expectedFixtureViolations))
	for _, v := range expectedFixtureViolations {
		expectedSet[diagKey(v)] = struct{}{}
	}

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
			"want %d; GREEN cases must produce 0; "+
			"see expectedFixtureViolations for the authoritative list",
			len(actualSet), len(expectedSet))
	}
}

// TestPGRepoAmbientTx_SelfCheck verifies the blind-spot list by asserting
// prohibited AST forms are absent from production packages.
func TestPGRepoAmbientTx_SelfCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping SelfCheck in -short mode")
	}

	root := findModuleRoot(t)
	patterns := prodscan.Patterns(root)

	// BS-1: no embedded/anonymous struct field in any production _repo.go /
	// _store.go file should transitively expose *pgxpool.Pool.
	var bs1Violations []string
	_ = RunTyped(t, TypedOpts{}, patterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			if !isRepoOrStoreFile(rel) {
				continue
			}
			EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
				EachInChildren[ast.TypeSpec](gd, func(ts *ast.TypeSpec) {
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						return
					}
					for _, field := range st.Fields.List {
						if len(field.Names) != 0 {
							continue
						}
						if embeddedStructHasPoolField(field.Type, p.TypesInfo) {
							bs1Violations = append(bs1Violations, fmt.Sprintf(
								"%s:%d: struct %s has embedded field that transitively "+
									"holds *pgxpool.Pool (BS-1 blind spot — extend R1 if needed)",
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
		"BS-1 self-check: no production repo/store struct may use an embedded field "+
			"that carries *pgxpool.Pool")

	// BS-3: pgexec.New must not appear as a function value (i.e., used as a
	// value rather than directly called) in any production package.
	var bs3Violations []string
	_ = RunTyped(t, TypedOpts{}, patterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			bs3Violations = append(bs3Violations,
				findPgexecNewValueUses(p.Fset, file, rel, p.TypesInfo)...)
		}
		return nil
	})
	assert.Empty(t, bs3Violations,
		"BS-3 self-check: pgexec.New must not be used as a function value in production")

	// BS-5: no struct in cells/accesscore (outside internal/) may carry
	// *pgxpool.Pool as a field. NewPGBundle accepts pool as a parameter but
	// does not persist it.
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
	assert.Equal(t, 0, bs5PoolFieldCount,
		"BS-5 self-check: no struct in cells/accesscore (outside internal/) may carry "+
			"*pgxpool.Pool — PGBundle must hold only derived primitives "+
			"(userRepo/roleRepo/setupLock/txRunner)")

	// BS-6: ExecDirect must not appear as a method value (used as a value
	// rather than directly called) in any production _repo.go / _store.go.
	var bs6Violations []string
	_ = RunTyped(t, TypedOpts{}, patterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			if !isRepoOrStoreFile(rel) {
				continue
			}
			bs6Violations = append(bs6Violations,
				findExecDirectValueUses(p.Fset, file, rel, p.TypesInfo)...)
		}
		return nil
	})
	assert.Empty(t, bs6Violations,
		"BS-6 self-check: pgexec.PGExecutor.ExecDirect must not be used as a method value")

	// BS-8: pgrepoapproved.ApprovedExecDirect markers must only appear in
	// approval scopes that also contain a sibling pgexec ExecDirect call.
	var bs8Violations []string
	_ = RunTyped(t, TypedOpts{}, patterns, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !p.IsFileInScope(file) {
				continue
			}
			rel := p.Rel(file)
			if !isRepoOrStoreFile(rel) {
				continue
			}
			visitScope := func(body *ast.BlockStmt, label string, pos token.Pos) {
				if body == nil {
					return
				}
				if !bodyHasApprovedExecDirectMarker(body, p.TypesInfo) {
					return
				}
				if !bodyCallsPGExecutorExecDirect(body, p.TypesInfo) {
					bs8Violations = append(bs8Violations, fmt.Sprintf(
						"%s:%d: %s has pgrepoapproved.ApprovedExecDirect marker but "+
							"no pgexec.PGExecutor.ExecDirect call in same approval scope — "+
							"marker is a spurious audit-trail entry; remove it or add the "+
							"corresponding ExecDirect call",
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
		"BS-8 self-check: pgrepoapproved.ApprovedExecDirect marker must only co-locate "+
			"with an actual pgexec.PGExecutor.ExecDirect call in the SAME approval scope")
}

// bodyCallsPGExecutorExecDirect reports whether body contains a CallExpr
// `<x>.ExecDirect(...)` whose receiver `<x>` resolves to the sealed
// pgexec.PGExecutor interface.
func bodyCallsPGExecutorExecDirect(body *ast.BlockStmt, info *types.Info) bool {
	found := false
	EachInSubtreeStopAt[ast.CallExpr](body, stopAtFuncLit, func(call *ast.CallExpr) {
		if found {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != execDirectName {
			return
		}
		if isPGExecutorInterfaceType(sel.X, info) {
			found = true
		}
	})
	return found
}

// findPgexecNewValueUses returns BS-3 violations: any Ident in the file that
// resolves to a *types.Func whose Name()=="New" and Pkg().Path() ends
// /internal/pgexec AND is NOT in the callee position of a direct call.
func findPgexecNewValueUses(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []string {
	calleePos := make(map[token.Pos]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		fn := resolveCalleeFunc(call.Fun, info)
		if fn == nil || fn.Name() != pgexecFactoryName {
			return
		}
		if fn.Pkg() == nil || !strings.HasSuffix(fn.Pkg().Path(), pgexecPkgSuffix) {
			return
		}
		switch e := call.Fun.(type) {
		case *ast.Ident:
			calleePos[e.Pos()] = struct{}{}
		case *ast.SelectorExpr:
			calleePos[e.Sel.Pos()] = struct{}{}
		}
	})
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
		if fn.Name() != pgexecFactoryName {
			return
		}
		if fn.Pkg() == nil || !strings.HasSuffix(fn.Pkg().Path(), pgexecPkgSuffix) {
			return
		}
		if _, isCallee := calleePos[id.Pos()]; isCallee {
			return
		}
		violations = append(violations,
			fmt.Sprintf("%s:%d: pgexec.New used as function value (not a direct call) "+
				"(BS-3 blind spot — R2 would not detect indirect calls)",
				rel, fset.Position(id.Pos()).Line))
	})
	return violations
}

// findExecDirectValueUses returns BS-6 violations: any Ident resolving to the
// ExecDirect method on a pgexec.PGExecutor receiver that is NOT in the callee
// position of a direct call.
func findExecDirectValueUses(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []string {
	calleePos := make(map[token.Pos]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != execDirectName {
			return
		}
		if !isPGExecutorInterfaceType(sel.X, info) {
			return
		}
		calleePos[sel.Sel.Pos()] = struct{}{}
	})
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
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Recv() == nil {
			return
		}
		recv := sig.Recv().Type()
		if ptr, ok := recv.(*types.Pointer); ok {
			recv = ptr.Elem()
		}
		named, ok := recv.(*types.Named)
		if !ok || named.Obj() == nil {
			return
		}
		if named.Obj().Name() != pgExecutorInterfaceName {
			return
		}
		if named.Obj().Pkg() == nil ||
			!strings.HasSuffix(named.Obj().Pkg().Path(), pgexecPkgSuffix) {
			return
		}
		if _, isCallee := calleePos[id.Pos()]; isCallee {
			return
		}
		violations = append(violations,
			fmt.Sprintf("%s:%d: pgexec.PGExecutor.ExecDirect used as method value "+
				"(BS-6 blind spot — R3 would not detect this)",
				rel, fset.Position(id.Pos()).Line))
	})
	return violations
}

// embeddedStructHasPoolField reports whether the type denoted by expr (an
// anonymous/embedded field type) is a named struct type containing a
// *pgxpool.Pool field. Checks one level of struct embedding only — does NOT
// recurse into nested embeddings. Sufficient for current production patterns
// where embedding is rare; documented as accepted limitation in BS-1.
func embeddedStructHasPoolField(expr ast.Expr, info *types.Info) bool {
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	t := tv.Type
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

// receiverTypeName is preserved as a shared helper for other archtest files
// (referenced by mem_tx_lock_ownership_test.go and
// sealed_marker_noop_transparency_test.go). It returns the bare type name
// from a FuncDecl's receiver, stripping any leading * pointer marker.
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return ReceiverTypeName(fn.Recv.List[0].Type)
}
