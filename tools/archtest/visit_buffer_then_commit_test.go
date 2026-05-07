// VISIT-BUFFER-THEN-COMMIT-01 — generated typed response envelope visit
// methods must follow the buffer-then-commit pattern:
//
//  1. var buf bytes.Buffer  (or assignment via :=)
//  2. json.NewEncoder(&buf).Encode(...)
//  3. (if err != nil branch returns)
//  4. w.Header().Set + w.WriteHeader(status)
//  5. buf.WriteTo(w)
//
// Anti-pattern (commit-then-stream): w.WriteHeader before json.Encode means
// encode failure commits a half-written response status that cannot be rolled
// back. Three independent upstream Go frameworks — oapi-codegen, Kratos,
// go-zero — all emit buffer-then-commit; this test guards the GoCell template
// against regressing to the old pattern.
//
// ref: oapi-codegen pkg/codegen/templates/strict/strict-responses.tmpl@main
// ref: ADR docs/architecture/202605061500-adr-typed-response-envelope.md
//
// Wave lifecycle: this archtest is RED between Wave 2 (template fix) and Wave 3
// (go generate regenerate). Set VISIT_BUFFER_THEN_COMMIT_PENDING_REGEN=1 to
// skip during the interim build window. GREEN state is reached after Wave 3
// regenerates all generated/contracts/**/types_gen.go files.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestVisitBufferThenCommit(t *testing.T) {
	if os.Getenv("VISIT_BUFFER_THEN_COMMIT_PENDING_REGEN") != "" {
		t.Skip("pending Wave 3 regenerate; archtest will GREEN after go generate ./tools/codegen/...")
	}

	// Resolve repo root relative to this test file's location.
	// tools/archtest/ → ../../ = repo root.
	repoRoot, err := filepath.Abs("../../")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	generatedContracts := filepath.Join(repoRoot, "generated", "contracts")

	var typesFiles []string
	err = filepath.Walk(generatedContracts, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "/types_gen.go") {
			typesFiles = append(typesFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk generated/contracts: %v", err)
	}
	if len(typesFiles) == 0 {
		t.Skip("no generated/contracts/**/types_gen.go found — run go generate ./tools/codegen/... first")
	}

	for _, path := range typesFiles {
		path := path // capture
		t.Run(path, func(t *testing.T) {
			checkVisitBufferThenCommit(t, path)
		})
	}
}

// checkVisitBufferThenCommit asserts that every method named visit*Response
// whose receiver type ends in "JSONResponse" (the success body-bearing variant)
// follows the buffer-then-commit pattern: encode into bytes.Buffer first,
// then WriteHeader, then buf.WriteTo(w).
//
// Excludes NoContentResponse (无 body, 不存在 buffer-then-commit 场景) 与
// ErrorResponse (走 WriteErrorWithStatus 而非 visit-self-render；其完整性
// 由 5xx wire-code single-source archtest 与 typed envelope C18 链头守
// 共同覆盖)。
func checkVisitBufferThenCommit(t *testing.T, path string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		if !strings.HasPrefix(fn.Name.Name, "visit") {
			continue
		}
		// Only check success body-bearing variants: receiver type ends in JSONResponse.
		// Use scanner.ReceiverTypeName which accepts ast.Expr.
		recvIdent := scanner.ReceiverTypeName(fn.Recv.List[0].Type)
		if !strings.HasSuffix(recvIdent, "JSONResponse") {
			continue
		}
		if !hasBufferThenCommit(fn.Body) {
			pos := fset.Position(fn.Pos())
			t.Errorf("VISIT-BUFFER-THEN-COMMIT-01 violated at %s (%s.%s): "+
				"success visit method must encode to bytes.Buffer first, "+
				"then WriteHeader, then buf.WriteTo(w)",
				pos, recvIdent, fn.Name.Name)
		}
	}
}

// hasBufferThenCommit returns true iff the function body contains, in source
// order:
//  1. a bytes.Buffer declaration (var or composite literal assignment)
//  2. an Encode(...) call at a position strictly before WriteHeader
//  3. a WriteHeader call
//  4. a WriteTo(w) call at a position strictly after WriteHeader
func hasBufferThenCommit(body *ast.BlockStmt) bool {
	var encodePos, writeHeaderPos, writeToPos token.Pos
	var hasBuffer bool

	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.GenDecl:
			// detect: var <name> bytes.Buffer
			for _, spec := range x.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if isBytesBufferType(vs.Type) {
					hasBuffer = true
				}
			}
		case *ast.AssignStmt:
			// detect: <name> := bytes.Buffer{}
			for _, rhs := range x.Rhs {
				if isBytesBufferLiteral(rhs) {
					hasBuffer = true
				}
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Encode":
				// Track the latest Encode position (there should be only one,
				// but take last to be safe).
				if encodePos == 0 || x.Pos() > encodePos {
					encodePos = x.Pos()
				}
			case "WriteHeader":
				writeHeaderPos = x.Pos()
			case "WriteTo":
				writeToPos = x.Pos()
			}
		}
		return true
	})

	if !hasBuffer {
		return false
	}
	if encodePos == 0 || writeHeaderPos == 0 || writeToPos == 0 {
		return false
	}
	// Encode must precede WriteHeader, WriteTo must follow WriteHeader.
	if encodePos >= writeHeaderPos {
		return false
	}
	if writeToPos <= writeHeaderPos {
		return false
	}
	return true
}

func isBytesBufferType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "bytes" && sel.Sel.Name == "Buffer"
}

func isBytesBufferLiteral(expr ast.Expr) bool {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	return isBytesBufferType(cl.Type)
}
