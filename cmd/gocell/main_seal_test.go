package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestMainSealsLogsToStderr is a regression test for #1432 C1.
//
// cmd/gocell is a governance/codegen CLI: it writes MACHINE output (export
// JSON/YAML, validate/check/verify results, SARIF) to STDOUT. The redacting slog
// seal installed in main() must therefore direct diagnostics to os.Stderr — if it
// writes to stdout, graceful-degrade slog.Warn/Error (e.g. export's wire-summary /
// dep-graph scan failures) corrupt that machine data stream. Because
// logging.NewHandler defaults Writer to os.Stdout, main() MUST pass
// Writer: os.Stderr explicitly. This asserts the source shape so a revert to the
// stdout default fails loudly. (CLI convention stdout=data / stderr=diagnostics,
// cf. spf13/cobra OutOrStdout / ErrOrStderr.)
func TestMainSealsLogsToStderr(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	found, writerStderr := loggingSealWritesToStderr(f)
	if !found {
		t.Fatal("cmd/gocell/main.go: no logging.NewHandler(logging.Options{...}) seal found")
	}
	if !writerStderr {
		t.Error("cmd/gocell main() seal must set logging.Options.Writer: os.Stderr —" +
			" logging.NewHandler defaults to os.Stdout, which would corrupt machine output" +
			" (export JSON/YAML/SARIF) written to stdout (#1432 C1)")
	}
}

func loggingSealWritesToStderr(f *ast.File) (found bool, writerStderr bool) {
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := loggingNewHandlerOptions(n)
		if !ok {
			return true
		}
		found = true
		if optionsWriterIsStderr(lit) {
			writerStderr = true
		}
		return true
	})
	return found, writerStderr
}

func loggingNewHandlerOptions(n ast.Node) (*ast.CompositeLit, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "NewHandler" {
		return nil, false
	}
	if len(call.Args) != 1 {
		return nil, false
	}
	lit, ok := call.Args[0].(*ast.CompositeLit)
	return lit, ok
}

func optionsWriterIsStderr(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Writer" {
			continue
		}
		if selectorIsOSStderr(kv.Value) {
			return true
		}
	}
	return false
}

func selectorIsOSStderr(expr ast.Expr) bool {
	vsel, ok := expr.(*ast.SelectorExpr)
	if !ok || vsel.Sel == nil {
		return false
	}
	vx, ok := vsel.X.(*ast.Ident)
	return ok && vx.Name == "os" && vsel.Sel.Name == "Stderr"
}
