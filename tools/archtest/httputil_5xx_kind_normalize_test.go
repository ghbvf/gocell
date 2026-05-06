// HTTPUTIL-5XX-KIND-NORMALIZE-01 — pkg/httputil/response.go 的 5xx 路径必须 normalize
// errcode.Error.Kind 为与 status 匹配的 5xx Kind，禁止透传 ecErr.Kind。
//
// 反例：out = errcode.New(ecErr.Kind, publicCode, msg)
// 正例：out = errcode.New(errcode.KindUnavailable, publicCode, msg)
//
// 透传 ecErr.Kind 的隐性炸弹：若 ecErr.Kind 为 4xx (如 KindNotFound)，
// MarshalJSON 的 IsClient() 返回 true，Details 不会 strip，5xx wire body
// 可能泄漏 runtime 字段。
//
// 检测方式（纯 AST）：扫描 WriteErrorWithStatus 和 writeErrcodeError 函数体，
// 找到 5xx 分支内 errcode.New(...) 调用，断言第一参数是 errcode.KindXxx 常量选择器
// 而非 ecErr.Kind（或任何形式的 .Kind 字段读取）。
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestHTTPUtil5xxKindNormalize enforces HTTPUTIL-5XX-KIND-NORMALIZE-01.
func TestHTTPUtil5xxKindNormalize(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	targetFile := filepath.Join(root, "pkg", "httputil", "response.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, targetFile, nil, 0)
	if err != nil {
		t.Fatalf("HTTPUTIL-5XX-KIND-NORMALIZE-01: parse %s: %v", targetFile, err)
	}

	targetFuncs := map[string]bool{
		"WriteErrorWithStatus": true,
		"writeErrcodeError":    true,
	}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !targetFuncs[fn.Name.Name] {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Match errcode.New(...) calls only.
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "errcode" || sel.Sel.Name != "New" {
				return true
			}
			// First argument must be an errcode.KindXxx selector expression,
			// not ecErr.Kind or any .Kind field access.
			firstArg := call.Args[0]
			argSel, ok := firstArg.(*ast.SelectorExpr)
			if !ok {
				// First arg is not a selector — could be a variable. Only
				// flag the specific anti-pattern of `.Kind` field access.
				return true
			}
			// Detect the anti-pattern: <anything>.Kind
			if argSel.Sel.Name == "Kind" {
				pos := fset.Position(call.Pos())
				t.Errorf(
					"HTTPUTIL-5XX-KIND-NORMALIZE-01 violated at %s: "+
						"errcode.New() in %s passes %q as Kind; "+
						"must use errcode.KindXxx constant, not a .Kind field access. "+
						"Transparent Kind pass-through allows 4xx-Kind ecErr to bypass "+
						"MarshalJSON's IsClient() Details-strip for 5xx wire bodies.",
					pos, fn.Name.Name,
					argSel.X.(*ast.Ident).Name+"."+argSel.Sel.Name,
				)
				return true
			}
			// Verify positive form: must start with "errcode.Kind".
			argPkgIdent, ok := argSel.X.(*ast.Ident)
			if !ok {
				return true
			}
			argText := argPkgIdent.Name + "." + argSel.Sel.Name
			if argPkgIdent.Name == "errcode" && strings.HasPrefix(argSel.Sel.Name, "Kind") {
				return true // OK, normalized constant
			}
			pos := fset.Position(call.Pos())
			t.Errorf(
				"HTTPUTIL-5XX-KIND-NORMALIZE-01 violated at %s: "+
					"errcode.New() in %s passes %q as Kind; "+
					"must use an errcode.KindXxx constant.",
				pos, fn.Name.Name, argText,
			)
			return true
		})
	}
}
