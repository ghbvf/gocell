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
	"github.com/ghbvf/gocell/runtime/auth"
)

// RegisterRoutes registers HTTP routes for accesscore.
func (c *AccessCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/access", func(sub cell.RouteMux) {
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
			auth.Declare(s, auth.RouteDecl{
				Method:  "POST",
				Path:    "/login",
				Handler: http.HandlerFunc(c.loginHandler.HandleLogin),
				Public:  true,
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
