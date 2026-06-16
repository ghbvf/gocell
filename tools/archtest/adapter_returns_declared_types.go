// Importable rule body for ADAPTER-RETURNS-DECLARED-TYPES-01. Migrated from
// the legacy _test.go form to a non-test .go (M3 #1639) so the rule is
// module-path-agnostic — the running module path is derived from go.mod via
// moduleImportPath, NOT bare "github.com/ghbvf/gocell…" literals
// (ARCHTEST-MODULE-PATH-FUNNEL-01). The dogfood + good/bad-fixture precision
// gates live in adapter_returns_declared_types_test.go.
//
// Not registered in StandardCellRules: this rule reads contract.yaml metadata
// and scans generated contract package imports, which are present in any Cell
// repo that uses GoCell codegen. Registration + external vacuous/effective
// semantics verification is a deferred backlog follow-up per #1639 D2
// (tracked at #1706). Kept importable + module-path-agnostic.
//
// # ADAPTER-RETURNS-DECLARED-TYPES-01
//
// ADAPTER-RETURNS-DECLARED-TYPES-01: adapter return status ⊆ contract declared.
// Scope: Ceiling guard only (adapter zero typed return is legal — full framework
// fallback is permitted). Floor guards land via roadmap GOCELL-INVARIANT-AUDIT-V1.
//
// Algorithm:
//
//  1. metadata.NewParser(repoRoot).Parse() to load declared status sets:
//     filter kind=="http" && Codegen==true, set = SuccessStatus ∪ Responses keys.
//  2. Glob cells/*/slices/*/handler.go ∪ cells/*/slices/*/service.go ∪
//     examples/*/cells/*/slices/*/handler.go ∪ examples/*/cells/*/slices/*/service.go.
//  3. For each file: AST parse, walk FuncDecl. Identify adapter methods by first
//     return type name ending in "ResponseObject". Resolve contract ID from imports.
//     Walk ReturnStmt → CompositeLit, extract status from struct name regex.
//     Status ∉ declared → Diagnostic.
//  4. nil / ident (non-CompositeLit) returns → skip (ceiling guard).
//
// # Blind spots (BS) — declared per ai-robust.md §"工具选定后强制盲区自检"
//
// This is a CEILING guard: it must never false-positive, so it tolerates the
// false negatives below (each skips, never mis-flags). A1 (FIXTURE-CELLID) /
// floor guards (roadmap GOCELL-INVARIANT-AUDIT-V1) close the other direction.
//
//   - BS-1 Multi-import ambiguity: contractIDForTypeName resolves the owning
//     contract by best-effort method-name-prefix match across the file's
//     contract imports; a file importing several contracts whose generated
//     struct prefixes collide may resolve to the wrong contract → that return
//     is checked against the wrong declared set (skipped, not mis-flagged).
//   - BS-2 Dot / aliased contract import: importAlias falls back to the last
//     import-path segment, which can collide; an unresolved alias yields
//     contractID == "" → skip.
//   - BS-3 Reflection / dynamically-built response value (non-CompositeLit
//     return): out of scope per ai-robust.md §3 — only inline CompositeLit
//     returns are inspected.
//
// ref: goa goagen/codegen/types/types.go
// ref: connect-go cmd/protoc-gen-connect-go
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/contractpath"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const adapterReturnsDeclaredRule = "ADAPTER-RETURNS-DECLARED-TYPES-01"

// adapterReturn records one CompositeLit return found in an adapter method.
type adapterReturn struct {
	File     string
	FuncName string
	Line     int
	Status   int
	TypeName string
}

// responseStructPattern matches generated typed response struct names.
// Examples: Get200JSONResponse, Delete204NoContentResponse, Post400ErrorResponse.
// Capture group 1 is the 3-digit HTTP status code.
var responseStructPattern = regexp.MustCompile(`^[A-Z][A-Za-z]*(\d{3})[A-Za-z]*(JSONResponse|NoContentResponse|ErrorResponse)$`)

// CheckAdapterReturnsDeclaredTypes runs ADAPTER-RETURNS-DECLARED-TYPES-01 over
// the running module and returns its diagnostics (single source). It is the
// importable entry point: GoCell's TestAdapterReturnsDeclaredTypes calls it
// directly — no parallel rule body.
func CheckAdapterReturnsDeclaredTypes(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	return collectAdapterReturnsViolations(t, root)
}

// collectAdapterReturnsViolations is the parameterised core used by
// CheckAdapterReturnsDeclaredTypes (real-repo) and by the fixture subtests
// (via collectAdapterReturnsViolationsAt). It loads contract status sets,
// gathers adapter files, resolves the module import path, and aggregates
// collectAdapterFileViolations over all files. Load/gather/module-path errors
// cause t.Fatalf (same fatal behavior as the prior runAdapterReturnsDeclaredTypes).
func collectAdapterReturnsViolations(t *testing.T, root string) []Diagnostic {
	t.Helper()

	contractStatuses, err := loadContractStatusSets(root)
	if err != nil {
		t.Fatalf("%s: load contract status sets: %v", adapterReturnsDeclaredRule, err)
	}

	files, err := gatherAdapterFiles(root)
	if err != nil {
		t.Fatalf("%s: gather adapter files: %v", adapterReturnsDeclaredRule, err)
	}

	modulePath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("%s: read module path: %v", adapterReturnsDeclaredRule, err)
	}

	return collectAdapterReturnsViolationsAt(root, modulePath, files, contractStatuses)
}

// collectAdapterReturnsViolationsAt is the pure (t-free) detection core shared
// by the real-repo path and the fixture subtests. It builds the forward
// import-path→contract-ID index once (single source via contractpath), then
// aggregates collectAdapterFileViolations over all fpath entries, relativising
// diagnostics to root where possible.
func collectAdapterReturnsViolationsAt(root, modulePath string, files []string, contractStatuses map[string]map[int]bool) []Diagnostic {
	importIndex := buildContractImportIndex(modulePath, contractStatuses)
	var out []Diagnostic
	for _, fpath := range files {
		out = append(out, collectAdapterFileViolations(fpath, root, importIndex, contractStatuses)...)
	}
	return out
}

// buildContractImportIndex maps each status-bearing contract's generated Go
// import path back to its contract ID. The path is derived from
// contractpath.ContractIDToImportPath — the single source of truth for the
// generated package layout (the "internal"→"internalapi" segment rewrite, the
// generated/contracts/ prefix) shared with cellgen and kernel/governance. This
// replaces a hand-written inverse of that layout (codex #1708 F4): deriving from
// the forward function means a future change to the generated path scheme can no
// longer drift between codegen and this archtest.
func buildContractImportIndex(modulePath string, contractStatuses map[string]map[int]bool) map[string]string {
	index := make(map[string]string, len(contractStatuses))
	for id := range contractStatuses {
		index[contractpath.ContractIDToImportPath(modulePath, id)] = id
	}
	return index
}

// collectAdapterFileViolations is the single detection core: returns one
// Diagnostic per disallowed status return in fpath. Parse/import-resolve
// failures yield no diagnostics (skip — ceiling guard does not chase
// unresolved imports), matching the prior variants' early-return behavior.
func collectAdapterFileViolations(
	fpath, root string,
	importIndex map[string]string,
	contractStatuses map[string]map[int]bool,
) []Diagnostic {
	returns, parseErr := extractAdapterReturnStatuses(fpath)
	if parseErr != nil {
		return nil
	}

	contractImports, err := resolveContractImports(fpath, importIndex)
	if err != nil {
		return nil
	}

	rel := fpath
	if r, err := filepath.Rel(root, fpath); err == nil {
		rel = r
	}

	var out []Diagnostic
	for _, ret := range returns {
		contractID := contractIDForTypeName(ret.TypeName, contractImports)
		if contractID == "" {
			continue
		}
		declared, ok := contractStatuses[contractID]
		if !ok {
			continue
		}
		if !declared[ret.Status] {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: ret.Line,
				Message: fmt.Sprintf(
					"func %s: returns %s (status %d) but %s declares statuses %v",
					ret.FuncName, ret.TypeName, ret.Status, contractID, sortedStatuses(declared),
				),
			})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Core helpers
// ---------------------------------------------------------------------------

// loadContractStatusSets builds a map[contractID]map[status]bool from the
// project metadata rooted at rootDir.
// Only kind=="http" && Codegen==true contracts are included.
// The declared set = {SuccessStatus} ∪ keys(Responses).
func loadContractStatusSets(rootDir string) (map[string]map[int]bool, error) {
	project, err := metadata.NewParser(rootDir).Parse()
	if err != nil {
		return nil, err
	}

	out := make(map[string]map[int]bool, len(project.Contracts))
	for id, c := range project.Contracts {
		if c.Kind != "http" || !c.Codegen {
			continue
		}
		http := c.Endpoints.HTTP
		if http == nil {
			continue
		}
		set := make(map[int]bool)
		if http.SuccessStatus != 0 {
			set[http.SuccessStatus] = true
		}
		for status := range http.Responses {
			set[status] = true
		}
		out[id] = set
	}
	return out, nil
}

// gatherAdapterFiles collects candidate handler.go and service.go files
// under cells/ and examples/*/cells/ in the given root directory.
func gatherAdapterFiles(root string) ([]string, error) {
	scope := scanner.DirsScope(root, platformAndExampleCellScanDirs())
	all, err := scope.Files()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range all {
		name := filepath.Base(path)
		if name == "handler.go" || name == "service.go" {
			files = append(files, path)
		}
	}
	return files, nil
}

// extractAdapterReturnStatuses parses filePath and returns all CompositeLit
// return statements inside adapter methods (methods whose first return type
// name ends in "ResponseObject").
//
// nil/ident returns are skipped (ceiling guard: zero typed return is legal).
// Non-matching struct names are also skipped.
func extractAdapterReturnStatuses(filePath string) ([]adapterReturn, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, 0)
	if err != nil {
		return nil, err
	}

	var results []adapterReturn
	scanner.EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if !isAdapterMethod(fn) {
			return
		}
		walkReturns(fn, fset, func(ret adapterReturn) {
			ret.FuncName = fn.Name.Name
			results = append(results, ret)
		})
	})
	return results, nil
}

// isAdapterMethod reports whether fn is an adapter method:
// it has a receiver AND its first return type is an ident whose name ends in
// "ResponseObject".
func isAdapterMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || fn.Type == nil || fn.Type.Results == nil {
		return false
	}
	results := fn.Type.Results.List
	if len(results) == 0 {
		return false
	}
	return returnTypeEndsInResponseObject(results[0].Type)
}

// returnTypeEndsInResponseObject checks if an expression is an identifier
// whose name ends in "ResponseObject".
func returnTypeEndsInResponseObject(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return strings.HasSuffix(e.Name, "ResponseObject")
	case *ast.SelectorExpr:
		return strings.HasSuffix(e.Sel.Name, "ResponseObject")
	}
	return false
}

// walkReturns walks all ReturnStmt in fn and for each top-level CompositeLit
// whose type name matches responseStructPattern, calls emit with the extracted
// return info. EachInChildren visits only direct children of ret so nested
// composites (e.g. `return Foo{X: Bar{}}`) are not over-matched.
func walkReturns(fn *ast.FuncDecl, fset *token.FileSet, emit func(adapterReturn)) {
	scanner.EachInSubtree[ast.ReturnStmt](fn.Body, func(ret *ast.ReturnStmt) {
		scanner.EachInChildren[ast.CompositeLit](ret, func(cl *ast.CompositeLit) {
			typeName := compositeLitTypeName(cl)
			if typeName == "" {
				return
			}
			m := responseStructPattern.FindStringSubmatch(typeName)
			if m == nil {
				return
			}
			status, _ := strconv.Atoi(m[1])
			pos := fset.Position(cl.Pos())
			emit(adapterReturn{
				File:     pos.Filename,
				Line:     pos.Line,
				Status:   status,
				TypeName: typeName,
			})
		})
	})
}

// compositeLitTypeName extracts the base type name from a CompositeLit.
// Handles qualified names (pkg.Type) and plain names (Type).
func compositeLitTypeName(cl *ast.CompositeLit) string {
	switch t := cl.Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// resolveContractImports parses the import block of filePath and returns a
// map from Go package alias (or last segment of path) to contract ID. Each
// import path is resolved against importIndex (the forward import-path→contract-ID
// map built by buildContractImportIndex); only imports present in the index are
// included.
func resolveContractImports(filePath string, importIndex map[string]string) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string)
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		raw := strings.Trim(imp.Path.Value, `"`)
		contractID, ok := importIndex[raw]
		if !ok {
			continue
		}
		alias := importAlias(imp, raw)
		out[alias] = contractID
	}
	return out, nil
}

// importAlias returns the effective local name for an import declaration.
// If the import has an explicit alias, use it; otherwise use the last path segment.
func importAlias(imp *ast.ImportSpec, rawPath string) string {
	if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" && imp.Name.Name != "." {
		return imp.Name.Name
	}
	parts := strings.Split(rawPath, "/")
	return parts[len(parts)-1]
}

// contractIDForTypeName resolves the contract ID that owns typeName, given
// the map of alias→contractID for the current file.
//
// typeName is the bare struct name extracted from the CompositeLit. In the
// generated package, struct names are unqualified (Get200JSONResponse). When
// the adapter imports the generated package under an alias (e.g. `logingen`),
// the CompositeLit in the caller's file appears as a qualified selector
// (logingen.Login201JSONResponse); the AST walk calls compositeLitTypeName
// which returns only the Sel part (Login201JSONResponse — unqualified).
//
// Strategy: try all contract packages in the import map whose generated
// struct prefix matches the method-name prefix extracted from typeName.
// This is a best-effort heuristic; false negatives are acceptable for the
// ceiling guard (we must not false-positive).
func contractIDForTypeName(typeName string, contractImports map[string]string) string {
	methodPrefix := extractMethodPrefix(typeName)
	if methodPrefix == "" {
		return ""
	}

	if len(contractImports) == 1 {
		for _, cid := range contractImports {
			return cid
		}
	}

	lowerPrefix := strings.ToLower(methodPrefix)
	for _, cid := range contractImports {
		parts := strings.Split(cid, ".")
		if len(parts) < 2 {
			continue
		}
		action := strings.ToLower(parts[len(parts)-2])
		if action == lowerPrefix {
			return cid
		}
	}

	return ""
}

// extractMethodPrefix returns the Go-identifier prefix before the first digit
// run in typeName. "Get200JSONResponse" → "Get", "Login201JSONResponse" → "Login".
func extractMethodPrefix(typeName string) string {
	for i, ch := range typeName {
		if ch >= '0' && ch <= '9' {
			return typeName[:i]
		}
	}
	return ""
}

// sortedStatuses returns a sorted slice of statuses from the declared set.
func sortedStatuses(declared map[int]bool) []int {
	statuses := make([]int, 0, len(declared))
	for s := range declared {
		statuses = append(statuses, s)
	}
	for i := 1; i < len(statuses); i++ {
		for j := i; j > 0 && statuses[j-1] > statuses[j]; j-- {
			statuses[j-1], statuses[j] = statuses[j], statuses[j-1]
		}
	}
	return statuses
}
