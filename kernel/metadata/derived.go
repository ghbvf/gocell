// Package metadata — derived.go provides secondary projections built on top
// of ProjectMeta. Currently: per-cell wire summary used by
// /api/v1/devtools/catalog to expose listener / route / subscribe surface
// for ops visibility.
//
// Derived data is stateless (no caching); callers re-derive on each request.
// Cost is O(cells) string copy — under 1ms for ~10 cells, no premature
// optimization.
//
// Decoupling note: this file does NOT import tools/codegen/markergen.
// Callers that own a markergen.WireBundle (e.g. runtime/http/devtools)
// convert it to CellWireBundle before calling DeriveCellWireSummaries.
// This keeps kernel/metadata dependency-free from the tools/ layer.
package metadata

import "sort"

// CellWireBundle is a dependency-free mirror of markergen.WireBundle.
// Callers in runtime/ or cmd/ translate markergen.WireBundle → CellWireBundle
// so that kernel/metadata stays pure (no import of tools/codegen/*).
type CellWireBundle struct {
	Listeners  []WireBundleListener
	Routes     []WireBundleRoute
	Subscribes []WireBundleSubscribe
}

// WireBundleListener mirrors markergen.ListenerSpec.
type WireBundleListener struct {
	Ref    string
	Prefix string
}

// WireBundleRoute mirrors markergen.RouteSpec (fields relevant to catalog exposure).
type WireBundleRoute struct {
	Slice    string
	Listener string
	SubPath  string
	Method   string // empty when marker omitted (defaults to "RegisterRoutes")
}

// WireBundleSubscribe mirrors markergen.SubscribeSpec (fields relevant to catalog).
type WireBundleSubscribe struct {
	Slice   string
	Topic   string
	Handler string
	Group   string
}

// CellWireSummary aggregates a cell's wire surface for catalog exposure.
// JSON / YAML tags follow the project camelCase convention.
type CellWireSummary struct {
	CellID     string              `json:"cellId"     yaml:"cellId"`
	Listeners  []WireListenerView  `json:"listeners"  yaml:"listeners"`
	Routes     []WireRouteView     `json:"routes"     yaml:"routes"`
	Subscribes []WireSubscribeView `json:"subscribes" yaml:"subscribes"`
}

// WireListenerView is the catalog wire view of a single listener declaration.
type WireListenerView struct {
	Ref    string `json:"ref"              yaml:"ref"`
	Prefix string `json:"prefix,omitempty" yaml:"prefix,omitempty"`
}

// WireRouteView is the catalog wire view of a single route mount.
type WireRouteView struct {
	Slice    string `json:"slice"            yaml:"slice"`
	Listener string `json:"listener"         yaml:"listener"`
	SubPath  string `json:"subPath,omitempty" yaml:"subPath,omitempty"`
	Method   string `json:"method,omitempty"  yaml:"method,omitempty"`
}

// WireSubscribeView is the catalog wire view of a single event subscription.
type WireSubscribeView struct {
	Slice   string `json:"slice"   yaml:"slice"`
	Topic   string `json:"topic"   yaml:"topic"`
	Handler string `json:"handler" yaml:"handler"`
	Group   string `json:"group"   yaml:"group"`
}

// DeriveCellWireSummaries converts a per-cell CellWireBundle map into an
// ordered []CellWireSummary sorted by CellID for deterministic catalog output.
//
// Only cell IDs present in both project.Cells and bundles appear in the
// result — cells with no bundle entry are silently omitted (they contribute
// an empty summary only when the caller explicitly supplies an empty
// CellWireBundle for that cell ID).
//
// nil or empty bundles map → returns an empty (non-nil) slice so that
// callers can iterate without nil checks.
func DeriveCellWireSummaries(project *ProjectMeta, bundles map[string]CellWireBundle) []CellWireSummary {
	if project == nil || len(bundles) == 0 {
		return []CellWireSummary{}
	}

	result := make([]CellWireSummary, 0, len(bundles))
	for cellID, b := range bundles {
		if _, ok := project.Cells[cellID]; !ok {
			continue
		}
		result = append(result, cellWireSummaryFrom(cellID, b))
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CellID < result[j].CellID
	})
	return result
}

// cellWireSummaryFrom converts one CellWireBundle into a CellWireSummary.
// Inner slices are always non-nil (use empty slice, not nil) so that JSON
// serialization emits [] rather than null, keeping the wire shape stable.
func cellWireSummaryFrom(cellID string, b CellWireBundle) CellWireSummary {
	listeners := make([]WireListenerView, 0, len(b.Listeners))
	for _, l := range b.Listeners {
		listeners = append(listeners, WireListenerView(l))
	}

	routes := make([]WireRouteView, 0, len(b.Routes))
	for _, r := range b.Routes {
		routes = append(routes, WireRouteView(r))
	}

	subscribes := make([]WireSubscribeView, 0, len(b.Subscribes))
	for _, s := range b.Subscribes {
		subscribes = append(subscribes, WireSubscribeView(s))
	}

	return CellWireSummary{
		CellID:     cellID,
		Listeners:  listeners,
		Routes:     routes,
		Subscribes: subscribes,
	}
}
