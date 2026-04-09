package cell

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// ---------------------------------------------------------------------------
// Optional registration interfaces
// ---------------------------------------------------------------------------
// These interfaces are optionally implemented by Cells. During bootstrap,
// the Assembly (or any orchestrator) discovers them via type assertion:
//
//	if r, ok := cell.(HTTPRegistrar); ok {
//	    r.RegisterRoutes(mux)
//	}
//
// This keeps the core Cell interface slim while allowing Cells to opt-in to
// HTTP serving and event consumption.

// RouteMux is a minimal route registration interface.
// kernel/ does not import any specific router (chi, gorilla, etc.);
// concrete implementations are provided by runtime/ or adapters/.
//
// For testing, use kernel/cell/celltest.TestMux.
type RouteMux interface {
	// Handle registers handler for the given pattern.
	// Pattern follows Go 1.22+ enhanced ServeMux syntax: "METHOD /path/{param}".
	// Path parameters are extracted by the underlying router implementation and
	// accessible via r.PathValue("param") in the handler.
	//
	// Examples:
	//   mux.Handle("GET /users/{id}", handler)
	//   mux.Handle("POST /", handler)
	//   mux.Handle("DELETE /sessions/{id}", handler)
	Handle(pattern string, handler http.Handler)

	// Route creates a sub-router under pattern with prefix stripping.
	// Use for GoCell native route registration — the sub-router participates
	// in the framework's pattern matching, PathValue binding, and test model.
	Route(pattern string, fn func(sub RouteMux))

	// Mount attaches an opaque http.Handler sub-tree under pattern with prefix
	// stripping. The mounted handler is a "black box" that does not need to
	// follow GoCell routing conventions. Use Route + RegisterRoutes instead
	// when the sub-tree is a GoCell cell/slice.
	Mount(pattern string, handler http.Handler)

	// Group creates a same-level scope sharing the parent prefix.
	// Useful for applying middleware to a subset of routes.
	Group(fn func(RouteMux))

	// With returns a new RouteMux that inherits all routes and middleware
	// from this scope, plus the additional middleware provided.
	// Unlike a mutable Use(), With is safe to call after routes are registered
	// and does not modify the receiver.
	//
	// ref: go-chi/chi Mux.With — returns an inline router sharing the parent tree.
	With(mw ...func(http.Handler) http.Handler) RouteMux
}

// HTTPRegistrar is optionally implemented by Cells that expose HTTP endpoints.
type HTTPRegistrar interface {
	RegisterRoutes(mux RouteMux)
}

// EventRegistrar is optionally implemented by Cells that subscribe to events.
type EventRegistrar interface {
	RegisterSubscriptions(sub outbox.Subscriber)
}
