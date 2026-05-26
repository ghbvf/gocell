// Package lifecycle provides a runtime read-surface over the maturity phase
// (kernel/cellvocab.Phase) of the cells composing a running assembly.
//
// The PhaseAggregator exposes each registered cell's current declared phase;
// consumers compute whatever "gap" they need (e.g. the maturity distribution
// of the running assembly). The composition root logs that distribution at
// startup so operators see, at a glance, whether a deployment is running
// experimental cells in a context that expects assets.
//
// This is the live-cell counterpart to the static catalog CellSpec.phase
// (devtools/catalog), which exposes declared phases from on-disk metadata.
// Both derive from the same single source — CellMeta.Lifecycle, parsed once at
// NewBaseCell construction — so they never diverge.
package lifecycle

import (
	"sort"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
)

// PhaseEntry pairs a cell's ID with its current declared maturity phase.
type PhaseEntry struct {
	CellID string
	Phase  cellvocab.Phase
}

// PhaseAggregator reports the maturity phase of each cell in an assembly.
type PhaseAggregator interface {
	// Snapshot returns one entry per cell, sorted by CellID for deterministic
	// output. The gap/aggregate (e.g. distribution) is left to the consumer.
	Snapshot() []PhaseEntry
}

type inMemoryPhaseAggregator struct {
	entries []PhaseEntry
}

// NewPhaseAggregator builds an aggregator over the given cells. The phases are
// snapshotted at construction (a cell's declared phase is immutable for its
// lifetime), so Snapshot is allocation-light and lock-free.
func NewPhaseAggregator(cells []cell.CellIdentity) PhaseAggregator {
	entries := make([]PhaseEntry, 0, len(cells))
	for _, c := range cells {
		entries = append(entries, PhaseEntry{CellID: c.ID(), Phase: c.Phase()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CellID < entries[j].CellID })
	return &inMemoryPhaseAggregator{entries: entries}
}

// Snapshot returns a defensive copy of the entries.
func (a *inMemoryPhaseAggregator) Snapshot() []PhaseEntry {
	if len(a.entries) == 0 {
		return nil
	}
	out := make([]PhaseEntry, len(a.entries))
	copy(out, a.entries)
	return out
}
