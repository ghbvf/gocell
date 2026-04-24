package cell

// RouteGroup declares where a batch of routes physically lives:
// which listener, what path prefix, optional policy override.
// Cells implement [RouteGroupContributor] to return a slice of these;
// bootstrap collects them at phase5, validates prefix-vs-listener
// consistency, and mounts the sub-trees on the correct chi.Mux.
type RouteGroup struct {
	// Listener identifies the physical HTTP listener this group belongs to.
	Listener ListenerRef
	// Prefix is the URL path prefix for all routes in this group
	// (e.g. "/api/v1/access", "/internal/v1/access").
	Prefix string
	// Policy optionally overrides the listener's default policy for this group.
	// nil means inherit the listener's default policy.
	Policy Policy
	// Register is called by bootstrap to mount the cell's sub-tree on the
	// chosen mux. Required; a nil Register is a programmer error detected
	// at phase5 validation time.
	Register func(mux RouteMux)
}

// RouteGroupContributor is implemented by cells (or other components)
// that expose HTTP routes through the RouteGroup declarative API.
// Replaces the legacy HTTPRegistrar.RegisterRoutes single-mux approach.
//
// Bootstrap discovers RouteGroupContributor via type assertion during
// phase5 and calls RouteGroups() to collect all declared groups before
// mounting. An empty or nil slice is valid — the cell simply contributes
// no routes via this mechanism.
type RouteGroupContributor interface {
	RouteGroups() []RouteGroup
}
