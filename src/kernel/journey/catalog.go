// Package journey provides query access to Journey metadata and status.
package journey

import (
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Catalog provides query access to all journeys and their status.
type Catalog struct {
	journeys    map[string]*metadata.JourneyMeta
	statusBoard map[string]*metadata.StatusBoardEntry // keyed by journeyId
}

// NewCatalog creates a Catalog from parsed project metadata.
// A nil or zero-value ProjectMeta produces an empty but usable Catalog.
func NewCatalog(project *metadata.ProjectMeta) *Catalog {
	c := &Catalog{
		journeys:    make(map[string]*metadata.JourneyMeta),
		statusBoard: make(map[string]*metadata.StatusBoardEntry),
	}
	if project == nil {
		return c
	}

	for id, j := range project.Journeys {
		c.journeys[id] = j
	}
	for i := range project.StatusBoard {
		entry := &project.StatusBoard[i]
		c.statusBoard[entry.JourneyID] = entry
	}
	return c
}

// Validate checks that every cell and contract referenced by journeys in this
// catalog actually exists in the provided sets. It returns an error (with code
// ErrReferenceBroken) listing all broken references, or nil if all references
// are valid.
//
// cellIDs and contractIDs are the known-good identifiers from the project
// registry. Passing nil sets is equivalent to passing empty sets.
func (c *Catalog) Validate(cellIDs, contractIDs map[string]struct{}) error {
	var msgs []string
	for _, j := range c.journeys {
		for _, cellRef := range j.Cells {
			if _, ok := cellIDs[cellRef]; !ok {
				msgs = append(msgs, fmt.Sprintf(
					"journey %q references unknown cell %q", j.ID, cellRef))
			}
		}
		for _, ctrRef := range j.Contracts {
			if _, ok := contractIDs[ctrRef]; !ok {
				msgs = append(msgs, fmt.Sprintf(
					"journey %q references unknown contract %q", j.ID, ctrRef))
			}
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	// Sort for deterministic output.
	sort.Strings(msgs)
	combined := msgs[0]
	for _, m := range msgs[1:] {
		combined += "; " + m
	}
	return errcode.New(errcode.ErrReferenceBroken, combined)
}

// Get returns a journey by ID, or nil if not found.
func (c *Catalog) Get(id string) *metadata.JourneyMeta {
	return c.journeys[id]
}

// List returns all journeys sorted by ID.
func (c *Catalog) List() []*metadata.JourneyMeta {
	result := make([]*metadata.JourneyMeta, 0, len(c.journeys))
	for _, j := range c.journeys {
		result = append(result, j)
	}
	sort.Slice(result, func(i, k int) bool {
		return result[i].ID < result[k].ID
	})
	return result
}

// CellJourneys returns journeys that reference the given cell ID,
// sorted by journey ID.
func (c *Catalog) CellJourneys(cellID string) []*metadata.JourneyMeta {
	var result []*metadata.JourneyMeta
	for _, j := range c.journeys {
		for _, cell := range j.Cells {
			if cell == cellID {
				result = append(result, j)
				break
			}
		}
	}
	sort.Slice(result, func(i, k int) bool {
		return result[i].ID < result[k].ID
	})
	return result
}

// ContractJourneys returns journeys that reference the given contract ID,
// sorted by journey ID.
func (c *Catalog) ContractJourneys(contractID string) []*metadata.JourneyMeta {
	var result []*metadata.JourneyMeta
	for _, j := range c.journeys {
		for _, ctr := range j.Contracts {
			if ctr == contractID {
				result = append(result, j)
				break
			}
		}
	}
	sort.Slice(result, func(i, k int) bool {
		return result[i].ID < result[k].ID
	})
	return result
}

// Status returns the status-board entry for a journey, or nil if not found.
func (c *Catalog) Status(journeyID string) *metadata.StatusBoardEntry {
	return c.statusBoard[journeyID]
}

// CrossCellJourneys returns journeys that involve more than one cell,
// sorted by journey ID.
func (c *Catalog) CrossCellJourneys() []*metadata.JourneyMeta {
	var result []*metadata.JourneyMeta
	for _, j := range c.journeys {
		if len(j.Cells) > 1 {
			result = append(result, j)
		}
	}
	sort.Slice(result, func(i, k int) bool {
		return result[i].ID < result[k].ID
	})
	return result
}

// Count returns the total number of journeys.
func (c *Catalog) Count() int {
	return len(c.journeys)
}
