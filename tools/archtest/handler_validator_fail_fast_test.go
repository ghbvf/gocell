// HANDLER-VALIDATOR-FAIL-FAST-01
//
// Invariant: generated handler_gen.go files that embed requestSchemaJSON MUST
// use the fail-fast panic pattern in NewHandler, not the swallow pattern
// (if err == nil { h.requestValidator = v }).
//
// The swallow pattern is unsafe because a schema that fails to compile (e.g.
// invalid JSON or unsupported draft) causes requestValidator to remain nil.
// The nil-guard "if h.requestValidator != nil" then lets every request bypass
// schema validation — a silent security regression.
//
// Correct pattern (ref: k8s scheme.go init-panic; oapi-codegen strict):
//
//	v, err := schemavalidate.NewValidator(requestSchemaJSON)
//	if err != nil {
//	    panic(...)
//	}
//	h.requestValidator = v
//
// Banned pattern (fail-open):
//
//	if v, err := schemavalidate.NewValidator(requestSchemaJSON); err == nil {
//	    h.requestValidator = v
//	}
//
// Detection: AST-scan NewHandler function bodies in generated handler_gen.go
// files that reference requestSchemaJSON. Assert the function body contains a
// panic() call expression. Assert it does NOT contain the banned "err == nil"
// swallow pattern on a schemavalidate.NewValidator call.
//
// Negative fixture: tools/archtest/testdata/handler_validator_fail_fast/violates/handler_gen.go
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const handlerValidatorFailFastRule = "HANDLER-VALIDATOR-FAIL-FAST-01"

// TestHANDLER_VALIDATOR_FAIL_FAST_01 scans all generated handler_gen.go files
// that embed requestSchemaJSON and asserts the fail-fast panic pattern.
func TestHANDLER_VALIDATOR_FAIL_FAST_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	genContractsDir := filepath.Join(root, "generated", "contracts")
	var handlerFiles []string
	_ = filepath.WalkDir(genContractsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // archtest skips unreadable nodes silently
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "worktrees", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "handler_gen.go") {
			handlerFiles = append(handlerFiles, path)
		}
		return nil
	})

	if len(handlerFiles) == 0 {
		t.Fatalf("%s: no handler_gen.go files found under %s — scan broken",
			handlerValidatorFailFastRule, genContractsDir)
	}

	filesWithSchema := 0
	for _, f := range handlerFiles {
		rel, _ := filepath.Rel(root, f)
		violations := checkHandlerValidatorFailFast(f, rel)
		for _, v := range violations {
			t.Errorf("%s: %s", handlerValidatorFailFastRule, v)
		}
		// Count files that actually have requestSchemaJSON.
		if fileContainsRequestSchema(f) {
			filesWithSchema++
		}
	}

	if filesWithSchema == 0 {
		t.Fatalf("%s: no handler_gen.go with requestSchemaJSON found — scan logic broken",
			handlerValidatorFailFastRule)
	}
	t.Logf("%s: scanned %d handler_gen.go files, %d embed requestSchemaJSON",
		handlerValidatorFailFastRule, len(handlerFiles), filesWithSchema)
}

// TestHANDLER_VALIDATOR_FAIL_FAST_01_NegativeFixture verifies the scanner
// detects the swallow pattern in the negative fixture.
func TestHANDLER_VALIDATOR_FAIL_FAST_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	fixture := filepath.Join(root, "tools", "archtest", "testdata",
		"handler_validator_fail_fast", "violates", "handler_gen.go")
	if _, err := os.Stat(fixture); os.IsNotExist(err) {
		t.Fatalf("negative fixture missing: %s", fixture)
	}

	rel := "tools/archtest/testdata/handler_validator_fail_fast/violates/handler_gen.go"
	violations := checkHandlerValidatorFailFast(fixture, rel)
	if len(violations) == 0 {
		t.Errorf("negative fixture produced no violations — scanner broken")
	}
}

// fileContainsRequestSchema reports whether a file references requestSchemaJSON.
// Uses a quick string search to avoid parsing files that have no schema.
func fileContainsRequestSchema(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // archtest scans repo paths it discovered itself
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "requestSchemaJSON")
}

// checkHandlerValidatorFailFast parses the handler_gen.go file and checks the
// NewHandler function body for the fail-fast pattern. Returns violations found.
func checkHandlerValidatorFailFast(path, rel string) []string {
	// Quick pre-screen: skip files without requestSchemaJSON entirely.
	if !fileContainsRequestSchema(path) {
		return nil
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse error: %v", rel, err)}
	}

	var violations []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "NewHandler" || fn.Body == nil {
			continue
		}
		v := analyzeNewHandlerBody(fn.Body, rel, fset)
		violations = append(violations, v...)
	}
	return violations
}

// analyzeNewHandlerBody inspects a NewHandler function body AST for the
// banned swallow pattern and verifies the panic pattern is present.
func analyzeNewHandlerBody(body *ast.BlockStmt, rel string, fset *token.FileSet) []string {
	var violations []string
	hasPanic := false
	hasSwallow := false

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			// Detect panic(...) call.
			if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "panic" {
				hasPanic = true
			}
		case *ast.IfStmt:
			// Detect the swallow pattern:
			// if v, err := schemavalidate.NewValidator(requestSchemaJSON); err == nil { ... }
			// Signature: IfStmt with Init (short var decl calling NewValidator) and
			// Cond that is a BinaryExpr "err == nil".
			if node.Init != nil && isNewValidatorInit(node.Init) && isErrEqNilCond(node.Cond) {
				pos := fset.Position(node.Pos())
				hasSwallow = true
				violations = append(violations, fmt.Sprintf(
					"%s:%d: NewHandler uses fail-open swallow pattern "+
						"(if v, err := schemavalidate.NewValidator(...); err == nil) — "+
						"schema compile failure silently skips validation; "+
						"use panic on err != nil (codegen invariant)",
					rel, pos.Line,
				))
			}
		}
		return true
	})

	// If the file has requestSchemaJSON but NewHandler doesn't panic, flag it
	// (unless we already flagged the swallow pattern above).
	if !hasPanic && !hasSwallow {
		violations = append(violations, fmt.Sprintf(
			"%s: NewHandler does not panic on schema compile failure — "+
				"use `if err != nil { panic(...) }` after schemavalidate.NewValidator",
			rel,
		))
	}
	return violations
}

// isNewValidatorInit returns true when stmt is a short variable declaration
// that calls schemavalidate.NewValidator or NewValidator.
func isNewValidatorInit(stmt ast.Stmt) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok {
		return false
	}
	if assign.Tok.String() != ":=" {
		return false
	}
	for _, rhs := range assign.Rhs {
		call, ok := rhs.(*ast.CallExpr)
		if !ok {
			continue
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == "NewValidator" {
				return true
			}
		case *ast.Ident:
			if fn.Name == "NewValidator" {
				return true
			}
		}
	}
	return false
}

// isErrEqNilCond returns true when expr is a binary expression "err == nil".
func isErrEqNilCond(expr ast.Expr) bool {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	if bin.Op.String() != "==" {
		return false
	}
	ident, ok := bin.X.(*ast.Ident)
	if !ok {
		return false
	}
	nilIdent, ok := bin.Y.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "err" && nilIdent.Name == "nil"
}
