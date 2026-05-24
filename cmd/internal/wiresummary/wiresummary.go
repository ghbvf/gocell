// Package wiresummary provides the shared BuildCellWireSummaries helper used by
// both the HTTP catalog (cmd/corebundle) and the CLI export (cmd/gocell/app).
// The two surfaces previously carried verbatim copies; this package is the
// single source of truth (SonarCloud deduplication fix, K#05 PR-2).
package wiresummary

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
//
// Subscribes are derived from slice.yaml contractUsages[role=subscribe]
// (single-source flip, issue #856). markergen no longer carries subscribe
// markers; the subscribe surface is authoritative in project metadata.
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
	// Subscribes are now derived from project slice metadata, not from markers.
	cellBundles := make(map[string]metadata.CellWireBundle, len(bundles))
	for cellID, wb := range bundles {
		cwb := wireBundleToCellWireBundle(wb)
		cwb.Subscribes = subscribesFromProject(project, cellID)
		cellBundles[cellID] = cwb
	}

	return metadata.DeriveCellWireSummaries(project, cellBundles), nil
}

// subscribesFromProject derives the subscribe surface for a cell from
// slice.yaml contractUsages[role=subscribe] entries in the project metadata.
// This is the single source of truth after the issue #856 flip.
func subscribesFromProject(project *metadata.ProjectMeta, cellID string) []metadata.WireBundleSubscribe {
	var out []metadata.WireBundleSubscribe
	for key, s := range project.Slices {
		if s.BelongsToCell != cellID {
			continue
		}
		sliceID := key[len(cellID)+1:] // strip "cellID/" prefix
		for _, cu := range s.ContractUsages {
			if cu.Role != "subscribe" {
				continue
			}
			out = append(out, metadata.WireBundleSubscribe{
				Slice:   sliceID,
				Topic:   cu.Contract,
				Handler: cu.Handler,
				Group:   cu.Group,
			})
		}
	}
	return out
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

	// Subscribes are populated by the caller from project slice metadata
	// (subscribesFromProject), not from the markergen bundle.
	return metadata.CellWireBundle{
		Listeners: listeners,
		Routes:    routes,
	}
}
