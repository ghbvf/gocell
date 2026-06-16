package command

// QueueWithScanner is the command-queue handle the runtime injects into a
// Cell: a single value that is simultaneously the [Queue] (enqueue/dequeue
// lifecycle) AND the [ActiveScanner] (ScanActive/GetCommand). The dequeue path,
// the Sweeper, and the internal ops view all need ScanActive, so the registrar
// contract demands both up front rather than letting each implementer assert
// ActiveScanner at runtime and forget.
//
// INVARIANT QUEUE-REGISTRAR-SCANNER-REQUIRED-01 (Hard): [QueueRegistrar]'s
// RegisterCommandQueue parameter is frozen to QueueWithScanner (Queue +
// ActiveScanner), so registering a non-scanner queue is a compile error — not a
// runtime fail-fast. Reflect-frozen in
// tools/archtest/queue_registrar_scanner_required_test.go.
type QueueWithScanner interface {
	Queue
	ActiveScanner
}

// QueueRegistrar is an optional interface a Cell may implement to receive
// its command-queue dependency from the runtime; it is the optional
// injection-direction interface for command queue handles; the cell-side
// equivalent is collapsed into cell.Registrar.
// Runtimes SHOULD probe this via type assertion during Cell.Init.
//
// The concrete queue instance is owned by the composition root (or a
// runtime/command discovery phase), not the Cell. It is passed as the composite
// [QueueWithScanner] so that the "must also be an ActiveScanner" requirement is
// a compile-time constraint at the call site (see QueueWithScanner's invariant).
//
// The runtime consumer lives in runtime/command.DiscoverQueueRegistrars; see
// that package for wiring examples. The Sweeper is driven as a
// reconcile.Reconciler via a kernel/reconcile.Loop.
type QueueRegistrar interface {
	RegisterCommandQueue(q QueueWithScanner)
}
