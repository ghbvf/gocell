package cell

// ADR: kernel/cell depends on net/http (standard library)
//
// Status: Accepted
//
// Decision: kernel/cell uses net/http types (http.Handler, http.ResponseWriter,
// http.Request) in the RouteMux and HTTPRegistrar interfaces.
//
// Rationale: net/http is part of the Go standard library. The project's
// layering rules (CLAUDE.md) state "kernel/ only depends on stdlib + pkg/",
// so net/http is an allowed dependency. The Go 1.22+ enhanced ServeMux
// pattern syntax ("METHOD /path/{param}") gives kernel a powerful routing
// abstraction without importing any third-party router.
//
// Alternatives considered:
//   - Define custom Handler/ResponseWriter/Request interfaces to abstract
//     away net/http entirely. Rejected: this would add complexity (type
//     conversions, adapter layers) for no practical benefit, since net/http
//     is guaranteed stable by the Go compatibility promise.
//
// Consequences: Any Cell implementing HTTPRegistrar receives an http.Handler-
// compatible interface. Concrete routers (chi, gorilla) are provided by
// runtime/ or adapters/ and implement RouteMux, keeping kernel free of
// third-party dependencies.

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

// EventRouter declares event subscriptions. Cells call AddHandler during
// RegisterSubscriptions to declare intent; the caller (bootstrap/Router)
// is responsible for starting consumption.
//
// The minimal interface lives in kernel/cell so Cells can depend on it
// without importing runtime/. The concrete implementation is in
// runtime/eventrouter.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddHandler registers
// intent; Router.Run starts consumption. GoCell simplifies to topic+handler
// (no publish side in the same call).
type EventRouter interface {
	AddHandler(topic string, handler outbox.EntryHandler)
}

// EventRegistrar is optionally implemented by Cells that subscribe to events.
// RegisterSubscriptions declares subscriptions by calling r.AddHandler for
// each topic. It MUST NOT start goroutines or block — the Router manages
// the subscription lifecycle.
type EventRegistrar interface {
	RegisterSubscriptions(r EventRouter) error
}
