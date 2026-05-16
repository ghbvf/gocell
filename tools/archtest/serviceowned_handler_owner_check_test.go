// INVARIANT: SERVICEOWNED-HANDLER-OWNER-CHECK-01
//
// Package archtest enforces SERVICEOWNED-HANDLER-OWNER-CHECK-01: every
// contract.yaml endpoint with auth.serviceOwned=true must have its serving
// slice service.go contain an owner-guard branch comparing a fetched
// resource owner field against the caller identity and returning
// errcode.New(errcode.KindNotFound, ...). Anchored at SERVICE layer (not
// handler) to preserve the IDOR-safe 404-collapse design.
//
// AI-rebust: Medium (contract-decl ↔ service-guard AST ↔ errcode.KindNotFound
// type-resolved via archtest.ResolvePackageRef, three-factor cross-binding;
// see blindspot inventory for the residual escape). Hard-upgrade tracked in
// docs/backlog/cap-14-tooling.md §14.1 entry
// SERVICEOWNED-HANDLER-OWNER-CHECK-01-HARD-UPGRADE — reviewers follow that
// entry for the funnel-collapse upgrade path (AI-collab charter mandates the
// Medium funnel name its upgrade backlog in-comment).
//
// Detection is type-aware: the first argument of every errcode.New call inside
// an owner-guard IfStmt is resolved through go/types (archtest.ResolvePackageRef,
// which delegates to typeseval.ResolvePackageRef) to confirm it is specifically
// errcode.KindNotFound from "github.com/ghbvf/gocell/pkg/errcode". String-name
// matching (Soft) is replaced by pkgPath+name resolved identity (Medium).
//
// Blindspot inventory (tools: metadata.NewParser + archtest.RunTyped +
// archtest.ResolvePackageRef + scanner.EachInSubtree[ast.IfStmt]):
//
//   - Cross-function wrapping: if the owner check is extracted into a helper
//     `ensureOwnership(sess, caller)` called from service.go, the guard-shaped
//     IfStmt appears in a different scope and this rule will NOT detect it via
//     direct file scan. The guard must appear directly in the scanned file.
//     Distinction: inline closures (func literals assigned or invoked within the
//     same function body, such as the named closure `revokeAndPublish` in
//     sessionlogout/service.go) are detected by EachInSubtree because it
//     recursively visits the full AST subtree including nested FuncLit bodies.
//     Only extraction to a top-level named function (in the same file or a
//     different file) constitutes a cross-function escape that EachInSubtree
//     cannot reach. This is the primary residual escape and the motivation for
//     the Hard-upgrade backlog entry (cross-function callgraph analysis → Hard).
//
//   - Service file location: the rule scans
//     cells/<cellDir>/slices/<sliceDir>/service.go only. If a slice's ownership
//     check lives in a differently-named file, the rule misses it.
//     Mitigation: canonical GoCell slices use service.go.
//
//   - errcode.New call shape: the New selector match is syntactic (name "New").
//     If errcode is dot-imported, the bare identifier `New` is also accepted.
//     Package identity of the KindNotFound argument is fully type-resolved, so
//     import aliasing does NOT evade the Kind check.
//
// Self-check: TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture loads
// three testdata packages with full types.Info via archtest.RunTyped,
// sharing the same ownerGuardCheck rule closure as the production scan.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

const (
	ruleServiceOwnedOwnerCheck01 = "SERVICEOWNED-HANDLER-OWNER-CHECK-01"
	// serviceOwnedErrcodePkg is the canonical import path of the errcode package.
	// Used for type-resolution of KindNotFound via archtest.ResolvePackageRef.
	serviceOwnedErrcodePkg = "github.com/ghbvf/gocell/pkg/errcode"
	// serviceOwnedKindNotFoundSym is the symbol name within errcodePkg that
	// constitutes the IDOR-safe 404-collapse error kind.
	serviceOwnedKindNotFoundSym = "KindNotFound"
)

// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01 enforces that every contract with
// auth.serviceOwned=true has an owner-guard IfStmt in its serving slice's
// service.go returning errcode.New(errcode.KindNotFound, ...).
//
// Current production scope: http.auth.session.delete.v1 → sessionlogout/service.go
// (GREEN — rule reports 0 violations).
//
// Detection is type-aware via archtest.RunTyped + archtest.ResolvePackageRef:
// KindNotFound is confirmed by package path resolution against the go/types graph,
// not by string-name matching. Import aliasing (e.g. `import ec ".../errcode"`)
// is covered by ResolvePackageRef's types.Info lookup.
func TestSERVICEOWNED_HANDLER_OWNER_CHECK_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("%s: metadata.NewParser: %v", ruleServiceOwnedOwnerCheck01, err)
	}

	serviceOwnedContracts := collectServiceOwnedContracts(project)
	if len(serviceOwnedContracts) == 0 {
		return
	}

	// Build set of service.go paths we need to check.
	// key: module-relative slash path; value: absolute path for stat check.
	type target struct {
		rel        string
		abs        string
		contractID string
		sliceID    string
	}
	var targets []target
	var missingFileDiags []Diagnostic

	for contractID, servingSlices := range serviceOwnedContracts {
		for _, sl := range servingSlices {
			rel := filepath.ToSlash(
				filepath.Join("cells", sl.CellDir, "slices", sl.Dir, "service.go"))
			absPath := filepath.Join(root, rel)

			if _, statErr := os.Stat(absPath); os.IsNotExist(statErr) {
				missingFileDiags = append(missingFileDiags, Diagnostic{
					Rel:  rel,
					Line: 0,
					Message: fmt.Sprintf(
						"contract %q (auth.serviceOwned=true) serves slice %q but %s not found",
						contractID, sl.ID, rel),
				})
				continue
			}
			targets = append(targets, target{
				rel: rel, abs: absPath,
				contractID: contractID, sliceID: sl.ID,
			})
		}
	}

	Report(t, ruleServiceOwnedOwnerCheck01, missingFileDiags)

	if len(targets) == 0 {
		return
	}

	// Load cells/ with full type info via archtest.RunTyped.
	// archtest.FlatNonDefaultTags() ensures build-tagged files are included.
	// seenRels accumulates all file paths observed during the RunTyped pass so
	// we can cross-check that every target was actually loaded (i.e. not
	// excluded by a build tag). Accumulated inside the single RunTyped closure
	// to avoid a redundant packages.Load compilation pass.
	seenRels := map[string]bool{}
	diags := RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		[]string{"./cells/..."},
		func(pass *Pass) []Diagnostic {
			if !pass.Typed() {
				return nil
			}
			var d []Diagnostic
			for _, file := range pass.Files {
				rel := pass.Rel(file)
				seenRels[rel] = true
				// Only check service.go files that correspond to serviceOwned targets.
				var matchedTarget *target
				for i := range targets {
					if targets[i].rel == rel {
						matchedTarget = &targets[i]
						break
					}
				}
				if matchedTarget == nil {
					continue
				}
				d = append(d, ownerGuardCheck(pass.TypesInfo, file, rel, matchedTarget.contractID)...)
			}
			return d
		})

	// Cross-check: any target whose service.go was never seen by the RunTyped
	// pass above (e.g. build-tag exclusion) needs an explicit diagnostic.
	for i := range targets {
		if !seenRels[targets[i].rel] {
			diags = append(diags, Diagnostic{
				Rel:  targets[i].rel,
				Line: 0,
				Message: fmt.Sprintf(
					"contract %q: %s not loaded by typeseval "+
						"(build tag excluded?); owner-guard check skipped",
					targets[i].contractID, targets[i].rel),
			})
		}
	}

	Report(t, ruleServiceOwnedOwnerCheck01, diags)
}

// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture verifies the
// type-aware detector fires on RED fixtures and stays silent on GREEN.
//
// Fixtures are loaded via archtest.RunTyped with full types.Info, sharing
// the same ownerGuardCheck rule closure as the production scan.
//
// red_wrong_kind correctness proof: the fixture contains BOTH a valid
// errcode.KindNotFound (in the error-nil path) AND an errcode.KindPermissionDenied
// (in the owner-guard body). A Soft string-name detector would match the first
// KindNotFound and report GREEN. The type-aware detector correctly identifies that
// the owner-guard IfStmt body returns KindPermissionDenied (resolved via
// archtest.ResolvePackageRef to pkg="…/errcode", name="KindPermissionDenied")
// and reports RED. This is the key Medium-vs-Soft differentiator.
//
// Fixture layout (each subdir is an independent Go package in the main module):
//   - green/service.go: owner-guard + KindNotFound → 0 diagnostics
//   - red_missing_guard/service.go: no owner-guard → ≥1 diagnostics
//   - red_wrong_kind/service.go: guard present, KindPermissionDenied → ≥1 diagnostics
func TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture(t *testing.T) {
	t.Parallel()

	fixtureBase := "tools/archtest/testdata/serviceowned_handler_owner_check"

	cases := []struct {
		subdir         string
		wantViolations bool
	}{
		{subdir: "green", wantViolations: false},
		{subdir: "red_missing_guard", wantViolations: true},
		{subdir: "red_wrong_kind", wantViolations: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.subdir, func(t *testing.T) {
			t.Parallel()

			pattern := "./" + fixtureBase + "/" + tc.subdir
			diags := RunTyped(t,
				TypedOpts{Tests: false, Tags: nil},
				[]string{pattern},
				func(pass *Pass) []Diagnostic {
					if !pass.Typed() {
						return nil
					}
					var d []Diagnostic
					for _, file := range pass.Files {
						rel := pass.Rel(file)
						d = append(d, ownerGuardCheck(pass.TypesInfo, file, rel, "fixture-contract")...)
					}
					return d
				})

			if tc.wantViolations && len(diags) == 0 {
				t.Errorf("%s: fixture %q expected ≥1 diagnostic got 0 — type-aware detector broken",
					ruleServiceOwnedOwnerCheck01, tc.subdir)
			}
			if !tc.wantViolations && len(diags) > 0 {
				t.Errorf("%s: fixture %q expected 0 diagnostics got %d:",
					ruleServiceOwnedOwnerCheck01, tc.subdir, len(diags))
				for _, d := range diags {
					t.Errorf("  %s:%d: %s", d.Rel, d.Line, d.Message)
				}
			}
		})
	}
}

// collectServiceOwnedContracts returns a map from contract ID to the list of
// SliceMeta entries that serve that contract (contractUsages role="serve").
func collectServiceOwnedContracts(project *metadata.ProjectMeta) map[string][]*metadata.SliceMeta {
	result := map[string][]*metadata.SliceMeta{}
	for contractID, contract := range project.Contracts {
		if contract.Kind != "http" || contract.Endpoints.HTTP == nil {
			continue
		}
		if !contract.Endpoints.HTTP.Auth.ServiceOwned {
			continue
		}
		for _, sl := range project.Slices {
			for _, usage := range sl.ContractUsages {
				if usage.Contract == contractID && usage.Role == "serve" {
					result[contractID] = append(result[contractID], sl)
					break
				}
			}
		}
	}
	return result
}

// ownerGuardCheck is the shared detection core for both the production scan and
// the fixture self-check. It is passed as the rule closure to archtest.RunTyped.
//
// Returns a diagnostic if file does not contain a qualifying owner-guard IfStmt.
// A qualifying owner-guard is an IfStmt whose:
//  1. Condition is a direct != (NEQ) binary expression where NEITHER operand is
//     the bare identifier "nil" (distinguishes ownership check from err-nil checks).
//  2. Body contains a ReturnStmt calling errcode.New where the first argument
//     type-resolves to errcode.KindNotFound via archtest.ResolvePackageRef
//     (pkgPath == serviceOwnedErrcodePkg, name == serviceOwnedKindNotFoundSym).
//
// Parameters:
//   - typesInfo: pass.TypesInfo for type resolution (must be non-nil for type-aware check)
//   - file: AST file to inspect
//   - rel: slash-relative path (for diagnostic messages)
//   - contractID: the serviceOwned contract ID (for diagnostic messages)
func ownerGuardCheck(typesInfo *types.Info, file *ast.File, rel, contractID string) []Diagnostic {
	if svcFileHasOwnerGuard(typesInfo, file) {
		return nil
	}
	return []Diagnostic{{
		Rel:  rel,
		Line: 0,
		Message: fmt.Sprintf(
			"contract %q (auth.serviceOwned=true) serving slice in %s "+
				"is missing an owner-guard IfStmt returning "+
				"errcode.New(errcode.KindNotFound, ...) on owner-mismatch. "+
				"Returning any other Kind leaks resource existence (IDOR). "+
				"Canonical form: cells/accesscore/slices/sessionlogout/service.go",
			contractID, rel),
	}}
}

// svcFileHasOwnerGuard reports whether file contains at least one qualifying
// owner-guard IfStmt, using type-aware KindNotFound resolution via typesInfo.
func svcFileHasOwnerGuard(typesInfo *types.Info, file *ast.File) bool {
	found := false
	EachInSubtree[ast.IfStmt](file, func(ifStmt *ast.IfStmt) {
		if found {
			return
		}
		if !conditionIsOwnerNEQ(ifStmt.Cond) {
			return
		}
		if bodyHasKindNotFoundReturn(typesInfo, ifStmt.Body) {
			found = true
		}
	})
	return found
}

// conditionIsOwnerNEQ reports whether expr is a top-level BinaryExpr with
// token.NEQ where NEITHER operand is the bare identifier "nil".
//
// This distinguishes owner-guard conditions (`sess.SubjectID != callerUserID`)
// from error-nil checks (`err != nil`) that appear in the same function.
func conditionIsOwnerNEQ(expr ast.Expr) bool {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	return !isNilIdentExpr(bin.X) && !isNilIdentExpr(bin.Y)
}

// isNilIdentExpr reports whether expr is the bare identifier "nil".
func isNilIdentExpr(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}

// bodyHasKindNotFoundReturn reports whether block contains a ReturnStmt whose
// body includes a call to errcode.New with its first argument type-resolved to
// errcode.KindNotFound.
func bodyHasKindNotFoundReturn(typesInfo *types.Info, body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	EachInSubtree[ast.ReturnStmt](body, func(ret *ast.ReturnStmt) {
		if found {
			return
		}
		EachInSubtree[ast.CallExpr](ret, func(call *ast.CallExpr) {
			if found {
				return
			}
			if !isErrCodeNewCall(call) {
				return
			}
			if len(call.Args) == 0 {
				return
			}
			if isKindNotFoundArg(typesInfo, call.Args[0]) {
				found = true
			}
		})
	})
	return found
}

// isErrCodeNewCall reports whether call is shaped like errcode.New(...).
// Accepts SelectorExpr (qualified) and bare Ident (dot-import) forms.
// Package identity is confirmed by isKindNotFoundArg via type resolution.
func isErrCodeNewCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name == "New"
	case *ast.Ident:
		return fn.Name == "New"
	default:
		return false
	}
}

// isKindNotFoundArg reports whether arg resolves via go/types to
// errcode.KindNotFound from "github.com/ghbvf/gocell/pkg/errcode".
//
// Uses archtest.ResolvePackageRef (which delegates to typeseval.ResolvePackageRef)
// for full type resolution. This covers:
//   - Qualified selector `errcode.KindNotFound` (normal import)
//   - Aliased import `ec.KindNotFound` (resolved via types.Info.Uses)
//   - Dot-import bare `KindNotFound` (resolved via types.Info.Uses to *types.Const)
//
// This type-resolution is the Medium-grade factor: the KindNotFound identity
// is bound to the package graph, not to a string token in source.
func isKindNotFoundArg(typesInfo *types.Info, arg ast.Expr) bool {
	pkgPath, name, ok := ResolvePackageRef(typesInfo, arg)
	if !ok {
		return false
	}
	return pkgPath == serviceOwnedErrcodePkg && name == serviceOwnedKindNotFoundSym
}
