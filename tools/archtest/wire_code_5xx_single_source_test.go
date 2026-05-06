// WIRE-CODE-5XX-SINGLE-SOURCE-01: 5xx wire-code single-source authority.
//
// Authority: pkg/errcode/status.go::Kind.PublicCode() — every 5xx Kind
// projects to one of {ErrInternal, ErrServiceUnavailable, ErrServerTimeout}.
//
// Mirror: pkg/errcode/status.go::PublicCodeForStatus — explicit cases
// allowed only for 503/504 (whose Kind has a dedicated wire code); every
// other 5xx must fall through to ErrInternal default.
//
// Adding a new dedicated 5xx wire code requires extending Kind.PublicCode()
// AND PublicCodeForStatus together in the same change.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"testing"
)

// TestWireCode5xxSingleSource enforces WIRE-CODE-5XX-SINGLE-SOURCE-01.
//
// It parses pkg/errcode/status.go via AST and asserts two invariants:
//
//  1. PublicCodeForStatus has explicit switch cases ONLY for
//     http.StatusServiceUnavailable (503) and http.StatusGatewayTimeout (504)
//     in the 5xx range; every other 5xx must fall through to the default branch.
//
//  2. The set of code identifiers returned by those two explicit cases
//     ({ErrServiceUnavailable, ErrServerTimeout}) matches exactly the set
//     of non-default return identifiers in Kind.PublicCode().
//
// Any engineer adding a new 5xx case (e.g. 501→ErrNotImplemented) will see
// this test fail, prompting them to extend both functions together.
func TestWireCode5xxSingleSource(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	statusFile := filepath.Join(root, "pkg", "errcode", "status.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, statusFile, nil, 0)
	if err != nil {
		t.Fatalf("WIRE-CODE-5XX-SINGLE-SOURCE-01: parse %s: %v", statusFile, err)
	}

	// Collect switch cases from PublicCodeForStatus and Kind.PublicCode.
	publicCodeForStatusCases := collectSwitchCases(t, f, "PublicCodeForStatus")
	kindPublicCodeCases := collectSwitchCases(t, f, "PublicCode")

	// --- Assertion 1 ---
	// PublicCodeForStatus must have explicit cases only for 503 and 504 in
	// the 500-599 range. Any other 5xx explicit case is a violation.
	allowed5xxStatuses := map[int]bool{
		http.StatusServiceUnavailable: true, // 503
		http.StatusGatewayTimeout:     true, // 504
	}

	for statusVal, codeIdent := range publicCodeForStatusCases {
		if statusVal < 500 || statusVal > 599 {
			continue // 4xx and other ranges are not our concern here
		}
		if !allowed5xxStatuses[statusVal] {
			t.Errorf(
				"WIRE-CODE-5XX-SINGLE-SOURCE-01 violated: PublicCodeForStatus has explicit "+
					"case for status %d → %s; only 503 and 504 may have dedicated 5xx wire codes. "+
					"Extend Kind.PublicCode() in tandem if a new dedicated code is truly needed.",
				statusVal, codeIdent,
			)
		}
	}

	// --- Assertion 2 ---
	// The code identifiers returned by the two allowed explicit 5xx cases in
	// PublicCodeForStatus must exactly match the non-default returns in
	// Kind.PublicCode().
	//
	// Expected: {ErrServiceUnavailable, ErrServerTimeout} in both functions.
	publicForStatusCodes := make(map[string]bool)
	for statusVal, codeIdent := range publicCodeForStatusCases {
		if statusVal >= 500 && statusVal <= 599 && allowed5xxStatuses[statusVal] {
			publicForStatusCodes[codeIdent] = true
		}
	}

	kindPublicCodes := make(map[string]bool)
	for _, codeIdent := range kindPublicCodeCases {
		kindPublicCodes[codeIdent] = true
	}

	// Every explicit 5xx code in PublicCodeForStatus must appear in Kind.PublicCode.
	for codeIdent := range publicForStatusCodes {
		if !kindPublicCodes[codeIdent] {
			t.Errorf(
				"WIRE-CODE-5XX-SINGLE-SOURCE-01 violated: PublicCodeForStatus returns %s for "+
					"an explicit 5xx case, but Kind.PublicCode() has no corresponding case. "+
					"Both functions must be extended together.",
				codeIdent,
			)
		}
	}

	// Every explicit case in Kind.PublicCode must appear in PublicCodeForStatus.
	for codeIdent := range kindPublicCodes {
		if !publicForStatusCodes[codeIdent] {
			t.Errorf(
				"WIRE-CODE-5XX-SINGLE-SOURCE-01 violated: Kind.PublicCode() returns %s for "+
					"an explicit case, but PublicCodeForStatus has no corresponding 5xx case. "+
					"Both functions must be extended together.",
				codeIdent,
			)
		}
	}

	// Sanity: the baseline set must be exactly these two identifiers.
	// If neither violation above triggered but the set is somehow empty or
	// diverged, something structural changed.
	expected := map[string]bool{
		"ErrServiceUnavailable": true,
		"ErrServerTimeout":      true,
	}
	for want := range expected {
		if !kindPublicCodes[want] {
			t.Errorf(
				"WIRE-CODE-5XX-SINGLE-SOURCE-01 baseline check: expected Kind.PublicCode() "+
					"to contain %s but it is missing. Structural change detected.",
				want,
			)
		}
		if !publicForStatusCodes[want] {
			t.Errorf(
				"WIRE-CODE-5XX-SINGLE-SOURCE-01 baseline check: expected PublicCodeForStatus "+
					"to have an explicit 5xx case returning %s but it is missing. "+
					"Structural change detected.",
				want,
			)
		}
	}
}

// collectSwitchCases parses the named function's body in f and returns a map
// of (numeric status constant value → returned Code identifier) for explicit
// switch cases. Default branches are excluded.
//
// For Kind.PublicCode(), which switches on a Kind receiver rather than an int
// status, we collect (case index → returned Code identifier) using an
// incrementing pseudo-key so the caller can extract the identifier set.
// The map key is set to a negative sentinel (-1, -2, …) for Kind cases to
// avoid collisions with real HTTP status ints.
//
// The function handles two switch shapes:
//
//	switch status { case http.StatusServiceUnavailable: return ErrServiceUnavailable }
//	switch k { case KindUnavailable: return ErrServiceUnavailable }
func collectSwitchCases(t *testing.T, f *ast.File, funcName string) map[int]string {
	t.Helper()
	result := make(map[int]string)

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		// Match both top-level func and method by name.
		if fn.Name.Name != funcName {
			continue
		}

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}

			kindIndex := -1 // used for Kind-based switch cases
			for _, clause := range sw.Body.List {
				cc, ok := clause.(*ast.CaseClause)
				if !ok || cc.List == nil {
					continue // default clause — skip
				}

				// Extract the returned Code identifier from the case body.
				codeIdent := extractReturnIdent(cc.Body)
				if codeIdent == "" {
					continue
				}

				// Try to interpret the case expression as an http.StatusXxx
				// selector (e.g. http.StatusServiceUnavailable → 503).
				for _, expr := range cc.List {
					sel, ok := expr.(*ast.SelectorExpr)
					if !ok {
						// Not a selector; might be a Kind constant like KindUnavailable.
						// Store with negative pseudo-key.
						kindIndex--
						result[kindIndex] = codeIdent
						continue
					}
					statusVal := httpStatusValue(sel)
					if statusVal != 0 {
						result[statusVal] = codeIdent
					} else {
						// Selector but not an http package constant (e.g. errcode.KindXxx).
						kindIndex--
						result[kindIndex] = codeIdent
					}
				}
			}
			return true
		})
	}
	return result
}

// extractReturnIdent walks the statement list of a case body and returns the
// identifier name of the first return value if it is a simple selector or
// identifier (e.g. `return ErrServiceUnavailable` or `return errcode.ErrXxx`).
func extractReturnIdent(stmts []ast.Stmt) string {
	for _, stmt := range stmts {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			continue
		}
		switch e := ret.Results[0].(type) {
		case *ast.Ident:
			return e.Name
		case *ast.SelectorExpr:
			return e.Sel.Name
		}
	}
	return ""
}

// httpStatusValue maps an ast.SelectorExpr (e.g. http.StatusServiceUnavailable)
// to its numeric value. Returns 0 if the selector is not from the "http" package
// or is not a known 5xx constant.
func httpStatusValue(sel *ast.SelectorExpr) int {
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "http" {
		return 0
	}
	switch sel.Sel.Name {
	case "StatusInternalServerError":
		return http.StatusInternalServerError // 500
	case "StatusNotImplemented":
		return http.StatusNotImplemented // 501
	case "StatusBadGateway":
		return http.StatusBadGateway // 502
	case "StatusServiceUnavailable":
		return http.StatusServiceUnavailable // 503
	case "StatusGatewayTimeout":
		return http.StatusGatewayTimeout // 504
	case "StatusHTTPVersionNotSupported":
		return http.StatusHTTPVersionNotSupported // 505
	case "StatusVariantAlsoNegotiates":
		return http.StatusVariantAlsoNegotiates // 506
	case "StatusInsufficientStorage":
		return http.StatusInsufficientStorage // 507
	case "StatusLoopDetected":
		return http.StatusLoopDetected // 508
	case "StatusNotExtended":
		return http.StatusNotExtended // 510
	case "StatusNetworkAuthenticationRequired":
		return http.StatusNetworkAuthenticationRequired // 511
	default:
		return 0
	}
}
