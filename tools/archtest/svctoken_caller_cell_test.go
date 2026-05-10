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
// Detection: type-aware — the SelectorExpr.X must resolve via go/types to
// the runtime/auth PkgName (closes PR445-FU-PACKAGEALIASES-TYPE-AWARE-01:
// the prior PackageAliases-based AST scan only matched syntactic alias
// names; type-aware uses pkg.TypesInfo.Uses[id].(*types.PkgName).Imported()
// to handle dot-import / blank-import / re-export cases via the type
// checker authoritatively).
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"regexp"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// ruleSvctokenCallerCellRequired01 is the archtest rule identifier; not a credential.
//
//nolint:gosec // G101 false positive: archtest rule identifier, not a credential
const ruleSvctokenCallerCellRequired01 = "SVCTOKEN-CALLER-CELL-REQUIRED-01"

// authRuntimeImportPath is the canonical import path for runtime/auth.
const authRuntimeImportPath = "github.com/ghbvf/gocell/runtime/auth"

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

	// tests=true loads the test variants of every package, so test helpers
	// (e.g. examples/ssobff/walkthrough_test.go) that call
	// GenerateServiceToken are also scanned.
	resolver, err := typeseval.SharedResolver(root, true, nil,
		"./runtime/...", "./cells/...", "./cmd/...", "./examples/...", "./tests/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	var diags []scanner.Diagnostic
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			scanner.EachNode[ast.CallExpr](file, func(call *ast.CallExpr) {
				if !isAuthFuncCall(call, pkg.TypesInfo, "GenerateServiceToken") {
					return
				}
				pos := pkg.Fset.Position(call.Pos())

				// The 4-part signature is GenerateServiceToken(ring, callerCell, method, path, query, ts).
				// callerCell is argument index 1 (0-based).
				if len(call.Args) < 2 {
					diags = append(diags, scanner.Diagnostic{
						Rel:     rel,
						Line:    pos.Line,
						Message: "auth.GenerateServiceToken called with fewer than 2 arguments — missing callerCell",
					})
					return
				}

				arg1 := call.Args[1]
				lit, isLit := arg1.(*ast.BasicLit)
				if !isLit {
					diags = append(diags, scanner.Diagnostic{
						Rel:     rel,
						Line:    pos.Line,
						Message: "auth.GenerateServiceToken second argument (callerCell) must be a string literal",
					})
					return
				}
				callerCell, ok := scanner.StringLitValue(lit)
				if !ok {
					diags = append(diags, scanner.Diagnostic{
						Rel:     rel,
						Line:    pos.Line,
						Message: "auth.GenerateServiceToken second argument (callerCell) must be a string literal",
					})
					return
				}

				if callerCell == "" {
					diags = append(diags, scanner.Diagnostic{
						Rel:     rel,
						Line:    pos.Line,
						Message: "auth.GenerateServiceToken callerCell must not be empty",
					})
					return
				}

				if !cellIDRegex.MatchString(callerCell) {
					diags = append(diags, scanner.Diagnostic{
						Rel:     rel,
						Line:    pos.Line,
						Message: fmt.Sprintf("auth.GenerateServiceToken callerCell %q does not match ^[a-z][a-z0-9-]*$", callerCell),
					})
					return
				}

				if !knownCells[callerCell] {
					diags = append(diags, scanner.Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"auth.GenerateServiceToken callerCell %q is not a known cell ID"+
								" — register it in cells/ or actors.yaml", callerCell),
					})
				}
			})
		}
	}
	scanner.Report(t, ruleSvctokenCallerCellRequired01, diags)
}

// TestSVCTOKEN_CALLER_CELL_REQUIRED_01_BuildTaggedFilesScanned_Wave5_RED is a
// RED-step regression test (TDD per ai-collab.md) for PR445-FU finding F2.
//
// The production rule TestSVCTOKEN_CALLER_CELL_REQUIRED_01 calls
// `typeseval.SharedResolver(root, true, nil, ...)` — the third argument
// `tags []string` is nil, so packages.Load uses the default build set and
// silently skips every file gated by `//go:build <tag>`. Two real callsites
// of auth.GenerateServiceToken are gated this way and therefore escape the
// rule:
//
//   - examples/ssobff/walkthrough_test.go  (//go:build integration)
//   - tests/integration/internal_rpc_caller_cell_test.go  (//go:build integration)
//
// Wave 5 introduces typeseval.KnownNonDefaultTags() and iterates each
// tag-set so the rule scans every variant. This test asserts the load
// contract directly: build-tagged files MUST be loaded so the rule's
// scanForCallExpr loop actually reaches their callsites.
//
// Wave 1 (current, tags=nil): integration-tagged files NOT in resolver
// output → assertion fails → RED.
//
// Wave 5 (multi-tag): integration-tagged files loaded under {integration}
// tag-set → assertion passes → GREEN. The sub-test must therefore also
// switch to multi-tag iteration in Wave 5 to mirror the production loader.
func TestSVCTOKEN_CALLER_CELL_REQUIRED_01_BuildTaggedFilesScanned_Wave5_RED(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Mirror the production rule's loader call exactly.
	resolver, err := typeseval.SharedResolver(root, true, nil,
		"./runtime/...", "./cells/...", "./cmd/...", "./examples/...", "./tests/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	loadedFiles := map[string]bool{}
	for _, pkg := range resolver.Packages() {
		if pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			loadedFiles[rel] = true
		}
	}

	// Files known to contain auth.GenerateServiceToken callsites under
	// `//go:build integration`. Wave 5's KnownNonDefaultTags iteration must
	// load each.
	expectedScanned := []string{
		"examples/ssobff/walkthrough_test.go",
		"tests/integration/internal_rpc_caller_cell_test.go",
	}

	var missing []string
	for _, want := range expectedScanned {
		if !loadedFiles[want] {
			missing = append(missing, want)
		}
	}

	if len(missing) > 0 {
		t.Errorf("SVCTOKEN-CALLER-CELL-REQUIRED-01: %d build-tagged files are silently skipped by the current tags=nil load — these contain auth.GenerateServiceToken callsites that the rule MUST scan: %v", len(missing), missing)
	}
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

// isAuthFuncCall reports whether call is a call expression of the form
// `<auth-import-name>.<funcName>(...)` resolved via go/types — the receiver
// of the SelectorExpr must be a *types.PkgName whose imported package path
// equals authRuntimeImportPath. This is import-aware authoritatively (renamed
// imports are handled because PkgName.Imported().Path() reports the resolved
// import path, not the local name).
func isAuthFuncCall(call *ast.CallExpr, info *types.Info, funcName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != funcName {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	pkgName, ok := info.Uses[id].(*types.PkgName)
	if !ok {
		return false
	}
	return pkgName.Imported().Path() == authRuntimeImportPath
}
