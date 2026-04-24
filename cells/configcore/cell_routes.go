// cell_routes.go hosts ConfigCore.RegisterRoutes (HTTP endpoint registration)
// and ConfigCore.RegisterSubscriptions (outbox event handler registration).
// Init-time wiring lives in cell_init.go.
package configcore

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/configcore/slices/configsubscribe"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
)

// RouteGroups declares configcore's HTTP route groups on the PrimaryListener.
// All admin-guarded write handlers delegate to the slice's RegisterRoutes
// (which applies auth.Declare + auth.AnyRole(RoleAdmin)) so the policy
// declaration cannot drift between production wiring and contract/integration
// tests — there is exactly one place where each write endpoint's policy is
// declared.
//
// Read endpoints (GET /config, GET /config/{key}, GET /flags, GET /flags/{key},
// POST /flags/{key}/evaluate) are declared with auth.Authenticated() to gain
// an explicit auth declaration; previously these relied on implicit authed-by-
// default behaviour.
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
					// config-read — authenticated via auth.Declare.
					auth.Declare(cfg, auth.RouteDecl{
						Method:  "GET",
						Path:    "/",
						Handler: http.HandlerFunc(c.readHandler.HandleList),
						Policy:  auth.Authenticated(),
					})
					auth.Declare(cfg, auth.RouteDecl{
						Method:  "GET",
						Path:    "/{key}",
						Handler: http.HandlerFunc(c.readHandler.HandleGet),
						Policy:  auth.Authenticated(),
					})
					// config-write — admin-guarded via slice RegisterRoutes.
					c.writeHandler.RegisterRoutes(cfg)
					// config-publish — admin-guarded via slice RegisterRoutes.
					c.publishHandler.RegisterRoutes(cfg)
				})

				// /api/v1/flags hosts feature-flag (read + evaluate, L0) and flag-write
				// (write + toggle + delete, L2 + admin-guarded).
				mux.Route("/flags", func(f cell.RouteMux) {
					// feature-flag (read) slice — authenticated via auth.Declare.
					auth.Declare(f, auth.RouteDecl{
						Method:  "GET",
						Path:    "/",
						Handler: http.HandlerFunc(c.flagHandler.HandleList),
						Policy:  auth.Authenticated(),
					})
					auth.Declare(f, auth.RouteDecl{
						Method:  "GET",
						Path:    "/{key}",
						Handler: http.HandlerFunc(c.flagHandler.HandleGet),
						Policy:  auth.Authenticated(),
					})
					auth.Declare(f, auth.RouteDecl{
						Method:  "POST",
						Path:    "/{key}/evaluate",
						Handler: http.HandlerFunc(c.flagHandler.HandleEvaluate),
						Policy:  auth.Authenticated(),
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
	handler := outbox.WrapLegacyHandler(c.subscribeSvc.HandleEvent)
	r.AddHandler(configsubscribe.TopicConfigChanged, handler, "configcore")
	return nil
}
