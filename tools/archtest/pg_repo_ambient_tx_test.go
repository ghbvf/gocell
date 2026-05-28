// invariants:
//   - INVARIANT: PG-REPO-AMBIENT-TX-01
//
// # Package archtest — PG-REPO-AMBIENT-TX-01
//
// PG-REPO-AMBIENT-TX-01 enforces the ambient-tx routing contract for
// PostgreSQL-backed repositories. The funnel is sealed at the Go visibility
// level: the sanctioned holder type pgExecutor lives in a per-adapter
// /internal/pgexec/ sub-package and is unexported there; parent packages can
// only obtain a value via pgexec.New(pool) and can only invoke methods through
// the exported pgexec.PGExecutor interface, which does not expose the raw
// *pgxpool.Pool field. The interface itself is SEALED via an unexported marker
// method (see InterfaceSealed below) so a parent package cannot declare a
// parallel implementation.
//
//   - R1 (no raw pool in repo/store layer): every *_repo.go / *_store.go file
//     must not declare a *pgxpool.Pool field. Pool access is funneled through
//     the exported pgexec.PGExecutor interface (held as a field).
//     Infrastructure files (pool.go, tx_manager.go) and the sub-package's own
//     pgexec.go legitimately hold the raw pool and are out of scope by
//     file-extension filter — a PRE-EXISTING Soft scope boundary (#1206 tracks
//     the full pool-holder seal that would remove it).
//
//   - R2 (cross-package wrap funnel): every *_repo.go / *_store.go file's
//     function parameter of type *pgxpool.Pool must satisfy BOTH (a) function
//     name starts with "New" AND (b) function body calls pgexec.New(param)
//     resolved via *types.Info to a *types.Func with Pkg().Path() ending
//     /internal/pgexec AND Name() == "New". Unnamed pool params are counted
//     too (an unnamed New* param cannot be referenced, hence cannot be wrapped,
//     so it is flagged). Same PRE-EXISTING file-extension Soft scope as R1.
//
//   - R3 (ExecDirect call-bound approval, GLOBAL scope): every CallExpr
//     resolving to pgexec.ExecDirect (callee identity via *types.Info →
//     *types.Func with Pkg().Path() ending /internal/pgexec AND Name() ==
//     "ExecDirect") MUST pass, as its FIRST argument, an inline CallExpr to
//     pgrepoapproved.Approve whose own first argument is a kebab-case string
//     literal (^[a-z][a-z0-9-]+$, non-placeholder). R3 scans ALL production
//     files regardless of filename — ExecDirect is rare and identified by
//     callee identity, so no file-extension scope is needed (unlike R1/R2).
//
// # AI-robust grading (Funnel 双向锁评级, amended 2026-05-28)
//
// **上游 — pgExecutor 形态包外不可达**: **Hard** (compile-time, Go visibility).
// pgExecutor struct + *pgxpool.Pool field unexported in per-adapter
// internal/pgexec/ sub-package — sealed construction (ai-robust.md §Hard 范本 #6).
//
// **上游 — PGExecutor interface 包外不可实现**: **Hard** (Go visibility, sealed
// interface) + **archtest regression backstop** (InterfaceSealed). The
// unexported sealPGExecutor marker method means external packages cannot
// declare a parallel PGExecutor; TestPGRepoAmbientTx_InterfaceSealed asserts
// the marker stays present (exactly one unexported interface method) and that
// *pgExecutor implements it, so the seal cannot be silently removed by a later
// edit (sibling form to DETAILS-SEALED-FIELD-FROZEN-01 / ListenerAuth marker).
//
// **下游 R1 / R2 — pool field + wrap funnel**: archtest via *types.Info global
// predicate, scoped by *_repo.go / *_store.go file extension — PRE-EXISTING
// Soft scope (PR #917), upgrade tracked by #1206 (PG-INFRA-FULL-SEAL-01). The
// compile-time Hard comes from the sub-pkg seal; archtest is defense-in-depth.
//
// **下游 R3 — ExecDirect callsite**: **Hard via callee identity + call-bound
// approval**. ExecDirect is a top-level function pgexec.ExecDirect(approval, e,
// ctx, sql, args...), NOT a method — subset-interface bypass is closed at the
// type level. The approval token is the first argument: a bypass cannot exist
// without an inline pgrepoapproved.Approve("<kebab-literal>) (call-bound, no
// scope co-location, no marker reuse, no nested-closure smuggling). Form-
// uniqueness on the Approve arg (BasicLit + token.STRING + kebab regex +
// non-placeholder) mirrors panicregister (ai-robust.md §Hard 范本 #2).
//
// # RED fixtures
//
// The authoritative expected-violation MULTISET is expectedFixtureViolations,
// asserted by TestPGRepoAmbientTx_RedFixtureDetected with exact per-key counts.
// The fixture package at tools/archtest/internal/pgrepoambienttxfixture/:
//
//   - fixture_repo.go: R1 + R2 (incl. unnamed-param cases) + R3 call-bound RED
//     forms + GREEN controls.
//   - fixture_service.go: a NON-_repo.go file holding an R3 RED case — proves
//     R3's global scope (R1/R2 would exempt this filename; R3 must not).
//   - internal/pgexec/pgexec.go: sealed sub-package mirroring production form.
//
// # Blind spots
//
// BS-1 Field embedding: an embedded struct transitively carrying *pgxpool.Pool
// — covered by TestPGRepoAmbientTx_SelfCheck via *types.Info.
//
// BS-2 Interface-typed field carrying *pgxpool.Pool at runtime — R1 is a
// static-type check; accepted per ai-robust.md §3.
//
// BS-3 Function-value indirection of pgexec.New (`var fn = pgexec.New;
// fn(pool)`) — reverse check asserts no function-value reference to pgexec.New.
//
// BS-4 *pgxpool.Pool type alias — resolves to the same *types.Named; documented.
//
// BS-5 cells/accesscore PGBundle helper accepts *pgxpool.Pool as a param but
// does not persist it — reverse check asserts no struct field outside internal/.
//
// BS-6 Function-value indirection of pgexec.ExecDirect (`var fn =
// pgexec.ExecDirect; fn(approval, ...)`) — R3 resolves the direct CallExpr
// callee, so a function-value call would escape it; reverse check asserts no
// function-value reference to pgexec.ExecDirect in production.
//
// BS-7 Function-value indirection of pgrepoapproved.Approve (`f := Approve;
// ExecDirect(f("x"), ...)`) — NOT a separate reverse check: R3's
// execDirectHasInlineApproval requires arg[0] to be a direct CallExpr whose
// callee resolves (via *types.Info) to pgrepoapproved.Approve. `f("x")` has
// callee Ident `f` resolving to a *types.Var (not the *types.Func), so
// resolveCalleeFunc returns nil and R3 flags it — caught by the main rule, no
// reverse check needed.
//
// (BS-8 spurious-marker retired: there is no standalone marker statement under
// the call-bound form — an approval cannot exist without an ExecDirect call.)
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

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	pgxpoolImportPath        = "github.com/jackc/pgx/v5/pgxpool"
	pgxpoolTypeName          = "Pool"
	pgexecPkgSuffix          = "/internal/pgexec"
	pgexecFactoryName        = "New"
	pgExecutorInterfaceName  = "PGExecutor"
	pgExecutorImplName       = "pgExecutor"
	execDirectName           = "ExecDirect"
	approvedMarkerImportPath = "github.com/ghbvf/gocell/pkg/pgrepoapproved"
	approveFuncName          = "Approve"
	approvalReasonTypeName   = "ApprovalReason"
)

// isRepoOrStoreFile reports whether rel's basename ends with _repo.go or
// _store.go. This is the file-extension scope filter for R1 / R2 (PRE-EXISTING
// Soft, #1206), separating the repo layer (where pool access must be funneled)
// from the infrastructure layer (pool.go / tx_manager.go / internal/pgexec/
// pgexec.go — these legitimately hold the raw pool and are out of scope).
func isRepoOrStoreFile(rel string) bool {
	base := filepath.Base(rel)
	return strings.HasSuffix(base, "_repo.go") || strings.HasSuffix(base, "_store.go")
}

// isPgexecSubpackage reports whether pkgPath ends with /internal/pgexec. The
// sub-package owns the New factory + raw pool; it is exempt from R1/R2.
func isPgexecSubpackage(pkgPath string) bool {
	return strings.HasSuffix(pkgPath, pgexecPkgSuffix)
}

// TestPGRepoAmbientTx guards PG-REPO-AMBIENT-TX-01 against the production
// module. R1/R2 are file-extension-scoped global predicates; R3 is a global
// predicate over all production files. RED fixtures are exercised by
// TestPGRepoAmbientTx_RedFixtureDetected; the interface seal regression guard
// is TestPGRepoAmbientTx_InterfaceSealed; blind-spot self-checks are in
// TestPGRepoAmbientTx_SelfCheck.
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
// R3 (ExecDirect call-bound approval) and R4 (orphan Approve reverse ban) run
// GLOBALLY over every non-generated file — ExecDirect and Approve are both
// rare and identified by callee identity, so no file scope is needed. R1/R2
// (pool field / wrap funnel) remain *_repo.go / *_store.go scoped
// (PRE-EXISTING Soft, #1206) and exempt the /internal/pgexec sub-package,
// which owns the New factory and the raw pool.
func pgRepoAmbientTxRule(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var pkgPath string
	if p.Pkg != nil {
		pkgPath = p.Pkg.Path()
	}
	subpkg := isPgexecSubpackage(pkgPath)

	var diags []Diagnostic
	for _, file := range p.Files {
		if p.IsGenerated(file) {
			continue
		}
		rel := p.Rel(file)
		// R3 (forward) + R4 (reverse) are global: every file, any filename.
		diags = append(diags, scanR3ExecDirect(p.Fset, file, rel, p.TypesInfo)...)
		diags = append(diags, scanR4OrphanApprove(p.Fset, file, rel, p.TypesInfo)...)
		// R1/R2 are file-extension scoped and exempt the sub-package.
		if subpkg || !isRepoOrStoreFile(rel) {
			continue
		}
		diags = append(diags, scanR1PoolFields(p.Fset, file, rel, p.TypesInfo)...)
		diags = append(diags, scanR2PoolParams(p.Fset, file, rel, p.TypesInfo)...)
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
// /internal/pgexec/ sub-package.
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
		// R2b: New*-prefixed function must call pgexec.New(poolParam) in body.
		// An unnamed pool param ("_") cannot be referenced, hence cannot be
		// wrapped — bodyCallsPgexecNewWith never matches it, so it is flagged.
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
							"internal/pgexec/.New factory (an unnamed pool param cannot be "+
							"wrapped — give it a name and call pgexec.New)",
						fn.Name.Name, paramName, paramName,
					),
				})
			}
		}
	})
	return diags
}

// scanR3ExecDirect implements R3 (GLOBAL scope): every CallExpr resolving to
// pgexec.ExecDirect must pass, as its first argument, an inline CallExpr to
// pgrepoapproved.Approve("<kebab-literal>"). The approval is bound to the call
// expression — there is no standalone marker, no approval scope, no nested-
// closure handling.
func scanR3ExecDirect(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isPgexecExecDirectCall(call, info) {
			return
		}
		if execDirectHasInlineApproval(call, info) {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "R3: pgexec.ExecDirect callsite must pass an inline " +
				"pgrepoapproved.Approve(<catalog-const>) as its FIRST argument " +
				"(call-bound authorization). The Approve argument must resolve via " +
				"*types.Info.Uses to a *types.Const declared in pkg/pgrepoapproved " +
				"with type pgrepoapproved.ApprovalReason — i.e. one of the catalog " +
				"constants minted there (RevokeSessionCascade, IntegrationTest*). " +
				"Rejected forms: missing approval; a reused/pre-constructed Approval " +
				"variable; Approve called with a type conversion (ApprovalReason(\"…\")), " +
				"a locally-declared ApprovalReason const outside pkg/pgrepoapproved, a " +
				"string literal, BinaryExpr concatenation, fmt.Sprintf, or any other " +
				"runtime expression. " +
				"Production reference: " +
				"adapters/postgres/refresh_store.go::revokeSessionDetachedAt. ADR " +
				"docs/architecture/202605241400-003-pg-repo-ambient-tx-discovery-hard.md",
		})
	})
	return diags
}

// isPgexecExecDirectCall reports whether call's callee resolves via
// *types.Info.Uses to a *types.Func with Pkg().Path() ending /internal/pgexec
// AND Name() == "ExecDirect".
func isPgexecExecDirectCall(call *ast.CallExpr, info *types.Info) bool {
	fn := resolveCalleeFunc(call.Fun, info)
	if fn == nil || fn.Name() != execDirectName {
		return false
	}
	if fn.Pkg() == nil {
		return false
	}
	return strings.HasSuffix(fn.Pkg().Path(), pgexecPkgSuffix)
}

// isPgrepoapprovedApproveCall reports whether call's callee resolves to
// pgrepoapproved.Approve (the typed-marker minter). Used by R4 reverse scan.
func isPgrepoapprovedApproveCall(call *ast.CallExpr, info *types.Info) bool {
	fn := resolveCalleeFunc(call.Fun, info)
	if fn == nil || fn.Name() != approveFuncName {
		return false
	}
	if fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == approvedMarkerImportPath
}

// scanR4OrphanApprove implements R4 (reverse direction of R3): every
// pgrepoapproved.Approve callsite MUST appear as the first argument of a
// pgexec.ExecDirect call. Orphan calls — `_ = Approve(reason)`, assignment to
// a variable, return value, argument to a non-ExecDirect function, etc. —
// leak the "approval without a call" form that ADR §轴B 2026-05-28 promises
// is unrepresentable. The check is the symmetric backstop for R3: R3 ensures
// every ExecDirect goes through Approve; R4 ensures every Approve goes
// through ExecDirect.
//
// Implementation: two passes over the file using the same EachInSubtree walker
// SCANNER-FRAMEWORK-USAGE-01 mandates. The first pass collects every Approve
// CallExpr that is syntactically bound to an ExecDirect's Args[0]; the second
// pass walks all Approve CallExprs and flags any not in that bound set. Both
// passes are CallExpr-typed visits so no parent-tracking walker is needed —
// the bound-set captures the only parent-direction information R4 cares
// about (immediate-parent = ExecDirect AND occupies the Args[0] slot).
func scanR4OrphanApprove(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	bound := make(map[*ast.CallExpr]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isPgexecExecDirectCall(call, info) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		approveCall, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			return
		}
		if !isPgrepoapprovedApproveCall(approveCall, info) {
			return
		}
		bound[approveCall] = struct{}{}
	})

	var diags []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isPgrepoapprovedApproveCall(call, info) {
			return
		}
		if _, ok := bound[call]; ok {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: fset.Position(call.Pos()).Line,
			Message: "R4: pgrepoapproved.Approve callsite must be the first " +
				"argument of a pgexec.ExecDirect call (reverse-direction Hard: " +
				"every Approve goes through ExecDirect). Orphan Approve forms — " +
				"`_ = Approve(reason)`, assignment to a variable not passed inline " +
				"to ExecDirect, return value, argument to any non-ExecDirect " +
				"function — leak the \"approval without a call\" form that the " +
				"funnel forbids. If you need a fresh approval token for a new " +
				"ExecDirect callsite, inline it: " +
				"pgexec.ExecDirect(pgrepoapproved.Approve(pgrepoapproved.<Reason>), " +
				"db, ctx, sql, args...). ADR " +
				"docs/architecture/202605241400-003-pg-repo-ambient-tx-discovery-hard.md",
		})
	})
	return diags
}

// execDirectHasInlineApproval reports whether an ExecDirect call's first
// argument is an inline CallExpr to pgrepoapproved.Approve whose own first
// argument is an identifier or selector resolving to a *types.Const declared
// in the pgrepoapproved package with type pgrepoapproved.ApprovalReason.
//
// The 5-gate form-uniqueness chain (Hard 范本 #2 + #3 combined):
//
//  1. arg[0] of ExecDirect is *ast.CallExpr — rejects reused / pre-constructed
//     Approval variables (those are *ast.Ident).
//  2. that CallExpr's callee resolves (via *types.Info.Uses) to a *types.Func
//     named "Approve" in package pgrepoapproved — rejects look-alike functions
//     and Approve-as-function-value forms (callee resolves to *types.Var).
//  3. that CallExpr has at least one argument.
//  4. that argument resolves (via *types.Info.Uses) to a *types.Const —
//     rejects type conversions like ApprovalReason("untyped-orphan"), runtime
//     expressions, function calls, BasicLit string literals, BinaryExpr
//     concatenations, fmt.Sprintf, and empty arguments.
//  5. the *types.Const is declared in package pgrepoapproved AND has type
//     ApprovalReason — rejects locally-declared ApprovalReason consts (a
//     fixture / external package defining its own typed const) and consts of
//     unrelated types that happen to be named identically.
func execDirectHasInlineApproval(call *ast.CallExpr, info *types.Info) bool {
	if info == nil || len(call.Args) == 0 {
		return false
	}
	approveCall, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return false // reused / pre-constructed Approval variable, not inline
	}
	fn := resolveCalleeFunc(approveCall.Fun, info)
	if fn == nil || fn.Name() != approveFuncName {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != approvedMarkerImportPath {
		return false
	}
	if len(approveCall.Args) == 0 {
		return false
	}
	return isApprovedReasonConst(approveCall.Args[0], info)
}

// isApprovedReasonConst reports whether expr resolves (via *types.Info.Uses)
// to a *types.Const declared in package pgrepoapproved with type
// pgrepoapproved.ApprovalReason. Accepts *ast.Ident (unqualified in-package
// reference) and *ast.SelectorExpr (qualified `pgrepoapproved.Name`); rejects
// every other AST form (CallExpr for type conversion, BasicLit, BinaryExpr,
// etc.).
func isApprovedReasonConst(expr ast.Expr, info *types.Info) bool {
	var ident *ast.Ident
	switch e := expr.(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return false
	}
	obj, ok := info.Uses[ident]
	if !ok {
		return false
	}
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	if c.Pkg() == nil || c.Pkg().Path() != approvedMarkerImportPath {
		return false
	}
	named, ok := c.Type().(*types.Named)
	if !ok {
		return false
	}
	tn := named.Obj()
	if tn == nil || tn.Pkg() == nil {
		return false
	}
	return tn.Pkg().Path() == approvedMarkerImportPath && tn.Name() == approvalReasonTypeName
}

// collectPGPoolParams returns the names of fn's parameters whose type resolves
// to *pgxpool.Pool. Unnamed parameters are reported as "_" so R2 does not
// silently drop them (an unnamed pool param in a New* constructor cannot be
// referenced, hence cannot be wrapped via pgexec.New, and must be flagged).
func collectPGPoolParams(fn *ast.FuncDecl, info *types.Info) []string {
	var names []string
	for _, field := range fn.Type.Params.List {
		if !isPgxPoolType(field.Type, info) {
			continue
		}
		if len(field.Names) == 0 {
			names = append(names, "_")
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
	_, ok := FindFirstInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) bool {
		fn := resolveCalleeFunc(call.Fun, info)
		if fn == nil || fn.Name() != pgexecFactoryName {
			return false
		}
		if fn.Pkg() == nil || !strings.HasSuffix(fn.Pkg().Path(), pgexecPkgSuffix) {
			return false
		}
		if len(call.Args) == 0 {
			return false
		}
		arg, ok := call.Args[0].(*ast.Ident)
		return ok && arg.Name == paramName
	})
	return ok
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

// fixtureViolation is a (base, ruleID_prefix, line) triple identifying one
// expected RED fixture diagnostic. base is the file basename (filepath.Base of
// the diagnostic's module-relative path): including it distinguishes
// same-line diagnostics across fixture files (F2 — a {rulePrefix, line}-only
// key collapses cross-file collisions and is blind to which file a regression
// landed in).
type fixtureViolation struct {
	base       string
	rulePrefix string
	line       int
}

// expectedFixtureViolations is the authoritative expected MULTISET for
// TestPGRepoAmbientTx_RedFixtureDetected. Lines are pinned to the fixture
// sources. When the fixture changes intentionally, update both the fixture
// and this set together. The oracle compares exact per-key counts (F2), so a
// duplicate diagnostic (same base+prefix+line emitted twice) is a mismatch
// unless listed twice here.
//
// File layout:
//   - fixture.go: package godoc only (no rules apply — not _repo.go)
//   - fixture_repo.go: R1 + R2 RED (file-extension scoped) + R3 RED + GREEN controls
//   - fixture_service.go: R3 RED in a NON-_repo.go file (proves R3 global scope)
//   - internal/pgexec/pgexec.go: sealed sub-package mirroring production form
var expectedFixtureViolations = []fixtureViolation{
	// R1 RED (fixture_repo.go) — pointer to field type pos.
	{"fixture_repo.go", "R1:", 21}, // badR1Repo.pool
	// R2 RED (fixture_repo.go) — pointer to FuncDecl name pos.
	{"fixture_repo.go", "R2:", 31}, // badR2NonNew (named param)
	{"fixture_repo.go", "R2:", 37}, // NewBadR2NoWrap (named param, no pgexec.New)
	{"fixture_repo.go", "R2:", 44}, // badR2Unnamed (F1: unnamed non-New param)
	{"fixture_repo.go", "R2:", 49}, // NewBadR2Unnamed (F1: unnamed New param, cannot wrap)
	// R3 RED (fixture_repo.go) — pointer to pgexec.ExecDirect call pos.
	// Call-bound approval: arg[0] must be inline Approve(<catalog const>).
	{"fixture_repo.go", "R3:", 69}, // badR3LocalConst (locally-declared ApprovalReason const)
	{"fixture_repo.go", "R3:", 76}, // badR3TypeConversion (ApprovalReason("...") type conversion)
	{"fixture_repo.go", "R3:", 84}, // badR3ReusedApproval (arg[0] is *ast.Ident, not inline CallExpr)
	// R3 RED (fixture_service.go) — NON-_repo.go file; proves R3 global scope.
	{"fixture_service.go", "R3:", 21}, // serviceLayerBadExecDirect (reused approval, non-repo file)
	// R4 RED (fixture_repo.go) — pointer to pgrepoapproved.Approve call pos.
	// Orphan Approve: every Approve callsite must be Args[0] of ExecDirect.
	{"fixture_repo.go", "R4:", 83},  // badR3ReusedApproval's Approve is not inline at ExecDirect
	{"fixture_repo.go", "R4:", 95},  // badR4OrphanDiscarded (Approve assigned to blank)
	{"fixture_repo.go", "R4:", 102}, // badR4OrphanAssigned (Approve assigned to local var)
	// R4 RED (fixture_service.go) — non-_repo.go file proving R4 global scope.
	{"fixture_service.go", "R4:", 20}, // serviceLayerBadExecDirect's Approve is not inline
}

// TestPGRepoAmbientTx_RedFixtureDetected asserts the rule catches every RED
// violation in tools/archtest/internal/pgrepoambienttxfixture. The assertion
// is an exact MULTISET match on (base, ruleID_prefix, line) triples — counts
// must match exactly (F2), so duplicate or cross-file collisions are caught.
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
		base       string
		rulePrefix string
		line       int
	}
	actualCounts := make(map[diagKey]int, len(diags))
	for _, d := range diags {
		var prefix string
		switch {
		case strings.HasPrefix(d.Message, "R1:"):
			prefix = "R1:"
		case strings.HasPrefix(d.Message, "R2:"):
			prefix = "R2:"
		case strings.HasPrefix(d.Message, "R3:"):
			prefix = "R3:"
		case strings.HasPrefix(d.Message, "R4:"):
			prefix = "R4:"
		default:
			t.Errorf("unexpected diagnostic with unrecognized rulePrefix at %s:%d: %s",
				d.Rel, d.Line, d.Message)
			continue
		}
		actualCounts[diagKey{filepath.Base(d.Rel), prefix, d.Line}]++
	}

	expectedCounts := make(map[diagKey]int, len(expectedFixtureViolations))
	for _, v := range expectedFixtureViolations {
		expectedCounts[diagKey(v)]++
	}

	for k, want := range expectedCounts {
		if got := actualCounts[k]; got != want {
			t.Errorf("PG-REPO-AMBIENT-TX-01 RED fixture: %s %s:%d expected %d diagnostic(s) "+
				"but got %d — rule may have regressed or fixture line shifted; "+
				"update expectedFixtureViolations if fixture changed intentionally",
				k.rulePrefix, k.base, k.line, want, got)
		}
	}
	for k, got := range actualCounts {
		if want := expectedCounts[k]; want != got {
			t.Errorf("PG-REPO-AMBIENT-TX-01 RED fixture: %s %s:%d produced %d diagnostic(s) "+
				"not matched by expectedFixtureViolations (want %d) — GREEN cases must "+
				"produce 0; update expectedFixtureViolations if fixture changed intentionally",
				k.rulePrefix, k.base, k.line, got, want)
		}
	}
}

// TestPGRepoAmbientTx_InterfaceSealed is the regression backstop for the
// PGExecutor interface seal (upstream Hard). The seal property — external
// packages cannot implement PGExecutor — comes from an unexported marker
// method (sealPGExecutor). Go visibility makes that Hard against external
// implementers, but cannot prevent a later edit from deleting the marker
// method (the interface would still compile, all impls still work). This test
// asserts, for every production pgexec sub-package, that PGExecutor has
// EXACTLY ONE unexported method (the marker — structural, not name-anchored)
// and that *pgExecutor implements PGExecutor. Sibling form to
// DETAILS-SEALED-FIELD-FROZEN-01 (publicValue marker) / ListenerAuth marker.
func TestPGRepoAmbientTx_InterfaceSealed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Dynamic discovery: scan the whole production module and check EVERY
	// package whose import path ends /internal/pgexec. A new PG adapter
	// sub-package is covered automatically — no hand-maintained allowlist (which
	// would be the same Soft scope this funnel exists to remove). The sanity
	// anchor below guards the only failure mode dynamic discovery introduces:
	// silently finding zero pgexec packages (e.g. prodscan regression) → the
	// seal guard would vacuously pass.
	root := findModuleRoot(t)
	patterns := prodscan.Patterns(root)
	const anchorPkg = "github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"

	var checked []string
	_ = RunTyped(t, TypedOpts{}, patterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || !isPgexecSubpackage(p.Pkg.Path()) {
			return nil
		}
		checked = append(checked, p.Pkg.Path())
		assertSealedInterface(t, p.Pkg, pgExecutorInterfaceName, pgExecutorImplName)
		return nil
	})

	assert.Contains(t, checked, anchorPkg,
		"InterfaceSealed: prodscan discovery did not find the canonical "+
			anchorPkg+" — discovery may have regressed; without it the seal "+
			"regression guard would vacuously pass")
}

// TestPGRepoApprovedSealed is the symmetric regression backstop for the
// pgrepoapproved.Approval token interface. R3 is the primary gate (arg[0] must
// be an inline Approve(literal) CallExpr), but the sealed Approval interface is
// defense-in-depth: if its unexported marker method were deleted, Approval
// would collapse to interface{} and a forged value could be passed as the
// approval token. This asserts the seal stays intact.
func TestPGRepoApprovedSealed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	const approvedPkg = "github.com/ghbvf/gocell/pkg/pgrepoapproved"
	found := false
	_ = RunTyped(t, TypedOpts{}, []string{approvedPkg}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != approvedPkg {
			return nil
		}
		found = true
		assertSealedInterface(t, p.Pkg, "Approval", "approval")
		return nil
	})
	assert.True(t, found, "TestPGRepoApprovedSealed: pkg/pgrepoapproved was not loaded/checked")
}

// assertSealedInterface asserts pkg's ifaceName interface has exactly one
// unexported marker method (structural, not name-anchored — the seal makes the
// interface unimplementable outside the package), that *implName implements
// it, AND that implName is the ONLY in-package named type implementing it.
// Removing the marker (interface still compiles), adding a second unexported
// method, or declaring a sibling impl in the same package all fail here. The
// uniqueness check closes the in-package drift hole that pure Go visibility
// cannot prevent: external impls are blocked by the unexported marker, but a
// package author could declare a parallel struct alongside *pgExecutor.
func assertSealedInterface(t *testing.T, pkg *types.Package, ifaceName, implName string) {
	t.Helper()
	path := pkg.Path()
	ifaceObj := pkg.Scope().Lookup(ifaceName)
	if ifaceObj == nil {
		t.Errorf("%s: no %s type declared", path, ifaceName)
		return
	}
	named, ok := ifaceObj.Type().(*types.Named)
	if !ok {
		t.Errorf("%s: %s is not a named type", path, ifaceName)
		return
	}
	iface, ok := named.Underlying().(*types.Interface)
	if !ok {
		t.Errorf("%s: %s underlying is not an interface", path, ifaceName)
		return
	}
	unexported := 0
	for i := range iface.NumMethods() {
		if !iface.Method(i).Exported() {
			unexported++
		}
	}
	if unexported != 1 {
		t.Errorf("%s: %s must have exactly one unexported marker method (seal); "+
			"got %d — the seal makes the interface unimplementable outside the "+
			"package; removing or duplicating it breaks the upstream Hard guarantee",
			path, ifaceName, unexported)
	}

	implObj := pkg.Scope().Lookup(implName)
	if implObj == nil {
		t.Errorf("%s: no %s impl type declared", path, implName)
		return
	}
	// typesutil.ImplementsInterface tries value-or-pointer (TYPESUTIL-
	// IMPLEMENTS-FUNNEL-01: raw go/types.Implements is funnel-banned here).
	if !typesutil.ImplementsInterface(implObj.Type(), iface) {
		t.Errorf("%s: *%s does not implement %s (sanctioned impl must satisfy the sealed interface)",
			path, implName, ifaceName)
	}

	// Uniqueness: enumerate every named type in pkg.Scope() and reject any
	// sibling implementation. This closes the in-package drift hole that the
	// unexported marker alone cannot prevent — Go visibility blocks external
	// impls (the marker method is unexported, so no parallel implementation can
	// be declared outside this package) but a package author could declare a
	// parallel struct alongside the sanctioned impl inside the same package.
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if name == implName {
			continue
		}
		obj := scope.Lookup(name)
		typeName, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		if typeName.IsAlias() {
			continue
		}
		siblingType := typeName.Type()
		if _, isInterface := siblingType.Underlying().(*types.Interface); isInterface {
			continue
		}
		if typesutil.ImplementsInterface(siblingType, iface) {
			t.Errorf("%s: %s implements sealed interface %s but is not the sanctioned impl %s — "+
				"the seal contract is that ONLY %s satisfies %s; declare neither a parallel "+
				"struct nor an alias in this package",
				path, name, ifaceName, implName, implName, ifaceName)
		}
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

	// BS-3: pgexec.New must not appear as a function value (used as a value
	// rather than directly called) in any production package.
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
				findPgexecFuncValueUses(p.Fset, file, rel, p.TypesInfo, pgexecFactoryName)...)
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

	// BS-6: pgexec.ExecDirect must not appear as a function value (used as a
	// value rather than directly called) in any production file. A function-
	// value call would escape R3's direct-CallExpr callee resolution.
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
			bs6Violations = append(bs6Violations,
				findPgexecFuncValueUses(p.Fset, file, rel, p.TypesInfo, execDirectName)...)
		}
		return nil
	})
	assert.Empty(t, bs6Violations,
		"BS-6 self-check: pgexec.ExecDirect must not be used as a function value in production")
}

// findPgexecFuncValueUses returns violations for any Ident in the file that
// resolves to a *types.Func whose Name() == funcName AND Pkg().Path() ends
// /internal/pgexec AND is NOT in the callee position of a direct call.
//
// Covers BS-3 (funcName == "New") and BS-6 (funcName == "ExecDirect"). Both
// are top-level functions in pgexec sub-packages; their identity check is
// uniform.
func findPgexecFuncValueUses(fset *token.FileSet, file *ast.File, rel string, info *types.Info, funcName string) []string {
	calleePos := make(map[token.Pos]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		fn := resolveCalleeFunc(call.Fun, info)
		if fn == nil || fn.Name() != funcName {
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
		if fn.Name() != funcName {
			return
		}
		if fn.Pkg() == nil || !strings.HasSuffix(fn.Pkg().Path(), pgexecPkgSuffix) {
			return
		}
		if _, isCallee := calleePos[id.Pos()]; isCallee {
			return
		}
		violations = append(violations,
			fmt.Sprintf("%s:%d: pgexec.%s used as function value (not a direct call) "+
				"— would bypass R2/R3 archtest direct-call detection",
				rel, fset.Position(id.Pos()).Line, funcName))
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
