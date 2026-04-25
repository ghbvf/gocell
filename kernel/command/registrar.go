package command

// QueueRegistrar is an optional interface a Cell may implement to receive
// its command.Queue dependency from the runtime (mirrors kernel/cell.RouteGroupContributor).
// Runtimes SHOULD probe this via type assertion during Cell.Init.
//
// The concrete Queue instance is owned by the composition root (or a
// runtime/command discovery phase), not the Cell.
//
// The runtime consumer lives in runtime/command.DiscoverQueueRegistrars; see
// that package for wiring examples and for SweeperLifecycle (which manages
// Sweeper goroutines across Cell Start/Stop).
type QueueRegistrar interface {
	RegisterCommandQueue(q Queue)
}
