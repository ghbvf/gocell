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

	oldStateHelper := "with" + "Cell" + "IDState"
	var readsCtxCellID, usesRuntimeSentinel, callsOldState bool
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == oldStateHelper {
				callsOldState = true
			}
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CellIDFrom" {
				readsCtxCellID = true
			}
		case *ast.Ident:
			if v.Name == "RuntimeCellIDSentinel" {
				usesRuntimeSentinel = true
			}
		}
		return true
	})

	assert.Truef(t, readsCtxCellID,
		"%s: %s — middleware.Metrics must read cell labels from kernel/ctxkeys.CellIDFrom",
		rel, ruleHTTPMetricsLabelCtxSource01)
	assert.Truef(t, usesRuntimeSentinel,
		"%s: %s — middleware.Metrics must default missing cell context to RuntimeCellIDSentinel",
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

	var hasCellAttribution, hasMountRouteGroup bool
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CellAttribution" {
				hasCellAttribution = true
			}
		case *ast.FuncDecl:
			if v.Recv != nil && v.Name.Name == "MountRouteGroup" {
				hasMountRouteGroup = true
			}
		}
		return true
	})

	assert.Truef(t, hasCellAttribution,
		"%s: %s — router root must install middleware.CellAttribution before protection middleware",
		rel, ruleHTTPMetricsLabelRouterAttribution)
	assert.Truef(t, hasMountRouteGroup,
		"%s: %s — RouteGroup mounting must be owned by runtime/http/router, not bootstrap helpers",
		rel, ruleHTTPMetricsLabelRouterAttribution)
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
