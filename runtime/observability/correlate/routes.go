package correlate

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/internal/contractbuild"
)

// specCorrelate is the framework-owned ContractSpec for the correlate
// reverse-lookup endpoint. The "http.framework." prefix marks it as
// runtime-owned, distinguishing it from cell-owned routes.
//
// Built via contractbuild.NewFrameworkHTTP — the only legitimate ContractSpec
// construction path under runtime/ per NO-MANUAL-CONTRACTSPEC-LITERAL-01.
//
// Route: GET /api/v1/observability/correlate
// Listener: PrimaryListener (JWT auth at listener level)
// Auth: admin-role Policy (auth.AnyRole(auth.RoleAdmin)) — mirrors the
// auditquery endpoint (cells/auditcore/slices/auditquery), the sibling that
// already serves audit data on PrimaryListener behind an admin gate. The path
// is deliberately under /api/v1/observability/ (a framework area, parallel to
// /api/v1/devtools/) rather than /api/v1/audit/ — the latter is owned by the
// auditcore cell (RouteGroup prefix /api/v1/audit) and a framework route there
// would collide with that namespace ownership.
//
// Why not /internal/v1/* (the prior placement): that namespace is the
// cell→cell control-plane, where ContractSpec.validateHTTP requires a non-empty
// Clients caller-cell allowlist (enforced at auth.Mount). correlate is an
// ops/admin endpoint with no caller cell, so it does not fit that model; it is
// authorized by admin role instead, which is the framework's standard pattern
// for privileged endpoints (see auditquery) and the convention in Kubernetes
// (RBAC) / Vault (ACL). See issue #1048.
var specCorrelate = contractbuild.NewFrameworkHTTP(
	"http.framework.audit.correlate.v1",
	"GET",
	"/api/v1/observability/correlate",
)

// CorrelateRouteGroups returns the RouteGroup that bootstrap mounts for the
// correlate reverse-lookup endpoint: a single framework-owned RouteGroup on
// PrimaryListener with no CellID (framework route attribution, metric
// cell="_runtime").
//
// Authorization is the admin-role Policy: a valid JWT bearing the admin role is
// required (Public:false → JWT enforced by the listener; Policy → admin role
// enforced per request). This is the same gate the auditquery endpoint uses,
// and it tightens the actorId exposure (issue #1048 C5) from "any caller" to
// "admin only".
func CorrelateRouteGroups(svc *Service) []cell.RouteGroup {
	return []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Register: func(mux cell.RouteMux) error {
				return auth.Mount(mux, auth.Route{
					Contract: specCorrelate,
					Handler:  svc.HTTPHandler(),
					Policy:   auth.AnyRole(auth.RoleAdmin),
				})
			},
		},
	}
}
