package celltest

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// Compile-time check: StubEventRouter implements cell.EventRouter.
var _ cell.EventRouter = (*StubEventRouter)(nil)

// StubEventRouter records AddContractHandler calls for testing. Topics,
// Handlers, ConsumerGroups and Contracts are parallel slices indexed by
// registration order.
type StubEventRouter struct {
	Topics         []string
	Handlers       []outbox.EntryHandler
	ConsumerGroups []string
	Contracts      []wrapper.ContractSpec
}

// AddContractHandler records the topic (derived from spec.Topic), handler,
// consumerGroup, and the full ContractSpec.
func (r *StubEventRouter) AddContractHandler(spec wrapper.ContractSpec, handler outbox.EntryHandler, consumerGroup string) {
	r.Topics = append(r.Topics, spec.Topic)
	r.Handlers = append(r.Handlers, handler)
	r.ConsumerGroups = append(r.ConsumerGroups, consumerGroup)
	r.Contracts = append(r.Contracts, spec)
}

// AddHandler is the round-4 legacy-test compat shim — see comment on
// cell.EventRouter interface. Records the topic as-is and appends a
// zero-value ContractSpec so the parallel-slice invariant holds.
//
// Deprecated-for-new-code: use AddContractHandler.
func (r *StubEventRouter) AddHandler(topic string, handler outbox.EntryHandler, consumerGroup string) {
	r.Topics = append(r.Topics, topic)
	r.Handlers = append(r.Handlers, handler)
	r.ConsumerGroups = append(r.ConsumerGroups, consumerGroup)
	r.Contracts = append(r.Contracts, wrapper.ContractSpec{})
}

// HandlerCount returns the number of registered handlers.
func (r *StubEventRouter) HandlerCount() int {
	return len(r.Topics)
}
