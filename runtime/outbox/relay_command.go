package outbox

import (
	"github.com/ghbvf/gocell/runtime/command"
)

// WithCommandDispatch wires the in-process async command bus into the relay
// (#1667 / ADR docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md
// §5 ④). dispatch maps each command id to its generated DispatchAsync; reg is
// the shared command.Registry the handlers were registered into. When a claimed
// entry's RoutingTopic equals a key in dispatch, publishBatch routes it to the
// mapped AsyncDispatchFunc in-process (decode payload → LookupHandler → handler)
// instead of marshaling + publishing to the broker. Events (and any topic not in
// the map) are unaffected.
//
// Composition root usage (the dispatch values MUST be generated DispatchAsync
// symbols — archtest COMMAND-ASYNC-DISPATCH-CALLER-01 locks this):
//
//	reg := command.NewRegistry()
//	_ = enqueue.Register(reg, handler)
//	relay.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
//	    enqueue.DispatchID: enqueue.DispatchAsync,
//	})
//
// This is an accumulating builder option (mirrors WithPendingDepthObserver): a
// nil reg or an empty/all-nil dispatch map is a silent no-op (the relay keeps
// operating event-only), and nil entries within the map are dropped. reg and the
// map values are concrete (pointer / func) types, so plain == nil is the correct
// nil check here (validation.IsNilInterface guards interface-typed values). Must
// be called before Start().
func (r *Relay) WithCommandDispatch(
	reg *command.Registry,
	dispatch map[command.CommandID]command.AsyncDispatchFunc,
) *Relay {
	if reg == nil || len(dispatch) == 0 {
		return r
	}
	m := make(map[command.CommandID]command.AsyncDispatchFunc, len(dispatch))
	for id, fn := range dispatch {
		if fn == nil {
			continue
		}
		m[id] = fn
	}
	if len(m) == 0 {
		return r
	}
	r.cmdRegistry = reg
	r.cmdDispatch = m
	return r
}

// commandDispatchFor returns the registered AsyncDispatchFunc for an entry's
// routing topic, or (nil, false) when the topic is not a registered command
// (the common event path). It is the single read point publishBatch uses to
// decide command-dispatch vs broker-publish.
func (r *Relay) commandDispatchFor(topic string) (command.AsyncDispatchFunc, bool) {
	if r.cmdDispatch == nil {
		return nil, false
	}
	fn, ok := r.cmdDispatch[command.CommandID(topic)]
	return fn, ok
}
