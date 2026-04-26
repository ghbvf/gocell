// cell_routes.go wires AccessCore's HTTP routes and event subscriptions.
// Cross-cutting service providers (HealthCheckers, TokenVerifier, Authorizer)
// live in cell_providers.go; constructor + options in cell.go; Init() and
// slice construction in cell_init.go.
package accesscore

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// Event specs use wrapper.EventSpec (id==topic). Previously the configreceive
// consumer's topic constant was aliased into the spec Topic field; that
// double-declaration meant FMT-18 silently skipped validation because the
// literal scanner only sees string literals. EventSpec makes the id==topic
// identity explicit and FMT-18 sees the ID literal in the call.
//
// HTTP contract specs are owned by each slice's handler.go (single source of
// truth); RouteGroups below delegates to slice.RegisterRoutes for HTTP wiring.
var (
	specEventConfigEntryUpserted = wrapper.EventSpec("event.config.entry-upserted.v1", "amqp")
	specEventConfigEntryDeleted  = wrapper.EventSpec("event.config.entry-deleted.v1", "amqp")
	specEventRoleAssigned        = wrapper.EventSpec("event.role.assigned.v1", "amqp")
	specEventRoleRevoked         = wrapper.EventSpec("event.role.revoked.v1", "amqp")
)

// RouteGroups declares accesscore's HTTP route groups across two listeners:
//   - PrimaryListener at /api/v1/access: public/authenticated business routes.
//   - InternalListener at /internal/v1/access: control-plane RBAC assignment.
//
// Each slice owns its own ContractSpec literals + auth.Route declarations in
// its handler.go's RegisterRoutes. cell_routes.go is pure wiring: it picks the
// listener + URL prefix and delegates to slice.RegisterRoutes. This keeps a
// single source of truth per endpoint (the slice) and lets CH-04/CH-05
// governance correlate contracts to handler functions in one place.
//
// ref: kubernetes/kubernetes pkg/endpoints/installer.go — one installer per
// resource owns its own route + authz declaration.
// ref: go-kratos/kratos transport/http/server.go — service self-declares
// routes; main only wires services into the server.
// ref: go-zero rest/server.go AddRoutes — per-listener route declaration.
func (c *AccessCore) RouteGroups() []cell.RouteGroup {
	return []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Prefix:   "/api/v1/access",
			Register: func(mux cell.RouteMux) {
				mux.Route("/setup", func(s cell.RouteMux) {
					c.setupHandler.RegisterRoutes(s)
				})
				mux.Route("/users", c.identityHandler.RegisterRoutes)
				mux.Route("/sessions", func(s cell.RouteMux) {
					c.loginHandler.RegisterRoutes(s)
					c.refreshHandler.RegisterRoutes(s)
					c.logoutHandler.RegisterRoutes(s)
				})
				mux.Route("/roles", c.rbacHandler.RegisterRoutes)
			},
		},
		{
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
