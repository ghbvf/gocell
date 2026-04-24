package command

// QueueRegistrar is an optional interface a Cell may implement to receive
// its command.Queue dependency from the runtime (mirrors kernel/cell.HTTPRegistrar).
// Runtimes SHOULD probe this via type assertion during Cell.Init.
type QueueRegistrar interface {
	RegisterCommandQueue(q Queue)
}
