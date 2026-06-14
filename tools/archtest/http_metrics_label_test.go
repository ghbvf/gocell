//go:build archtest

package archtest

// invariants:
//   - INVARIANT: HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01
//   - INVARIANT: HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01
//   - INVARIANT: HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01
//   - INVARIANT: HTTP-METRICS-LABEL-ROUTER-ATTRIBUTION-01
//   - INVARIANT: HTTP-METRICS-LABEL-BODYLIMIT-CTXSOURCE-01
//
// http_metrics_label_test.go enforces the HTTP-METRICS-LABEL-REALIGN
// contract (D1, 2026-05-04): cell identity is a router-root request
// attribution concern, not a metrics collector constructor field and not a
// RouteGroup handler-middleware side effect.
//
// M12b (#1093) update: the per-request cell-label resolution (ctxkeys.CellIDFrom
// read + RuntimeCellSentinel fallback) was pulled OUT of the two write points
// (metricsWithClock / recordBodyLimitRejection) INTO the sealed
// metrics.ResolveCellLabel funnel, which additionally validates the cell id
// against the assembly closed set. The CTXSOURCE / BODYLIMIT-CTXSOURCE
// invariants below now assert each write point routes through that funnel and no
// longer reads ctxkeys.CellIDFrom inline; the relocated ctx-read + sentinel +
// closed-set membership invariants live in cell_id_closed_set_test.go
// (CELL-ID-CLOSED-SET-01), which also subsumes the former
// HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	ruleHTTPMetricsLabelCtxSource01          = "HTTP-METRICS-LABEL-CELLID-CTXSOURCE-01"
	ruleHTTPMetricsLabelNoAssemblyDerive     = "HTTP-METRICS-LABEL-NO-ASSEMBLY-DERIVE-01"
	ruleHTTPMetricsLabelNoConfigCellID       = "HTTP-METRICS-LABEL-NO-CONFIG-CELLID-01"
	ruleHTTPMetricsLabelRuntimeSentinel      = "HTTP-METRICS-LABEL-RUNTIME-SENTINEL-01"
	ruleHTTPMetricsLabelRouterAttribution    = "HTTP-METRICS-LABEL-ROUTER-ATTRIBUTION-01"
	ruleHTTPMetricsLabelBodyLimitCtxSource01 = "HTTP-METRICS-LABEL-BODYLIMIT-CTXSOURCE-01"
)

// resolvedCellLabelVar returns the name of the variable assigned from
// metrics.ResolveCellLabel(...) within body (e.g. "cell" for
// `cell := metrics.ResolveCellLabel(ctx, valid)`), or "" when the result is not
// bound to a single variable (e.g. passed inline).
func resolvedCellLabelVar(body ast.Node) string {
	var name string
	scanner.EachInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) {
		if name != "" || len(as.Rhs) != 1 || len(as.Lhs) != 1 {
			return
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || !isSelectorCall(call, "metrics", "ResolveCellLabel") {
			return
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			name = id.Name
		}
	})
	return name
}

// argIsResolvedCellLabel reports whether expr is the cell label produced by the
// metrics.ResolveCellLabel funnel — either the bound variable labelVar (AST
// data-flow linkage to the assignment, no type info required) or an inline
// metrics.ResolveCellLabel(...) call. This binds the collector's cell argument
// to the funnel RESULT rather than matching a hardcoded identifier name: a
// regression that calls ResolveCellLabel but passes some OTHER value to the
// collector now fails here. (The sealed metrics.CellLabel type already makes a
// raw-string label a compile error; this asserts the funnel result specifically
// reaches the write point.)
func argIsResolvedCellLabel(expr ast.Expr, labelVar string) bool {
	if id, ok := expr.(*ast.Ident); ok {
		return labelVar != "" && id.Name == labelVar
	}
	if call, ok := expr.(*ast.CallExpr); ok {
		return isSelectorCall(call, "metrics", "ResolveCellLabel")
	}
	return false
}

// TestHTTPMetricsLabelCellIDCtxSource01 enforces (post-M12b) that metricsWithClock
// resolves the cell label through the sealed metrics.ResolveCellLabel funnel and
// feeds the resulting CellLabel to collector.RecordRequest — and no longer reads
// ctxkeys.CellIDFrom inline (that read, plus the sentinel fallback and the
// closed-set membership check, relocated into ResolveCellLabel; see
// CELL-ID-CLOSED-SET-01). RecordRequest can no longer be called with a raw
// string cell label — the sealed CellLabel type makes that a compile error — so
// this archtest's job is to lock that the write point routes through the funnel.
func TestHTTPMetricsLabelCellIDCtxSource01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "framework", "runtime", "http", "middleware", "metrics.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	fn := findHTTPMetricsFuncDecl(t, file, "metricsWithClock")
	metricsPath := narrowestFuncLitWithCollectorRecordRequest(fn.Body)
	require.NotNilf(t, metricsPath,
		"%s: %s — metricsWithClock must record HTTP metrics through collector.RecordRequest",
		rel, ruleHTTPMetricsLabelCtxSource01)

	// reqVarName binds the *http.Request formal so the arg0 check asserts
	// `<req>.Context()` rather than any-ident.Context().
	reqVarName := requestParamName(fn.Body)
	// resolvedVar binds RecordRequest's cell arg to the metrics.ResolveCellLabel
	// assignment (AST data-flow linkage) instead of a hardcoded `cell` name match.
	resolvedVar := resolvedCellLabelVar(metricsPath.Body)

	var (
		callsResolveCellLabel bool
		recordUsesCellArg     bool
		recordUsesReqCtxArg   bool
		resolveCellLabelPos   token.Pos
		recordRequestPos      token.Pos
	)
	scanner.EachInSubtree[ast.CallExpr](metricsPath.Body, func(v *ast.CallExpr) {
		if isSelectorCall(v, "metrics", "ResolveCellLabel") {
			callsResolveCellLabel = true
			rememberFirstPos(&resolveCellLabelPos, v.Pos())
		}
		if isSelectorCall(v, "collector", "RecordRequest") {
			recordRequestPos = v.Pos()
			// arg0 is the request ctx; arg1 is the CellLabel from ResolveCellLabel.
			if len(v.Args) > 0 && isRequestContextCall(v.Args[0], reqVarName) {
				recordUsesReqCtxArg = true
			}
			if len(v.Args) > 1 && argIsResolvedCellLabel(v.Args[1], resolvedVar) {
				recordUsesCellArg = true
			}
		}
	})

	// The ctx read + sentinel relocated INTO metrics.ResolveCellLabel; the write
	// point must not read ctxkeys.CellIDFrom inline anymore.
	var inlineCtxRead bool
	scanner.EachInSubtree[ast.CallExpr](fn.Body, func(v *ast.CallExpr) {
		if isSelectorCall(v, "ctxkeys", "CellIDFrom") {
			inlineCtxRead = true
		}
	})

	assert.Truef(t, callsResolveCellLabel,
		"%s: %s — middleware.Metrics must resolve the cell label through the sealed metrics.ResolveCellLabel funnel",
		rel, ruleHTTPMetricsLabelCtxSource01)
	assert.Truef(t, recordUsesCellArg,
		"%s: %s — collector.RecordRequest arg1 must be the CellLabel bound from metrics.ResolveCellLabel "+
			"(data-flow linkage to the funnel result, not just any value)",
		rel, ruleHTTPMetricsLabelCtxSource01)
	assert.Truef(t, recordUsesReqCtxArg,
		"%s: %s — collector.RecordRequest arg0 must be the request ctx (%s.Context())",
		rel, ruleHTTPMetricsLabelCtxSource01, reqVarName)
	assert.Truef(t, resolveCellLabelPos.IsValid() && recordRequestPos.IsValid() && resolveCellLabelPos < recordRequestPos,
		"%s: %s — metrics.ResolveCellLabel must feed the metrics path before collector.RecordRequest",
		rel, ruleHTTPMetricsLabelCtxSource01)
	assert.Falsef(t, inlineCtxRead,
		"%s: %s — cell resolution moved into metrics.ResolveCellLabel; metricsWithClock must not read ctxkeys.CellIDFrom inline",
		rel, ruleHTTPMetricsLabelCtxSource01)
}

// requestParamName walks root for the handler funcLit whose param list includes
// an `*http.Request`, returning that param's name (or "" if none). Matched by the
// type expression `*http.Request` (no type info under parser.ParseFile), so the
// arg0 check binds to the handler's actual request formal rather than any
// identifier named "r". The RecordRequest call lives in a nested no-param
// closure, so the request formal must be found in the enclosing handler funcLit.
func requestParamName(root ast.Node) string {
	var found string
	scanner.EachInSubtree[ast.FuncLit](root, func(fl *ast.FuncLit) {
		if found != "" || fl.Type == nil || fl.Type.Params == nil {
			return
		}
		for _, field := range fl.Type.Params.List {
			star, ok := field.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Request" {
				continue
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "http" {
				continue
			}
			if len(field.Names) > 0 {
				found = field.Names[0].Name
				return
			}
		}
	})
	return found
}

// isRequestContextCall reports whether expr is `<reqVar>.Context()` — a no-arg
// method call on the request identifier. Returns false when reqVar is empty
// (fail-closed: an unresolved request param must not silently pass).
func isRequestContextCall(expr ast.Expr, reqVar string) bool {
	if reqVar == "" {
		return false
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Context" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == reqVar
}

func TestHTTPMetricsLabelRouterAttribution01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "framework", "runtime", "http", "router", "router.go")
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
	scanner.EachInSubtree[ast.CallExpr](buildMux.Body, func(call *ast.CallExpr) {
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
		case isRouterUseWithDefaultMiddleware(call):
			rememberFirstPos(&defaultMWPos, call.Pos())
		}
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
	target := filepath.Join(root, "framework", "runtime", "bootstrap", "phases_http.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	var fn *ast.FuncDecl
	scanner.EachInSubtree[ast.FuncDecl](file, func(f *ast.FuncDecl) {
		if fn == nil && f.Name.Name == "autoWireHTTPMetricsCollector" {
			fn = f
		}
	})
	require.NotNilf(t, fn, "%s: autoWireHTTPMetricsCollector func not found", rel)

	var referencesAssemblyID, referencesAssemblyCoreID, referencesDefaultLiteral, referencesProviderCellKey bool
	scanner.EachInSubtree[ast.SelectorExpr](fn.Body, func(v *ast.SelectorExpr) {
		if id, ok := v.X.(*ast.Ident); ok && id.Name == "b" && v.Sel.Name == "assemblyID" {
			referencesAssemblyID = true
		}
		if call, ok := v.X.(*ast.SelectorExpr); ok {
			if id, ok := call.X.(*ast.Ident); ok && id.Name == "b" && call.Sel.Name == "assemblyCore" && v.Sel.Name == "ID" {
				referencesAssemblyCoreID = true
			}
		}
	})
	scanner.EachInSubtree[ast.BasicLit](fn.Body, func(v *ast.BasicLit) {
		if v.Kind == token.STRING && v.Value == `"default"` {
			referencesDefaultLiteral = true
		}
	})
	scanner.EachInSubtree[ast.KeyValueExpr](fn.Body, func(v *ast.KeyValueExpr) {
		if id, ok := v.Key.(*ast.Ident); ok && id.Name == "CellID" {
			referencesProviderCellKey = true
		}
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
	target := filepath.Join(root, "framework", "runtime", "observability", "metrics", "provider_collector.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	hasCellIDField := false
	scanner.EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if ts.Name.Name != "ProviderCollectorConfig" {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return
		}
		for _, f := range st.Fields.List {
			for _, name := range f.Names {
				if name.Name == "CellID" {
					hasCellIDField = true
				}
			}
		}
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
	var result *ast.FuncDecl
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if result == nil && fn.Name.Name == name {
			result = fn
		}
	})
	if result != nil {
		return result
	}
	require.Failf(t, "function not found", "expected function %s", name)
	return nil
}

func narrowestFuncLitWithCollectorRecordRequest(root ast.Node) *ast.FuncLit {
	var best *ast.FuncLit
	scanner.EachInSubtree[ast.FuncLit](root, func(fn *ast.FuncLit) {
		if !containsCollectorRecordRequest(fn.Body) {
			return
		}
		if best == nil || fn.End()-fn.Pos() < best.End()-best.Pos() {
			best = fn
		}
	})
	return best
}

func containsCollectorRecordRequest(root ast.Node) bool {
	found := false
	scanner.EachInSubtree[ast.CallExpr](root, func(call *ast.CallExpr) {
		if !found && isSelectorCall(call, "collector", "RecordRequest") {
			found = true
		}
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

func isRouterUseWithDefaultMiddleware(call *ast.CallExpr) bool {
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

// TestHTTPMetricsLabelBodyLimitCtxSource01 enforces (post-M12b) that the
// body-limit rejection helper (recordBodyLimitRejection in body_limit.go):
//
//  1. Resolves the cell label through the sealed metrics.ResolveCellLabel funnel
//     (the inline ctxkeys.CellIDFrom read + sentinel fallback relocated into it).
//  2. Calls collector.RecordBodyLimitRejection AFTER ResolveCellLabel, with the
//     resolved `cell` CellLabel as arg[1].
//  3. Derives the route label via RouteFor.
//  4. Passes ctx (an identifier, bound from r.Context()) as arg[0] and a named
//     route variable as arg[2] (OTel exemplar/baggage + low-cardinality route).
//  5. Does NOT read ctxkeys.CellIDFrom inline (relocated to the funnel).
//
// AI-robust rating: Medium (AST form check + position ordering). The downstream
// "label must be validated" guarantee is now type-system Hard (sealed CellLabel —
// a raw string cannot reach RecordBodyLimitRejection); this archtest locks that
// the helper routes through the funnel rather than constructing a CellLabel
// itself. The relocated ctx-read + sentinel + membership invariants are in
// CELL-ID-CLOSED-SET-01.
//
// Reverse self-check: BodyLimit's outer function must delegate to the helper and
// not resolve the cell label itself.
func TestHTTPMetricsLabelBodyLimitCtxSource01(t *testing.T) {
	root := findModuleRoot(t)
	target := filepath.Join(root, "framework", "runtime", "http", "middleware", "body_limit.go")
	rel := slashRel(t, root, target)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s: parse failed", rel)

	helperFn := findHTTPMetricsFuncDecl(t, file, "recordBodyLimitRejection")
	// Bind RecordBodyLimitRejection's cell arg to the ResolveCellLabel assignment.
	resolvedVar := resolvedCellLabelVar(helperFn.Body)

	var (
		callsResolveCellLabel bool
		readsCellIDFromInline bool
		callsRecordBLR        bool
		callsRouteFor         bool
		recordBLRArg0IsCtx    bool // arg[0] is an identifier (ctx var)
		recordBLRArg1IsCell   bool // arg[1] is the `cell` CellLabel ident
		recordBLRArg2IsIdent  bool // arg[2] is an identifier (route var)
		resolveCellLabelPos   token.Pos
		recordBLRPos          token.Pos
	)

	scanner.EachInSubtree[ast.CallExpr](helperFn.Body, func(v *ast.CallExpr) {
		if isSelectorCall(v, "ctxkeys", "CellIDFrom") {
			readsCellIDFromInline = true
		}
		if isSelectorCall(v, "metrics", "ResolveCellLabel") {
			callsResolveCellLabel = true
			rememberFirstPos(&resolveCellLabelPos, v.Pos())
		}
		if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "RouteFor" {
			callsRouteFor = true
		}
		if isSelectorCall(v, "collector", "RecordBodyLimitRejection") {
			callsRecordBLR = true
			rememberFirstPos(&recordBLRPos, v.Pos())
			if len(v.Args) > 0 {
				if _, ok := v.Args[0].(*ast.Ident); ok {
					recordBLRArg0IsCtx = true
				}
			}
			if len(v.Args) > 1 && argIsResolvedCellLabel(v.Args[1], resolvedVar) {
				recordBLRArg1IsCell = true
			}
			if len(v.Args) > 2 {
				if _, ok := v.Args[2].(*ast.Ident); ok {
					recordBLRArg2IsIdent = true
				}
			}
		}
	})

	assert.Truef(t, callsResolveCellLabel,
		"%s: %s — recordBodyLimitRejection must resolve the cell label through metrics.ResolveCellLabel",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)
	assert.Falsef(t, readsCellIDFromInline,
		"%s: %s — cell resolution moved into metrics.ResolveCellLabel; recordBodyLimitRejection must not read ctxkeys.CellIDFrom inline",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)
	assert.Truef(t, callsRecordBLR,
		"%s: %s — recordBodyLimitRejection must call collector.RecordBodyLimitRejection",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)
	assert.Truef(t, callsRouteFor,
		"%s: %s — recordBodyLimitRejection must derive route via RouteFor (not a bare literal or URL path)",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)
	assert.Truef(t, recordBLRArg0IsCtx,
		"%s: %s — RecordBodyLimitRejection arg[0] must be an identifier (the ctx variable)",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)
	assert.Truef(t, recordBLRArg1IsCell,
		"%s: %s — RecordBodyLimitRejection arg[1] must be the CellLabel bound from metrics.ResolveCellLabel "+
			"(data-flow linkage to the funnel result, not just any value)",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)
	assert.Truef(t, recordBLRArg2IsIdent,
		"%s: %s — RecordBodyLimitRejection arg[2] must be an identifier (the route variable)",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)
	assert.Truef(t, resolveCellLabelPos.IsValid() && recordBLRPos.IsValid() && resolveCellLabelPos < recordBLRPos,
		"%s: %s — metrics.ResolveCellLabel must be called before collector.RecordBodyLimitRejection",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)

	// Reverse self-check: the BodyLimit outer function must delegate cell
	// resolution to the helper, not resolve it itself.
	bodyLimitFn := findHTTPMetricsFuncDecl(t, file, "BodyLimit")
	var outerResolveCalls int
	scanner.EachInSubtree[ast.CallExpr](bodyLimitFn.Body, func(v *ast.CallExpr) {
		if isSelectorCall(v, "metrics", "ResolveCellLabel") {
			outerResolveCalls++
		}
	})
	assert.Zerof(t, outerResolveCalls,
		"%s: %s — BodyLimit must delegate cell resolution to recordBodyLimitRejection",
		rel, ruleHTTPMetricsLabelBodyLimitCtxSource01)
}

func rememberFirstPos(dst *token.Pos, pos token.Pos) {
	if !dst.IsValid() || pos < *dst {
		*dst = pos
	}
}
