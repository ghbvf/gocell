package archtest

// errcode_invariants.go — importable errcode-theme rule logic (#1302 M3).
//
// This is the non-test home of the detector logic for six errcode-theme
// CellRules so they can be compiled and run by an external Cell repository
// through CheckErrcodeKindLiteralBanned / CheckErrcodeMessageConstLiteral /
// CheckExportedErrorNew / CheckErrorFirstAPI01 / CheckErrorFirstTypedNil01 /
// CheckDetailsSealedFieldFrozen01 (via StandardCellRules / RunStandardCellRules).
// Go never compiles a dependency's _test.go, so rule logic that external repos
// must run cannot live in a _test.go file. GoCell's own Test* functions in
// errcode_invariants_test.go call the same Check* — single source, no parallel
// rule body.
//
// Two rules are intentionally NOT registered (register=no) because they are
// bound to GoCell's own registry/ADR, making them vacuous for external modules:
//
//   - ERRCODE-PREFIX-OWNERSHIP-01: scans pkg/errcode/testdata/prefix_set.golden
//     and the gocell platform prefix registry — meaningless for external cells.
//   - ERRCODE-CARVEOUT-ADR-CONSISTENCY-01: cross-checks errcodeKindLiteralCarveOuts
//     against docs/architecture/202605121800-adr-archtest-carveout-narrow.md,
//     a GoCell-internal ADR.
//
// Both are kept module-path-agnostic (no bare platform-path literals) but remain
// in errcode_invariants_test.go.
//
// Platform-symbol paths are anchored to [PlatformModulePath] (fixed: external
// repos import these packages as a GoCell dependency at that path). The scan
// SCOPE is the running module, supplied by the driver. See external.go.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ─── rule ID constants ────────────────────────────────────────────────────────

const (
	ruleErrcodeKindLiteral01     = "ERRCODE-KIND-LITERAL-01"
	ruleMessageConstLiteral01    = "MESSAGE-CONST-LITERAL-01"
	ruleErrorFirstAPI01          = "ERROR-FIRST-API-01"
	ruleErrorFirstTypedNil01     = "ERROR-FIRST-TYPED-NIL-01"
	ruleExportedErrorNew01       = "EXPORTED-ERROR-NEW-01"
	ruleDetailsSealedFieldFrozen = "DETAILS-SEALED-FIELD-FROZEN-01"
)

// ─── platform-symbol path constants (no bare literals) ───────────────────────

// errcodeImportPath is the canonical import path of the errcode package.
// Anchored to PlatformModulePath — a single-source update point.
const errcodeImportPath = PlatformModulePath + "/pkg/errcode"

// errcodePackagePath is the canonical import path of the errcode package
// used by message/code gating helpers.
const errcodePackagePath = PlatformModulePath + "/pkg/errcode"

const (
	httputilPackagePath  = PlatformModulePath + "/pkg/httputil"
	ctxcancelPackagePath = PlatformModulePath + "/pkg/ctxcancel"
)

// errcodeKernelClockPkgPath is the import path of kernel/clock, used by
// nillableParamKind to exempt clock.Clock parameters from IsNilInterface
// enforcement (governed by its own MustHaveClock funnel instead).
const errcodeKernelClockPkgPath = PlatformModulePath + "/kernel/clock"

// errcodeRegisterPrefixHint is the fix hint appended to unregistered-prefix
// diagnostics.
const errcodeRegisterPrefixHint = "(add a RegisterPrefix entry in pkg/errcode/prefix_registry.go, " +
	"then regenerate: ERRCODE_PREFIX_GOLDEN_UPDATE=1 go test ./pkg/errcode/...)"

// ─── errcode message/code constants ──────────────────────────────────────────

// errcodeMessageAllowlist exempts pkg/errcode/ from the gate.
const errcodeMessageAllowlist = "pkg/errcode/"

// errcodeMessageTestdataAllowlist exempts archtest fixtures.
const errcodeMessageTestdataAllowlist = "tools/archtest/testdata/"

// errcodeAllowlistPath is the canonical home of low-level sentinel errors;
// the gate exempts it because pkg/errcode is the migration destination.
const errcodeAllowlistPath = "pkg/errcode/"

// ─── gatedCallee / codeGatedCallee ───────────────────────────────────────────

// gatedCallee describes one message-receiving entry point checked by the rule.
type gatedCallee struct {
	pkgPath         string
	name            string
	messageArgIndex int
	displayName     string
}

var messageGatedCallees = []gatedCallee{
	{pkgPath: errcodePackagePath, name: "New", messageArgIndex: 2, displayName: "errcode.New"},
	{pkgPath: errcodePackagePath, name: "Wrap", messageArgIndex: 2, displayName: "errcode.Wrap"},
	{pkgPath: httputilPackagePath, name: "WritePublic", messageArgIndex: 4, displayName: "httputil.WritePublic"},
	{pkgPath: ctxcancelPackagePath, name: "WrapOrInfra", messageArgIndex: 4, displayName: "ctxcancel.WrapOrInfra"},
}

// fixtureASTPackageNames maps the local-import name a fixture file uses back
// to the canonical gatedCallee's displayName for AST-only fixture mode.
var fixtureASTPackageNames = map[string]struct{}{
	"errcode":   {},
	"httputil":  {},
	"ctxcancel": {},
}

// codeGatedCallee mirrors gatedCallee for code-arg scanning (arg index 1).
type codeGatedCallee struct {
	pkgPath      string
	name         string
	codeArgIndex int
	displayName  string
}

var codeGatedCallees = []codeGatedCallee{
	{pkgPath: errcodePackagePath, name: "New", codeArgIndex: 1, displayName: "errcode.New"},
	{pkgPath: errcodePackagePath, name: "Wrap", codeArgIndex: 1, displayName: "errcode.Wrap"},
	{pkgPath: errcodePackagePath, name: "WrapInfra", codeArgIndex: 0, displayName: "errcode.WrapInfra"},
	{pkgPath: httputilPackagePath, name: "WritePublic", codeArgIndex: 3, displayName: "httputil.WritePublic"},
	{pkgPath: ctxcancelPackagePath, name: "WrapOrInfra", codeArgIndex: 3, displayName: "ctxcancel.WrapOrInfra"},
}

// ─── carve-out types ──────────────────────────────────────────────────────────

// carveOut identifies a single function-level carve-out for ERRCODE-KIND-LITERAL-01.
type carveOut struct{ rel, fn string }

// errcodeKindLiteralCarveOuts is the authoritative code-side list of functions
// permitted to construct errcode.Error{} struct literals directly.
//
// This map must be kept in strict equality with the CARVEOUT-REGISTRY table in
// docs/architecture/202605121800-adr-archtest-carveout-narrow.md.
// Any drift (code-only or ADR-only entry) is detected by
// ERRCODE-CARVEOUT-ADR-CONSISTENCY-01 and causes CI to turn red.
//
// To add or remove a carve-out, update BOTH this map AND the ADR registry
// table in the same PR. Attempting either in isolation will fail CI.
var errcodeKindLiteralCarveOuts = map[carveOut]struct{}{
	{rel: "pkg/ctxcancel/ctxcancel.go", fn: "WrapOrInfra"}: {},
	{rel: "pkg/httputil/response.go", fn: "WritePublic"}:   {},
}

// ─── error_first constants ────────────────────────────────────────────────────

// errorFirstEnforcedFiles are the relative paths of files whose declarations
// must satisfy ERROR-FIRST-API-01.
var errorFirstEnforcedFiles = []string{
	"kernel/wrapper/handler.go",
	"kernel/wrapper/consumer.go",
	"kernel/contractspec/spec.go",
	"kernel/wrapper/lifecycle.go",
	"kernel/auth/auth_plan.go",
	"kernel/outbox/entry_id.go",
	"kernel/outbox/envelope.go",
	"kernel/idempotency/inmem.go",
	"kernel/worker/worker.go",
	"runtime/eventrouter/router.go",
	"runtime/eventrouter/contract_tracing_subscriber.go",
	"runtime/auth/route.go",
	"runtime/worker/worker.go",
	"runtime/distlock/locker.go",
	"runtime/auth/refresh/memstore/store.go",
	"runtime/http/middleware/circuit_breaker.go",
	"runtime/http/health/health.go",
	"runtime/http/router/router.go",
	"kernel/persistence/tx.go",
	"cells/accesscore/slices/sessionlogin/service.go",
	"cells/accesscore/slices/sessionrefresh/service.go",
	"cells/accesscore/slices/sessionlogout/service.go",
	"adapters/postgres/refresh_store.go",
}

// errorFirstPanicWhitelist exempts ADR-approved C-class re-throw functions
// from ERROR-FIRST-API-01.
// Key format: "<rel-path>::<funcName>".
var errorFirstPanicWhitelist = map[string]struct{}{
	"kernel/wrapper/lifecycle.go::recoverAndFinish":                          {},
	"runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure": {},
	"adapters/postgres/tx_manager.go::repanicAfterTopLevelTxRollback":        {},
	"adapters/postgres/tx_manager.go::repanicAfterSavepointRollback":         {},
}

// ─── ERRCODE-KIND-LITERAL-01 ──────────────────────────────────────────────────

// CheckErrcodeKindLiteralBanned runs ERRCODE-KIND-LITERAL-01 over the running
// module and returns its diagnostics. It is the importable CellRule body
// wrapped by StandardCellRules; GoCell's TestErrcodeLiteralConstructionBanned
// calls it directly — single source, no parallel rule body.
func CheckErrcodeKindLiteralBanned(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	errcodeKindAllowedRel := func(rel string) bool {
		return strings.HasPrefix(rel, "pkg/errcode/")
	}
	return Run(t, AST(ModuleScope(root, MatchRels(func(rel string) bool {
		return !errcodeKindAllowedRel(rel)
	}))), findErrcodeErrorLiteralsPass)
}

// findErrcodeErrorLiteralsPass reports errcode.Error composite literals
// constructed outside the function-level carve-outs.
func findErrcodeErrorLiteralsPass(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		for _, h := range scanErrcodeErrorLiteralsInAST(p.Fset, file, rel, errcodeKindLiteralCarveOuts) {
			out = append(out, Diagnostic{
				Rel:     rel,
				Line:    h.line,
				Message: fmt.Sprintf("%s constructs errcode.Error directly; use errcode.New/Wrap", rel),
			})
		}
	}
	return out
}

// errcodeErrorHit records a detected errcode.Error{} literal with its line number.
type errcodeErrorHit struct {
	line int
}

// scanErrcodeErrorLiteralsInAST is the unit-testable core of the scanner.
//
// AST forms OUTSIDE the declared detection range (pure-AST isErrcodeErrorType):
//
//	(a) Aliased errcode import: `import ec "github.com/.../pkg/errcode"` + `ec.Error{}`
//	    — errcodeImportNames collects the alias, so aliased imports ARE detected.
//	(b) Dot-import: `import . "github.com/.../pkg/errcode"` + `Error{}`
//	    — errcodeImportNames explicitly skips "." names.
//	    errcodeDotImported (reverse self-check) asserts no production file dot-imports.
//	(c) Cross-package type-alias re-export: `type Error = errcode.Error` in a
//	    third package + `thirdpkg.Error{}` — NOT detected.
//	    errcodeErrorAliasReexports (reverse self-check) asserts no production file re-exports.
func scanErrcodeErrorLiteralsInAST(fset *token.FileSet, f *ast.File, rel string, carveOuts map[carveOut]struct{}) []errcodeErrorHit {
	errcodeNames := errcodeImportNames(f)
	if len(errcodeNames) == 0 {
		return nil
	}

	var hits []errcodeErrorHit

	type posRange struct{ lo, hi token.Pos }
	var carvedRanges []posRange
	scanner.EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		if fd.Recv != nil {
			return
		}
		key := carveOut{rel: rel, fn: fd.Name.Name}
		if _, ok := carveOuts[key]; ok {
			carvedRanges = append(carvedRanges, posRange{fd.Body.Pos(), fd.Body.End()})
		}
	})

	inCarvedRange := func(pos token.Pos) bool {
		for _, r := range carvedRanges {
			if pos >= r.lo && pos < r.hi {
				return true
			}
		}
		return false
	}

	scanner.EachInSubtree[ast.CompositeLit](f, func(lit *ast.CompositeLit) {
		if !isErrcodeErrorType(lit.Type, errcodeNames) {
			return
		}
		if inCarvedRange(lit.Pos()) {
			return
		}
		hits = append(hits, errcodeErrorHit{fset.Position(lit.Pos()).Line})
	})
	return hits
}

// ─── MESSAGE-CONST-LITERAL-01 ────────────────────────────────────────────────

// CheckErrcodeMessageConstLiteral runs MESSAGE-CONST-LITERAL-01 over the
// running module and returns its diagnostics. It is the importable CellRule
// body wrapped by StandardCellRules.
func CheckErrcodeMessageConstLiteral(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	visited := map[string]bool{}
	return Run(t, Typed(
		TypedOpts{Tests: false, Tags: []string{"e2e", "integration", "pg"}},
		patterns,
	),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				abs := p.Abs(file)
				if visited[abs] {
					continue
				}
				visited[abs] = true

				rel := p.Rel(file)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if strings.HasPrefix(rel, errcodeMessageAllowlist) {
					continue
				}
				if strings.HasPrefix(rel, errcodeMessageTestdataAllowlist) {
					continue
				}
				out = append(out, scanErrcodeMessageASTDiags(p.Fset, file, rel, p.TypesInfo)...)
			}
			return out
		})
}

// scanErrcodeMessageASTDiags is the Diagnostic-returning form used by the
// CheckErrcodeMessageConstLiteral Pass-funnel rule.
func scanErrcodeMessageASTDiags(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		callee, ok := resolveGatedCallee(call, info)
		if !ok {
			return
		}
		if len(call.Args) <= callee.messageArgIndex {
			return
		}
		msgArg := call.Args[callee.messageArgIndex]
		if isAcceptableMessageExpr(msgArg, info) {
			return
		}
		line := fset.Position(call.Pos()).Line
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"%s(...) message must be a const literal (got %T) "+
					"— move runtime data to WithDetails(errcode.PublicString/PublicInt/PublicBool/PublicDuration/PublicTime(...)) or "+
					"WithInternal(errcode.InternalAttr(...))",
				callee.displayName, msgArg,
			),
		})
	})
	return out
}

// resolveGatedCallee matches call against messageGatedCallees.
func resolveGatedCallee(call *ast.CallExpr, info *types.Info) (gatedCallee, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return gatedCallee{}, false
	}
	if info != nil {
		obj := info.Uses[sel.Sel]
		if obj == nil {
			return gatedCallee{}, false
		}
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			return gatedCallee{}, false
		}
		pkgPath := fn.Pkg().Path()
		name := fn.Name()
		for _, c := range messageGatedCallees {
			if c.pkgPath == pkgPath && c.name == name {
				return c, true
			}
		}
		return gatedCallee{}, false
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return gatedCallee{}, false
	}
	if _, registered := fixtureASTPackageNames[xIdent.Name]; !registered {
		return gatedCallee{}, false
	}
	for _, c := range messageGatedCallees {
		shortName := lastPathSegment(c.pkgPath)
		if shortName == xIdent.Name && sel.Sel.Name == c.name {
			return c, true
		}
	}
	return gatedCallee{}, false
}

// resolveCodeGatedCallee is parallel to resolveGatedCallee but matches
// codeGatedCallees.
func resolveCodeGatedCallee(call *ast.CallExpr, info *types.Info) (codeGatedCallee, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return codeGatedCallee{}, false
	}
	if info != nil {
		obj := info.Uses[sel.Sel]
		if obj == nil {
			return codeGatedCallee{}, false
		}
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			return codeGatedCallee{}, false
		}
		pkgPath := fn.Pkg().Path()
		name := fn.Name()
		for _, c := range codeGatedCallees {
			if c.pkgPath == pkgPath && c.name == name {
				return c, true
			}
		}
		return codeGatedCallee{}, false
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return codeGatedCallee{}, false
	}
	if _, registered := fixtureASTPackageNames[xIdent.Name]; !registered {
		return codeGatedCallee{}, false
	}
	for _, c := range codeGatedCallees {
		shortName := lastPathSegment(c.pkgPath)
		if shortName == xIdent.Name && sel.Sel.Name == c.name {
			return c, true
		}
	}
	return codeGatedCallee{}, false
}

// lastPathSegment returns the substring after the final '/' in a Go import path.
func lastPathSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// isLiteralStringExpr reports whether expr is a string-literal expression
// or a BinaryExpr whose operands are both string literals (recursively).
func isLiteralStringExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.BinaryExpr:
		return isLiteralStringExpr(e.X) && isLiteralStringExpr(e.Y)
	default:
		return false
	}
}

// isAcceptableMessageExpr reports whether expr is a const literal or a
// package-level string constant.
func isAcceptableMessageExpr(expr ast.Expr, info *types.Info) bool {
	if info != nil {
		if tv, ok := info.Types[expr]; ok && tv.Value != nil {
			return true
		}
	}
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.Ident:
		if info == nil {
			return true
		}
		obj := info.Uses[e]
		_, isConst := obj.(*types.Const)
		return isConst
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return false
		}
		if info == nil {
			return true
		}
		obj := info.Uses[e.Sel]
		_, isConst := obj.(*types.Const)
		return isConst
	case *ast.BinaryExpr:
		if info != nil {
			return false
		}
		return isLiteralStringExpr(e.X) && isLiteralStringExpr(e.Y)
	default:
		return false
	}
}

// ─── ERROR-FIRST-API-01 ───────────────────────────────────────────────────────

// CheckErrorFirstAPI01 runs ERROR-FIRST-API-01 over the enforced file list and
// returns its diagnostics. It is the importable CellRule body wrapped by
// StandardCellRules.
func CheckErrorFirstAPI01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	enforcedSet := make(map[string]struct{}, len(errorFirstEnforcedFiles))
	for _, rel := range errorFirstEnforcedFiles {
		enforcedSet[rel] = struct{}{}
	}

	return Run(t, AST(ModuleScope(root, MatchRels(func(rel string) bool {
		_, ok := enforcedSet[rel]
		return ok
	}))),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				out = append(out, scanFileForErrorFirstViolations(p.Fset, file, rel)...)
			}
			return out
		})
}

// scanFileForErrorFirstViolations parses a single Go source file and returns
// any panic() call inside an error-less function.
func scanFileForErrorFirstViolations(fset *token.FileSet, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		if isInitFunc(fd) {
			return
		}
		if strings.HasPrefix(fd.Name.Name, "Must") {
			return
		}
		if signatureReturnsError(fd.Type.Results) {
			return
		}
		whitelistKey := rel + "::" + fd.Name.Name
		if _, whitelisted := errorFirstPanicWhitelist[whitelistKey]; whitelisted {
			return
		}
		findPanicCalls(fd.Body, func(callPos token.Pos) {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: fset.Position(callPos).Line,
				Message: fmt.Sprintf(
					"function %s does not return error but contains panic()",
					fd.Name.Name,
				),
			})
		})
	})
	return out
}

// ─── ERROR-FIRST-TYPED-NIL-01 ────────────────────────────────────────────────

// CheckErrorFirstTypedNil01 runs ERROR-FIRST-TYPED-NIL-01 over the enforced
// file list and returns its diagnostics. It is the importable CellRule body
// wrapped by StandardCellRules.
func CheckErrorFirstTypedNil01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	enforced := errorFirstEnforcedFileMap(root)
	return Run(t, Typed(TypedOpts{Tests: false}, errorFirstPackagePatterns()),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				abs := filepath.Clean(p.Abs(file))
				rel, ok := enforced[abs]
				if !ok {
					continue
				}
				out = append(out, scanTypedNilGuardsInFile(p.Fset, p.TypesInfo, file, rel)...)
			}
			return out
		})
}

func errorFirstPackagePatterns() []string {
	dirs := make(map[string]struct{})
	for _, rel := range errorFirstEnforcedFiles {
		dirs[filepath.Dir(filepath.FromSlash(rel))] = struct{}{}
	}
	patterns := make([]string, 0, len(dirs))
	for dir := range dirs {
		patterns = append(patterns, "./"+filepath.ToSlash(dir))
	}
	sort.Strings(patterns)
	return patterns
}

func errorFirstEnforcedFileMap(root string) map[string]string {
	out := make(map[string]string, len(errorFirstEnforcedFiles))
	for _, rel := range errorFirstEnforcedFiles {
		out[filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))] = rel
	}
	return out
}

func isErrorFirstConstructor(fd *ast.FuncDecl) bool {
	return fd.Recv == nil &&
		strings.HasPrefix(fd.Name.Name, "New") &&
		signatureReturnsError(fd.Type.Results)
}

// paramKind classifies how a function parameter is nil-able.
type paramKind int

const (
	paramNone paramKind = iota
	paramInterface
	paramPointerOrNillableConcrete
)

// paramRef pairs a parameter name with its kind.
type paramRef struct {
	name string
	kind paramKind
}

// nillableParamKind returns the paramKind for a Go type.
func nillableParamKind(t types.Type) paramKind {
	if t == nil {
		return paramNone
	}
	// clock.Clock is governed by its own MustHaveClock funnel — exempt it.
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil &&
			obj.Pkg().Path() == errcodeKernelClockPkgPath && obj.Name() == "Clock" {
			return paramNone
		}
	}
	switch t.Underlying().(type) {
	case *types.Interface:
		return paramInterface
	case *types.Pointer, *types.Map, *types.Chan, *types.Signature:
		return paramPointerOrNillableConcrete
	}
	return paramNone
}

// nillableDependencyParams returns the named, nil-able parameters of fd.
func nillableDependencyParams(info *types.Info, fd *ast.FuncDecl) []paramRef {
	if info == nil || fd.Type.Params == nil {
		return nil
	}
	var out []paramRef
	for _, field := range fd.Type.Params.List {
		kind := nillableParamKind(info.TypeOf(field.Type))
		if kind == paramNone {
			continue
		}
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}
			out = append(out, paramRef{name: name.Name, kind: kind})
		}
	}
	return out
}

// hasNilGuard returns true if body contains an IfStmt whose Cond is a nil
// check on paramName AND whose Then-branch surfaces the nil case.
func hasNilGuard(body *ast.BlockStmt, paramName string, kind paramKind) bool {
	_, found := FindFirstInSubtree[ast.IfStmt](body, func(ifStmt *ast.IfStmt) bool {
		return condMatchesNilCheck(ifStmt.Cond, paramName, kind) &&
			thenReturnsOrAssigns(ifStmt.Body, paramName)
	})
	return found
}

// condMatchesNilCheck returns true if expr nil-checks paramName.
func condMatchesNilCheck(expr ast.Expr, paramName string, kind paramKind) bool {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return condMatchesNilCheck(e.X, paramName, kind)
	case *ast.BinaryExpr:
		if e.Op == token.LOR {
			return condMatchesNilCheck(e.X, paramName, kind) ||
				condMatchesNilCheck(e.Y, paramName, kind)
		}
		if e.Op == token.EQL && kind == paramPointerOrNillableConcrete {
			return errcodeIsNilEquality(e, paramName)
		}
		return false
	case *ast.CallExpr:
		return isValidationIsNilInterfaceCall(e, paramName)
	}
	return false
}

// errcodeIsNilEquality returns true if e is `paramName == nil` or `nil == paramName`.
// Named with errcode prefix to avoid collision with isNilEquality in other test files
// when this non-test file is compiled together with test files in the same package.
func errcodeIsNilEquality(e *ast.BinaryExpr, paramName string) bool {
	if e.Op != token.EQL {
		return false
	}
	if isIdentNamed(e.X, paramName) && errcodeIsNilIdent(e.Y) {
		return true
	}
	if isIdentNamed(e.Y, paramName) && errcodeIsNilIdent(e.X) {
		return true
	}
	return false
}

// errcodeIsNilIdent returns true if expr is the identifier `nil`.
// Named with errcode prefix to avoid collision with isNilIdent defined in
// outbox_invariants_test.go (same package, but that is a _test.go file —
// non-test files and test files are compiled separately so there is no symbol
// conflict, but keeping distinct names avoids confusion).
func errcodeIsNilIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}

func isIdentNamed(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// isValidationIsNilInterfaceCall returns true if call is exactly
// validation.IsNilInterface(paramName).
func isValidationIsNilInterfaceCall(call *ast.CallExpr, paramName string) bool {
	if len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "IsNilInterface" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "validation" {
		return false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	return ok && arg.Name == paramName
}

// thenReturnsOrAssigns returns true if body contains a top-level ReturnStmt
// or an AssignStmt whose LHS includes paramName.
func thenReturnsOrAssigns(body *ast.BlockStmt, paramName string) bool {
	type posRange struct{ lo, hi token.Pos }
	var funcLitRanges []posRange
	EachInSubtree[ast.FuncLit](body, func(fl *ast.FuncLit) {
		funcLitRanges = append(funcLitRanges, posRange{fl.Pos(), fl.End()})
	})
	inFuncLit := func(pos token.Pos) bool {
		for _, r := range funcLitRanges {
			if pos >= r.lo && pos < r.hi {
				return true
			}
		}
		return false
	}

	_, foundReturn := FindFirstInSubtree[ast.ReturnStmt](body, func(s *ast.ReturnStmt) bool {
		return !inFuncLit(s.Pos())
	})
	if foundReturn {
		return true
	}
	_, foundAssign := FindFirstInSubtree[ast.AssignStmt](body, func(s *ast.AssignStmt) bool {
		if inFuncLit(s.Pos()) {
			return false
		}
		for _, lhs := range s.Lhs {
			if isIdentNamed(lhs, paramName) {
				return true
			}
		}
		return false
	})
	return foundAssign
}

// scanTypedNilGuardsInFile returns Diagnostic violations for
// ERROR-FIRST-TYPED-NIL-01 in a single file.
func scanTypedNilGuardsInFile(fset *token.FileSet, info *types.Info, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Body == nil || !isErrorFirstConstructor(fd) {
			return
		}
		// A constructor that delegates to the generated validateRequired()
		// funnel cedes required-dep nil checking to that single source of truth.
		if errcodeBodyCallsValidateRequired(fd.Body) {
			return
		}
		for _, param := range nillableDependencyParams(info, fd) {
			if hasNilGuard(fd.Body, param.name, param.kind) {
				continue
			}
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: fset.Position(fd.Pos()).Line,
				Message: fmt.Sprintf(
					"constructor %s: nil-able dependency %s is not guarded at construction time",
					fd.Name.Name, param.name,
				),
			})
		}
	})
	return out
}

// errcodeBodyCallsValidateRequired reports whether body contains a call to
// a method named validateRequired (the REQUIRED-DEP-NIL-GUARD-01 generated funnel).
// This is a local implementation in the non-test file so it does not depend on
// isValidateRequiredCallExpr declared in required_dep_nil_guard_test.go. The
// logic is identical: a method call whose selector name is "validateRequired".
func errcodeBodyCallsValidateRequired(body *ast.BlockStmt) bool {
	found := false
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel != nil && sel.Sel.Name == "validateRequired" {
			found = true
		}
	})
	return found
}

// ─── AST helpers (shared) ─────────────────────────────────────────────────────

// isInitFunc returns true if fd is `func init()`.
func isInitFunc(fd *ast.FuncDecl) bool {
	return fd.Name.Name == "init" && fd.Recv == nil
}

// signatureReturnsError returns true if the FieldList contains at least one
// field whose type is the identifier `error`.
func signatureReturnsError(results *ast.FieldList) bool {
	if results == nil {
		return false
	}
	for _, field := range results.List {
		if isErrorIdent(field.Type) {
			return true
		}
	}
	return false
}

// isErrorIdent returns true when expr is the unqualified identifier `error`.
func isErrorIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "error"
}

// findPanicCalls walks body and invokes onPanic for every call to panic.
func findPanicCalls(body *ast.BlockStmt, onPanic func(token.Pos)) {
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return
		}
		if ident.Name == "panic" {
			onPanic(call.Pos())
		}
	})
}

// errcodeImportNames returns the set of local names used to import pkg/errcode.
func errcodeImportNames(f *ast.File) map[string]struct{} {
	names := map[string]struct{}{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != errcodeImportPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				names[imp.Name.Name] = struct{}{}
			}
			continue
		}
		names["errcode"] = struct{}{}
	}
	return names
}

func isErrcodeErrorType(expr ast.Expr, errcodeNames map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Error" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = errcodeNames[pkg.Name]
	return ok
}

// errcodeDotImported reports whether f dot-imports pkg/errcode.
func errcodeDotImported(f *ast.File) bool {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != errcodeImportPath {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			return true
		}
	}
	return false
}

// errcodeErrorAliasReexports returns the names of every package-level type
// alias `type X = <errcode>.Error` in f.
func errcodeErrorAliasReexports(f *ast.File) []string {
	errcodeNames := errcodeImportNames(f)
	if len(errcodeNames) == 0 {
		return nil
	}
	var names []string
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if !ts.Assign.IsValid() {
			return
		}
		sel, ok := ts.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Error" {
			return
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		if _, inNames := errcodeNames[pkg.Name]; inNames {
			names = append(names, ts.Name.Name)
		}
	})
	return names
}

// ─── EXPORTED-ERROR-NEW-01 ───────────────────────────────────────────────────

// CheckExportedErrorNew runs EXPORTED-ERROR-NEW-01 over the running module and
// returns its diagnostics. It is the importable CellRule body wrapped by
// StandardCellRules.
func CheckExportedErrorNew(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	visited := map[string]bool{}
	return Run(t, Typed(
		TypedOpts{Tests: false, Tags: []string{"e2e", "integration", "pg"}},
		patterns,
	),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				abs := p.Abs(file)
				if visited[abs] {
					continue
				}
				visited[abs] = true

				rel := p.Rel(file)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if strings.HasPrefix(rel, errcodeAllowlistPath) {
					continue
				}
				out = append(out, scanExportedErrorNewASTDiags(p.Fset, file, rel, p.TypesInfo)...)
			}
			return out
		})
}

// scanExportedErrorNewASTDiags is the Diagnostic-returning form used by
// CheckExportedErrorNew.
func scanExportedErrorNewASTDiags(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.GenDecl](file, func(gen *ast.GenDecl) {
		if gen.Tok != token.VAR {
			return
		}
		EachInChildren[ast.ValueSpec](gen, func(vs *ast.ValueSpec) {
			for i, name := range vs.Names {
				if !isExportedErrSentinelName(name.Name) {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				if !isErrorsNewCall(vs.Values[i], info) {
					continue
				}
				pos := fset.Position(name.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"%s = errors.New(...) — migrate to errcode.New(code, message)",
						name.Name,
					),
				})
			}
		})
	})
	return out
}

// isExportedErrSentinelName reports whether name follows `Err` + ASCII uppercase.
func isExportedErrSentinelName(name string) bool {
	if !strings.HasPrefix(name, "Err") {
		return false
	}
	if len(name) <= 3 {
		return false
	}
	c := name[3]
	return c >= 'A' && c <= 'Z'
}

// isErrorsNewCall reports whether expr is a call to stdlib `errors.New`.
func isErrorsNewCall(expr ast.Expr, info *types.Info) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if info == nil {
		return false
	}
	obj := info.Uses[sel.Sel]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	if fn.Name() != "New" {
		return false
	}
	pkg := fn.Pkg()
	if pkg == nil {
		return false
	}
	return pkg.Path() == "errors"
}

// ─── DETAILS-SEALED-FIELD-FROZEN-01 ─────────────────────────────────────────

// CheckDetailsSealedFieldFrozen01 runs DETAILS-SEALED-FIELD-FROZEN-01 against
// the platform errcode package's PublicDetail/InternalDetail structs and
// returns its diagnostics. It is the importable CellRule body wrapped by
// StandardCellRules.
//
// This rule uses reflect on the actual errcode.PublicDetail and InternalDetail
// types — the platform package must be importable as a dependency, which it is
// for any external Cell that imports GoCell as a module.
func CheckDetailsSealedFieldFrozen01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()

	var diags []Diagnostic

	// PublicDetail checks.
	dt := reflect.TypeOf(errcode.PublicDetail{})
	for _, v := range checkSealedKeyValueShape("PublicDetail", dt) {
		diags = append(diags, Diagnostic{
			Rel:     "pkg/errcode/details.go",
			Line:    0,
			Message: "DETAILS-SEALED-FIELD-FROZEN-01: " + v,
		})
	}

	valueField, ok := dt.FieldByName("value")
	if !ok {
		diags = append(diags, Diagnostic{
			Rel:     "pkg/errcode/details.go",
			Line:    0,
			Message: "DETAILS-SEALED-FIELD-FROZEN-01: PublicDetail has no value field",
		})
	} else {
		if valueField.Type.Kind() != reflect.Interface {
			diags = append(diags, Diagnostic{
				Rel:  "pkg/errcode/details.go",
				Line: 0,
				Message: fmt.Sprintf(
					"DETAILS-SEALED-FIELD-FROZEN-01: PublicDetail.value Kind = %s, want Interface",
					valueField.Type.Kind()),
			})
		}
		if got := valueField.Type.Name(); got != "publicValue" {
			diags = append(diags, Diagnostic{
				Rel:  "pkg/errcode/details.go",
				Line: 0,
				Message: fmt.Sprintf(
					"DETAILS-SEALED-FIELD-FROZEN-01: PublicDetail.value type name = %q, want %q",
					got, "publicValue"),
			})
		}
		probe := reflect.ValueOf(errcode.PublicString("k", "v"))
		probeValue := probe.FieldByName("value")
		if probeValue.Kind() != reflect.Interface {
			diags = append(diags, Diagnostic{
				Rel:  "pkg/errcode/details.go",
				Line: 0,
				Message: fmt.Sprintf(
					"DETAILS-SEALED-FIELD-FROZEN-01: PublicString(...) produced value Kind %s, want Interface",
					probeValue.Kind()),
			})
		}
	}

	// AST-based implementer/constructor set check requires findModuleRoot.
	root := findModuleRoot(t)
	detailsPath := filepath.Join(root, "pkg", "errcode", "details.go")
	detailsFile := errcodeParseGoFile(t, detailsPath)
	if detailsFile != nil {
		errcodeAssertExactStringSet(t, &diags, "DETAILS-SEALED-FIELD-FROZEN-01 publicValue implementers",
			errcodeCollectPublicValueImplementers(detailsFile),
			[]string{"publicBool", "publicDuration", "publicInt", "publicString", "publicTime"})
		errcodeAssertExactStringSet(t, &diags, "DETAILS-SEALED-FIELD-FROZEN-01 PublicDetail constructors",
			errcodeCollectPublicDetailConstructors(detailsFile),
			[]string{"PublicBool", "PublicDuration", "PublicInt", "PublicString", "PublicTime"})
	}

	// InternalDetail checks.
	idt := reflect.TypeOf(errcode.InternalDetail{})
	for _, v := range checkSealedKeyValueShape("InternalDetail", idt) {
		diags = append(diags, Diagnostic{
			Rel:     "pkg/errcode/details.go",
			Line:    0,
			Message: "DETAILS-SEALED-FIELD-FROZEN-01: " + v,
		})
	}
	if ivField, ok := idt.FieldByName("value"); ok {
		if ivField.Type.Kind() != reflect.Interface || ivField.Type.Name() != "" {
			diags = append(diags, Diagnostic{
				Rel:  "pkg/errcode/details.go",
				Line: 0,
				Message: fmt.Sprintf(
					"DETAILS-SEALED-FIELD-FROZEN-01: InternalDetail.value type = %s, want untyped any",
					ivField.Type.String()),
			})
		}
	}

	return diags
}

// errcodeParseGoFile parses a Go source file; returns nil (not fatal) so
// CheckDetailsSealedFieldFrozen01 can still return reflect-based diagnostics
// when the AST check fails to load.
func errcodeParseGoFile(t *testing.T, path string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		// Use t.Errorf so the test still proceeds with reflect-based checks.
		t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: parse %s: %v", path, err)
		return nil
	}
	return file
}

func errcodeCollectPublicValueImplementers(file *ast.File) []string {
	seen := map[string]struct{}{}
	scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Recv == nil || fn.Name.Name != "publicValue" || len(fn.Recv.List) == 0 {
			return
		}
		if name := errcodeDetailReceiverTypeName(fn.Recv.List[0].Type); name != "" {
			seen[name] = struct{}{}
		}
	})
	return errcodeSortedSetKeys(seen)
}

func errcodeCollectPublicDetailConstructors(file *ast.File) []string {
	seen := map[string]struct{}{}
	scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil {
			return
		}
		for _, result := range fn.Type.Results.List {
			if ident, ok := result.Type.(*ast.Ident); ok && ident.Name == "PublicDetail" {
				seen[fn.Name.Name] = struct{}{}
			}
		}
	})
	return errcodeSortedSetKeys(seen)
}

func errcodeDetailReceiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return errcodeDetailReceiverTypeName(t.X)
	default:
		return ""
	}
}

func errcodeSortedSetKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func errcodeAssertExactStringSet(t *testing.T, diags *[]Diagnostic, name string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		*diags = append(*diags, Diagnostic{
			Rel:  "pkg/errcode/details.go",
			Line: 0,
			Message: fmt.Sprintf(
				"DETAILS-SEALED-FIELD-FROZEN-01: %s = %v, want %v. "+
					"Adding/removing a public detail scalar kind must update "+
					"details.go, error-response-v1.schema.json, the ADR, and this archtest together.",
				name, got, want),
		})
	}
}

// checkSealedKeyValueShape returns violation messages per shape axis.
// Pure function; used by both CheckDetailsSealedFieldFrozen01 and the
// reverse self-check in errcode_invariants_test.go.
func checkSealedKeyValueShape(name string, dt reflect.Type) []string {
	var violations []string
	if dt.NumField() != 2 {
		violations = append(violations, fmt.Sprintf(
			"%s NumField = %d, want 2 (adding a field re-opens the sealed-construction invariant; update the ADR amendment first)",
			name, dt.NumField(),
		))
		return violations
	}
	if keyField, ok := dt.FieldByName("key"); !ok {
		violations = append(violations, fmt.Sprintf(
			"%s has no 'key' field (renamed? exported? both break the sealed-construction invariant)", name,
		))
	} else {
		if keyField.PkgPath == "" {
			violations = append(violations, fmt.Sprintf(
				"%s.key is exported (PkgPath empty); outside-package literal construction becomes possible — re-seal by lowercasing",
				name,
			))
		}
		if keyField.Type.Kind() != reflect.String {
			violations = append(violations, fmt.Sprintf(
				"%s.key Kind = %s, want String", name, keyField.Type.Kind(),
			))
		}
	}
	if valueField, ok := dt.FieldByName("value"); !ok {
		violations = append(violations, fmt.Sprintf(
			"%s has no 'value' field (renamed? exported? both break the sealed-construction invariant)", name,
		))
	} else if valueField.PkgPath == "" {
		violations = append(violations, fmt.Sprintf(
			"%s.value is exported (PkgPath empty); outside-package literal construction becomes possible — re-seal by lowercasing",
			name,
		))
	}
	return violations
}

// ─── ERRCODE-PREFIX-OWNERSHIP-01 helpers (non-test, no bare literals) ────────

// isRuntimeAssembledCodeArg reports whether a code argument is runtime-assembled.
func isRuntimeAssembledCodeArg(expr ast.Expr) bool {
	if _, ok := scanner.FindFirstInSubtree[ast.CallExpr](expr, func(*ast.CallExpr) bool { return true }); ok {
		return true
	}
	_, ok := scanner.FindFirstInSubtree[ast.BinaryExpr](expr, func(*ast.BinaryExpr) bool { return true })
	return ok
}

// scanErrcodePrefixOwnershipDiags is the unit-testable core of the
// ERRCODE-PREFIX-OWNERSHIP-01 scanner.
func scanErrcodePrefixOwnershipDiags(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	scanTargetA bool,
) (diags []Diagnostic, seen []string) {
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !scanTargetA {
			return
		}
		callee, ok := resolveCodeGatedCallee(call, info)
		if !ok {
			return
		}
		if len(call.Args) <= callee.codeArgIndex {
			return
		}
		codeArg := call.Args[callee.codeArgIndex]

		if info != nil {
			codeStr, constOK := EvaluateConstString(info, codeArg)
			if !constOK {
				if isRuntimeAssembledCodeArg(codeArg) {
					line := fset.Position(call.Pos()).Line
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: line,
						Message: fmt.Sprintf(
							"%s code arg is a runtime-assembled Code value — "+
								"constructing Code via type-conversion or string concatenation "+
								"breaks the closed-set invariant "+
								"(ERRCODE-PREFIX-OWNERSHIP-01); use a named sentinel from pkg/errcode",
							callee.displayName,
						),
					})
				}
				return
			}
			seen = append(seen, codeStr)
			if _, owned := errcode.OwnerOfCode(errcode.Code(codeStr)); !owned {
				line := fset.Position(call.Pos()).Line
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf(
						"%s code %q prefix not registered "+
							errcodeRegisterPrefixHint,
						callee.displayName, codeStr,
					),
				})
			}
			return
		}

		if isRuntimeAssembledCodeArg(codeArg) {
			line := fset.Position(call.Pos()).Line
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"%s code arg is a runtime-assembled Code value — "+
						"constructing Code via type-conversion or string concatenation "+
						"breaks the closed-set invariant "+
						"(ERRCODE-PREFIX-OWNERSHIP-01); use a named sentinel from pkg/errcode",
					callee.displayName,
				),
			})
			return
		}
		lit, ok := codeArg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		codeStr := strings.Trim(lit.Value, `"`)
		seen = append(seen, codeStr)
		if _, owned := errcode.OwnerOfCode(errcode.Code(codeStr)); !owned {
			line := fset.Position(call.Pos()).Line
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"%s code %q prefix not registered "+
						errcodeRegisterPrefixHint,
					callee.displayName, codeStr,
				),
			})
		}
	})

	EachInSubtree[ast.GenDecl](file, func(gen *ast.GenDecl) {
		if gen.Tok != token.CONST && gen.Tok != token.VAR {
			return
		}
		EachInChildren[ast.ValueSpec](gen, func(vs *ast.ValueSpec) {
			for i, name := range vs.Names {
				if !isExportedErrSentinelName(name.Name) {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				codeStr := strings.Trim(lit.Value, `"`)
				if !strings.HasPrefix(codeStr, "ERR_") {
					continue
				}
				seen = append(seen, codeStr)
				if _, owned := errcode.OwnerOfCode(errcode.Code(codeStr)); !owned {
					pos := fset.Position(name.Pos())
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"%s = %q prefix not registered "+
								errcodeRegisterPrefixHint,
							name.Name, codeStr,
						),
					})
				}
			}
		})
	})
	return diags, seen
}
