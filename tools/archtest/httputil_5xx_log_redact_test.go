// HTTPUTIL-5XX-LOG-REDACT-01 — log5xx 必须经 redaction.RedactSlogAttr 处理
// ecErr.Details 中的每个 slog.Attr 后才追加到 logAttrs。透传 raw Details 会
// 把 runtime 字段（potentially 含 dsn/token 等敏感）泄漏到 slog 输出。
//
// 检测方式（纯 AST）：扫描 pkg/httputil/response.go 中的 log5xx 函数体，
// 断言函数体内存在至少一次 redaction.RedactSlogAttr 调用。
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestHTTPUtil5xxLogRedact enforces HTTPUTIL-5XX-LOG-REDACT-01.
func TestHTTPUtil5xxLogRedact(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	targetFile := filepath.Join(root, "pkg", "httputil", "response.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, targetFile, nil, 0)
	if err != nil {
		t.Fatalf("HTTPUTIL-5XX-LOG-REDACT-01: parse %s: %v", targetFile, err)
	}

	var log5xxFn *ast.FuncDecl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "log5xx" {
			log5xxFn = fn
			break
		}
	}
	if log5xxFn == nil {
		t.Fatal("HTTPUTIL-5XX-LOG-REDACT-01: log5xx function not found in pkg/httputil/response.go")
	}

	found := false
	ast.Inspect(log5xxFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "redaction" && sel.Sel.Name == "RedactSlogAttr" {
			found = true
			return false
		}
		return true
	})

	if !found {
		t.Errorf(
			"HTTPUTIL-5XX-LOG-REDACT-01 violated: log5xx must call " +
				"redaction.RedactSlogAttr on ecErr.Details elements before " +
				"appending to slog attrs. Transparent pass-through leaks " +
				"runtime fields (dsn, token, etc.) to log backends.",
		)
	}
}
