// Package registry provides in-memory registries for Cell and Contract
// instances, used by kernel/assembly to look up dependencies at init time
// and by kernel/governance for cross-reference validation.
//
// # Read-only registries (CellRegistry, ContractRegistry)
//
// CellRegistry and ContractRegistry are populated once during assembly
// bootstrap (phase2 in runtime/bootstrap) and remain immutable afterward.
// Lookups are concurrent-safe by virtue of immutability, not via locking, and
// return deep copies so callers cannot alias-mutate the backing data.
//
// # Runtime registration state machine (ContractRegistrar, 303-US2 #2233)
//
// ContractRegistrar is the runtime, MUTABLE counterpart: it accepts
// runtime-submitted contracts and drives each through the sealed
// RegistrationState lifecycle (submitted → probing → conformant →
// pending-approval → approved → active → retired, with terminal probing→rejected
// / pending-approval→rejected branches). Unlike the read-only registries above,
// it is concurrent-safe BY LOCKING: a single sync.Mutex guards an append-only
// per-registration event log (source of truth for history) and an in-mem
// projection index (current state), kept consistent in one critical section —
// the two-truth model of kernel/saga/journal.MemJournal. Transitions are
// validated against the frozen legalTransitions table before any append, so an
// illegal transition is fail-closed with no half-write.
//
// AI-robust grading: RegistrationState is sealed (unexported field + accessor
// functions), so forging a state or jumping the machine is a compile error
// (Hard); the append-only event log is Hard by encapsulation (unexported, no
// exported mutator deletes events). The runtime transition guard is Medium (Go
// has no typestate; runtime fail-closed is the ceiling), with the table's edge
// set and the value set pinned by anti-vacuity freeze tests. Those freeze tests
// are package-level unit tests (TestLegalTransitions_Frozen,
// TestRegistrationState_FrozenRegistry) — not tools/archtest INVARIANT guards,
// since the state machine is package-internal; if a future story exposes it to
// out-of-package callers, the freeze tests are the archtest upgrade path.
package registry
