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
	specEventConfigEntryUpserted = wrapper.EventSpec("event.config.entry-upserted.v1", "amqp")
	specEventConfigEntryDeleted  = wrapper.EventSpec("event.config.entry-deleted.v1", "amqp")
	specEventRoleAssigned        = wrapper.EventSpec("event.role.assigned.v1", "amqp")
	specEventRoleRevoked         = wrapper.EventSpec("event.role.revoked.v1", "amqp")
)

// RouteGroups declares accesscore's HTTP route groups across two listeners:
//   - PrimaryListener at /api/v1/access: public/authenticated business routes.
//   - InternalListener at /internal/v1/access: control-plane RBAC assignment.
//
// ref: go-zero rest/server.go AddRoutes — per-listener route declaration.
func (c *AccessCore) RouteGroups() []cell.RouteGroup {
	return []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Prefix:   "/api/v1/access",
			Register: func(mux cell.RouteMux) {
				// Interactive first-run admin provisioning, scoped under /access/ so
				// the path prefix matches Cell ownership (Consul /acl/bootstrap
				// convention, rather than Vault's top-level /sys/init). Both endpoints
				// are Public: no admin exists yet to authenticate against; once an
				// admin exists, the endpoint returns 410 Gone via a fast-path Status
				// check before bcrypt runs.
				mux.Route("/setup", func(s cell.RouteMux) {
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
				mux.Route("/users", c.identityHandler.RegisterRoutes)

				// Session endpoints: /api/v1/access/sessions.
				// Router.FinalizeAuth aggregates Public + PasswordResetExempt metas
				// across all Cells at Bootstrap phase 5.
				// Login and refresh are public (no JWT required). Logout requires the
				// caller to be authenticated as the session owner or an admin, and is
				// PasswordResetExempt so a token carrying password_reset_required=true
				// can still reach this endpoint.
				mux.Route("/sessions", func(s cell.RouteMux) {
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
					// Logout: {id} is a session id, NOT a user id, so the route-level
					// policy cannot be SelfOr("id", admin). Session ownership is enforced
					// inside HandleLogout by comparing the principal subject against the
					// session's user_id. Baseline AuthMiddleware still requires a valid
					// JWT; PasswordResetExempt keeps the route reachable while the caller
					// still owes a password reset (standard user-self-recovery flow).
					auth.Mount(s, auth.Route{
						Contract:            specAuthSessionDelete,
						Handler:             http.HandlerFunc(c.logoutHandler.HandleLogout),
						PasswordResetExempt: true,
					})
				})

				// RBAC queries: /api/v1/access/roles
				mux.Route("/roles", c.rbacHandler.RegisterRoutes)
			},
		},
		{
			// Internal admin endpoints: /internal/v1/access/roles
			Listener: cell.InternalListener,
			Prefix:   "/internal/v1/access",
			Register: func(mux cell.RouteMux) {
				mux.Route("/roles", c.rbacAssignHandler.RegisterRoutes)
			},
		},
	}
}

// RegisterSubscriptions declares event subscriptions for accesscore.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *AccessCore) RegisterSubscriptions(r cell.EventRouter) error {
	// config-receive: config state-sync events from configcore.
	upsertedHandler := outbox.WrapLegacyHandler(c.configReceiveSvc.HandleEntryUpserted)
	r.AddContractHandler(specEventConfigEntryUpserted, upsertedHandler, "accesscore")

	deletedHandler := outbox.WrapLegacyHandler(c.configReceiveSvc.HandleEntryDeleted)
	r.AddContractHandler(specEventConfigEntryDeleted, deletedHandler, "accesscore")

	// rbac-session-sync: invalidate sessions on role assignment or revocation.
	// Same handler + same consumer group across both topics — HandleRoleChanged
	// is topic-agnostic.
	roleHandler := outbox.WrapLegacyHandler(c.rbacSessionConsumer.HandleRoleChanged)
	r.AddContractHandler(specEventRoleAssigned, roleHandler, "accesscore-rbac-session-sync")
	r.AddContractHandler(specEventRoleRevoked, roleHandler, "accesscore-rbac-session-sync")
	return nil
}
