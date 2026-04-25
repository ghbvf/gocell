package cell

import "net/http"

// RouteGroup declares where a batch of routes physically lives:
// which listener, what path prefix, optional auth override.
// Cells implement [RouteGroupContributor] to return a slice of these;
// bootstrap collects them at phase5, validates prefix-vs-listener
// consistency, and mounts the sub-trees on the correct chi.Mux.
type RouteGroup struct {
	// Listener identifies the physical HTTP listener this group belongs to.
	Listener ListenerRef
	// Prefix is the URL path prefix for all routes in this group
	// (e.g. "/api/v1/access", "/internal/v1/access").
	Prefix string
	// Auth optionally overrides the listener's default auth plan for this group.
	// nil means inherit the listener's default auth (no group-level override).
	// Only GroupAuth implementations are accepted here at compile time — JWT
	// plans intentionally do NOT implement GroupAuth.
	Auth GroupAuth
	// Middleware holds additional (non-auth) HTTP middleware applied to routes
	// in this group after the Auth middleware. Evaluated in declaration order.
	Middleware []func(http.Handler) http.Handler
	// Register is called by bootstrap to mount the cell's sub-tree on the
	// chosen mux. Required; a nil Register is a programmer error detected
	// at phase5 validation time.
	Register func(mux RouteMux)
	// CellID is the identifier of the cell that contributed this group.
	// Set automatically by bootstrap during phase5CollectRouteGroups for
	// error-context enrichment (OPS-02). Cells do not need to populate this.
	CellID string
}

// SingleGroup is a convenience constructor for the common single-listener,
// single-prefix case. It returns a RouteGroup with the given listener, prefix,
// and register function. Equivalent to declaring the struct literal inline.
//
// DX-05: reduces boilerplate in cells that declare a single route group.
func SingleGroup(l ListenerRef, prefix string, fn func(RouteMux)) RouteGroup {
	return RouteGroup{Listener: l, Prefix: prefix, Register: fn}
}

// RouteGroupContributor is implemented by cells (or other components)
// that expose HTTP routes through the RouteGroup declarative API.
// Replaces the legacy single-mux RegisterRoutes approach.
//
// Bootstrap discovers RouteGroupContributor via type assertion during
// phase5 and calls RouteGroups() to collect all declared groups before
// mounting. An empty or nil slice is valid — the cell simply contributes
// no routes via this mechanism.
type RouteGroupContributor interface {
	RouteGroups() []RouteGroup
}
