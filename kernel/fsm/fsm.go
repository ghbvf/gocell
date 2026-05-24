// Package fsm provides generic helpers for map-based finite-state-machine
// transition tables used by kernel state machines. A transition table maps
// each state to the states reachable from it; terminal states are simply
// absent from the table (no outgoing edges), so a lookup miss yields a nil
// slice and denies all transitions.
//
// kernel/saga uses these helpers. kernel/command predates this package and
// still inlines the same lookup/defensive-copy bodies; migrating it is a
// candidate cleanup (it must be done without dropping command below the 90%
// kernel coverage gate, since the removed lines are fully covered). Tracked in
// gh issue #936.
//
// The helpers are intentionally tiny and type-agnostic: per-state-machine
// concerns — the Status enum, its String/Valid/IsTerminal methods, and the
// package-specific Transition error message — stay in the owning package.
// This removes the duplicated lookup/defensive-copy boilerplate without
// flattening the distinct state machines into one type.
package fsm

import "slices"

// CanTransition reports whether table permits a transition from → to. A state
// absent from table (e.g. a terminal state) permits no outgoing transitions.
func CanTransition[S comparable](table map[S][]S, from, to S) bool {
	return slices.Contains(table[from], to)
}

// AllowedTargets returns a defensive copy of the states reachable from `from`,
// or nil when there are none (terminal or unknown state). Mutating the result
// does not affect table.
func AllowedTargets[S comparable](table map[S][]S, from S) []S {
	targets := table[from]
	if len(targets) == 0 {
		return nil
	}
	out := make([]S, len(targets))
	copy(out, targets)
	return out
}
