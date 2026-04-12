package celltest

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Compile-time check: StubEventRouter implements cell.EventRouter.
var _ cell.EventRouter = (*StubEventRouter)(nil)

// StubEventRouter records AddHandler calls for testing.
type StubEventRouter struct {
	Topics         []string
	Handlers       []outbox.EntryHandler
	ConsumerGroups []string
}

// AddHandler records the topic, handler, and consumerGroup.
func (r *StubEventRouter) AddHandler(topic string, handler outbox.EntryHandler, consumerGroup string) {
	r.Topics = append(r.Topics, topic)
	r.Handlers = append(r.Handlers, handler)
	r.ConsumerGroups = append(r.ConsumerGroups, consumerGroup)
}

// HandlerCount returns the number of registered handlers.
func (r *StubEventRouter) HandlerCount() int {
	return len(r.Topics)
}
