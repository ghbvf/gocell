// cell_routes.go wires AccessCore's HTTP routes and event subscriptions.
// Cross-cutting service providers (HealthCheckers, TokenVerifier, Authorizer)
// live in cell_providers.go; constructor + options in cell.go; Init() and
// slice construction in cell_init.go.
package accesscore

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Contract spec literals — one per route / subscription; cross-checked
// against contracts/**/contract.yaml by FMT-18 governance (PR-A11-V).
var (
	specAuthSetupStatus = wrapper.ContractSpec{
		ID: "http.auth.setup.status.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/access/setup/status",
	}
	specAuthSetupAdmin = wrapper.ContractSpec{
		ID: "http.auth.setup.admin.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/access/setup/admin",
	}
	specAuthLogin = wrapper.ContractSpec{
		ID: "http.auth.login.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/access/sessions/login",
	}
	specAuthRefresh = wrapper.ContractSpec{
		ID: "http.auth.refresh.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/access/sessions/refresh",
	}
	specAuthSessionDelete = wrapper.ContractSpec{
		ID: "http.auth.session.delete.v1", Kind: "http", Transport: "http",
		Method: "DELETE", Path: "/api/v1/access/sessions/{id}",
	}

	// Event specs use wrapper.EventSpec (id==topic). Previously the
	// configreceive consumer's topic constant was aliased into the spec
	// Topic field; that double-declaration meant FMT-18 silently skipped
	// validation because the literal scanner only sees string literals.
	// EventSpec makes the id==topic identity explicit and FMT-18 sees the
	// ID literal in the call.
	specEventConfigEntryWritten     = wrapper.EventSpec("event.config.entry-written.v1", "amqp")
	specEventConfigVersionPublished = wrapper.EventSpec("event.config.version-published.v1", "amqp")
	specEventRoleAssigned           = wrapper.EventSpec("event.role.assigned.v1", "amqp")
	specEventRoleRevoked            = wrapper.EventSpec("event.role.revoked.v1", "amqp")
)

// RegisterRoutes registers HTTP routes for accesscore.
func (c *AccessCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/access", func(sub cell.RouteMux) {
		// Interactive first-run admin provisioning. Both endpoints are
		// Public: no admin exists yet to authenticate against; once an admin
		// exists, the endpoint returns 410 Gone via a fast-path Status check
		// before bcrypt runs.
		sub.Route("/setup", func(s cell.RouteMux) {
			auth.Mount(s, auth.Route{
				Contract: specAuthSetupStatus,
				Handler:  http.HandlerFunc(c.setupHandler.HandleStatus),
				Public:   true,
			})
			auth.Mount(s, auth.Route{
				Contract: specAuthSetupAdmin,
				Handler:  http.HandlerFunc(c.setupHandler.HandleCreateAdmin),
				Public:   true,
			})
		})

		// Identity management: /api/v1/access/users
		sub.Route("/users", c.identityHandler.RegisterRoutes)

		// Session endpoints: /api/v1/access/sessions.
		// Router.FinalizeAuth aggregates Public + PasswordResetExempt metas
		// across all Cells at Bootstrap phase 5.
		sub.Route("/sessions", func(s cell.RouteMux) {
			auth.Mount(s, auth.Route{
				Contract: specAuthLogin,
				Handler:  http.HandlerFunc(c.loginHandler.HandleLogin),
				Public:   true,
			})
			auth.Mount(s, auth.Route{
				Contract: specAuthRefresh,
				Handler:  http.HandlerFunc(c.refreshHandler.HandleRefresh),
				Public:   true,
			})
			// Logout: session ownership is enforced inside HandleLogout
			// (comparing principal subject against session user_id).
			// Baseline AuthMiddleware still requires a valid JWT;
			// PasswordResetExempt keeps the route reachable while the caller
			// still owes a password reset (standard user-self-recovery flow).
			auth.Mount(s, auth.Route{
				Contract:            specAuthSessionDelete,
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
	// config-receive: config entry-written + version-published from configcore.
	entryHandler := outbox.WrapLegacyHandler(c.configReceiveSvc.HandleEntryWritten)
	r.AddContractHandler(specEventConfigEntryWritten, entryHandler, "accesscore")

	publishedHandler := outbox.WrapLegacyHandler(c.configReceiveSvc.HandleVersionPublished)
	r.AddContractHandler(specEventConfigVersionPublished, publishedHandler, "accesscore")

	// rbac-session-sync: invalidate sessions on role assignment or revocation.
	// Same handler + same consumer group across both topics — HandleRoleChanged
	// is topic-agnostic.
	roleHandler := outbox.WrapLegacyHandler(c.rbacSessionConsumer.HandleRoleChanged)
	r.AddContractHandler(specEventRoleAssigned, roleHandler, "accesscore-rbac-session-sync")
	r.AddContractHandler(specEventRoleRevoked, roleHandler, "accesscore-rbac-session-sync")
	return nil
}
