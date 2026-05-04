package archtest

// http_metrics_label_test.go enforces the HTTP-METRICS-LABEL-REALIGN
// contract (D1, 2026-05-04): cell identity is a router-root request
// attribution concern, not a metrics collector constructor field and not a
// RouteGroup handler-middleware side effect.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleHTTPMetricsLabelCtxSource01       = "HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01"
	ruleHTTPMetricsLabelNoAssemblyDerive  = "HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01"
	ruleHTTPMetricsLabelNoConfigCellID    = "HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01"
	ruleHTTPMetricsLabelRuntimeSentinel   = "HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01"
	ruleHTTPMetricsLabelRouterAttribution = "HTTP-METRICS-LABEL-ROUTER-ATTRIBUTION-01"
)

func TestHTTPMetricsLabelCellIDCtxSource01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "http", "middleware", "metrics.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	fn := findHTTPMetricsFuncDecl(t, file, "metricsWithClock")
	metricsPath := narrowestFuncLitWithCollectorRecordRequest(fn.Body)
	require.NotNilf(t, metricsPath,
		"%s: %s — metricsWithClock must record HTTP metrics through collector.RecordRequest",
		rel, ruleHTTPMetricsLabelCtxSource01)

	oldStateHelper := "with" + "Cell" + "IDState"
	var callsOldState bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == oldStateHelper {
			callsOldState = true
		}
		return true
	})

	var (
		readsCtxCellID      bool
		usesRuntimeSentinel bool
		recordUsesCellIDArg bool
		ctxCellIDPos        token.Pos
		runtimeSentinelPos  token.Pos
		recordRequestPos    token.Pos
	)
	ast.Inspect(metricsPath.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if isSelectorCall(v, "ctxkeys", "CellIDFrom") {
				readsCtxCellID = true
				rememberFirstPos(&ctxCellIDPos, v.Pos())
			}
			if isSelectorCall(v, "collector", "RecordRequest") {
				recordRequestPos = v.Pos()
				if len(v.Args) > 0 {
					if id, ok := v.Args[0].(*ast.Ident); ok && id.Name == "cellID" {
						recordUsesCellIDArg = true
					}
				}
			}
		case *ast.Ident:
			if v.Name == "RuntimeCellIDSentinel" {
				usesRuntimeSentinel = true
				rememberFirstPos(&runtimeSentinelPos, v.Pos())
			}
		}
		return true
	})

	assert.Truef(t, readsCtxCellID,
		"%s: %s — middleware.Metrics must read cell labels from kernel/ctxkeys.CellIDFrom",
		rel, ruleHTTPMetricsLabelCtxSource01)
	assert.Truef(t, usesRuntimeSentinel,
		"%s: %s — middleware.Metrics must default missing cell context to RuntimeCellIDSentinel in the RecordRequest path",
		rel, ruleHTTPMetricsLabelRuntimeSentinel)
	assert.Truef(t, recordUsesCellIDArg,
		"%s: %s — collector.RecordRequest must receive the ctx-derived cellID variable, not a constructor/config value",
		rel, ruleHTTPMetricsLabelCtxSource01)
	assert.Truef(t, ctxCellIDPos.IsValid() && recordRequestPos.IsValid() && ctxCellIDPos < recordRequestPos,
		"%s: %s — ctxkeys.CellIDFrom must feed the metrics path before collector.RecordRequest",
		rel, ruleHTTPMetricsLabelCtxSource01)
	assert.Truef(t, runtimeSentinelPos.IsValid() && recordRequestPos.IsValid() && runtimeSentinelPos < recordRequestPos,
		"%s: %s — RuntimeCellIDSentinel must be the fallback before collector.RecordRequest",
		rel, ruleHTTPMetricsLabelRuntimeSentinel)
	assert.Falsef(t, callsOldState,
		"%s: %s — old mutable cell helper is deleted; cell attribution must happen at router root",
		rel, ruleHTTPMetricsLabelCtxSource01)
}

func TestHTTPMetricsLabelRouterAttribution01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "http", "router", "router.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	buildMux := findHTTPMetricsFuncDecl(t, file, "buildMux")
	mountRouteGroup := findHTTPMetricsFuncDecl(t, file, "MountRouteGroup")
	require.NotNilf(t, mountRouteGroup,
		"%s: %s — RouteGroup mounting must be owned by runtime/http/router, not bootstrap helpers",
		rel, ruleHTTPMetricsLabelRouterAttribution)

	var (
		cellAttributionPos token.Pos
		metricsPos         token.Pos
		defaultMWPos       token.Pos
		rateLimitPos       token.Pos
		circuitBreakerPos  token.Pos
		authPos            token.Pos
		bodyLimitPos       token.Pos
	)
	ast.Inspect(buildMux.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch {
		case isSelectorCall(call, "middleware", "CellAttribution"):
			rememberFirstPos(&cellAttributionPos, call.Pos())
		case isSelectorCall(call, "middleware", "Metrics"):
			rememberFirstPos(&metricsPos, call.Pos())
		case isSelectorCall(call, "middleware", "RateLimit"):
			rememberFirstPos(&rateLimitPos, call.Pos())
		case isSelectorCall(call, "middleware", "CircuitBreaker"):
			rememberFirstPos(&circuitBreakerPos, call.Pos())
		case isSelectorCall(call, "auth", "AuthMiddleware"):
			rememberFirstPos(&authPos, call.Pos())
		case isSelectorCall(call, "middleware", "BodyLimit"):
			rememberFirstPos(&bodyLimitPos, call.Pos())
		case isMuxUseWithDefaultMiddleware(call):
			rememberFirstPos(&defaultMWPos, call.Pos())
		}
		return true
	})

	require.Truef(t, cellAttributionPos.IsValid(),
		"%s: %s — buildMux must install middleware.CellAttribution at router root",
		rel, ruleHTTPMetricsLabelRouterAttribution)
	for _, check := range []struct {
		name string
		pos  token.Pos
	}{
		{name: "middleware.Metrics", pos: metricsPos},
		{name: "r.defaultMiddleware", pos: defaultMWPos},
		{name: "middleware.RateLimit", pos: rateLimitPos},
		{name: "middleware.CircuitBreaker", pos: circuitBreakerPos},
		{name: "auth.AuthMiddleware", pos: authPos},
		{name: "middleware.BodyLimit", pos: bodyLimitPos},
	} {
		assert.Truef(t, check.pos.IsValid(),
			"%s: %s — buildMux must wire %s in the router chain",
			rel, ruleHTTPMetricsLabelRouterAttribution, check.name)
		assert.Truef(t, check.pos.IsValid() && cellAttributionPos < check.pos,
			"%s: %s — middleware.CellAttribution must be installed before %s so short-circuits keep the owning cell label",
			rel, ruleHTTPMetricsLabelRouterAttribution, check.name)
	}
}

func TestHTTPMetricsLabelNoAssemblyDerive01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "bootstrap", "phases_http.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if ok && f.Name.Name == "autoWireHTTPMetricsCollector" {
			fn = f
			break
		}
	}
	require.NotNilf(t, fn, "%s: autoWireHTTPMetricsCollector func not found", rel)

	var referencesAssemblyID, referencesAssemblyCoreID, referencesDefaultLiteral, referencesProviderCellKey bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := v.X.(*ast.Ident); ok && id.Name == "b" && v.Sel.Name == "assemblyID" {
				referencesAssemblyID = true
			}
			if call, ok := v.X.(*ast.SelectorExpr); ok {
				if id, ok := call.X.(*ast.Ident); ok && id.Name == "b" && call.Sel.Name == "assemblyCore" && v.Sel.Name == "ID" {
					referencesAssemblyCoreID = true
				}
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING && v.Value == `"default"` {
				referencesDefaultLiteral = true
			}
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok && id.Name == "CellID" {
				referencesProviderCellKey = true
			}
		}
		return true
	})

	assert.Falsef(t, referencesAssemblyID,
		"%s: %s — autoWireHTTPMetricsCollector must not reference b.assemblyID as a cell label source",
		rel, ruleHTTPMetricsLabelNoAssemblyDerive)
	assert.Falsef(t, referencesAssemblyCoreID,
		"%s: %s — autoWireHTTPMetricsCollector must not call b.assemblyCore.ID() as a cell label source",
		rel, ruleHTTPMetricsLabelNoAssemblyDerive)
	assert.Falsef(t, referencesDefaultLiteral,
		`%s: %s — autoWireHTTPMetricsCollector must not use literal "default" as a fallback cell label`,
		rel, ruleHTTPMetricsLabelNoAssemblyDerive)
	assert.Falsef(t, referencesProviderCellKey,
		"%s: %s — ProviderCollectorConfig{CellID: ...} is forbidden; cellID is per request",
		rel, ruleHTTPMetricsLabelNoAssemblyDerive)
}

func TestHTTPMetricsLabelNoConfigCellID01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "observability", "metrics", "provider_collector.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	hasCellIDField := false
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.TYPE {
			return true
		}
		for _, spec := range decl.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "ProviderCollectorConfig" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, f := range st.Fields.List {
				for _, name := range f.Names {
					if name.Name == "CellID" {
						hasCellIDField = true
					}
				}
			}
		}
		return true
	})
	assert.Falsef(t, hasCellIDField,
		"%s: %s — ProviderCollectorConfig must not declare CellID; cellID is supplied per RecordRequest",
		rel, ruleHTTPMetricsLabelNoConfigCellID)
}

func slashRel(t *testing.T, root, target string) string {
	t.Helper()
	rel, err := filepath.Rel(root, target)
	require.NoError(t, err)
	return filepath.ToSlash(rel)
}

func findHTTPMetricsFuncDecl(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	require.Failf(t, "function not found", "expected function %s", name)
	return nil
}

func narrowestFuncLitWithCollectorRecordRequest(root ast.Node) *ast.FuncLit {
	var best *ast.FuncLit
	ast.Inspect(root, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		if !containsCollectorRecordRequest(fn.Body) {
			return true
		}
		if best == nil || fn.End()-fn.Pos() < best.End()-best.Pos() {
			best = fn
		}
		return true
	})
	return best
}

func containsCollectorRecordRequest(root ast.Node) bool {
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if ok && isSelectorCall(call, "collector", "RecordRequest") {
			found = true
			return false
		}
		return true
	})
	return found
}

func isSelectorCall(call *ast.CallExpr, qualifier, method string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return selectorQualifier(sel.X) == qualifier && sel.Sel.Name == method
}

func selectorQualifier(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		left := selectorQualifier(v.X)
		if left == "" {
			return v.Sel.Name
		}
		return left + "." + v.Sel.Name
	default:
		return ""
	}
}

func isMuxUseWithDefaultMiddleware(call *ast.CallExpr) bool {
	if !isSelectorCall(call, "r", "use") {
		return false
	}
	for _, arg := range call.Args {
		if selectorQualifier(arg) == "r.defaultMiddleware" {
			return true
		}
	}
	return false
}

func rememberFirstPos(dst *token.Pos, pos token.Pos) {
	if !dst.IsValid() || pos < *dst {
		*dst = pos
	}
}
