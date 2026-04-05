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
type RouteMux interface {
	Handle(pattern string, handler http.Handler)
	Group(fn func(RouteMux))
}

// HTTPRegistrar is optionally implemented by Cells that expose HTTP endpoints.
type HTTPRegistrar interface {
	RegisterRoutes(mux RouteMux)
}

// EventRegistrar is optionally implemented by Cells that subscribe to events.
type EventRegistrar interface {
	RegisterSubscriptions(sub outbox.Subscriber)
}
