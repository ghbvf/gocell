// INVARIANT: SVCTOKEN-CALLER-CELL-REQUIRED-01
//
// # SVCTOKEN-CALLER-CELL-REQUIRED-01
//
// Invariant: every call expression `auth.GenerateServiceToken(...)` must
// pass a non-empty string literal as its second argument (callerCell).
// The literal must:
//   - match the pattern ^[a-z][a-z0-9-]*$ (valid cell ID format)
//   - be a known cell ID according to cells/ directory names OR actors.yaml
//
// This gate prevents callers from omitting the caller identity or using an
// unregistered cell name, which would defeat the purpose of 4-part service
// token caller-cell propagation.
//
// Detection: AST walk of all production .go files, scanning call expressions
// of the form auth.GenerateServiceToken(...). The second argument (index 1)
// must be a string literal matching the cell-ID regex and appearing in the
// known-cells set derived from cells/ subdirectories and actors.yaml.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ruleSvctokenCallerCellRequired01 is the archtest rule identifier; not a credential.
//
//nolint:gosec // G101 false positive: archtest rule identifier, not a credential
const ruleSvctokenCallerCellRequired01 = "SVCTOKEN-CALLER-CELL-REQUIRED-01"

// cellIDRegex is the canonical cell-ID pattern: lowercase letter + lowercase
// alphanumeric/dash, at least 2 chars total.
var cellIDRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// TestSVCTOKEN_CALLER_CELL_REQUIRED_01 enforces that every call to
// auth.GenerateServiceToken passes a valid cell-ID string literal as its
// second argument (callerCell).
//
// Note: this test FAILS (RED) until Wave 2 updates GenerateServiceToken to
// accept callerCell as the second parameter AND all call sites are migrated.
func TestSVCTOKEN_CALLER_CELL_REQUIRED_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	knownCells := discoverKnownCells(t, root)

	// Scan all production roots (cells/, runtime/, cmd/, examples/, tests/),
	// including _test.go since test helpers also call GenerateServiceToken
	// and must declare a known callerCell. Direct walk is correct here:
	// the rule scope is wider than cell-managed code (e.g.,
	// examples/ssobff/walkthrough_test.go is non-cell example code).
	scope := scanner.DirsScope(root,
		[]string{"runtime", "cells", "cmd", "examples", "tests"},
		scanner.IncludeTests(),
	)

	var diags []scanner.Diagnostic
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		authAliases := authPackageAliases(fc.File)
		if len(authAliases) == 0 {
			return // file does not import runtime/auth
		}
		ast.Inspect(fc.File, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if !isAuthGenerateServiceTokenCall(call, authAliases) {
				return true
			}
			pos := fc.Fset.Position(call.Pos())

			// The 4-part signature is GenerateServiceToken(ring, callerCell, method, path, query, ts).
			// callerCell is argument index 1 (0-based).
			if len(call.Args) < 2 {
				diags = append(diags, scanner.Diagnostic{
					Rel:     fc.Rel,
					Line:    pos.Line,
					Message: "auth.GenerateServiceToken called with fewer than 2 arguments — missing callerCell",
				})
				return true
			}

			arg1 := call.Args[1]
			lit, ok := arg1.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				diags = append(diags, scanner.Diagnostic{
					Rel:     fc.Rel,
					Line:    pos.Line,
					Message: "auth.GenerateServiceToken second argument (callerCell) must be a string literal",
				})
				return true
			}

			// Strip quotes.
			callerCell := lit.Value
			if len(callerCell) >= 2 && callerCell[0] == '"' && callerCell[len(callerCell)-1] == '"' {
				callerCell = callerCell[1 : len(callerCell)-1]
			}

			if callerCell == "" {
				diags = append(diags, scanner.Diagnostic{
					Rel:     fc.Rel,
					Line:    pos.Line,
					Message: "auth.GenerateServiceToken callerCell must not be empty",
				})
				return true
			}

			if !cellIDRegex.MatchString(callerCell) {
				diags = append(diags, scanner.Diagnostic{
					Rel:     fc.Rel,
					Line:    pos.Line,
					Message: fmt.Sprintf("auth.GenerateServiceToken callerCell %q does not match ^[a-z][a-z0-9-]*$", callerCell),
				})
				return true
			}

			if !knownCells[callerCell] {
				diags = append(diags, scanner.Diagnostic{
					Rel:  fc.Rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"auth.GenerateServiceToken callerCell %q is not a known cell ID"+
							" — register it in cells/ or actors.yaml", callerCell),
				})
			}
			return true
		})
	})
	scanner.Report(t, ruleSvctokenCallerCellRequired01, diags)
}

// discoverKnownCells returns the set of valid caller cell IDs from
// ProjectMeta.Cells (covers both top-level and examples cells via
// metadata path-pattern matching) plus actor IDs from actors.yaml.
func discoverKnownCells(t *testing.T, root string) map[string]bool {
	t.Helper()
	known := map[string]bool{}

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("metadata.NewParser: %v", err)
	}
	for id := range project.Cells {
		if cellIDRegex.MatchString(id) {
			known[id] = true
		}
	}
	for _, a := range project.Actors {
		if cellIDRegex.MatchString(a.ID) {
			known[a.ID] = true
		}
	}
	return known
}

// isAuthGenerateServiceTokenCall reports whether call is a call expression
// of the form <alias>.GenerateServiceToken(...) where <alias> is one of the
// resolved local names for runtime/auth in the current file. This is
// import-aware so renamed imports (`import authpkg "…/runtime/auth"`) are
// still detected.
func isAuthGenerateServiceTokenCall(call *ast.CallExpr, authAliases map[string]struct{}) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "GenerateServiceToken" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, hit := authAliases[id.Name]
	return hit
}

// authPackageAliases returns the set of local package names by which f
// imports github.com/ghbvf/gocell/runtime/auth. Default name is "auth"
// when no rename is given; explicit aliases (`import x "…"`) are honored;
// dot-imports ("." alias) and blank imports ("_") are excluded because
// neither produces an AST `<name>.GenerateServiceToken` call expression.
func authPackageAliases(f *ast.File) map[string]struct{} {
	const target = `"github.com/ghbvf/gocell/runtime/auth"`
	out := map[string]struct{}{}
	for _, imp := range f.Imports {
		if imp.Path == nil || imp.Path.Value != target {
			continue
		}
		name := "auth"
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				continue
			}
			name = imp.Name.Name
		}
		out[name] = struct{}{}
	}
	return out
}
