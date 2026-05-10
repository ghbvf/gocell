// INVARIANT: ADAPTER-RETURNS-DECLARED-TYPES-01
//
// ADAPTER-RETURNS-DECLARED-TYPES-01: adapter return status ⊆ contract declared.
// Scope: Ceiling guard only (adapter zero typed return is legal — full framework
// fallback is permitted). Floor guards land via roadmap GOCELL-INVARIANT-AUDIT-V1.
//
// Algorithm:
//  1. metadata.NewParser(repoRoot).Parse() to load declared status sets:
//     filter kind=="http" && Codegen==true, set = SuccessStatus ∪ Responses keys.
//  2. Glob cells/*/slices/*/handler.go ∪ cells/*/slices/*/service.go ∪
//     examples/*/cells/*/slices/*/handler.go ∪ examples/*/cells/*/slices/*/service.go.
//  3. For each file: AST parse, walk FuncDecl. Identify adapter methods by first
//     return type name ending in "ResponseObject". Resolve contract ID from imports.
//     Walk ReturnStmt → CompositeLit, extract status from struct name regex.
//     Status ∉ declared → t.Errorf.
//  4. nil / ident (non-CompositeLit) returns → skip (ceiling guard).
//
// ref: goa goagen/codegen/types/types.go
// ref: connect-go cmd/protoc-gen-connect-go
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
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

// TestAdapterReturnsDeclaredTypes implements ADAPTER-RETURNS-DECLARED-TYPES-01.
// It verifies that every CompositeLit typed return in an adapter method
// corresponds to a status code declared in the matching contract.yaml
// (SuccessStatus ∪ Responses keys). Zero typed return (nil, err) is legal.
func TestAdapterReturnsDeclaredTypes(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	runAdapterReturnsDeclaredTypes(t, root)
}

// runAdapterReturnsDeclaredTypes is the parameterised core used by both the
// real-repo test and the testdata self-validation subtests.
func runAdapterReturnsDeclaredTypes(t *testing.T, root string) {
	t.Helper()

	contractStatuses, err := loadContractStatusSets(root)
	if err != nil {
		t.Fatalf("%s: load contract status sets: %v", adapterReturnsDeclaredRule, err)
	}

	files, err := gatherAdapterFiles(root)
	if err != nil {
		t.Fatalf("%s: gather adapter files: %v", adapterReturnsDeclaredRule, err)
	}

	modulePath, err := readModulePathFromRoot(root)
	if err != nil {
		t.Fatalf("%s: read module path: %v", adapterReturnsDeclaredRule, err)
	}

	for _, fpath := range files {
		checkAdapterFile(t, fpath, modulePath, contractStatuses)
	}
}

// TestAdapterReturnsDeclaredTypes_GoodFixture verifies that the good/ testdata
// fixture passes the ADAPTER-RETURNS-DECLARED-TYPES-01 check (zero violations).
func TestAdapterReturnsDeclaredTypes_GoodFixture(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	root := filepath.Join(archDir, "testdata", "adapter_returns", "good")
	// The fixture uses the real module path for import resolution; we pass the
	// real module root so readModulePathFromRoot works correctly.
	realRoot := findModuleRoot(t)
	modulePath, err := readModulePathFromRoot(realRoot)
	if err != nil {
		t.Fatalf("read module path: %v", err)
	}

	contractStatuses, err := loadContractStatusSets(root)
	if err != nil {
		t.Fatalf("load contract status sets from good fixture: %v", err)
	}

	files, err := gatherAdapterFiles(root)
	if err != nil {
		t.Fatalf("gather adapter files from good fixture: %v", err)
	}

	var violations int
	for _, fpath := range files {
		violations += countAdapterFileViolations(t, fpath, modulePath, contractStatuses)
	}
	if violations != 0 {
		t.Errorf("%s: good fixture must produce 0 violations; got %d", adapterReturnsDeclaredRule, violations)
	}
}

// TestAdapterReturnsDeclaredTypes_BadFixture verifies that the bad/ testdata
// fixture triggers at least one violation (self-validation of the scanner).
func TestAdapterReturnsDeclaredTypes_BadFixture(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	root := filepath.Join(archDir, "testdata", "adapter_returns", "bad")
	realRoot := findModuleRoot(t)
	modulePath, err := readModulePathFromRoot(realRoot)
	if err != nil {
		t.Fatalf("read module path: %v", err)
	}

	contractStatuses, err := loadContractStatusSets(root)
	if err != nil {
		t.Fatalf("load contract status sets from bad fixture: %v", err)
	}

	files, err := gatherAdapterFiles(root)
	if err != nil {
		t.Fatalf("gather adapter files from bad fixture: %v", err)
	}

	var violations int
	for _, fpath := range files {
		violations += countAdapterFileViolationsNoReport(fpath, modulePath, contractStatuses)
	}
	if violations == 0 {
		t.Errorf("%s: bad fixture must produce ≥1 violations; got 0 — scanner is not detecting the violation",
			adapterReturnsDeclaredRule)
	}
}

// TestImportPathToContractID tests the importPathToContractID helper.
func TestImportPathToContractID(t *testing.T) {
	t.Parallel()
	const mod = "github.com/ghbvf/gocell"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "auth_login",
			in:   "github.com/ghbvf/gocell/generated/contracts/http/auth/login/v1",
			want: "http.auth.login.v1",
		},
		{
			name: "audit_list",
			in:   "github.com/ghbvf/gocell/generated/contracts/http/audit/list/v1",
			want: "http.audit.list.v1",
		},
		{
			name: "internalapi_segment_reversed",
			in:   "github.com/ghbvf/gocell/generated/contracts/http/internalapi/devicecommands/list/v1",
			want: "http.internal.devicecommands.list.v1",
		},
		{
			name: "non_generated",
			in:   "github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogin",
			want: "",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := importPathToContractID(mod, tc.in)
			if got != tc.want {
				t.Errorf("importPathToContractID(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
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

// importPathToContractID converts a full Go import path for a generated
// contract package back to its contract ID.
// It is the inverse of pathx.ContractIDToPackagePath + module prefix.
//
// "github.com/ghbvf/gocell/generated/contracts/http/auth/login/v1" → "http.auth.login.v1"
// "github.com/ghbvf/gocell/generated/contracts/http/internalapi/foo/v1" → "http.internal.foo.v1"
// Non-generated import paths → "".
// Empty string → "".
func importPathToContractID(modulePath, importPath string) string {
	if importPath == "" {
		return ""
	}
	prefix := modulePath + "/generated/contracts/"
	if !strings.HasPrefix(importPath, prefix) {
		return ""
	}
	tail := strings.TrimPrefix(importPath, prefix)
	segments := strings.Split(tail, "/")
	for i, seg := range segments {
		if seg == "internalapi" {
			segments[i] = "internal"
		}
	}
	return strings.Join(segments, ".")
}

// gatherAdapterFiles collects candidate handler.go and service.go files
// under cells/ and examples/*/cells/ in the given root directory.
func gatherAdapterFiles(root string) ([]string, error) {
	scope := scanner.DirsScope(root, []string{"cells", "examples"})
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

// checkAdapterFile parses one file and reports violations via t.Errorf.
func checkAdapterFile(t *testing.T, fpath, modulePath string, contractStatuses map[string]map[int]bool) {
	t.Helper()
	violations, parseErr := extractAdapterReturnStatuses(fpath)
	if parseErr != nil {
		t.Logf("%s: parse %s: %v (skipped)", adapterReturnsDeclaredRule, fpath, parseErr)
		return
	}
	// Resolve imports for this file to map alias → contractID.
	contractImports, err := resolveContractImports(fpath, modulePath)
	if err != nil {
		t.Logf("%s: resolve imports %s: %v (skipped)", adapterReturnsDeclaredRule, fpath, err)
		return
	}

	for _, ret := range violations {
		// Determine which contract the struct type belongs to.
		contractID := contractIDForTypeName(ret.TypeName, contractImports)
		if contractID == "" {
			// Cannot resolve — skip (ceiling guard does not chase unresolved imports).
			continue
		}
		declared, ok := contractStatuses[contractID]
		if !ok {
			// Contract not found (may be non-codegen or unknown) — skip.
			continue
		}
		if !declared[ret.Status] {
			t.Errorf("%s: %s: func %s (line %d): returns %s (status %d) but %s declares statuses %v",
				adapterReturnsDeclaredRule, fpath, ret.FuncName, ret.Line,
				ret.TypeName, ret.Status, contractID, sortedStatuses(declared))
		}
	}
}

// countAdapterFileViolations is like checkAdapterFile but reports via t.Log
// and returns the violation count.  Used for fixture self-validation.
func countAdapterFileViolations(t *testing.T, fpath, modulePath string, contractStatuses map[string]map[int]bool) int {
	t.Helper()
	returns, parseErr := extractAdapterReturnStatuses(fpath)
	if parseErr != nil {
		return 0
	}
	contractImports, err := resolveContractImports(fpath, modulePath)
	if err != nil {
		return 0
	}
	count := 0
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
			t.Logf("[violation] %s: func %s (line %d): returns %s (status %d) not in %v",
				fpath, ret.FuncName, ret.Line, ret.TypeName, ret.Status, sortedStatuses(declared))
			count++
		}
	}
	return count
}

// countAdapterFileViolationsNoReport is the silent variant for bad-fixture
// self-test (does not call t.Logf, just counts).
func countAdapterFileViolationsNoReport(fpath, modulePath string, contractStatuses map[string]map[int]bool) int {
	returns, parseErr := extractAdapterReturnStatuses(fpath)
	if parseErr != nil {
		return 0
	}
	contractImports, err := resolveContractImports(fpath, modulePath)
	if err != nil {
		return 0
	}
	count := 0
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
			count++
		}
	}
	return count
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
	scanner.EachNode[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
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

// walkReturns walks all ReturnStmt in fn and for each CompositeLit whose type
// name matches responseStructPattern, calls emit with the extracted return info.
func walkReturns(fn *ast.FuncDecl, fset *token.FileSet, emit func(adapterReturn)) {
	scanner.EachNode[ast.ReturnStmt](fn.Body, func(ret *ast.ReturnStmt) {
		for _, result := range ret.Results {
			switch cl := result.(type) {
			case *ast.CompositeLit:
				typeName := compositeLitTypeName(cl)
				if typeName == "" {
					continue
				}
				m := responseStructPattern.FindStringSubmatch(typeName)
				if m == nil {
					continue
				}
				status, _ := strconv.Atoi(m[1])
				pos := fset.Position(cl.Pos())
				emit(adapterReturn{
					File:     pos.Filename,
					Line:     pos.Line,
					Status:   status,
					TypeName: typeName,
				})
			}
		}
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
// map from Go package alias (or last segment of path) to contract ID.
// Only imports whose path resolves to a contract ID are included.
func resolveContractImports(filePath, modulePath string) (map[string]string, error) {
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
		contractID := importPathToContractID(modulePath, raw)
		if contractID == "" {
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
	// Extract the method-name prefix from the type name.
	// E.g. "Get200JSONResponse" → method prefix is everything before the digits.
	// "Login201JSONResponse" → "Login"
	methodPrefix := extractMethodPrefix(typeName)
	if methodPrefix == "" {
		return ""
	}

	// If there is exactly one contract in scope, use it directly.
	if len(contractImports) == 1 {
		for _, cid := range contractImports {
			return cid
		}
	}

	// Multiple contracts: pick the one whose last domain segment matches.
	// "http.auth.login.v1" last action segment is "login".
	// Method prefix "Login" lowercased is "login" — matches.
	lowerPrefix := strings.ToLower(methodPrefix)
	for _, cid := range contractImports {
		parts := strings.Split(cid, ".")
		if len(parts) < 2 {
			continue
		}
		// Compare against second-to-last segment (action/domain).
		action := strings.ToLower(parts[len(parts)-2])
		if action == lowerPrefix {
			return cid
		}
	}

	return ""
}

// extractMethodPrefix returns the Go-identifier prefix before the first digit
// run in typeName.  "Get200JSONResponse" → "Get", "Login201JSONResponse" → "Login".
func extractMethodPrefix(typeName string) string {
	for i, ch := range typeName {
		if ch >= '0' && ch <= '9' {
			return typeName[:i]
		}
	}
	return ""
}

// readModulePathFromRoot reads the module path from go.mod at root.
func readModulePathFromRoot(root string) (string, error) {
	return readGoModModulePath(filepath.Join(root, "go.mod"))
}

// readGoModModulePath parses the given go.mod file and returns the module path
// declared on the "module" directive line.
func readGoModModulePath(goModPath string) (string, error) {
	data, err := os.ReadFile(goModPath) //nolint:gosec // path constructed from module root
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("no module directive in %s", goModPath)
}

// sortedStatuses returns a sorted slice of statuses from the declared set.
func sortedStatuses(declared map[int]bool) []int {
	statuses := make([]int, 0, len(declared))
	for s := range declared {
		statuses = append(statuses, s)
	}
	// Simple insertion sort — small n.
	for i := 1; i < len(statuses); i++ {
		for j := i; j > 0 && statuses[j-1] > statuses[j]; j-- {
			statuses[j-1], statuses[j] = statuses[j], statuses[j-1]
		}
	}
	return statuses
}
