// cell_routes.go hosts ConfigCore.RouteGroups (HTTP endpoint registration)
// and ConfigCore.RegisterSubscriptions (outbox event handler registration).
// Init-time wiring lives in cell_init.go.
package configcore

import (
	"net/http"

	dto "github.com/ghbvf/gocell/cells/configcore/internal/dto"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Contract spec literals — cross-checked against contracts/**/contract.yaml
// by FMT-18 governance (PR-A11-V).
var (
	specConfigList = wrapper.ContractSpec{
		ID: "http.config.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/config/",
	}
	specConfigGet = wrapper.ContractSpec{
		ID: "http.config.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/config/{key}",
	}
	specFlagsList = wrapper.ContractSpec{
		ID: "http.config.flags.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/flags/",
	}
	specFlagsGet = wrapper.ContractSpec{
		ID: "http.config.flags.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/flags/{key}",
	}
	specFlagsEvaluate = wrapper.ContractSpec{
		ID: "http.config.flags.evaluate.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/flags/{key}/evaluate",
	}

	// Event spec uses wrapper.EventSpec (id==topic) so the ID literal
	// participates in FMT-18's literal-vs-YAML cross-check — the previous
	// `Topic: configsubscribe.TopicConfigChanged` form was invisible to
	// the scanner because the regex only sees string literals.
	specEventConfigEntryUpserted = wrapper.EventSpec("event.config.entry-upserted.v1", "amqp")
	specEventConfigEntryDeleted  = wrapper.EventSpec("event.config.entry-deleted.v1", "amqp")
)

// RouteGroups declares configcore's HTTP route groups on the PrimaryListener.
// All handlers — read and write alike — are admin-gated via auth.AnyRole(dto.RoleAdmin).
// Write handlers delegate to the slice's RegisterRoutes (which applies
// auth.Mount + auth.AnyRole(RoleAdmin)); read handlers are mounted here directly
// with the same policy. There is exactly one declaration point per endpoint,
// so policy cannot drift between production wiring and tests.
//
// ref: kubernetes/kubernetes pkg/endpoints/installer.go — one installer per
// resource, authz chain applied once at registration.
// ref: go-kratos/kratos transport/http/server.go — route + middleware pair
// declared once; runtime and test paths share the same registration call.
// ref: go-zero rest/server.go AddRoutes — per-listener route declaration.
func (c *ConfigCore) RouteGroups() []cell.RouteGroup {
	return []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Prefix:   "/api/v1",
			Register: func(mux cell.RouteMux) {
				// Config CRUD + publish/rollback under /api/v1/config.
				mux.Route("/config", func(cfg cell.RouteMux) {
					// config-read — admin-gated via auth.Mount.
					auth.Mount(cfg, auth.Route{
						Contract: specConfigList,
						Handler:  http.HandlerFunc(c.readHandler.HandleList),
						Policy:   auth.AnyRole(dto.RoleAdmin),
					})
					auth.Mount(cfg, auth.Route{
						Contract: specConfigGet,
						Handler:  http.HandlerFunc(c.readHandler.HandleGet),
						Policy:   auth.AnyRole(dto.RoleAdmin),
					})
					// config-write — admin-guarded via slice RegisterRoutes.
					c.writeHandler.RegisterRoutes(cfg)
					// config-publish — admin-guarded via slice RegisterRoutes.
					c.publishHandler.RegisterRoutes(cfg)
				})

				// /api/v1/flags hosts feature-flag (read + evaluate, L0) and flag-write
				// (write + toggle + delete, L2 + admin-guarded).
				mux.Route("/flags", func(f cell.RouteMux) {
					// feature-flag (read) slice — admin-gated via auth.Mount.
					auth.Mount(f, auth.Route{
						Contract: specFlagsList,
						Handler:  http.HandlerFunc(c.flagHandler.HandleList),
						Policy:   auth.AnyRole(dto.RoleAdmin),
					})
					auth.Mount(f, auth.Route{
						Contract: specFlagsGet,
						Handler:  http.HandlerFunc(c.flagHandler.HandleGet),
						Policy:   auth.AnyRole(dto.RoleAdmin),
					})
					auth.Mount(f, auth.Route{
						Contract: specFlagsEvaluate,
						Handler:  http.HandlerFunc(c.flagHandler.HandleEvaluate),
						Policy:   auth.AnyRole(dto.RoleAdmin),
					})
					// flag-write — admin-guarded via slice RegisterRoutes.
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
