package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPanicLogMustUseRedactAny enforces that every slog.Any("panic", X) call
// in production code wraps X with redaction.RedactAny(...). This prevents
// panic values containing DSNs, tokens, or credentials from reaching log sinks
// un-redacted.
//
// Rule ID: PANIC-REDACT-01
// Wave 0: fails against the current codebase (11 violations in Wave 0).
// Wave 3: all violations remediated; white-list stays empty permanently.
func TestPanicLogMustUseRedactAny(t *testing.T) {
	repoRoot := findRepoRootPanicRedact(t)
	fset := token.NewFileSet()
	var violations []string

	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == "vendor" || base == "node_modules" || base == ".git" || base == "worktrees" || base == "generated" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			//nolint:nilerr // unparseable files (generated, vendored 3p, broken WIP) are not the archtest's concern
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Any" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "slog" {
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			// First arg must be string literal "panic".
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || lit.Value != `"panic"` {
				return true
			}
			// Second arg must be a call to redaction.RedactAny(...).
			arg := call.Args[1]
			argCall, ok := arg.(*ast.CallExpr)
			if !ok {
				violations = append(violations, panicRedactPosStr(fset, call.Pos()))
				return true
			}
			argSel, ok := argCall.Fun.(*ast.SelectorExpr)
			if !ok || argSel.Sel.Name != "RedactAny" {
				violations = append(violations, panicRedactPosStr(fset, call.Pos()))
				return true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(violations) > 0 {
		t.Errorf("slog.Any(\"panic\", X) must wrap X with redaction.RedactAny(...). %d violation(s):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func panicRedactPosStr(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return p.String()
}

func findRepoRootPanicRedact(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		fi, e := os.Stat(filepath.Join(dir, "go.mod"))
		if e == nil && !fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found — cannot determine repo root")
		}
		dir = parent
	}
}
