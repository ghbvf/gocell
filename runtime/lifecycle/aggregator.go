// Package lifecycle provides a runtime read-surface over the maturity lifecycle
// (kernel/cellvocab.CellLifecycle) of the cells composing a running assembly.
//
// The LifecycleAggregator exposes each registered cell's current declared lifecycle;
// consumers compute whatever "gap" they need (e.g. the maturity distribution
// of the running assembly). The composition root logs that distribution at
// startup so operators see, at a glance, whether a deployment is running
// experimental cells in a context that expects assets.
//
// This is the live-cell counterpart to the static catalog CellSpec.lifecycle
// (devtools/catalog), which exposes declared lifecycles from on-disk metadata.
// Both derive from the same single source — CellMeta.Lifecycle, parsed once at
// NewBaseCell construction — so they never diverge.
package lifecycle

import (
	"sort"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
)

// LifecycleEntry pairs a cell's ID with its current declared maturity lifecycle.
type LifecycleEntry struct {
	CellID    string
	Lifecycle cellvocab.CellLifecycle
}

// LifecycleAggregator reports the maturity lifecycle of each cell in an assembly.
type LifecycleAggregator interface {
	// Snapshot returns one entry per cell, sorted by CellID for deterministic
	// output. The aggregate summary (e.g. the lifecycle distribution across the
	// assembly) is computed by the consumer.
	Snapshot() []LifecycleEntry
}

type inMemoryLifecycleAggregator struct {
	entries []LifecycleEntry
}

// NewLifecycleAggregator builds an aggregator over the given cells. The lifecycles
// are snapshotted at construction (a cell's declared lifecycle is immutable for
// its lifetime), so Snapshot is allocation-light and lock-free.
func NewLifecycleAggregator(cells []cell.CellIdentity) LifecycleAggregator {
	entries := make([]LifecycleEntry, 0, len(cells))
	for _, c := range cells {
		entries = append(entries, LifecycleEntry{CellID: c.ID(), Lifecycle: c.Lifecycle()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CellID < entries[j].CellID })
	return &inMemoryLifecycleAggregator{entries: entries}
}

// Snapshot returns a defensive copy of the entries.
func (a *inMemoryLifecycleAggregator) Snapshot() []LifecycleEntry {
	if len(a.entries) == 0 {
		return nil
	}
	out := make([]LifecycleEntry, len(a.entries))
	copy(out, a.entries)
	return out
}
