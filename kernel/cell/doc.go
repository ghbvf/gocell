// Package cell defines the core Cell and Slice abstractions for the GoCell
// framework: interfaces (CellIdentity / CellLifecycle / CellStatus /
// CellInventory composed into Cell), the BaseCell implementation, and the
// Registrar surface a Cell uses to declare routes / subscriptions / health
// probes / lifecycle hooks / config-reload callbacks.
//
// History:
//
//   - G-04 (2026-05-10): Vocabulary types (CellType / ContractKind /
//     ContractRole / Lifecycle / Level + L0..L4 + Parse* +
//     ValidRolesForKind / IsProviderRole / IsConsumerRole +
//     InternalPathPrefix) moved to kernel/cellvocab to break the
//     governance→cell and metadata→cell/levelrank reverse edges.
//     ContractSpec moved to kernel/contractspec to break the cell→wrapper
//     reverse edge. kernel/cell/levelrank/ was absorbed into
//     kernel/cellvocab.
//   - G-10 (2026-05-24, this PR): AuthPlan + the auth dependency interfaces
//     moved to kernel/auth. DurabilityMode / Nooper / CheckNotNoop /
//     DemoTxRunner + the cell emitter mode resolver moved to kernel/outbox.
//     The Registry interface was renamed Registrar (Kratos verb-noun
//     distinction). The cell.ErrDegraded alias was removed; callers reach
//     outbox.ErrDegraded directly.
//
// Cells under cells/ embed BaseCell and call its Init / Start / Stop
// methods; they consume Registrar to declare capabilities. The bootstrap
// runtime (runtime/bootstrap) drains a RegistrySnapshot in phase5/phase6
// to wire HTTP routes and event subscriptions.
package cell
