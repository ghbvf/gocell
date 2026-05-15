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
// selector name, three-factor cross-binding; cross-function helper wrapping
// is a theoretical form-escape). Hard-upgrade tracked in
// docs/backlog/cap-14-tooling.md §14.1 entry
// SERVICEOWNED-HANDLER-OWNER-CHECK-01-HARD-UPGRADE — reviewers follow that
// entry for the funnel-collapse upgrade path (AI-collab charter mandates the
// Medium funnel name its upgrade backlog in-comment).
//
// Blindspot inventory (tools selected: metadata.NewParser for contract lookup,
// go/parser.ParseFile + EachInSubtree[ast.IfStmt] for guard detection,
// syntactic KindNotFound selector-name matching — no typeseval, see rationale):
//
//   - Cross-function wrapping: if the guard is extracted into a helper
//     `checkOwner(sess, caller)` and that helper is called from the function
//     under test, this rule will NOT detect the guard. The guard-shaped IfStmt
//     must appear directly in service.go. This is the primary escape hatch and
//     the motivation for the Hard-upgrade backlog entry.
//
//   - Service file location: the rule scans
//     cells/<cellDir>/slices/<sliceDir>/service.go. If a slice uses a
//     differently-named file (e.g. logout_service.go), the rule misses it.
//     Mitigation: the sessionlogout slice uses the canonical service.go name,
//     and new serviceOwned slices should follow the same convention.
//
//   - Multiple service files: only the canonical service.go is scanned.
//     If guard logic lives in a separate file, the rule misses it.
//
//   - Import alias / dot-import: the KindNotFound check matches the selector
//     name "KindNotFound" syntactically and the package alias "errcode".
//     Dot-importing errcode (`import . "..."`) would produce a bare Ident "KindNotFound"
//     which is also matched. Aliasing errcode to a different name (e.g. "ec")
//     would evade the check. GoCell code convention prohibits import renaming
//     for well-known framework packages; no production file in cells/ renames
//     errcode.
//
// Rationale for AST-only (no typeseval): the rule is scoped to cells/*/slices/*/service.go
// files that always import errcode under the canonical alias "errcode" per GoCell
// convention. typeseval.SharedResolver would add ~10s load overhead for a rule
// whose only type-sensitive check (KindNotFound vs KindPermissionDenied) is fully
// expressible via the syntactic selector name. The blindspot (import aliasing) is
// accepted and documented; the Hard-upgrade path will address it.
//
// Self-check: TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture exercises
// all three fixture variants to confirm the detector fires on RED cases and
// remains silent on GREEN.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	ruleServiceOwnedOwnerCheck01 = "SERVICEOWNED-HANDLER-OWNER-CHECK-01"
	// serviceOwnedKindNotFoundName is the expected selector name for the
	// IDOR-safe error kind. Using a package-scoped constant avoids the string
	// appearing in multiple places and makes grep-based audits reliable.
	serviceOwnedKindNotFoundName = "KindNotFound"
)

// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01 enforces that every contract with
// auth.serviceOwned=true has an owner-guard branch in its serving slice's
// service.go returning errcode.New(errcode.KindNotFound, ...).
//
// Current production scope: http.auth.session.delete.v1 → sessionlogout/service.go
// (GREEN — rule should report 0 violations).
//
// Detection is AST-based: the rule parses service.go and looks for an IfStmt
// whose condition contains a != (NEQ) binary expression and whose body contains
// a ReturnStmt calling errcode.New with KindNotFound as the first argument.
// The guard must appear directly in service.go (not delegated to a cross-function
// helper — see blindspot inventory above).
func TestSERVICEOWNED_HANDLER_OWNER_CHECK_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("%s: metadata.NewParser: %v", ruleServiceOwnedOwnerCheck01, err)
	}

	// Collect all contracts with auth.serviceOwned=true.
	serviceOwnedContracts := collectServiceOwnedContracts(project)
	if len(serviceOwnedContracts) == 0 {
		// No contracts with serviceOwned — nothing to enforce, vacuously OK.
		return
	}

	var diags []scanner.Diagnostic

	for contractID, servingSlices := range serviceOwnedContracts {
		for _, sl := range servingSlices {
			serviceFile := filepath.Join(root, "cells", sl.CellDir, "slices", sl.Dir, "service.go")
			rel := filepath.ToSlash(filepath.Join("cells", sl.CellDir, "slices", sl.Dir, "service.go"))

			if _, statErr := os.Stat(serviceFile); os.IsNotExist(statErr) {
				diags = append(diags, scanner.Diagnostic{
					Rel:  rel,
					Line: 0,
					Message: fmt.Sprintf(
						"contract %q (auth.serviceOwned=true) serves slice %q but %s does not exist — "+
							"owner-guard cannot be verified",
						contractID, sl.ID, rel),
				})
				continue
			}

			fileDiags := checkServiceFileForOwnerGuard(serviceFile, rel, contractID)
			diags = append(diags, fileDiags...)
		}
	}

	scanner.Report(t, ruleServiceOwnedOwnerCheck01, diags)
}

// TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture verifies the
// detector fires correctly on the three testdata fixtures.
//
// Blindspot self-check: this test exercises all three fixture shapes to ensure
// the detector is not silently broken. If checkServiceFileForOwnerGuard is
// refactored incorrectly, this test will catch it before the production scan
// becomes a silent no-op.
//
// Fixture shapes:
//   - green_service.go: owner-guard present + KindNotFound → 0 diagnostics
//   - red_missing_guard.go: no owner-guard → ≥1 diagnostics
//   - red_wrong_kind.go: guard present but KindPermissionDenied → ≥1 diagnostics
func TestSERVICEOWNED_HANDLER_OWNER_CHECK_01_NegativeFixture(t *testing.T) {
	t.Parallel()

	archDir := findArchTestDir(t)
	fixtureDir := filepath.Join(archDir, "testdata", "serviceowned_handler_owner_check")

	cases := []struct {
		file           string
		wantViolations bool
	}{
		{file: "green_service.go", wantViolations: false},
		{file: "red_missing_guard.go", wantViolations: true},
		{file: "red_wrong_kind.go", wantViolations: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(fixtureDir, tc.file)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Fatalf("fixture missing: %s", path)
			}
			rel := filepath.ToSlash(filepath.Join("tools", "archtest", "testdata",
				"serviceowned_handler_owner_check", tc.file))
			diags := checkServiceFileForOwnerGuard(path, rel, "fixture-contract")
			if tc.wantViolations && len(diags) == 0 {
				t.Errorf("%s: fixture %q should produce ≥1 diagnostic but got 0 — detector is broken",
					ruleServiceOwnedOwnerCheck01, tc.file)
			}
			if !tc.wantViolations && len(diags) > 0 {
				t.Errorf("%s: fixture %q should produce 0 diagnostics but got %d:",
					ruleServiceOwnedOwnerCheck01, tc.file, len(diags))
				for _, d := range diags {
					t.Errorf("  %s:%d: %s", d.Rel, d.Line, d.Message)
				}
			}
		})
	}
}

// collectServiceOwnedContracts returns a map from contract ID to the list of
// SliceMeta entries that serve that contract (role="serve" in contractUsages).
func collectServiceOwnedContracts(project *metadata.ProjectMeta) map[string][]*metadata.SliceMeta {
	result := map[string][]*metadata.SliceMeta{}
	for contractID, contract := range project.Contracts {
		if contract.Kind != "http" {
			continue
		}
		if contract.Endpoints.HTTP == nil {
			continue
		}
		if !contract.Endpoints.HTTP.Auth.ServiceOwned {
			continue
		}
		// Find all slices that serve this contract.
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

// checkServiceFileForOwnerGuard parses the given service.go file and returns
// diagnostics if no owner-guard IfStmt with errcode.KindNotFound is found.
//
// An owner-guard is defined as an IfStmt that:
//  1. Has a condition containing a BinaryExpr with token.NEQ (!=) operator
//  2. Has a body containing at least one ReturnStmt that calls errcode.New
//     with KindNotFound as the first argument
//
// Parameters:
//   - path: absolute filesystem path to the service.go file
//   - rel: slash-separated path relative to repo root (used in diagnostics)
//   - contractID: the serviceOwned contract ID (used in diagnostic messages)
func checkServiceFileForOwnerGuard(path, rel, contractID string) []scanner.Diagnostic {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return []scanner.Diagnostic{{
			Rel:     rel,
			Line:    0,
			Message: fmt.Sprintf("contract %q: parse error in %s: %v", contractID, rel, err),
		}}
	}

	if serviceFileHasOwnerGuard(f) {
		return nil
	}

	return []scanner.Diagnostic{{
		Rel:  rel,
		Line: 0,
		Message: fmt.Sprintf(
			"contract %q (auth.serviceOwned=true) serving slice in %s "+
				"is missing an owner-guard IfStmt that returns "+
				"errcode.New(errcode.KindNotFound, ...) on subject-vs-caller mismatch. "+
				"Returning 403/other leaks session existence (IDOR). "+
				"Canonical form: cells/accesscore/slices/sessionlogout/service.go",
			contractID, rel),
	}}
}

// serviceFileHasOwnerGuard walks the AST of f looking for at least one IfStmt
// that constitutes an owner-guard: a != condition whose body returns
// errcode.New(errcode.KindNotFound, ...).
//
// Owner-guard shape:
//
//	if <ownerField> != <callerIdent> {
//	    return errcode.New(errcode.KindNotFound, ...)
//	}
//
// The condition must be a direct != binary expression where NEITHER side is the
// bare identifier "nil" — this distinguishes owner-guard from error-nil checks
// (`if err != nil { ... }`). Error-nil checks may also return KindNotFound for
// domain not-found cases, but they are not owner-guards.
func serviceFileHasOwnerGuard(f *ast.File) bool {
	found := false
	scanner.EachInSubtree[ast.IfStmt](f, func(ifStmt *ast.IfStmt) {
		if found {
			return
		}
		if !conditionIsOwnerNEQ(ifStmt.Cond) {
			return
		}
		if bodyReturnsKindNotFound(ifStmt.Body) {
			found = true
		}
	})
	return found
}

// conditionIsOwnerNEQ reports whether expr is a BinaryExpr with token.NEQ (!=)
// where neither operand is the bare identifier "nil". This distinguishes
// owner-guard conditions (`sess.SubjectID != callerUserID`) from error-nil
// checks (`err != nil`) that also appear in service files but are not
// owner-guards.
//
// Only the top-level binary expression is inspected — nested boolean operators
// (&&, ||) are not descended into. The canonical owner-guard has a single
// direct != condition, not a compound boolean.
func conditionIsOwnerNEQ(expr ast.Expr) bool {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	if bin.Op != token.NEQ {
		return false
	}
	// Exclude `x != nil` and `nil != x`.
	if isNilIdentExpr(bin.X) || isNilIdentExpr(bin.Y) {
		return false
	}
	return true
}

// isNilIdentExpr reports whether expr is the bare identifier "nil".
func isNilIdentExpr(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}

// bodyReturnsKindNotFound reports whether the block contains at least one
// ReturnStmt that, directly or transitively, contains a CallExpr to errcode.New
// with KindNotFound as the first argument.
//
// Detection is syntactic: the first argument must be a SelectorExpr
// `errcode.KindNotFound` (selector name == "KindNotFound", package alias == "errcode")
// or a bare Ident "KindNotFound" (dot-import form).
//
// We walk ReturnStmt nodes inside the body, then walk CallExpr nodes inside each
// ReturnStmt using scanner.EachInSubtree to stay within the SCANNER-FRAMEWORK-USAGE-01
// constraint (no for-range over []ast.Expr + type assertion).
func bodyReturnsKindNotFound(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	scanner.EachInSubtree[ast.ReturnStmt](body, func(ret *ast.ReturnStmt) {
		if found {
			return
		}
		scanner.EachInSubtree[ast.CallExpr](ret, func(call *ast.CallExpr) {
			if found {
				return
			}
			if !isErrCodeNewCallExpr(call) {
				return
			}
			if len(call.Args) == 0 {
				return
			}
			if argIsKindNotFound(call.Args[0]) {
				found = true
			}
		})
	})
	return found
}

// isErrCodeNewCallExpr reports whether call is syntactically shaped like
// errcode.New(...) — a SelectorExpr with selector "New".
// Bare `New(...)` (dot-import) is also accepted.
// The package identity is confirmed via the KindNotFound argument check.
func isErrCodeNewCallExpr(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name == "New"
	case *ast.Ident:
		return fn.Name == "New"
	default:
		return false
	}
}

// argIsKindNotFound reports whether arg syntactically represents errcode.KindNotFound.
//
// Accepted forms:
//   - SelectorExpr `errcode.KindNotFound`: both package alias and selector name checked
//   - Bare Ident `KindNotFound`: covers dot-import form
func argIsKindNotFound(arg ast.Expr) bool {
	switch a := arg.(type) {
	case *ast.SelectorExpr:
		if a.Sel.Name != serviceOwnedKindNotFoundName {
			return false
		}
		// Confirm the package alias is "errcode" — all GoCell slices import
		// errcode under this canonical alias per convention.
		xIdent, ok := a.X.(*ast.Ident)
		return ok && xIdent.Name == "errcode"
	case *ast.Ident:
		// dot-import form: bare KindNotFound
		return a.Name == serviceOwnedKindNotFoundName
	default:
		return false
	}
}
