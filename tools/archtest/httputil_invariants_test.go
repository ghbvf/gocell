package archtest

// invariants:
//   - INVARIANT: HTTPUTIL-5XX-KIND-NORMALIZE-01
//   - INVARIANT: HTTPUTIL-5XX-LOG-REDACT-01
//   - INVARIANT: HTTPUTIL-SURFACE-REGISTERED-01
//
// httputil_invariants_test.go — consolidated AST guards for pkg/httputil invariants.
//
// Invariants covered:
//   HTTPUTIL-5XX-KIND-NORMALIZE-01   errcode.New() in 5xx path must use errcode.KindXxx constant, not .Kind field access
//   HTTPUTIL-5XX-LOG-REDACT-01       log5xx must call redaction.RedactSlogAttr on ecErr.Details before appending to slog attrs
//   HTTPUTIL-SURFACE-REGISTERED-01   every exported pkg/httputil function must appear in doc.go or governance maps

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
)

// httputilResponseGoScope returns a DirsScope restricted to pkg/httputil/response.go.
func httputilResponseGoScope(root string) Scope {
	return DirsScope(root, []string{"pkg/httputil"},
		MatchRels(func(rel string) bool {
			return filepath.ToSlash(rel) == "pkg/httputil/response.go"
		}),
	)
}

// INVARIANT: HTTPUTIL-5XX-KIND-NORMALIZE-01
//
// TestHTTPUtil5xxKindNormalize enforces HTTPUTIL-5XX-KIND-NORMALIZE-01.
//
// pkg/httputil/response.go の 5xx 路径必须 normalize errcode.Error.Kind 为与 status
// 匹配的 5xx Kind，禁止透传 ecErr.Kind。
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
func TestHTTPUtil5xxKindNormalize(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	targetFuncs := map[string]bool{
		"WriteErrorWithStatus": true,
		"writeErrcodeError":    true,
	}

	diags := Run(t, httputilResponseGoScope(root), func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if !targetFuncs[fn.Name.Name] {
					return
				}
				EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
					if len(call.Args) < 1 {
						return
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					// Match errcode.New(...) calls only.
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "errcode" || sel.Sel.Name != "New" {
						return
					}
					// First argument must be an errcode.KindXxx selector expression,
					// not ecErr.Kind or any .Kind field access.
					firstArg := call.Args[0]
					argSel, ok := firstArg.(*ast.SelectorExpr)
					if !ok {
						// First arg is not a selector — could be a variable. Only
						// flag the specific anti-pattern of `.Kind` field access.
						return
					}
					// Detect the anti-pattern: <anything>.Kind
					if argSel.Sel.Name == "Kind" {
						pos := p.Fset.Position(call.Pos())
						argIdent, _ := argSel.X.(*ast.Ident)
						argText := ""
						if argIdent != nil {
							argText = argIdent.Name + "." + argSel.Sel.Name
						}
						ds = append(ds, Diagnostic{
							Rel:  p.Rel(f),
							Line: pos.Line,
							Message: "errcode.New() in " + fn.Name.Name + " passes " + argText + " as Kind; " +
								"must use errcode.KindXxx constant, not a .Kind field access. " +
								"Transparent Kind pass-through allows 4xx-Kind ecErr to bypass " +
								"MarshalJSON's IsClient() Details-strip for 5xx wire bodies.",
						})
						return
					}
					// Verify positive form: must start with "errcode.Kind".
					argPkgIdent, ok := argSel.X.(*ast.Ident)
					if !ok {
						return
					}
					argText := argPkgIdent.Name + "." + argSel.Sel.Name
					if argPkgIdent.Name == "errcode" && strings.HasPrefix(argSel.Sel.Name, "Kind") {
						return // OK, normalized constant
					}
					pos := p.Fset.Position(call.Pos())
					ds = append(ds, Diagnostic{
						Rel:  p.Rel(f),
						Line: pos.Line,
						Message: "errcode.New() in " + fn.Name.Name + " passes " + argText + " as Kind; " +
							"must use an errcode.KindXxx constant.",
					})
				})
			})
		}
		return ds
	})
	Report(t, "HTTPUTIL-5XX-KIND-NORMALIZE-01", diags)
}

// INVARIANT: HTTPUTIL-5XX-LOG-REDACT-01
//
// TestHTTPUtil5xxLogRedact enforces HTTPUTIL-5XX-LOG-REDACT-01.
//
// log5xx 必须经 redaction.RedactSlogAttr 处理 ecErr.Details 中的每个 slog.Attr
// 后才追加到 logAttrs。透传 raw Details 会把 runtime 字段（potentially 含
// dsn/token 等敏感）泄漏到 slog 输出。
//
// 检测方式（纯 AST）：扫描 pkg/httputil/response.go 中的 log5xx 函数体，
// 断言函数体内存在至少一次 redaction.RedactSlogAttr 调用。
func TestHTTPUtil5xxLogRedact(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	diags := Run(t, httputilResponseGoScope(root), func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			var log5xxFn *ast.FuncDecl
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if log5xxFn == nil && fn.Name.Name == "log5xx" {
					log5xxFn = fn
				}
			})
			if log5xxFn == nil {
				ds = append(ds, Diagnostic{
					Rel:     p.Rel(f),
					Line:    0,
					Message: "log5xx function not found in pkg/httputil/response.go",
				})
				continue
			}

			found := false
			EachInSubtree[ast.CallExpr](log5xxFn.Body, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return
				}
				if ident.Name == "redaction" && sel.Sel.Name == "RedactSlogAttr" {
					found = true
				}
			})

			if !found {
				ds = append(ds, Diagnostic{
					Rel:  p.Rel(f),
					Line: 0,
					Message: "log5xx must call redaction.RedactSlogAttr on ecErr.Details elements before " +
						"appending to slog attrs. Transparent pass-through leaks " +
						"runtime fields (dsn, token, etc.) to log backends.",
				})
			}
		}
		return ds
	})
	Report(t, "HTTPUTIL-5XX-LOG-REDACT-01", diags)
}

// INVARIANT: HTTPUTIL-SURFACE-REGISTERED-01
//
// TestHttputilExportedRegistry enforces HTTPUTIL-SURFACE-REGISTERED-01: every
// exported function in pkg/httputil must appear in at least one of the three
// authority tables:
//
//  1. pkg/httputil/doc.go Stable Surface comment (pattern: "  - FuncName")
//  2. kernel/governance/rules_http.go httpHelperWritesStatuses map
//  3. kernel/governance/rules_http.go knownNonWriters map (inline)
//
// This ensures that when a new exported function is added to pkg/httputil, the
// author is forced to register it in either the doc surface or the governance
// allowlist — preventing silent drift between the documented API surface and
// the actual exported surface.
func TestHttputilExportedRegistry(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	docGoPath := filepath.Join(root, "pkg", "httputil", "doc.go")
	governancePath := filepath.Join(root, "kernel", "governance", "rules_http.go")

	// 1. Collect all exported functions from pkg/httputil (excluding test files).
	exported := collectExportedFuncs(t, root, "pkg/httputil")

	// 2. Collect names registered in doc.go Stable Surface comment.
	docRegistered := collectDocRegistered(t, docGoPath)

	// 3. Collect names registered in governance maps (httpHelperWritesStatuses + knownNonWriters).
	governanceRegistered := collectGovernanceRegistered(t, governancePath)

	// 4. Collect every unregistered exported func as a Diagnostic and report via Report.
	var diags []Diagnostic
	for fn := range exported {
		inDoc := docRegistered[fn]
		inGov := governanceRegistered[fn]
		if !inDoc && !inGov {
			diags = append(diags, Diagnostic{
				Rel:  "pkg/httputil",
				Line: 0,
				Message: "exported function " + fn + " is not registered in " +
					"pkg/httputil/doc.go Stable Surface OR kernel/governance maps — " +
					"add it to pkg/httputil/doc.go and/or kernel/governance/rules_http.go",
			})
		}
	}
	Report(t, "HTTPUTIL-SURFACE-REGISTERED-01", diags)
}

// collectExportedFuncs returns a set of top-level exported function names
// declared in non-test .go files under root/dirRel (single dir, not recursive
// into sub-packages — same shape as the original os.ReadDir loop).
func collectExportedFuncs(t *testing.T, root, dirRel string) map[string]bool {
	t.Helper()
	scope := DirsScope(root, []string{dirRel},
		MatchRels(func(rel string) bool {
			// Single-dir semantics: only files directly under dirRel, no sub-pkgs.
			return filepath.ToSlash(filepath.Dir(rel)) == filepath.ToSlash(dirRel)
		}),
	)
	result := make(map[string]bool)
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Recv != nil {
					// skip methods — only top-level functions
					return
				}
				name := fn.Name.Name
				if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
					result[name] = true
				}
			})
		}
		return nil
	})
	return result
}

// collectDocRegistered extracts function names from the Stable Surface section
// of doc.go. It matches lines of the form "  - FuncName" (with optional args).
func collectDocRegistered(t *testing.T, path string) map[string]bool {
	t.Helper()
	content := fileutil.MustReadFile(t, path)
	result := make(map[string]bool)
	for _, line := range strings.Split(string(content), "\n") {
		// Match "//   - FuncName" or "//   - FuncName(...)"
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "//")
		trimmed = strings.TrimSpace(trimmed)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "- ")
		// Function name is the identifier before '(' or space or end
		name := rest
		if idx := strings.IndexAny(rest, "( "); idx >= 0 {
			name = rest[:idx]
		}
		if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
			result[name] = true
		}
	}
	return result
}

// collectGovernanceRegistered extracts string literal keys from
// httpHelperWritesStatuses and knownNonWriters map literals in the governance
// file. Simple string-scanning approach (no full AST) — robust enough for
// stable map literals with one entry per line.
func collectGovernanceRegistered(t *testing.T, path string) map[string]bool {
	t.Helper()
	content := fileutil.MustReadFile(t, path)
	result := make(map[string]bool)
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		// Lines like: "WriteError": ... or "DecodeJSON": ...
		if !strings.HasPrefix(trimmed, `"`) {
			continue
		}
		end := strings.Index(trimmed[1:], `"`)
		if end < 0 {
			continue
		}
		key := trimmed[1 : end+1]
		if len(key) > 0 && key[0] >= 'A' && key[0] <= 'Z' {
			result[key] = true
		}
	}
	return result
}
