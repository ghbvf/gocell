// Package markergen scans cell.go marker comments (// +cell:listener,
// // +slice:route) and projects them into a per-cell WireBundle that drives
// cellgen wire generation.
//
// Event subscriptions are no longer sourced from cell.go markers — they are
// read from slice.yaml contractUsages[role=subscribe] by cellgen directly
// (subscribe single-source flip, K05 W3). The slice:subscribe marker kind is
// removed from knownMarkers; any existing cell.go that still contains
// // +slice:subscribe: will produce an "unknown marker" error from Merge,
// prompting migration to slice.yaml.
//
// CellMeta does NOT carry Listeners — wire is single-sourced in markers,
// projected into WireBundle, and consumed by cellgen + governance. This
// makes "wire single source = marker" hold at the type layer (K#05).
//
// ref: kubernetes-sigs/controller-tools pkg/markers/parse.go
//
//	(splitMarker formal grammar adopted; Registry/Definition abstraction NOT
//	adopted — GoCell has a closed set of 2 marker kinds, switch dispatch
//	is sufficient and ~350 lines lighter than the controller-tools model)
//
// ref: kubernetes-sigs/controller-tools pkg/markers/collect.go
//
//	(MaybeErrList non-fail-fast aggregation adopted)
package markergen

// WireBundle aggregates per-cell wire facts derived from cell.go marker comments.
// Subscribe entries are no longer carried here — subscriptions are derived from
// slice.yaml contractUsages[role=subscribe] by cellgen (K05 single-source flip).
type WireBundle struct {
	Listeners []ListenerSpec
	Routes    []RouteSpec
}

// ListenerSpec mirrors a `// +cell:listener:ref=...,prefix=...` marker
// declared on the cell struct.
type ListenerSpec struct {
	Ref    string
	Prefix string
}

// RouteSpec mirrors a `// +slice:route:slice=...,listener=...,subPath=...,method=...`
// marker declared on a handler field. Listener defaults to
// "cell.PrimaryListener" when omitted (covers the 90% single-listener case);
// Method defaults to "RegisterRoutes". HandlerField is auto-derived from the
// AST field name on which the marker is declared — cellgen renders
// `c.<HandlerField>.<Method>(s)`. Multiple route markers on the same field
// are allowed (e.g. one for primary + one for internal listener).
type RouteSpec struct {
	Slice        string
	Listener     string
	SubPath      string
	Method       string
	HandlerField string
}
