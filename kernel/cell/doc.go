// Package cell defines the core Cell and Slice abstractions for the GoCell
// framework: interfaces (CellIdentity / CellLifecycle / CellStatus /
// CellInventory composed into Cell), the BaseCell implementation, the
// Registry surface a Cell uses to declare routes / subscriptions / health
// probes / lifecycle hooks / config-reload callbacks, and consistency-mode
// resolution (mode_resolver.go) for L0..L4 emitter selection.
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
// kernel/cell imports — in alphabetical order — kernel/cellvocab,
// kernel/clock, kernel/contractspec, kernel/metadata,
// kernel/observability/metrics, kernel/outbox, and kernel/persistence.
// It does NOT import kernel/wrapper (after the G-04 contractspec
// extraction) and does NOT carry vocabulary types itself (after the
// G-04 cellvocab extraction). The runtime types it owns are the
// runtime-shaped pieces — BaseCell construction, Registry recorder,
// HookEvent / HookPhase, HealthStatus, DurabilityMode, mode-resolver
// emitter selection — not enum vocabulary or contract-spec values.
//
// History (G-04, 2026-05-10):
//
//   - Vocabulary types (CellType / ContractKind / ContractRole / Lifecycle
//     / Level + L0..L4 + Parse* + ValidRolesForKind / IsProviderRole /
//     IsConsumerRole + InternalPathPrefix) moved to kernel/cellvocab to
//     break the governance→cell and metadata→cell/levelrank reverse edges.
//   - ContractSpec moved to kernel/contractspec to break the cell→wrapper
//     reverse edge.
//   - kernel/cell/levelrank/ sub-package was absorbed into kernel/cellvocab.
//
// Cells under cells/ embed BaseCell and call its Init / Start / Stop
// methods; they consume Registry to declare capabilities. The bootstrap
// runtime (runtime/bootstrap) drains a RegistrySnapshot in phase5/phase6
// to wire HTTP routes and event subscriptions.
package cell
