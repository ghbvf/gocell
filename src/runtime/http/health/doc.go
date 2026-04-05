// Package health provides /healthz (liveness) and /readyz (readiness) HTTP
// endpoints that aggregate kernel/assembly health status and custom readiness
// checkers.
//
// The liveness probe (/healthz) calls Health() on every registered Cell in the
// CoreAssembly. The readiness probe (/readyz) additionally runs all named
// checkers registered via RegisterChecker.
//
// Both endpoints return HTTP 200 when all checks pass, or HTTP 503 with a JSON
// body when any check fails:
//
//	{"status": "unhealthy", "checks": {"my-cell": "unhealthy", "db": "healthy"}}
//
// # Usage
//
//	h := health.New(asm)
//	h.RegisterChecker("db", func() error {
//	    return db.Ping()
//	})
//
//	// Register with router:
//	router.Get("/healthz", h.LivezHandler())
//	router.Get("/readyz", h.ReadyzHandler())
//
// When using runtime/http/router, pass WithHealthHandler(h) and the endpoints
// are registered automatically.
package health
