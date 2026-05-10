// Package assembly is the runtime that mounts and lifecycles a set of Cells
// declared in assembly.yaml. CoreAssembly orchestrates Cell lifecycle
// (register, init, start, stop, health): Cells are started in registration
// order (FIFO) and stopped in reverse order (LIFO).
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
//   - assembly imports kernel/cell, kernel/clock, kernel/metadata,
//     kernel/observability/metrics, and kernel/registry. Nothing in
//     kernel/ imports back into kernel/assembly — it is a top-tier
//     coordinator.
//   - assembly does not import runtime/, adapters/, or cells/. Concrete
//     Cell implementations are supplied by cmd/corebundle wiring; assembly
//     operates against the kernel/cell.Cell interface.
//   - The generator side (`gocell generate assembly`) reads
//     metadata.ProjectMeta + registry.{CellRegistry,ContractRegistry} to
//     emit cmd/<id>/modules_gen.go; this is a build-time concern, not runtime.
//
// Hook timing: assembly enforces hook deadlines via clock.Clock. Test code
// uses clock.FakeClock; production wires adapters/clock.SystemClock.
//
// ref: uber-go/fx App.Start / App.Stop — phased lifecycle with rollback on
// failure during boot.
// ref: kubernetes-sigs/controller-runtime manager.Start — supervisor that
// owns goroutine ownership for registered Runnable controllers.
package assembly
