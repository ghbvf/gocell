package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestHandlerReceiptWrite enforces HANDLER-RECEIPT-WRITE-01: cell handlers that
// implement outbox.EntryHandler must not write the HandleResult.Receipt field.
// Receipt is reserved for ConsumerBase → Subscriber delivery-loop hand-off.
// Business handlers reading or writing Receipt observe unspecified intermediate
// state and would break Commit/Release sequencing (see ADR Q1, K#12).
func TestHandlerReceiptWrite(t *testing.T) {
	root := hrFindModuleRoot(t)
	files, err := hrFindCellProductionGoFiles(root)
	if err != nil {
		t.Fatalf("walking cells/: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no production .go files found under cells/")
	}

	var violations []string
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		vs, err := hrCheckFile(path, rel)
		if err != nil {
			t.Fatalf("parsing %s: %v", rel, err)
		}
		violations = append(violations, vs...)
	}

	for _, v := range violations {
		t.Errorf("%s", v)
	}
}

// TestHandlerReceiptWrite_DetectsViolation is a self-test ensuring the detector
// is correctly wired and not silently passing. It parses an inline fixture that
// contains a handler that writes HandleResult.Receipt and asserts the detector
// flags it.
func TestHandlerReceiptWrite_DetectsViolation(t *testing.T) {
	src := `package x
import "github.com/ghbvf/gocell/kernel/outbox"
func HandleFoo(ctx interface{}, entry interface{}) outbox.HandleResult {
	return outbox.HandleResult{Receipt: nil}
}`
	vs, err := hrCheckSource("<fixture>", src)
	if err != nil {
		t.Fatalf("hrCheckSource: %v", err)
	}
	if len(vs) == 0 {
		t.Error("HANDLER-RECEIPT-WRITE-01 detector did not flag Receipt field write in fixture")
	}
}

// hrCheckFile parses a single file and returns HANDLER-RECEIPT-WRITE-01 violations.
func hrCheckFile(path, rel string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return hrCheckAST(fset, f, rel), nil
}

// hrCheckSource parses src (a complete Go source string) and returns violations.
// label is used in violation messages in place of a file path.
func hrCheckSource(label, src string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, label, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return hrCheckAST(fset, f, label), nil
}

// hrCheckAST walks a parsed AST file and emits HANDLER-RECEIPT-WRITE-01
// violations for any function that:
//
//  1. Has a return type matching outbox.HandleResult (SelectorExpr or bare
//     Ident "HandleResult"), and
//  2. In its body contains either:
//     (a) a CompositeLit of type HandleResult with a "Receipt" key-value field, or
//     (b) an assignment whose LHS is a SelectorExpr whose Sel.Name == "Receipt"
//     on an expression that was previously assigned a HandleResult value.
//
// Because AST-only analysis has no type information, the detector uses naming
// heuristics: it flags SelectorExpr.Sel.Name == "Receipt" on any selector that
// appears as an assignment target, or a KeyValueExpr with Key.Name == "Receipt"
// inside a composite literal whose type name is "HandleResult".
func hrCheckAST(fset *token.FileSet, f *ast.File, label string) []string {
	var violations []string

	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		if !hrReturnsHandleResult(fn) {
			return true
		}
		// Walk the function body for Receipt writes.
		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			switch node := inner.(type) {
			case *ast.CompositeLit:
				if hrIsHandleResultType(node.Type) {
					for _, elt := range node.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := kv.Key.(*ast.Ident)
						if ok && key.Name == "Receipt" {
							pos := fset.Position(node.Pos())
							violations = append(violations, hrViolation(label, pos.Line))
						}
					}
				}
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == "Receipt" {
						pos := fset.Position(node.Pos())
						violations = append(violations, hrViolation(label, pos.Line))
					}
				}
			}
			return true
		})
		return true
	})
	return violations
}

// hrViolation formats a HANDLER-RECEIPT-WRITE-01 violation message.
func hrViolation(file string, line int) string {
	return "HANDLER-RECEIPT-WRITE-01: " + file + ":" + itoa(line) +
		": handler must not write HandleResult.Receipt field;" +
		" the field is reserved for ConsumerBase → Subscriber-delivery-loop hand-off (see ADR Q1, K#12)"
}

// hrReturnsHandleResult reports whether fn's result list includes a type
// expression matching outbox.HandleResult (via SelectorExpr) or bare "HandleResult"
// (via Ident). Methods and standalone functions are both checked.
func hrReturnsHandleResult(fn *ast.FuncDecl) bool {
	if fn.Type == nil || fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		if hrIsHandleResultType(field.Type) {
			return true
		}
	}
	return false
}

// hrIsHandleResultType reports whether expr is a type expression for
// outbox.HandleResult. Matches:
//   - SelectorExpr where X is an Ident named "outbox" and Sel is "HandleResult"
//   - Ident named "HandleResult" (bare, within same package)
func hrIsHandleResultType(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		return ok && pkg.Name == "outbox" && e.Sel.Name == "HandleResult"
	case *ast.Ident:
		return e.Name == "HandleResult"
	}
	return false
}

// --- file-walking helpers (self-contained; cannot access package archtest internals) ---

// hrFindModuleRoot walks up from the test's working directory to find go.mod.
func hrFindModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from working directory")
		}
		dir = parent
	}
}

// hrFindCellProductionGoFiles returns all non-test .go files under cells/.
func hrFindCellProductionGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "cells"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "worktrees", "testdata", "generated", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

// itoa converts an int to its decimal string representation without importing
// strconv (keeps the file dependency-light).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
