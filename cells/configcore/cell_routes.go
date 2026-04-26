// cell_routes.go hosts ConfigCore.RouteGroups (HTTP endpoint registration)
// and ConfigCore.RegisterSubscriptions (outbox event handler registration).
// Init-time wiring lives in cell_init.go.
package configcore

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// Event spec uses wrapper.EventSpec (id==topic) so the ID literal participates
// in FMT-18's literal-vs-YAML cross-check — the previous
// `Topic: configsubscribe.TopicConfigChanged` form was invisible to the
// scanner because the regex only sees string literals.
//
// HTTP contract specs are owned by each slice's handler.go (single source of
// truth); RouteGroups below delegates to slice.RegisterRoutes for HTTP wiring.
var (
	specEventConfigEntryUpserted = wrapper.EventSpec("event.config.entry-upserted.v1", "amqp")
	specEventConfigEntryDeleted  = wrapper.EventSpec("event.config.entry-deleted.v1", "amqp")
)

// RouteGroups declares configcore's HTTP route groups across listeners.
// Each slice owns its own ContractSpec literals + auth.Route declarations
// (admin / service-token policy included) in its handler.go's
// RegisterRoutes / RegisterInternalRoutes. cell_routes.go is pure wiring:
// it picks the listener + URL prefix and delegates to slice methods.
// This keeps a single source of truth per endpoint.
//
// ref: kubernetes/kubernetes pkg/endpoints/installer.go — one installer per
// resource owns its own route + authz declaration.
// ref: go-kratos/kratos transport/http/server.go — service self-declares
// routes; main only wires services into the server.
// ref: go-zero rest/server.go AddRoutes — per-listener route declaration.
func (c *ConfigCore) RouteGroups() []cell.RouteGroup {
	return []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Prefix:   "/api/v1",
			Register: func(mux cell.RouteMux) error {
				mux.Route("/config", func(cfg cell.RouteMux) {
					c.readHandler.RegisterRoutes(cfg)
					c.writeHandler.RegisterRoutes(cfg)
					c.publishHandler.RegisterRoutes(cfg)
				})
				mux.Route("/flags", func(f cell.RouteMux) {
					c.flagHandler.RegisterRoutes(f)
					c.flagWriteHandler.RegisterRoutes(f)
				})
				return nil
			},
		},
		{
			// InternalListener: service-token authentication is enforced by the
			// listener chain (cell.NewAuthServiceToken). Per-route Policy further
			// requires the principal's RoleInternalAdmin (injected by
			// ServiceTokenMiddleware). PR-CFG-G1: refetch endpoint lets accesscore
			// fetch the current value after receiving an entry-upserted event.
			Listener: cell.InternalListener,
			Prefix:   "/internal/v1",
			Register: func(mux cell.RouteMux) error {
				mux.Route("/config", func(cfg cell.RouteMux) {
					c.readHandler.RegisterInternalRoutes(cfg)
				})
				return nil
			},
		},
	}
}

// RegisterSubscriptions declares event subscriptions for configcore.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *ConfigCore) RegisterSubscriptions(r cell.EventRouter) error {
	upsertedHandler := outbox.WrapLegacyHandler(c.subscribeSvc.HandleEntryUpserted)
	if err := r.AddContractHandler(specEventConfigEntryUpserted, upsertedHandler, "configcore"); err != nil {
		return fmt.Errorf("configcore: subscribe %s: %w", specEventConfigEntryUpserted.Topic, err)
	}

	deletedHandler := outbox.WrapLegacyHandler(c.subscribeSvc.HandleEntryDeleted)
	if err := r.AddContractHandler(specEventConfigEntryDeleted, deletedHandler, "configcore"); err != nil {
		return fmt.Errorf("configcore: subscribe %s: %w", specEventConfigEntryDeleted.Topic, err)
	}
	return nil
}
