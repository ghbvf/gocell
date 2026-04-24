// cell_routes.go hosts ConfigCore.RegisterRoutes (HTTP endpoint registration)
// and ConfigCore.RegisterSubscriptions (outbox event handler registration).
// Init-time wiring lives in cell_init.go.
package configcore

import (
	"net/http"

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
	specEventConfigChanged = wrapper.EventSpec("event.config.changed.v1", "amqp")
)

// RegisterRoutes registers HTTP routes for configcore. All admin-guarded
// write handlers delegate to the slice's RegisterRoutes (which applies
// auth.Mount + auth.AnyRole(RoleAdmin)) so the policy declaration cannot
// drift between production wiring and contract/integration tests — there is
// exactly one place where each write endpoint's policy is declared.
//
// ref: kubernetes/kubernetes pkg/endpoints/installer.go — one installer per
// resource, authz chain applied once at registration.
// ref: go-kratos/kratos transport/http/server.go — route + middleware pair
// declared once; runtime and test paths share the same registration call.
func (c *ConfigCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1", func(v1 cell.RouteMux) {
		// Config CRUD + publish/rollback under /api/v1/config.
		v1.Route("/config", func(cfg cell.RouteMux) {
			auth.Mount(cfg, auth.Route{
				Contract: specConfigList,
				Handler:  http.HandlerFunc(c.readHandler.HandleList),
				Policy:   auth.Authenticated(),
			})
			auth.Mount(cfg, auth.Route{
				Contract: specConfigGet,
				Handler:  http.HandlerFunc(c.readHandler.HandleGet),
				Policy:   auth.Authenticated(),
			})
			// config-write — admin-guarded via slice RegisterRoutes.
			c.writeHandler.RegisterRoutes(cfg)
			// config-publish — admin-guarded via slice RegisterRoutes.
			c.publishHandler.RegisterRoutes(cfg)
		})

		// /api/v1/flags hosts feature-flag (read + evaluate, L0) and flag-write
		// (write + toggle + delete, L2 + admin-guarded).
		v1.Route("/flags", func(f cell.RouteMux) {
			auth.Mount(f, auth.Route{
				Contract: specFlagsList,
				Handler:  http.HandlerFunc(c.flagHandler.HandleList),
				Policy:   auth.Authenticated(),
			})
			auth.Mount(f, auth.Route{
				Contract: specFlagsGet,
				Handler:  http.HandlerFunc(c.flagHandler.HandleGet),
				Policy:   auth.Authenticated(),
			})
			auth.Mount(f, auth.Route{
				Contract: specFlagsEvaluate,
				Handler:  http.HandlerFunc(c.flagHandler.HandleEvaluate),
				Policy:   auth.Authenticated(),
			})
			// flag-write — admin-guarded via slice RegisterRoutes.
			c.flagWriteHandler.RegisterRoutes(f)
		})
	})
}

// RegisterSubscriptions declares event subscriptions for configcore.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *ConfigCore) RegisterSubscriptions(r cell.EventRouter) error {
	handler := outbox.WrapLegacyHandler(c.subscribeSvc.HandleEvent)
	r.AddContractHandler(specEventConfigChanged, handler, "configcore")
	return nil
}
