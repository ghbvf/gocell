package app

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// buildCellWireSummaries runs markergen.Merge to collect per-cell wire markers
// under root, then projects them into []metadata.CellWireSummary via
// metadata.DeriveCellWireSummaries.
//
// CLI export and HTTP catalog share this projection logic (the HTTP path
// uses an identical helper in cmd/corebundle/wiresummary.go) so the
// wireSummary view is consistent across surfaces.
//
// Errors are returned so CLI callers can surface them; best-effort callers
// may log and discard.
func buildCellWireSummaries(root string, project *metadata.ProjectMeta) ([]metadata.CellWireSummary, error) {
	if project == nil {
		return []metadata.CellWireSummary{}, nil
	}
	bundles, err := markergen.Merge(root, project)
	if err != nil {
		return nil, fmt.Errorf("wire summary: marker scan: %w", err)
	}

	cellBundles := make(map[string]metadata.CellWireBundle, len(bundles))
	for cellID, wb := range bundles {
		cellBundles[cellID] = wireBundleToCellWireBundle(wb)
	}

	return metadata.DeriveCellWireSummaries(project, cellBundles), nil
}

// wireBundleToCellWireBundle converts a markergen.WireBundle to the
// kernel-side metadata.CellWireBundle keeping kernel/metadata import-free
// from tools/codegen/markergen.
func wireBundleToCellWireBundle(wb markergen.WireBundle) metadata.CellWireBundle {
	listeners := make([]metadata.WireBundleListener, 0, len(wb.Listeners))
	for _, l := range wb.Listeners {
		listeners = append(listeners, metadata.WireBundleListener{
			Ref:    l.Ref,
			Prefix: l.Prefix,
		})
	}

	routes := make([]metadata.WireBundleRoute, 0, len(wb.Routes))
	for _, r := range wb.Routes {
		routes = append(routes, metadata.WireBundleRoute{
			Slice:    r.Slice,
			Listener: r.Listener,
			SubPath:  r.SubPath,
			Method:   r.Method,
		})
	}

	subscribes := make([]metadata.WireBundleSubscribe, 0, len(wb.Subscribes))
	for _, s := range wb.Subscribes {
		subscribes = append(subscribes, metadata.WireBundleSubscribe{
			Slice:   s.Slice,
			Topic:   s.Topic,
			Handler: s.Handler,
			Group:   s.Group,
		})
	}

	return metadata.CellWireBundle{
		Listeners:  listeners,
		Routes:     routes,
		Subscribes: subscribes,
	}
}
