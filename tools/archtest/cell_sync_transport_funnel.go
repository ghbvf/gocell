package archtest

// cell_sync_transport_funnel.go — importable CELL-SYNC-TRANSPORT-FUNNEL-01 rule
// logic (Epic #1423 US4 #1963). Non-test home so the detector core can be shared
// by the production scan, the synthetic-detector table, and the RED fixture scan
// (single source, no parallel rule body) — mirrors svctoken_caller_cell.go and
// grpc_cell_no_client_dial_test.go.

import (
	"fmt"
	"go/ast"
)

// ruleCellSyncTransportFunnel is the archtest rule identifier.
const ruleCellSyncTransportFunnel = "CELL-SYNC-TRANSPORT-FUNNEL-01"

// netHTTPLibPath is the canonical import path of the stdlib HTTP package.
const netHTTPLibPath = "net/http"

// forbiddenHTTPClientRef classifies a (pkgPath, name) package-symbol reference as
// a forbidden raw cross-cell HTTP client-dispatch symbol. It returns the
// violation kind ("client-type" / "default-client" / "convenience") and true when
// forbidden, or ("", false) otherwise. Request-shaping and server-side symbols
// (http.NewRequest*, http.Handler, http.Request, http.ResponseWriter,
// http.Method*, http.Status*, http.Header, ...) and any non-net/http symbol are
// NOT forbidden. Pure detector core shared by the production scan and the
// synthetic table (mirrors forbiddenGRPCClientRef).
//
// Rationale (epic #1423 US4 — sync transport seam): cells communicate only
// through contracts. A cell reaches an EXTERNAL system through adapters/ (the
// only layer that may hold an *http.Client), never directly. So a cell holding or
// constructing a raw net/http client (the *http.Client type, the http.DefaultClient
// var, or the http.Get/Post/Head/PostForm convenience funcs that dispatch via
// DefaultClient) can only be dialing a SIBLING cell — which MUST go through an
// injected transport.CellTransport (DoContract) instead. Building a request
// (http.NewRequestWithContext) is legitimate (the transport sends it); only the
// CLIENT/dispatch surface is forbidden.
func forbiddenHTTPClientRef(pkgPath, name string) (kind string, forbidden bool) {
	if pkgPath != netHTTPLibPath {
		return "", false
	}
	switch name {
	case "Client":
		return "client-type", true
	case "DefaultClient":
		return "default-client", true
	case "Get", "Post", "Head", "PostForm":
		return "convenience", true
	default:
		return "", false
	}
}

// scanCellHTTPClientDispatch walks one file's AST for qualified selector
// references (`http.Name`) and emits a Diagnostic for every forbidden raw HTTP
// client-dispatch symbol. The caller decides whether the file belongs to a cell
// (production scan) or not (fixture scan). Dot-imported bare `Name` is NOT walked
// (selector-only); cells cannot dot-import (the revive dot-imports linter is the
// gate), so that form is an independent linter concern.
func scanCellHTTPClientDispatch(p *Pass, file *ast.File, rel string) []Diagnostic {
	var diags []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		path, name, ok := ResolvePackageRef(p.TypesInfo, sel)
		if !ok {
			return
		}
		kind, forbidden := forbiddenHTTPClientRef(path, name)
		if !forbidden {
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"%s [%s]: cell production code references net/http.%s at %s:%d — a cell must not hold or "+
					"construct a raw HTTP client to dial a sibling cell. Cross-cell sync (http) calls go through an "+
					"injected transport.CellTransport (DoContract); external HTTP reach belongs in adapters/, never a "+
					"cell (epic #1423 US4 #1963).",
				ruleCellSyncTransportFunnel, kind, name, rel, line),
		})
	})
	return diags
}
