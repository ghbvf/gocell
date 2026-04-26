// cell_routes.go hosts ConfigCore.RouteGroups (HTTP endpoint registration)
// and ConfigCore.RegisterSubscriptions (outbox event handler registration).
// Init-time wiring lives in cell_init.go.
package configcore

import (
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

// RouteGroups declares configcore's HTTP route groups on the PrimaryListener.
// Each slice owns its own ContractSpec literals + auth.Route declarations
// (admin policy included) in its handler.go's RegisterRoutes. cell_routes.go
// is pure wiring: it picks the listener + URL prefix and delegates to
// slice.RegisterRoutes. This keeps a single source of truth per endpoint.
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
			Register: func(mux cell.RouteMux) {
				mux.Route("/config", func(cfg cell.RouteMux) {
					c.readHandler.RegisterRoutes(cfg)
					c.writeHandler.RegisterRoutes(cfg)
					c.publishHandler.RegisterRoutes(cfg)
				})
				mux.Route("/flags", func(f cell.RouteMux) {
					c.flagHandler.RegisterRoutes(f)
					c.flagWriteHandler.RegisterRoutes(f)
				})
			},
		},
	}
}

// RegisterSubscriptions declares event subscriptions for configcore.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *ConfigCore) RegisterSubscriptions(r cell.EventRouter) error {
	upsertedHandler := outbox.WrapLegacyHandler(c.subscribeSvc.HandleEntryUpserted)
	r.AddContractHandler(specEventConfigEntryUpserted, upsertedHandler, "configcore")

	deletedHandler := outbox.WrapLegacyHandler(c.subscribeSvc.HandleEntryDeleted)
	r.AddContractHandler(specEventConfigEntryDeleted, deletedHandler, "configcore")
	return nil
}
