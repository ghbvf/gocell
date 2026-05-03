package archtest

// http_metrics_label_test.go enforces the four invariants of the
// HTTP-METRICS-LABEL-REALIGN contract (D1, 2026-05-04):
//
//   - HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01:
//     runtime/http/middleware/metrics.go must read the cell label from the
//     in-flight cellIDState via withCellIDState/cs.cellID — chi-style mutable
//     pointer pattern that lets sub-mux WithCellIDContext middleware override
//     the sentinel set at the listener root. A revert that pulls cellID from
//     a struct field, adds a string-valued ctxkeys read on the wrong layer,
//     or branches on a fallback would silently erode the per-request label
//     guarantee; this rule blocks it.
//
//   - HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01:
//     runtime/bootstrap/phases_http.go must not derive any metrics cellID from
//     b.assemblyID, b.assemblyCore.ID(), or the literal "default". Pre-realign
//     code did exactly this and broke multi-cell assembly attribution; the new
//     contract is "cellID flows from RouteGroup.CellID via mutable state,
//     never from assembly identity".
//
//   - HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01:
//     runtime/observability/metrics.ProviderCollectorConfig must not contain a
//     CellID field. Holding cellID on the collector instance rather than per
//     RecordRequest call is the architectural pattern this PR removes.
//
//   - HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01:
//     runtime/http/middleware/metrics.go must seed the cellIDState with
//     RuntimeCellIDSentinel ("_runtime"), so framework-owned requests
//     (healthz / readyz / metrics endpoint / unmatched 404s) emit the
//     framework label rather than an empty value or a per-listener guess.
//
// All four rules use go/ast so reorderings, alias renames, and indirect
// imports are detected even without changing the surface API.

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
	ruleHTTPMetricsLabelCtxSource01      = "HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01"
	ruleHTTPMetricsLabelNoAssemblyDerive = "HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01"
	ruleHTTPMetricsLabelNoConfigCellID   = "HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01"
	ruleHTTPMetricsLabelRuntimeSentinel  = "HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01"

	httpMetricsRuntimeSentinelLiteral = `"_runtime"`
)

// TestHTTPMetricsLabelCellIDCtxSource01 verifies metrics.go uses the chi-style
// mutable cellIDState pattern: a call to withCellIDState(...) seeds the per-
// request struct, and the recorder reads the resolved cellID from that
// struct's field. Plain ctxkeys reads on the listener-root layer would miss
// values written by sub-mux WithCellIDContext middleware (chi child contexts
// are detached from the parent), so this rule pins the correct mechanism.
func TestHTTPMetricsLabelCellIDCtxSource01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "http", "middleware", "metrics.go")
	rel, _ := filepath.Rel(root, target)
	rel = filepath.ToSlash(rel)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	var (
		callsWithCellIDState bool
		readsCellIDStateFld  bool
	)
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "withCellIDState" {
				callsWithCellIDState = true
			}
		case *ast.SelectorExpr:
			// Detect any access of `something.cellID` — typically `cs.cellID`
			// inside the safeObserve closure that hands the resolved label
			// to collector.RecordRequest.
			if v.Sel.Name == "cellID" {
				readsCellIDStateFld = true
			}
		}
		return true
	})

	assert.Truef(t, callsWithCellIDState,
		"%s: %s — middleware.Metrics must call withCellIDState(...) to seed the "+
			"per-request mutable cell-id container; only this layer can be observed by "+
			"sub-mux WithCellIDContext mutations after next.ServeHTTP returns",
		rel, ruleHTTPMetricsLabelCtxSource01)
	assert.Truef(t, readsCellIDStateFld,
		"%s: %s — middleware.Metrics must read the resolved label via cs.cellID; "+
			"reading ctxkeys.CellIDFrom on the listener-root layer would miss the per-cell "+
			"override written into a chi sub-mux child context",
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

// TestHTTPMetricsLabelRuntimeSentinel01 verifies metrics.go contains the
// "_runtime" string literal in a withCellIDState call — proving the recorder
// seeds its mutable cell-id state with the framework sentinel rather than an
// empty value or a per-listener guess. A regression that drops the sentinel
// would leave framework-owned requests (healthz, readyz, /metrics, unmatched
// 404s) emitting an empty cell label, breaking dashboards that match
// `cell="_runtime"` for framework traffic.
//
// The walk is structurally pinned: the literal must appear as an argument
// to a call to withCellIDState, not just somewhere in the file (e.g. a
// comment, an unrelated helper, an unused constant).
func TestHTTPMetricsLabelRuntimeSentinel01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "runtime", "http", "middleware", "metrics.go")
	rel, _ := filepath.Rel(root, target)
	rel = filepath.ToSlash(rel)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	seedsRuntimeSentinel := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Match `withCellIDState(<ctx>, RuntimeCellIDSentinel)` —
		// either a bare identifier or a selector. Permitting either
		// keeps the rule resilient to package-internal vs imported
		// usage changes.
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "withCellIDState" {
			return true
		}
		for _, arg := range call.Args {
			switch v := arg.(type) {
			case *ast.Ident:
				if v.Name == "RuntimeCellIDSentinel" {
					seedsRuntimeSentinel = true
				}
			case *ast.BasicLit:
				if v.Kind == token.STRING && v.Value == httpMetricsRuntimeSentinelLiteral {
					seedsRuntimeSentinel = true
				}
			}
		}
		return true
	})

	assert.Truef(t, seedsRuntimeSentinel,
		`%s: %s — middleware.Metrics must seed withCellIDState with RuntimeCellIDSentinel `+
			`(or the literal "_runtime"); without it, framework-owned routes emit an empty cell label`,
		rel, ruleHTTPMetricsLabelRuntimeSentinel)
}
