package correlate

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/runtime/auth"
)

// specCorrelate is the framework-internal ContractSpec for the correlate
// reverse-lookup endpoint. The "http.framework." prefix marks it as
// runtime-internal, distinguishing it from cell-owned routes.
//
// Built via contractspec.NewFrameworkHTTP — the only legitimate ContractSpec
// construction path under runtime/ per NO-MANUAL-CONTRACTSPEC-LITERAL-01.
//
// Route: GET /internal/v1/audit/correlate
// Listener: InternalListener (service-token auth at listener level)
// Public: false — any valid service token passes (no Clients allowlist →
//
//	no RequireCallerCell guard, intended ops posture per issue #1048).
var specCorrelate = contractspec.NewFrameworkHTTP(
	"http.framework.audit.correlate.v1",
	"GET",
	"/internal/v1/audit/correlate",
)

// CorrelateRouteGroups returns the RouteGroup that bootstrap mounts for the
// correlate reverse-lookup endpoint. It mirrors the HealthRouteGroups
// precedent: a single framework-owned RouteGroup on InternalListener with no
// CellID (framework route attribution).
//
// Public:false with no Contract.Clients means any valid InternalListener
// service token can call this endpoint — the intended ops posture.
func CorrelateRouteGroups(svc *Service) []cell.RouteGroup {
	return []cell.RouteGroup{
		{
			Listener: cell.InternalListener,
			Register: func(mux cell.RouteMux) error {
				return auth.Mount(mux, auth.Route{
					Contract: specCorrelate,
					Handler:  svc.HTTPHandler(),
					Public:   false,
				})
			},
		},
	}
}
