package archtest

// invariants:
//   - INVARIANT: HTTPUTIL-5XX-KIND-NORMALIZE-01
//   - INVARIANT: HTTPUTIL-SURFACE-REGISTERED-01
//   - INVARIANT: HTTPUTIL-5XX-LOG-REDACT-01
//
// httputil_invariants_test.go — consolidated AST guards for pkg/httputil invariants.
//
// Invariants covered:
//   HTTPUTIL-5XX-KIND-NORMALIZE-01   errcode.New() in 5xx path must use errcode.KindXxx constant, not .Kind field access
//   HTTPUTIL-SURFACE-REGISTERED-01   every exported pkg/httputil function must appear in doc.go or governance maps
//   HTTPUTIL-5XX-LOG-REDACT-01       every Detail AsSlogAttr() in log4xx/log5xx must be wrapped in redaction.RedactSlogAttr
//
// HTTPUTIL-5XX-LOG-REDACT-01 was retired as a Soft "RedactSlogAttr appears" check
// in PR #1036 Batch 2 (sink-side redaction superseded its value-redaction role).
// It is RESTORED here (#1432) as a NARROW Medium form-lock — not the old global
// Soft form — to keep the call-site defense-in-depth from silently regressing in
// the two named functions (log4xx / log5xx) that log error Details: those run in
// library code that may execute before any process-global slog seal is installed
// (pkg/httputil is imported by tests / tools), so the call-site wrap is the only
// protection there. The lock binds every `d.AsSlogAttr()` call in those two
// functions to a `redaction.RedactSlogAttr(...)` wrapper (AST form-lock); a bare
// AsSlogAttr append is flagged.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// runHTTPUtilResponseRule runs rule against httputilResponseGoScope and guards
// against silent-pass when pkg/httputil/response.go is absent (renamed, deleted,
// or scope drift). Symmetric to the parser.ParseFile fail-loud behavior that
// preceded the DirsScope migration.
//
// Pattern mirrors refresh_invariants_test.go TestRefreshCrossStoreTX01 /
// TestRefreshAmbientTX01: foundFile flag set inside Run callback, require.True
// asserted after Run, before Report.
func runHTTPUtilResponseRule(t *testing.T, ruleID string, rule func(*Pass) []Diagnostic) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	const targetRel = "pkg/httputil/response.go"
	var foundFile bool
	diags := Run(t, httputilResponseGoScope(root), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			if filepath.ToSlash(p.Rel(f)) == targetRel {
				foundFile = true
			}
		}
		return rule(p)
	})
	require.True(t, foundFile, "%s: %s not found (renamed/deleted/scope drift)", ruleID, targetRel)
	return diags
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

	targetFuncs := map[string]bool{
		"WriteErrorWithStatus": true,
		"writeErrcodeError":    true,
	}

	diags := runHTTPUtilResponseRule(t, "HTTPUTIL-5XX-KIND-NORMALIZE-01", func(p *Pass) []Diagnostic {
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

// httputilLogRedactTargets are the two named functions that log error Details
// and must wrap every AsSlogAttr() in redaction.RedactSlogAttr (HTTPUTIL-5XX-LOG-REDACT-01).
var httputilLogRedactTargets = map[string]bool{
	"log4xx": true,
	"log5xx": true,
}

// httputilLogRedactViolations is the HTTPUTIL-5XX-LOG-REDACT-01 detector: within
// each target function, every `<d>.AsSlogAttr()` call must be a direct argument
// of a `redaction.RedactSlogAttr(...)` call. A bare AsSlogAttr append (no wrap)
// is flagged. Shared by the production rule and the reverse self-check.
func httputilLogRedactViolations(p *Pass, f *ast.File) []Diagnostic {
	redactionLocal := redactionLocalName(f)
	var ds []Diagnostic
	EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Body == nil || !httputilLogRedactTargets[fn.Name.Name] {
			return
		}
		// Collect AsSlogAttr() CallExprs that are direct args of redaction.RedactSlogAttr(...).
		wrapped := make(map[*ast.CallExpr]bool)
		EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			if redactionLocal == "" || !callMatches(call, redactionLocal, slogFunnelRedactSlogAttrFunc) {
				return
			}
			// Direct CallExpr children of this RedactSlogAttr(...) call are its
			// argument calls (its Fun is a SelectorExpr, not a CallExpr).
			EachInChildren[ast.CallExpr](call, func(ac *ast.CallExpr) {
				wrapped[ac] = true
			})
		})
		EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "AsSlogAttr" {
				return
			}
			if wrapped[call] {
				return
			}
			pos := p.Fset.Position(call.Pos())
			ds = append(ds, Diagnostic{
				Rel:  filepath.ToSlash(p.Rel(f)),
				Line: pos.Line,
				Message: "AsSlogAttr() in " + fn.Name.Name + " must be wrapped in" +
					" redaction.RedactSlogAttr(...) — call-site defense-in-depth must not" +
					" regress (HTTPUTIL-5XX-LOG-REDACT-01)",
			})
		})
	})
	return ds
}

// INVARIANT: HTTPUTIL-5XX-LOG-REDACT-01
//
// TestHTTPUtil5xxLogRedact enforces HTTPUTIL-5XX-LOG-REDACT-01: log4xx and log5xx
// in pkg/httputil/response.go must wrap every error-Detail AsSlogAttr() in
// redaction.RedactSlogAttr. AI-robust rating: Medium (AST form-lock scoped to two
// named functions, callee resolved via the redaction import local name; not the
// retired global Soft form). See file-header note for the defense-in-depth rationale.
func TestHTTPUtil5xxLogRedact(t *testing.T) {
	t.Parallel()
	diags := runHTTPUtilResponseRule(t, "HTTPUTIL-5XX-LOG-REDACT-01", func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			ds = append(ds, httputilLogRedactViolations(p, f)...)
		}
		return ds
	})
	Report(t, "HTTPUTIL-5XX-LOG-REDACT-01", diags)
}

// TestHTTPUtil5xxLogRedact_DetectsViolation is the reverse self-check: a synthetic
// log5xx whose AsSlogAttr() append drops the redaction.RedactSlogAttr wrap must be
// flagged; the wrapped form must not be.
func TestHTTPUtil5xxLogRedact_DetectsViolation(t *testing.T) {
	t.Parallel()

	const redactionImport = `"github.com/ghbvf/gocell/pkg/redaction"`
	cases := map[string]struct {
		src     string
		wantVio bool
		desc    string
	}{
		"bare_asslogattr": {
			src: `package httputil
import (
	"log/slog"
	` + redactionImport + `
)
func log5xx(ecErr fakeErr) {
	logAttrs := []any{}
	for _, d := range ecErr.Details {
		logAttrs = append(logAttrs, d.AsSlogAttr())  // VIOLATION: not wrapped
	}
	_ = logAttrs
	_ = slog.Int
	_ = redaction.Mask
}
`,
			wantVio: true,
			desc:    "bare d.AsSlogAttr() append (no RedactSlogAttr wrap) must be flagged",
		},
		"wrapped_asslogattr": {
			src: `package httputil
import (
	"log/slog"
	` + redactionImport + `
)
func log5xx(ecErr fakeErr) {
	logAttrs := []any{}
	for _, d := range ecErr.Details {
		logAttrs = append(logAttrs, redaction.RedactSlogAttr(d.AsSlogAttr()))
	}
	_ = logAttrs
	_ = slog.Int
}
`,
			wantVio: false,
			desc:    "wrapped redaction.RedactSlogAttr(d.AsSlogAttr()) is compliant",
		},
	}

	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "response.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, "fixture must parse: %s", tc.desc)
			p := &Pass{
				Fset:  fset,
				Files: []*ast.File{file},
				Rel:   func(*ast.File) string { return "pkg/httputil/response.go" },
			}
			diags := httputilLogRedactViolations(p, file)
			if tc.wantVio {
				assert.NotEmpty(t, diags, "must detect %q: %s", name, tc.desc)
			} else {
				assert.Empty(t, diags, "must not flag %q: %s; got %v", name, tc.desc, diags)
			}
		})
	}
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
