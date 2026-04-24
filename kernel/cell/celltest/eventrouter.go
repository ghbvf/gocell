package celltest

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// Compile-time check: StubEventRouter implements cell.EventRouter.
var _ cell.EventRouter = (*StubEventRouter)(nil)

// StubEventRouter records AddHandler + AddContractHandler calls for testing.
// Contracts captures the ContractSpec argument for contract-first
// subscriptions (nil-valued zero struct when AddHandler was used); Topics
// mirrors the resolved topic string so assertions stay uniform across the
// legacy and contract-first registration shapes.
type StubEventRouter struct {
	Topics         []string
	Handlers       []outbox.EntryHandler
	ConsumerGroups []string
	Contracts      []wrapper.ContractSpec
}

// AddHandler records the topic, handler, and consumerGroup. Contracts is
// appended with the zero-value ContractSpec so the parallel-slice invariant
// holds.
func (r *StubEventRouter) AddHandler(topic string, handler outbox.EntryHandler, consumerGroup string) {
	r.Topics = append(r.Topics, topic)
	r.Handlers = append(r.Handlers, handler)
	r.ConsumerGroups = append(r.ConsumerGroups, consumerGroup)
	r.Contracts = append(r.Contracts, wrapper.ContractSpec{})
}

// AddContractHandler records the topic (derived from spec.Topic), handler,
// consumerGroup, and the full ContractSpec so tests can assert the
// contract-first registration shape.
func (r *StubEventRouter) AddContractHandler(spec wrapper.ContractSpec, handler outbox.EntryHandler, consumerGroup string) {
	r.Topics = append(r.Topics, spec.Topic)
	r.Handlers = append(r.Handlers, handler)
	r.ConsumerGroups = append(r.ConsumerGroups, consumerGroup)
	r.Contracts = append(r.Contracts, spec)
}

// HandlerCount returns the number of registered handlers.
func (r *StubEventRouter) HandlerCount() int {
	return len(r.Topics)
}
