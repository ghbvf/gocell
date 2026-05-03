package archtest

// http_metrics_label_test.go enforces the four invariants of the
// HTTP-METRICS-LABEL-REALIGN contract (D1, 2026-05-04):
//
//   - HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01:
//     runtime/http/middleware/metrics.go must read the cell label from ctx via
//     ctxkeys.MustCellIDFrom — proving there is no fallback branch and no
//     instance-level cellID. A revert that pulls cellID from a struct field or
//     adds an `if cellID == "" { cellID = "_runtime" }` fallback would silently
//     erode the per-request label guarantee; this rule blocks it.
//
//   - HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01:
//     runtime/bootstrap/phases_http.go must not derive any metrics cellID from
//     b.assemblyID, b.assemblyCore.ID(), or the literal "default". Pre-realign
//     code did exactly this and broke multi-cell assembly attribution; the new
//     contract is "cellID flows from RouteGroup.CellID via ctx, never from
//     assembly identity".
//
//   - HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01:
//     runtime/observability/metrics.ProviderCollectorConfig must not contain a
//     CellID field. Holding cellID on the collector instance rather than per
//     RecordRequest call is the architectural pattern this PR removes.
//
//   - HTTP-METRICS-LABEL-LISTENER-ROOT-RUNTIME-01:
//     runtime/http/router/router.go must install
//     middleware.WithCellIDContext("_runtime") before middleware.Metrics on the
//     listener root mux. This is the *single* listener-root sentinel injection
//     point — bootstrap and other layers no longer inject any fallback. If
//     this call is removed or moved (e.g. someone tries to install the
//     sentinel inside bootstrap or inside Metrics itself), unmatched and
//     framework-owned requests would emit an empty cell label, breaking
//     dashboards.
//
// All four rules use go/ast so reorderings, alias renames, and indirect
// imports are detected even without changing the surface API.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleHTTPMetricsLabelCtxSource01      = "HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01"
	ruleHTTPMetricsLabelNoAssemblyDerive = "HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01"
	ruleHTTPMetricsLabelNoConfigCellID   = "HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01"
	ruleHTTPMetricsLabelListenerRoot     = "HTTP-METRICS-LABEL-LISTENER-ROOT-RUNTIME-01"

	httpMetricsRuntimeSentinelLiteral = `"_runtime"`
)

// TestHTTPMetricsLabelCellIDCtxSource01 verifies metrics.go calls
// ctxkeys.MustCellIDFrom(r.Context()) — i.e. cell label originates from ctx,
// not from a struct field, fallback default, or local computation.
func TestHTTPMetricsLabelCellIDCtxSource01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "http", "middleware", "metrics.go")
	rel, _ := filepath.Rel(root, target)
	rel = filepath.ToSlash(rel)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	importsCtxkeys := false
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "github.com/ghbvf/gocell/kernel/ctxkeys" {
			importsCtxkeys = true
			break
		}
	}
	assert.Truef(t, importsCtxkeys,
		"%s: %s — file must import kernel/ctxkeys to read the cell label from request context",
		rel, ruleHTTPMetricsLabelCtxSource01)

	callsMustCellIDFrom := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "MustCellIDFrom" {
			return true
		}
		callsMustCellIDFrom = true
		return false
	})
	assert.Truef(t, callsMustCellIDFrom,
		"%s: %s — middleware.Metrics must call ctxkeys.MustCellIDFrom(...) "+
			"to obtain the cell label; a fallback branch or instance-level cellID "+
			"silently undermines the per-request labeling contract",
		rel, ruleHTTPMetricsLabelCtxSource01)
}

// TestHTTPMetricsLabelNoAssemblyDerive01 ensures phases_http.go does not
// derive a metrics cellID from assembly identity. Specifically, no token
// inside this file may reference `assemblyID`, `assemblyCore.ID`, or the
// literal "default" *as the cellID source*. We assert structurally on the
// prior derivation idiom (b.assemblyID / b.assemblyCore.ID() / "default")
// being absent from the autoWireHTTPMetricsCollector function body.
func TestHTTPMetricsLabelNoAssemblyDerive01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "bootstrap", "phases_http.go")
	rel, _ := filepath.Rel(root, target)
	rel = filepath.ToSlash(rel)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if f.Name.Name == "autoWireHTTPMetricsCollector" {
			fn = f
			break
		}
	}
	require.NotNilf(t, fn, "%s: autoWireHTTPMetricsCollector func not found", rel)

	var (
		referencesAssemblyID      bool
		referencesAssemblyCoreID  bool
		referencesDefaultLiteral  bool
		referencesProviderCellKey bool
	)
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
		"%s: %s — autoWireHTTPMetricsCollector must not reference b.assemblyID; "+
			"cell label originates from request ctx, not assembly identity",
		rel, ruleHTTPMetricsLabelNoAssemblyDerive)
	assert.Falsef(t, referencesAssemblyCoreID,
		"%s: %s — autoWireHTTPMetricsCollector must not call b.assemblyCore.ID() "+
			"as a cellID source",
		rel, ruleHTTPMetricsLabelNoAssemblyDerive)
	assert.Falsef(t, referencesDefaultLiteral,
		`%s: %s — autoWireHTTPMetricsCollector must not use literal "default" as a `+
			"fallback cellID; framework-owned routes use the _runtime sentinel "+
			"installed by router.go",
		rel, ruleHTTPMetricsLabelNoAssemblyDerive)
	assert.Falsef(t, referencesProviderCellKey,
		"%s: %s — ProviderCollectorConfig{CellID: ...} field assignment is forbidden; "+
			"the field has been removed and cellID is now a per-call argument to RecordRequest",
		rel, ruleHTTPMetricsLabelNoAssemblyDerive)
}

// TestHTTPMetricsLabelNoConfigCellID01 verifies the ProviderCollectorConfig
// struct does not declare a CellID field. Defending against revert is
// important because pre-realign call sites pattern-matched on this exact
// shape and a re-introduction would silently break per-cell labeling.
func TestHTTPMetricsLabelNoConfigCellID01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "observability", "metrics", "provider_collector.go")
	rel, _ := filepath.Rel(root, target)
	rel = filepath.ToSlash(rel)

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
		"%s: %s — ProviderCollectorConfig must not declare a CellID field; "+
			"cellID is supplied per call to RecordRequest, not at constructor time",
		rel, ruleHTTPMetricsLabelNoConfigCellID)
}

// TestHTTPMetricsLabelListenerRootRuntime01 verifies router.go installs
// middleware.WithCellIDContext("_runtime") immediately before
// middleware.Metrics on the listener root mux. The order matters: cell-id
// must populate ctx before Metrics tries to read it.
func TestHTTPMetricsLabelListenerRootRuntime01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "http", "router", "router.go")
	rel, _ := filepath.Rel(root, target)
	rel = filepath.ToSlash(rel)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	// Walk the AST collecting every middleware.WithCellIDContext("_runtime")
	// and middleware.Metrics(...) call. Then assert (a) the WithCellIDContext
	// call exists with literal "_runtime", and (b) it precedes the Metrics
	// call within the same statement list.
	type callRef struct {
		name string
		pos  token.Pos
		arg0 string
	}
	var calls []callRef

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "WithCellIDContext", "Metrics":
			ref := callRef{name: sel.Sel.Name, pos: call.Pos()}
			if len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					ref.arg0 = lit.Value
				}
			}
			calls = append(calls, ref)
		}
		return true
	})

	var sentinelPos, metricsPos token.Pos
	for _, c := range calls {
		if c.name == "WithCellIDContext" && c.arg0 == httpMetricsRuntimeSentinelLiteral {
			if sentinelPos == token.NoPos || c.pos < sentinelPos {
				sentinelPos = c.pos
			}
		}
		if c.name == "Metrics" {
			if metricsPos == token.NoPos || c.pos < metricsPos {
				metricsPos = c.pos
			}
		}
	}

	require.NotEqualf(t, token.NoPos, sentinelPos,
		`%s: %s — router.go must call middleware.WithCellIDContext("_runtime") on the listener root mux`,
		rel, ruleHTTPMetricsLabelListenerRoot)
	require.NotEqualf(t, token.NoPos, metricsPos,
		"%s: %s — router.go must call middleware.Metrics on the listener root mux",
		rel, ruleHTTPMetricsLabelListenerRoot)

	assert.Lessf(t, int(sentinelPos), int(metricsPos),
		`%s: %s — middleware.WithCellIDContext("_runtime") must appear before middleware.Metrics `+
			"so the sentinel populates request ctx before the recorder reads it",
		rel, ruleHTTPMetricsLabelListenerRoot)
}
