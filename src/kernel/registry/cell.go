package registry

import (
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// CellRegistry provides indexed access to cells and their slices.
type CellRegistry struct {
	cells  map[string]*metadata.CellMeta
	slices map[string][]*metadata.SliceMeta // keyed by cellID
}

// NewCellRegistry builds a registry from parsed project metadata.
func NewCellRegistry(project *metadata.ProjectMeta) *CellRegistry {
	r := &CellRegistry{
		cells:  make(map[string]*metadata.CellMeta),
		slices: make(map[string][]*metadata.SliceMeta),
	}
	if project == nil {
		return r
	}
	for id, c := range project.Cells {
		if c == nil {
			continue
		}
		r.cells[id] = c
	}
	for compositeKey, s := range project.Slices {
		if s == nil {
			continue
		}
		// Slices map is keyed by "cellID/sliceID"; extract cellID.
		cellID := s.BelongsToCell
		if cellID == "" {
			// Fallback: derive cellID from the composite key.
			if idx := strings.IndexByte(compositeKey, '/'); idx > 0 {
				cellID = compositeKey[:idx]
			}
		}
		r.slices[cellID] = append(r.slices[cellID], s)
	}
	return r
}

// Get returns a shallow copy of a cell by ID, or nil if not found.
func (r *CellRegistry) Get(id string) *metadata.CellMeta {
	c := r.cells[id]
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// SlicesFor returns copies of all slices belonging to the given cell.
func (r *CellRegistry) SlicesFor(cellID string) []*metadata.SliceMeta {
	src := r.slices[cellID]
	if len(src) == 0 {
		return nil
	}
	out := make([]*metadata.SliceMeta, len(src))
	for i, s := range src {
		cp := *s
		out[i] = &cp
	}
	return out
}

// AllIDs returns all cell IDs sorted alphabetically.
func (r *CellRegistry) AllIDs() []string {
	ids := make([]string, 0, len(r.cells))
	for id := range r.cells {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Count returns the total number of cells.
func (r *CellRegistry) Count() int {
	return len(r.cells)
}
