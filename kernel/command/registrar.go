package command

// QueueRegistrar is an optional interface a Cell may implement to receive
// its command.Queue dependency from the runtime; it is the optional
// injection-direction interface for command queue handles; the cell-side
// equivalent is collapsed into cell.Registrar.
// Runtimes SHOULD probe this via type assertion during Cell.Init.
//
// The concrete Queue instance is owned by the composition root (or a
// runtime/command discovery phase), not the Cell.
//
// The runtime consumer lives in runtime/command.DiscoverQueueRegistrars; see
// that package for wiring examples. The Sweeper is driven as a
// reconcile.Reconciler via a kernel/reconcile.Loop.
type QueueRegistrar interface {
	RegisterCommandQueue(q Queue)
}
