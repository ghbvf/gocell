package authorizationdecide

import (
	"context"

	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	decidegen "github.com/ghbvf/gocell/generated/contracts/http/auth/decide/v1"
)

// DecideAdapter implements decidegen.Service for http.auth.decide.v1 (#1863,
// BR-004). It exposes the existing ABAC PDP Service (auth.Authorizer) over HTTP
// so the frontend <Can> component can ask "may the current user do <action>
// [on <resource>]?".
//
// The decision subject is the authenticated JWT principal — never a request-body
// field (the request schema declares no subject, additionalProperties:false). The
// engine evaluates the ctx principal's attributes + tenant policy; req.Resource is
// forwarded so owner-scoped queries (e.g. "may I read user X?") can match the
// PDP's ownership rules.
type DecideAdapter struct{ S *Service }

// Decide implements decidegen.Service.
//
// A policy DENY for the queried action is NOT an HTTP error: it is a successful
// 200 with data.allowed=false. Only infrastructure/identity faults surface as
// 4xx/5xx — an absent principal (401) or an unknown action (400) are returned as
// typed responses; an Authorize error (e.g. missing tenant → 403, policy store
// down → 503) flows through (nil, err) so httputil.WriteError derives the status
// from errcode.Kind.
func (a DecideAdapter) Decide(ctx context.Context, req *decidegen.Request) (decidegen.DecideResponseObject, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		// Defense-in-depth: the RequirePermissionForSelf gate already requires a
		// principal, so this is unreachable on the wired route — but the subject is
		// load-bearing (it is the decision subject), so fail closed if absent.
		return decidegen.Decide401ErrorResponse{Body: *errcode.New(
			errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "missing principal",
		)}, nil
	}
	// Validate the action resolves to a registered permission. PermissionByName
	// is the closed-set resolver (sealed authz.Permission registry); an unknown
	// or empty action fails closed with 400 rather than reaching the PDP, where it
	// would silently never match any rule (a dead always-deny).
	if _, ok := authz.PermissionByName(req.Action); !ok {
		return decidegen.Decide400ErrorResponse{Body: *errcode.New(
			errcode.KindInvalid, errcode.ErrAuthRBACInvalidInput, "unknown action",
		)}, nil
	}

	dec, err := a.S.Authorize(ctx, p.Subject, req.Resource, req.Action)
	if err != nil {
		// Undeclared-status framework path: Authorize wraps infra/identity faults
		// (KindUnavailable→503, KindPermissionDenied→403, KindUnauthenticated→401);
		// httputil.WriteError derives the wire status from errcode.Kind.
		return nil, err
	}
	// Never surface dec.Reason() (server-side diagnostic; per observability rules
	// it must not cross the wire). The boolean verdict is the whole contract.
	return decidegen.Decide200JSONResponse{Data: &decidegen.ResponseData{Allowed: dec.IsAllow()}}, nil
}

// Handler is the route handler for the authorizationdecide slice's HTTP surface.
type Handler struct {
	decideH *decidegen.Handler
}

// NewHandler creates an authorizationdecide HTTP Handler from the PDP Service.
//
// The decide endpoint is gated by auth.RequirePermissionForSelf(access:decide):
// it forwards the caller's OWN subject to the PDP as resource, so the access:decide
// baseline self rule (subject.sub == resource.id) grants any authenticated user the
// right to introspect themselves — a conditioned PDP grant, not a Go short-circuit
// and not an unconditional allow (see baseline.go + BASELINE-OWNER-RULE-TENANT-FREEZE-01).
func NewHandler(svc *Service) *Handler {
	policy := auth.RequirePermissionForSelf(authz.PermAccessDecide())
	return &Handler{
		decideH: decidegen.NewHandler(DecideAdapter{svc}, policy),
	}
}

// RegisterRoutes mounts the decide contract handler on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.decideH.RegisterRoutes(mux)
}
