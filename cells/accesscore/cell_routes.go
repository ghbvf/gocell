// cell_routes.go wires AccessCore's HTTP routes and event subscriptions.
// Cross-cutting service providers (HealthCheckers, TokenVerifier, Authorizer)
// live in cell_providers.go; constructor + options in cell.go; Init() and
// slice construction in cell_init.go.
package accesscore

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/auth"
)

// httpAuthLoginV1 is the wrapper.ContractSpec literal for contract
// http.auth.login.v1 (POST /api/v1/access/sessions/login). Hand-coded
// alongside the Mount call so every span annotation trail (gocell.contract.id,
// gocell.contract.transport, http.method, http.route) matches the YAML in
// contracts/http/auth/login/v1/contract.yaml — a future governance rule
// (FMT-17 SPEC-CONTRACT-SYNC) will cross-reference this literal against the
// YAML at validate time so drift fails in CI.
var httpAuthLoginV1 = wrapper.ContractSpec{
	ID:        "http.auth.login.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "POST",
	Path:      "/api/v1/access/sessions/login",
}

// RegisterRoutes registers HTTP routes for accesscore.
func (c *AccessCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/access", func(sub cell.RouteMux) {
		// Interactive first-run admin provisioning, scoped under /access/ so
		// the path prefix matches Cell ownership (Consul /acl/bootstrap
		// convention, rather than Vault's top-level /sys/init). Both endpoints
		// are Public: no admin exists yet to authenticate against; once an
		// admin exists, the endpoint returns 410 Gone via a fast-path Status
		// check before bcrypt runs.
		sub.Route("/setup", func(s cell.RouteMux) {
			auth.Declare(s, auth.RouteDecl{
				Method:  "GET",
				Path:    "/status",
				Handler: http.HandlerFunc(c.setupHandler.HandleStatus),
				Public:  true,
			})
			auth.Declare(s, auth.RouteDecl{
				Method:  "POST",
				Path:    "/admin",
				Handler: http.HandlerFunc(c.setupHandler.HandleCreateAdmin),
				Public:  true,
			})
		})

		// Identity management: /api/v1/access/users
		sub.Route("/users", c.identityHandler.RegisterRoutes)

		// Session endpoints: /api/v1/access/sessions.
		// Public routes, password-reset-exempt routes and their implicit hint are
		// all declared inline here. Router.FinalizeAuth aggregates every Cell's
		// declarations at Bootstrap phase 5.
		// Login and refresh are public (no JWT required). Logout requires the
		// caller to be authenticated as the session owner or an admin, and is
		// PasswordResetExempt so a token carrying password_reset_required=true
		// can still reach this endpoint.
		sub.Route("/sessions", func(s cell.RouteMux) {
			auth.Mount(s, auth.Route{
				Contract: httpAuthLoginV1,
				Method:   "POST",
				Path:     "/login",
				Handler:  http.HandlerFunc(c.loginHandler.HandleLogin),
				Public:   true,
			})
			auth.Declare(s, auth.RouteDecl{
				Method:  "POST",
				Path:    "/refresh",
				Handler: http.HandlerFunc(c.refreshHandler.HandleRefresh),
				Public:  true,
			})
			// Logout: {id} is a session id, NOT a user id, so the route-level
			// policy cannot be SelfOr("id", admin). Session ownership is enforced
			// inside HandleLogout by comparing the principal subject against the
			// session's user_id. Baseline AuthMiddleware still requires a valid
			// JWT; PasswordResetExempt keeps the route reachable while the caller
			// still owes a password reset (standard user-self-recovery flow).
			auth.Declare(s, auth.RouteDecl{
				Method:              "DELETE",
				Path:                "/{id}",
				Handler:             http.HandlerFunc(c.logoutHandler.HandleLogout),
				PasswordResetExempt: true,
			})
		})

		// RBAC queries: /api/v1/access/roles
		sub.Route("/roles", c.rbacHandler.RegisterRoutes)
	})

	// Internal admin endpoints: /internal/v1/access/roles
	mux.Route("/internal/v1/access", func(sub cell.RouteMux) {
		sub.Route("/roles", c.rbacAssignHandler.RegisterRoutes)
	})
}

// RegisterSubscriptions declares event subscriptions for accesscore.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *AccessCore) RegisterSubscriptions(r cell.EventRouter) error {
	// config-receive: config.changed events from configcore.
	handler := outbox.WrapLegacyHandler(c.configReceiveSvc.HandleEvent)
	r.AddHandler(configreceive.TopicConfigChanged, handler, "accesscore")

	// rbac-session-sync: invalidate sessions on role assignment or revocation.
	// Both topics share the same handler and consumer group — HandleRoleChanged is topic-agnostic.
	roleHandler := outbox.WrapLegacyHandler(c.rbacSessionConsumer.HandleRoleChanged)
	r.AddHandler(dto.TopicRoleAssigned, roleHandler, "accesscore-rbac-session-sync")
	r.AddHandler(dto.TopicRoleRevoked, roleHandler, "accesscore-rbac-session-sync")
	return nil
}
