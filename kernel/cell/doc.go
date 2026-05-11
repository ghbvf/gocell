// Package cell defines the core Cell and Slice abstractions for the GoCell
// framework: interfaces (CellIdentity / CellLifecycle / CellStatus /
// CellInventory composed into Cell), the BaseCell implementation, the
// Registry surface a Cell uses to declare routes / subscriptions / health
// probes / lifecycle hooks / config-reload callbacks, and consistency-mode
// resolution (mode_resolver.go) for L0..L4 emitter selection.
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
