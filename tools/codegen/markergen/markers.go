// Package markergen scans cell.go marker comments (// +cell:listener,
// // +slice:route, // +slice:subscribe) and projects them into a per-cell
// WireBundle that drives cellgen wire generation.
//
// CellMeta does NOT carry Listeners — wire is single-sourced in markers,
// projected into WireBundle, and consumed by cellgen + governance. This
// makes "wire single source = marker" hold at the type layer (K#05).
//
// ref: kubernetes-sigs/controller-tools pkg/markers/parse.go
//
//	(splitMarker formal grammar adopted; Registry/Definition abstraction NOT
//	adopted — GoCell has a closed set of 3 marker kinds, switch dispatch
//	is sufficient and ~350 lines lighter than the controller-tools model)
//
// ref: kubernetes-sigs/controller-tools pkg/markers/collect.go
//
//	(MaybeErrList non-fail-fast aggregation adopted)
package markergen

// WireBundle aggregates per-cell wire facts derived from cell.go marker comments.
type WireBundle struct {
	Listeners  []ListenerSpec
	Routes     []RouteSpec
	Subscribes []SubscribeSpec
}

// ListenerSpec mirrors a `// +cell:listener:ref=...,prefix=...` marker
// declared on the cell struct.
type ListenerSpec struct {
	Ref    string
	Prefix string
}

// RouteSpec mirrors a `// +slice:route:slice=...,listener=...,subPath=...`
// marker declared on a handler field. Listener defaults to
// "cell.PrimaryListener" when omitted (covers the 90% single-listener case).
// HandlerField is auto-derived from the AST field name on which the marker
// is declared — cellgen renders `c.<HandlerField>.RegisterRoutes(s)`.
type RouteSpec struct {
	Slice        string
	Listener     string
	SubPath      string
	HandlerField string
}

// SubscribeSpec mirrors a
// `// +slice:subscribe:slice=...,topic=...,handler=...,group=...` marker
// declared on a service/consumer field. SliceField is auto-derived from the
// AST field name — cellgen renders `c.<SliceField>.<Handler>` as the
// reg.Subscribe handler expression.
type SubscribeSpec struct {
	Slice      string
	Topic      string
	Handler    string
	Group      string
	SliceField string
}
