// Package assembly is the runtime that mounts and lifecycles a set of Cells
// declared in assembly.yaml. CoreAssembly orchestrates Cell lifecycle
// (register, init, start, stop, health): Cells are started in registration
// order (FIFO) and stopped in reverse order (LIFO).
//
// Hook timing: assembly enforces hook deadlines via clock.Clock. Test code
// uses clock.FakeClock; production wires adapters/clock.SystemClock.
//
// ref: uber-go/fx App.Start / App.Stop — phased lifecycle with rollback on
// failure during boot.
// ref: kubernetes-sigs/controller-runtime manager.Start — supervisor that
// owns goroutine ownership for registered Runnable controllers.
package assembly
