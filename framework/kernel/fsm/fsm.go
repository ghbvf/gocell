// Package fsm provides generic helpers for map-based finite-state-machine
// transition tables used by kernel state machines. A transition table maps
// each state to the states reachable from it. A terminal state has no outgoing
// edges: it may be either absent from the table (lookup miss → nil slice) or
// present with an explicit empty slice — both deny all transitions identically
// (slices.Contains over nil and []S{} is false). kernel/outbox uses the
// explicit-empty-key form so OUTBOX-STATE-TRANSITION-COMPLETENESS-01 can require
// every State const to be a key; kernel/saga uses the absent form. Either is
// valid.
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
// with no outgoing edges (absent from table, or present with an empty slice —
// e.g. a terminal state) permits no transitions.
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
