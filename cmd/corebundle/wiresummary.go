package main

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// BuildCellWireSummaries runs markergen.Merge to collect per-cell wire markers
// under root, then projects them into []metadata.CellWireSummary via
// metadata.DeriveCellWireSummaries.
//
// HTTP catalog (devtoolsOption) and CLI export (exportCatalog) share this
// single producer path so the wireSummary view is identical across surfaces.
//
// On marker scan errors the function returns a nil slice and the error so
// callers can choose to degrade gracefully (catalog endpoint) or surface the
// error (CLI --strict mode). Cells whose cell.go is absent or carries no
// markers contribute an empty summary entry — they are not omitted.
func BuildCellWireSummaries(root string, project *metadata.ProjectMeta) ([]metadata.CellWireSummary, error) {
	if project == nil {
		return []metadata.CellWireSummary{}, nil
	}
	bundles, err := markergen.Merge(root, project)
	if err != nil {
		return nil, fmt.Errorf("wire summary: marker scan: %w", err)
	}

	// Convert markergen.WireBundle → metadata.CellWireBundle (kernel boundary:
	// metadata/ must not import tools/codegen/markergen).
	cellBundles := make(map[string]metadata.CellWireBundle, len(bundles))
	for cellID, wb := range bundles {
		cellBundles[cellID] = wireBundleToCellWireBundle(wb)
	}

	return metadata.DeriveCellWireSummaries(project, cellBundles), nil
}

// wireBundleToCellWireBundle converts a markergen.WireBundle to the
// kernel-side metadata.CellWireBundle. The conversion is field-by-field with
// no information loss — both types have identical shape by design.
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
