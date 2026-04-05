// Package router provides a chi-based HTTP router implementing
// kernel/cell.RouteMux with a default middleware chain and automatic
// registration of health and metrics endpoints.
//
// The default middleware chain applied to every request is:
//
//	RequestID -> RealIP -> Recovery -> AccessLog -> SecurityHeaders -> BodyLimit
//
// An optional metrics middleware is appended when WithMetricsCollector is used.
//
// ref: go-chi/chi/v5 — Mux pattern (Group, Mount, Route, Use)
// Adopted: chi.NewRouter as the underlying multiplexer.
// Deviated: wrapped behind kernel/cell.RouteMux so Cells stay decoupled from
// the chi import path.
//
// # Usage
//
//	h := health.New(asm)
//	mc := metrics.NewInMemoryCollector()
//
//	r := router.New(
//	    router.WithHealthHandler(h),
//	    router.WithMetricsCollector(mc),
//	    router.WithBodyLimit(4 << 20), // 4 MB
//	)
//
//	// Register Cell routes
//	myCell.RegisterRoutes(r)
//
//	http.ListenAndServe(":8080", r)
//
// # Auto-registered endpoints
//
//   - GET /healthz  — liveness probe (when WithHealthHandler provided)
//   - GET /readyz   — readiness probe (when WithHealthHandler provided)
//   - GET /metrics  — request metrics (when WithMetricsCollector provided)
package router
