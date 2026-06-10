package archtest

// serviceowned_handler_owner_check.go — importable
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 rule logic (#1638 M3 PR-8a).
//
// Non-test home so external Cell repos can import and run this rule; Go
// never compiles a dependency's _test.go. GoCell's own Test* in
// serviceowned_handler_owner_check_test.go dogfoods the same Check*
// (single source, no parallel rule body).
//
// # SERVICEOWNED-HANDLER-OWNER-CHECK-01
//
// Every contract.yaml HTTP endpoint with auth.serviceOwned=true must
// perform its service-layer ownership check exclusively through the
// runtime/auth.CheckOwner typed funnel, never via inline errcode.New
// with KindNotFound.
//
// Three Hard sub-predicates:
//
//   - B1 downstream handler-adapter-bound reachability lock: each
//     serviceOwned slice's service.go must invoke runtime/auth.CheckOwner
//     transitively from a FuncDecl whose name appears in the
//     handler-adapter-resolved entry set.
//
//   - B2 funnel body lock: runtime/auth/owner_guard.go::CheckOwner body
//     must contain exactly one errcode.New(KindNotFound,...) call and the
//     guard IF condition must be a call to the same-package
//     ownershipMismatch helper.
//
//   - B3 upstream zero-tolerance ban: service.go files in serviceOwned
//     slices must contain zero errcode.New/Wrap(KindNotFound,...) callsites.
//
// # AI-robust grade
//
// B1/B2/B3 are all Hard. See _test.go godoc for full blindspot inventory.
//
// # Not registered (register=none)
//
// Not registered in StandardCellRules: gocell-internal-layout funnel;
// vacuous-green/false-red in an external module (depends on
// cells/*/slices/*/service.go layout + runtime/auth/owner_guard.go +
// contract.yaml auth.serviceOwned). Enforced in GoCell via
// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

const (
	// serviceOwnedErrcodePkg is the canonical import path of the errcode package.
	// Derived from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01).
	serviceOwnedErrcodePkg = PlatformModulePath + "/pkg/errcode"
	// serviceOwnedKindNotFoundSym is the symbol name within errcodePkg that
	// constitutes the IDOR-safe 404-collapse error kind.
	serviceOwnedKindNotFoundSym = "KindNotFound"
	// serviceOwnedAuthPkg is the canonical import path of the runtime/auth
	// package where the CheckOwner funnel lives.
	// Derived from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01).
	serviceOwnedAuthPkg = PlatformModulePath + "/runtime/auth"
	// serviceOwnedCheckOwnerSym is the function name that constitutes the
	// sole sanctioned ownership-check funnel.
	serviceOwnedCheckOwnerSym = "CheckOwner"
	// serviceOwnedMismatchHelperSym is the unexported same-package helper
	// that encapsulates the "what counts as owner mismatch" predicate.
	serviceOwnedMismatchHelperSym = "ownershipMismatch"
	// serviceOwnedOwnerGuardFile is the production funnel implementation
	// file. B2 asserts exactly this file contains the canonical funnel.
	serviceOwnedOwnerGuardFile = "runtime/auth/owner_guard.go"
)

// CheckServiceownedHandlerOwnerCheck01 runs B1+B2+B3 predicates of
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 against the running module and
// returns diagnostics. It does NOT call t.Errorf; the caller should
// funnel results through
// Report(t, "SERVICEOWNED-HANDLER-OWNER-CHECK-01", ...).
//
// Not registered in StandardCellRules: gocell-internal-layout funnel;
// vacuous-green/false-red in an external module; enforced in GoCell via
// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01.
func CheckServiceownedHandlerOwnerCheck01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("SERVICEOWNED-HANDLER-OWNER-CHECK-01: metadata.NewParser: %v", err)
	}

	serviceOwnedContracts := collectServiceOwnedContracts(project)

	var targets []serviceOwnedTarget
	var missingFileDiags []Diagnostic

	for contractID, servingSlices := range serviceOwnedContracts {
		for _, sl := range servingSlices {
			// Derive service.go as a sibling of the slice's own slice.yaml, so the
			// path is correct for the corecells platform module (corecells/<cell>/
			// slices/<slice>/) and example cells alike — never a hardcoded cells/ root.
			rel := path.Dir(filepath.ToSlash(sl.File)) + "/service.go"
			absPath := filepath.Join(root, rel)
			if _, statErr := os.Stat(absPath); os.IsNotExist(statErr) {
				missingFileDiags = append(missingFileDiags, Diagnostic{
					Rel: rel, Line: 0,
					Message: fmt.Sprintf(
						"contract %q (auth.serviceOwned=true) serves slice %q but %s not found",
						contractID, sl.ID, rel,
					),
				})
				continue
			}
			targets = append(targets, serviceOwnedTarget{
				rel: rel, abs: absPath,
				contractID: contractID, sliceID: sl.ID,
			})
		}
	}

	var allDiags []Diagnostic
	allDiags = append(allDiags, missingFileDiags...)

	seenRels := map[string]bool{}
	scanDiags := Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		[]string{"./corecells/...", "./runtime/auth/..."}),
		func(pass *Pass) []Diagnostic {
			return serviceOwnedScanPass(pass, targets, seenRels)
		})

	allDiags = append(allDiags, scanDiags...)
	allDiags = append(allDiags, serviceOwnedCrossCheckTargets(targets, seenRels)...)

	return allDiags
}

// serviceOwnedScanPass is the per-pass callback for one typed package load.
func serviceOwnedScanPass(pass *Pass, targets []serviceOwnedTarget, seenRels map[string]bool) []Diagnostic {
	if !pass.Typed() {
		return nil
	}
	var d []Diagnostic
	for _, file := range pass.Files {
		rel := pass.Rel(file)
		seenRels[rel] = true
		if rel == serviceOwnedOwnerGuardFile {
			d = append(d, checkFunnelBody(pass, file, rel)...)
		}
	}
	for i := range targets {
		d = append(d, serviceOwnedScanTarget(pass, &targets[i], seenRels)...)
	}
	return d
}

// serviceOwnedTarget is a local alias to keep the closure param readable.
type serviceOwnedTarget struct {
	rel        string
	abs        string
	contractID string
	sliceID    string
}

// serviceOwnedScanTarget checks one (service.go, contract) target within a
// typed pass, matching files by slice directory.
func serviceOwnedScanTarget(pass *Pass, target *serviceOwnedTarget, seenRels map[string]bool) []Diagnostic {
	sliceDir := path.Dir(target.rel)
	var serviceFile *ast.File
	var siblings []*ast.File
	for _, f := range pass.Files {
		rel := pass.Rel(f)
		if path.Dir(rel) != sliceDir {
			continue
		}
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if rel == target.rel {
			serviceFile = f
		} else {
			siblings = append(siblings, f)
		}
	}
	if serviceFile == nil {
		return nil
	}
	seenRels[target.rel] = true
	entries := resolveHandlerAdapterEntries(pass, siblings)
	var d []Diagnostic
	d = append(d, checkServiceFileCalleeLock(pass, serviceFile, target.rel, target.contractID, entries)...)
	d = append(d, checkServiceFileZeroToleranceBan(pass, serviceFile, target.rel, target.contractID)...)
	return d
}

// serviceOwnedCrossCheckTargets emits diagnostics for targets whose service.go
// was never seen by the typed scan (build-tag exclusion) or when owner_guard.go
// was not loaded.
func serviceOwnedCrossCheckTargets(targets []serviceOwnedTarget, seenRels map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for i := range targets {
		if !seenRels[targets[i].rel] {
			diags = append(diags, Diagnostic{
				Rel: targets[i].rel, Line: 0,
				Message: fmt.Sprintf(
					"contract %q: %s not loaded by typeseval "+
						"(build tag excluded?); B1/B3 check skipped",
					targets[i].contractID, targets[i].rel,
				),
			})
		}
	}
	if !seenRels[serviceOwnedOwnerGuardFile] {
		diags = append(diags, Diagnostic{
			Rel: serviceOwnedOwnerGuardFile, Line: 0,
			Message: fmt.Sprintf(
				"%s not loaded by typeseval; B2 funnel body check skipped",
				serviceOwnedOwnerGuardFile,
			),
		})
	}
	return diags
}

// collectServiceOwnedContracts returns a map from contract ID to the list of
// SliceMeta entries that serve that contract (contractUsages role="serve").
func collectServiceOwnedContracts(project *metadata.ProjectMeta) map[string][]*metadata.SliceMeta {
	result := map[string][]*metadata.SliceMeta{}
	for contractID, contract := range project.Contracts {
		if !isServiceOwnedHTTPContract(contract) {
			continue
		}
		appendServingSlices(result, contractID, project.Slices)
	}
	return result
}

// isServiceOwnedHTTPContract reports whether a contract is an HTTP endpoint
// with auth.serviceOwned=true.
func isServiceOwnedHTTPContract(contract *metadata.ContractMeta) bool {
	if contract.Kind != "http" || contract.Endpoints.HTTP == nil {
		return false
	}
	return contract.Endpoints.HTTP.Auth.ServiceOwned
}

// appendServingSlices adds any slice that serves contractID to result.
func appendServingSlices(result map[string][]*metadata.SliceMeta, contractID string, slices map[string]*metadata.SliceMeta) {
	for _, sl := range slices {
		if sliceServesContract(sl, contractID) {
			result[contractID] = append(result[contractID], sl)
		}
	}
}

// sliceServesContract reports whether sl has a contractUsage with role="serve"
// matching contractID.
func sliceServesContract(sl *metadata.SliceMeta, contractID string) bool {
	for _, usage := range sl.ContractUsages {
		if usage.Contract == contractID && usage.Role == "serve" {
			return true
		}
	}
	return false
}

// checkServiceFileCalleeLock (B1) reports a diagnostic if no exported
// FuncDecl in file that is in the handler-adapter-resolved entry set can
// transitively reach a call to runtime/auth.CheckOwner via intra-file
// function/method invocations.
func checkServiceFileCalleeLock(pass *Pass, file *ast.File, rel, contractID string, entries map[string]bool) []Diagnostic {
	if len(entries) == 0 {
		return []Diagnostic{{
			Rel: rel, Line: 0,
			Message: fmt.Sprintf(
				"contract %q (auth.serviceOwned=true) serving slice in %s "+
					"has no handler.go adapter file calling a Service "+
					"method — B1 reachability cannot be validated without "+
					"an adapter binding to identify the contract-mapped "+
					"entry method. Canonical structure: handler.go's "+
					"Adapter.<HTTPMethod>(...) calls a.S.<ServiceMethod>(...) "+
					"which (transitively) calls auth.CheckOwner.",
				contractID, rel,
			),
		}}
	}
	if fileCallsAuthCheckOwnerFromEntries(pass.TypesInfo, file, entries) {
		return nil
	}
	return []Diagnostic{{
		Rel: rel, Line: 0,
		Message: fmt.Sprintf(
			"contract %q (auth.serviceOwned=true) serving slice in %s "+
				"is missing an auth.CheckOwner call reachable from the "+
				"handler-adapter-resolved entry set (%v). Service-layer "+
				"ownership checks must go through the CheckOwner funnel "+
				"(see runtime/auth/owner_guard.go). Canonical form: "+
				"corecells/accesscore/slices/sessionlogout/service.go "+
				"(Service.Logout calls Service.revokeAndPublish which "+
				"calls auth.CheckOwner) — dead code at file scope and "+
				"unreachable helpers do not satisfy reachability. "+
				"B3 separately enforces zero raw "+
				"errcode.New/Wrap(errcode.KindNotFound, ...) in this file.",
			contractID, rel, sortedKeys(entries),
		),
	}}
}

// resolveHandlerAdapterEntries scans sibling files (typically handler.go)
// for CallExpr where the receiver's static type is the local "Service"
// struct, and returns the set of method names invoked.
func resolveHandlerAdapterEntries(pass *Pass, siblings []*ast.File) map[string]bool {
	entries := map[string]bool{}
	if pass.Pkg == nil {
		return entries
	}
	serviceObj := pass.Pkg.Scope().Lookup("Service")
	if serviceObj == nil {
		return entries
	}
	serviceType := serviceObj.Type()
	ptrToService := types.NewPointer(serviceType)
	for _, file := range siblings {
		collectServiceCallEntries(pass, file, serviceType, ptrToService, entries)
	}
	return entries
}

// collectServiceCallEntries walks file and adds to entries every Service
// method name called by a receiver of type serviceType or *serviceType.
func collectServiceCallEntries(pass *Pass, file *ast.File, serviceType, ptrToService types.Type, entries map[string]bool) {
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		recvType := pass.TypesInfo.TypeOf(sel.X)
		if recvType == nil {
			return
		}
		if types.Identical(recvType, serviceType) || types.Identical(recvType, ptrToService) {
			entries[sel.Sel.Name] = true
		}
	})
}

// sortedKeys returns map keys in deterministic order for diagnostic formatting.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// checkServiceFileZeroToleranceBan (B3) reports a diagnostic for every
// errcode.New(errcode.KindNotFound, ...) call in file. Zero is the only
// passing count.
func checkServiceFileZeroToleranceBan(pass *Pass, file *ast.File, rel, contractID string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isErrCodeNewOrWrapCall(pass.TypesInfo, call) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		if !isKindNotFoundArg(pass, call.Args[0]) {
			return
		}
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: pass.Fset.Position(call.Pos()).Line,
			Message: fmt.Sprintf(
				"contract %q (auth.serviceOwned=true): %s contains a raw "+
					"errcode.New / errcode.Wrap with errcode.KindNotFound. "+
					"All KindNotFound returns in serviceOwned service.go "+
					"must come from the runtime/auth.CheckOwner funnel "+
					"(zero-tolerance ban — IDOR collapse depends on a "+
					"single sanctioned funnel body). Fix: delete this "+
					"call and let the nil-safe accessor (returns \"\" on "+
					"nil/not-found resource) collapse lookup-failure into "+
					"the funnel. See testdata fixtures green/ (correct form) "+
					"and red_raw_kindnotfound_in_service/ + "+
					"red_b3_wrap_kindnotfound/ (anti-patterns).",
				contractID, rel,
			),
		})
	})
	return diags
}

// checkFunnelBody (B2) reports diagnostics on the runtime/auth/owner_guard.go
// CheckOwner function body if it deviates from the canonical form.
func checkFunnelBody(pass *Pass, file *ast.File, rel string) []Diagnostic {
	fn := findCheckOwnerFunc(file)
	if fn == nil {
		return []Diagnostic{{
			Rel: rel, Line: 0,
			Message: fmt.Sprintf(
				"%s does not declare a top-level CheckOwner function — "+
					"funnel implementation moved or renamed.",
				rel,
			),
		}}
	}
	return checkFunnelBodyContents(pass, fn, file, rel)
}

// findCheckOwnerFunc locates the top-level (non-method) CheckOwner FuncDecl
// in file, or returns nil.
func findCheckOwnerFunc(file *ast.File) *ast.FuncDecl {
	var fn *ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fn != nil {
			return
		}
		if fd.Name == nil || fd.Name.Name != serviceOwnedCheckOwnerSym {
			return
		}
		if fd.Recv != nil {
			return
		}
		fn = fd
	})
	return fn
}

// checkFunnelBodyContents validates the body of the CheckOwner funnel function.
func checkFunnelBodyContents(pass *Pass, fn *ast.FuncDecl, file *ast.File, rel string) []Diagnostic {
	notFoundCalls, otherCalls := countFunnelErrcodeCalls(pass, fn, rel)

	var diags []Diagnostic
	diags = append(diags, otherCalls...)
	if notFoundCalls != 1 {
		diags = append(diags, Diagnostic{
			Rel: rel, Line: pass.Fset.Position(fn.Pos()).Line,
			Message: fmt.Sprintf(
				"%s CheckOwner body must contain exactly one "+
					"errcode.New(errcode.KindNotFound, ...) call; found %d. "+
					"Multiple KindNotFound calls obscure the sole sanctioned "+
					"funnel exit and weaken Hard-form uniqueness.",
				rel, notFoundCalls,
			),
		})
	}
	diags = append(diags, checkFunnelReturnForms(pass, fn, rel)...)
	diags = append(diags, checkFunnelGuardCondition(pass, fn, file, rel)...)
	diags = append(diags, checkOwnershipMismatchBody(pass, file, rel)...)
	return diags
}

// countFunnelErrcodeCalls walks the CheckOwner body and counts
// errcode.New(KindNotFound,...) calls (returned as notFoundCalls) and
// errcode.New with other kinds (returned as otherCalls diagnostics).
func countFunnelErrcodeCalls(pass *Pass, fn *ast.FuncDecl, rel string) (int, []Diagnostic) {
	var notFoundCalls int
	var otherCalls []Diagnostic
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		if !isErrCodeNewCall(pass.TypesInfo, call) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		if isKindNotFoundArg(pass, call.Args[0]) {
			notFoundCalls++
			return
		}
		otherCalls = append(otherCalls, Diagnostic{
			Rel:  rel,
			Line: pass.Fset.Position(call.Pos()).Line,
			Message: fmt.Sprintf(
				"%s CheckOwner body contains errcode.New with a "+
					"non-KindNotFound first argument — funnel has "+
					"drifted from IDOR-safe 404 collapse.",
				rel,
			),
		})
	})
	return notFoundCalls, otherCalls
}

// checkFunnelGuardCondition (B2 supplement) finds the IF statement in
// CheckOwner whose body returns the canonical errcode.New(KindNotFound,...)
// and asserts its condition expression is exactly a same-package call to
// ownershipMismatch.
func checkFunnelGuardCondition(pass *Pass, fn *ast.FuncDecl, file *ast.File, rel string) []Diagnostic {
	_ = file
	var diags []Diagnostic
	guardCount := 0
	EachInChildren[ast.IfStmt](fn.Body, func(ifStmt *ast.IfStmt) {
		if !ifBodyReturnsCanonicalNotFound(pass, ifStmt.Body) {
			return
		}
		guardCount++
		if !isOwnershipMismatchCall(pass, ifStmt.Cond) {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pass.Fset.Position(ifStmt.Cond.Pos()).Line,
				Message: fmt.Sprintf(
					"%s CheckOwner guard IF condition is not a call to the "+
						"same-package ownershipMismatch helper. The IDOR-collapse "+
						"predicate must be funneled through ownershipMismatch so "+
						"future evolution of the mismatch semantics lives in one "+
						"place; inline predicates are rejected (operator-flip "+
						"bugs caught at the helper level).",
					rel,
				),
			})
		}
	})
	if guardCount == 0 {
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: pass.Fset.Position(fn.Pos()).Line,
			Message: fmt.Sprintf(
				"%s CheckOwner has no IF statement whose body returns "+
					"errcode.New(errcode.KindNotFound, ...) — the canonical "+
					"guard structure is missing.",
				rel,
			),
		})
	}
	return diags
}

// ifBodyReturnsCanonicalNotFound reports whether the IF body contains a
// ReturnStmt with at least one errcode.New call whose first argument
// resolves to errcode.KindNotFound.
func ifBodyReturnsCanonicalNotFound(pass *Pass, body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	_, match := FindFirstInSubtree[ast.ReturnStmt](body, func(ret *ast.ReturnStmt) bool {
		_, hit := FindFirstChild[ast.CallExpr](ret, func(call *ast.CallExpr) bool {
			if !isErrCodeNewCall(pass.TypesInfo, call) {
				return false
			}
			if len(call.Args) == 0 {
				return false
			}
			return isKindNotFoundArg(pass, call.Args[0])
		})
		return hit
	})
	return match
}

// isOwnershipMismatchCall reports whether cond is a CallExpr whose callee
// resolves via go/types to the same-package ownershipMismatch helper.
func isOwnershipMismatchCall(pass *Pass, cond ast.Expr) bool {
	call, ok := cond.(*ast.CallExpr)
	if !ok {
		return false
	}
	ref := unwrapCalleeForResolve(call.Fun)
	if ref == nil {
		return false
	}
	name := calleeIdentName(ref)
	if name != serviceOwnedMismatchHelperSym {
		return false
	}
	var id *ast.Ident
	switch v := ref.(type) {
	case *ast.Ident:
		id = v
	case *ast.SelectorExpr:
		id = v.Sel
	default:
		return false
	}
	if id == nil {
		return false
	}
	obj := pass.TypesInfo.ObjectOf(id)
	if obj == nil {
		return false
	}
	if obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == pass.Pkg.Path()
}

// checkOwnershipMismatchBody (B2b) locks the ownershipMismatch helper body
// to the canonical AST form.
func checkOwnershipMismatchBody(pass *Pass, file *ast.File, rel string) []Diagnostic {
	helper := findOwnershipMismatchHelper(file)
	if helper == nil {
		return []Diagnostic{{
			Rel: rel, Line: 0,
			Message: fmt.Sprintf(
				"%s missing %s helper — CheckOwner's IF condition lock "+
					"requires a same-package ownershipMismatch[T] function "+
					"encapsulating the mismatch predicate.",
				rel, serviceOwnedMismatchHelperSym,
			),
		}}
	}
	return checkMismatchHelperShape(pass, helper, rel)
}

// findOwnershipMismatchHelper locates the ownershipMismatch FuncDecl in file.
func findOwnershipMismatchHelper(file *ast.File) *ast.FuncDecl {
	var helper *ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name != nil && fd.Name.Name == serviceOwnedMismatchHelperSym {
			helper = fd
		}
	})
	return helper
}

// checkMismatchHelperShape validates the parameter count and body shape of the
// ownershipMismatch helper.
func checkMismatchHelperShape(pass *Pass, helper *ast.FuncDecl, rel string) []Diagnostic {
	params := flattenParamIdents(helper.Type.Params)
	if len(params) != 3 {
		return []Diagnostic{{
			Rel: rel, Line: pass.Fset.Position(helper.Pos()).Line,
			Message: fmt.Sprintf(
				"%s ownershipMismatch must have exactly 3 parameters "+
					"(resource T, ownerID func(T) string, callerID string); "+
					"found %d.",
				rel, len(params),
			),
		}}
	}
	resourceP, ownerIDP, callerIDP := params[0], params[1], params[2]

	if helper.Body == nil || len(helper.Body.List) != 1 {
		stmtCount := 0
		if helper.Body != nil {
			stmtCount = len(helper.Body.List)
		}
		return []Diagnostic{{
			Rel: rel, Line: pass.Fset.Position(helper.Pos()).Line,
			Message: fmt.Sprintf(
				"%s ownershipMismatch body must be exactly one return "+
					"statement; got %d statements.",
				rel, stmtCount,
			),
		}}
	}
	return checkMismatchReturnExpr(pass, helper, rel, resourceP, ownerIDP, callerIDP)
}

// checkMismatchReturnExpr validates the single return expression of the
// ownershipMismatch helper.
func checkMismatchReturnExpr(pass *Pass, helper *ast.FuncDecl, rel, resourceP, ownerIDP, callerIDP string) []Diagnostic {
	ret, ok := helper.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return []Diagnostic{{
			Rel: rel, Line: pass.Fset.Position(helper.Body.List[0].Pos()).Line,
			Message: fmt.Sprintf(
				"%s ownershipMismatch body must be a single ReturnStmt with "+
					"one expression.", rel,
			),
		}}
	}
	if !isCanonicalMismatchExpr(ret.Results[0], resourceP, ownerIDP, callerIDP) {
		return []Diagnostic{{
			Rel: rel, Line: pass.Fset.Position(ret.Results[0].Pos()).Line,
			Message: fmt.Sprintf(
				"%s ownershipMismatch return expression is not the canonical "+
					"form `callerID == \"\" || ownerID(resource) != callerID`. "+
					"Operator inversion or operand reordering would silently "+
					"flip IDOR semantics; B2b form-lock rejects all deviations.",
				rel,
			),
		}}
	}
	return nil
}

// isCanonicalMismatchExpr verifies expr is exactly:
//
//	callerID == "" || ownerID(resource) != callerID
//
// with operand identities equal to the supplied param names.
func isCanonicalMismatchExpr(expr ast.Expr, resourceP, ownerIDP, callerIDP string) bool {
	lor, ok := expr.(*ast.BinaryExpr)
	if !ok || lor.Op.String() != "||" {
		return false
	}
	emptyCheck, ok := lor.X.(*ast.BinaryExpr)
	if !ok || emptyCheck.Op.String() != "==" {
		return false
	}
	if !isIdentNamed(emptyCheck.X, callerIDP) {
		return false
	}
	lit, ok := emptyCheck.Y.(*ast.BasicLit)
	if !ok || lit.Value != `""` {
		return false
	}
	mismatch, ok := lor.Y.(*ast.BinaryExpr)
	if !ok || mismatch.Op.String() != "!=" {
		return false
	}
	call, ok := mismatch.X.(*ast.CallExpr)
	if !ok || !isIdentNamed(call.Fun, ownerIDP) {
		return false
	}
	if len(call.Args) != 1 || !isIdentNamed(call.Args[0], resourceP) {
		return false
	}
	if !isIdentNamed(mismatch.Y, callerIDP) {
		return false
	}
	return true
}

// flattenParamIdents collects the parameter Idents from a FuncDecl param
// FieldList into a positional slice.
func flattenParamIdents(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var names []string
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

// checkFunnelReturnForms (B2 supplement) reports a diagnostic for every
// non-nil return value in the CheckOwner body that is NOT the canonical
// errcode.New(errcode.KindNotFound, _, _) call.
func checkFunnelReturnForms(pass *Pass, fn *ast.FuncDecl, rel string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.ReturnStmt](fn.Body, func(ret *ast.ReturnStmt) {
		diags = append(diags, checkReturnStmtForms(pass, ret, rel)...)
	})
	return diags
}

// checkReturnStmtForms validates one ReturnStmt in the CheckOwner body.
func checkReturnStmtForms(pass *Pass, ret *ast.ReturnStmt, rel string) []Diagnostic {
	var diags []Diagnostic
	nilCount := 0
	callExprCount := 0

	EachInChildren[ast.Ident](ret, func(id *ast.Ident) {
		if id.Name == "nil" {
			nilCount++
		}
	})

	EachInChildren[ast.CallExpr](ret, func(call *ast.CallExpr) {
		callExprCount++
		line := pass.Fset.Position(call.Pos()).Line
		if !isErrCodeNewCall(pass.TypesInfo, call) {
			diags = append(diags, Diagnostic{
				Rel: rel, Line: line,
				Message: fmt.Sprintf(
					"%s CheckOwner has a non-nil return constructing an "+
						"error via a call other than errcode.New — every "+
						"non-nil exit must be the canonical "+
						"errcode.New(errcode.KindNotFound, ...) call.",
					rel,
				),
			})
			return
		}
		if len(call.Args) == 0 || !isKindNotFoundArg(pass, call.Args[0]) {
			return
		}
	})

	otherCount := len(ret.Results) - nilCount - callExprCount
	if otherCount > 0 {
		line := pass.Fset.Position(ret.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel: rel, Line: line,
			Message: fmt.Sprintf(
				"%s CheckOwner has %d non-nil return value(s) that are "+
					"not call expressions — every non-nil exit must be "+
					"the canonical errcode.New(errcode.KindNotFound, "+
					"...) call (IDOR collapse uniqueness).",
				rel, otherCount,
			),
		})
	}
	return diags
}

// isErrCodeNewCall reports whether call resolves via go/types to errcode.New.
func isErrCodeNewCall(typesInfo *types.Info, call *ast.CallExpr) bool {
	return IsCallToPkgFunc(typesInfo, call, serviceOwnedErrcodePkg, "New")
}

// isErrCodeNewOrWrapCall reports whether call resolves via go/types to
// errcode.New OR errcode.Wrap from pkg/errcode.
func isErrCodeNewOrWrapCall(typesInfo *types.Info, call *ast.CallExpr) bool {
	return IsCallToPkgFunc(typesInfo, call, serviceOwnedErrcodePkg, "New") ||
		IsCallToPkgFunc(typesInfo, call, serviceOwnedErrcodePkg, "Wrap")
}

// isKindNotFoundArg reports whether arg evaluates to errcode.KindNotFound,
// covering constant folding and package-ref resolution.
func isKindNotFoundArg(pass *Pass, arg ast.Expr) bool {
	if tv, ok := pass.TypesInfo.Types[arg]; ok && tv.Value != nil {
		if expected := lookupErrcodeKindNotFoundValue(pass); expected != nil {
			if constant.Compare(tv.Value, token.EQL, expected) {
				return true
			}
		}
	}
	pkgPath, name, ok := ResolvePackageRef(pass.TypesInfo, arg)
	if !ok {
		return false
	}
	return pkgPath == serviceOwnedErrcodePkg && name == serviceOwnedKindNotFoundSym
}

// lookupErrcodeKindNotFoundValue returns the constant.Value of
// errcode.KindNotFound by walking pass.Pkg's direct imports.
func lookupErrcodeKindNotFoundValue(pass *Pass) constant.Value {
	if pass.Pkg == nil {
		return nil
	}
	for _, imp := range pass.Pkg.Imports() {
		if imp.Path() != serviceOwnedErrcodePkg {
			continue
		}
		obj := imp.Scope().Lookup(serviceOwnedKindNotFoundSym)
		if c, ok := obj.(*types.Const); ok {
			return c.Val()
		}
	}
	return nil
}

// fileCallsAuthCheckOwnerFromEntries reports whether file contains a
// reachable call to runtime/auth.CheckOwner starting from FuncDecls whose
// names appear in entries.
func fileCallsAuthCheckOwnerFromEntries(typesInfo *types.Info, file *ast.File, entries map[string]bool) bool {
	allFuncs, entry := buildFuncIndex(file, entries)
	if len(entry) == 0 {
		return false
	}
	return bfsForAuthCheckOwner(typesInfo, allFuncs, entry)
}

// buildFuncIndex indexes all FuncDecls in file by name and collects the
// entry subset (those whose names appear in entries).
func buildFuncIndex(file *ast.File, entries map[string]bool) (map[string]*ast.FuncDecl, []*ast.FuncDecl) {
	allFuncs := map[string]*ast.FuncDecl{}
	var entry []*ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil {
			return
		}
		allFuncs[fd.Name.Name] = fd
		if entries[fd.Name.Name] {
			entry = append(entry, fd)
		}
	})
	return allFuncs, entry
}

// bfsForAuthCheckOwner performs BFS from entry FuncDecls, following intra-file
// call edges, and returns true if auth.CheckOwner is reachable.
func bfsForAuthCheckOwner(typesInfo *types.Info, allFuncs map[string]*ast.FuncDecl, entry []*ast.FuncDecl) bool {
	visited := map[string]bool{}
	queue := append([]*ast.FuncDecl{}, entry...)
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		if fn.Name == nil || visited[fn.Name.Name] || fn.Body == nil {
			continue
		}
		visited[fn.Name.Name] = true
		if found, next := scanFuncBodyForCheckOwner(typesInfo, fn, allFuncs, visited); found {
			return true
		} else {
			queue = append(queue, next...)
		}
	}
	return false
}

// scanFuncBodyForCheckOwner inspects one FuncDecl body for CheckOwner calls
// and returns (true, nil) if found, or (false, nextFuncs) to expand BFS.
func scanFuncBodyForCheckOwner(
	typesInfo *types.Info,
	fn *ast.FuncDecl,
	allFuncs map[string]*ast.FuncDecl,
	visited map[string]bool,
) (bool, []*ast.FuncDecl) {
	var next []*ast.FuncDecl
	authCheckFound := false
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		ref := unwrapCalleeForResolve(call.Fun)
		if ref == nil {
			return
		}
		if pkgPath, name, ok := ResolvePackageRef(typesInfo, ref); ok {
			if pkgPath == serviceOwnedAuthPkg && name == serviceOwnedCheckOwnerSym {
				authCheckFound = true
				return
			}
		}
		calleeName := calleeIdentName(ref)
		if calleeName == "" {
			return
		}
		if nextFn, ok := allFuncs[calleeName]; ok && !visited[calleeName] {
			next = append(next, nextFn)
		}
	})
	return authCheckFound, next
}

// calleeIdentName extracts the function/method name from a callee expression.
func calleeIdentName(ref ast.Expr) string {
	switch v := ref.(type) {
	case *ast.SelectorExpr:
		if v.Sel != nil {
			return v.Sel.Name
		}
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// unwrapCalleeForResolve strips ParenExpr, IndexExpr, and IndexListExpr
// wrappers so the underlying SelectorExpr/Ident can be passed to
// ResolvePackageRef.
func unwrapCalleeForResolve(fun ast.Expr) ast.Expr {
	switch v := fun.(type) {
	case *ast.ParenExpr:
		return unwrapCalleeForResolve(v.X)
	case *ast.SelectorExpr, *ast.Ident:
		return v
	case *ast.IndexExpr:
		return unwrapCalleeForResolve(v.X)
	case *ast.IndexListExpr:
		return unwrapCalleeForResolve(v.X)
	}
	return nil
}
