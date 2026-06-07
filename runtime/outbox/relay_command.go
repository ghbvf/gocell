package outbox

import (
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/runtime/command"
)

// WithCommandDispatch wires the in-process async command bus into the relay
// (#1667 / ADR docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md
// §5 ④). dispatch maps each command id to its generated DispatchAsync; reg is
// the shared command.Registry the handlers were registered into; claimer is the
// two-phase idempotency Claimer that wraps every in-process command dispatch
// (Claim → dispatch → Commit/Release) so an at-least-once source event redelivery
// cannot enqueue the same command twice (#1698). When a claimed entry's
// RoutingTopic equals a key in dispatch, publishBatch routes it to the mapped
// AsyncDispatchFunc in-process (decode payload → LookupHandler → handler) instead
// of marshaling + publishing to the broker. Events (and any topic not in the map)
// are unaffected.
//
// claimer is a REQUIRED positional dependency: it is compile-time inexpressible to
// "wire command dispatch but skip deduplication". A nil claimer combined with a
// non-empty dispatch map is rejected at Start() (fail-closed); see the nil-guard
// in Start.
//
// Composition root usage (the dispatch values MUST be generated DispatchAsync
// symbols — archtest COMMAND-ASYNC-DISPATCH-CALLER-01 locks this):
//
//	reg := command.NewRegistry()
//	_ = enqueue.Register(reg, handler)
//	relay.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
//	    enqueue.DispatchID: enqueue.DispatchAsync,
//	}, claimer)
//
// This is a replace-semantics builder option, NOT accumulating: each call sets
// (does not merge) cmdRegistry + cmdDispatch + cmdClaimer, so a second call
// REPLACES the whole table (last-write-wins) — pass all commands in one call. A
// nil reg or an empty/all-nil dispatch map is a silent no-op (the relay keeps
// operating event-only, leaving any prior table untouched), and nil entries within
// the map are dropped. reg and the map values are concrete (pointer / func) types,
// so plain == nil is the correct nil check here (validation.IsNilInterface guards
// the interface-typed claimer at Start). Must be called before Start().
func (r *Relay) WithCommandDispatch(
	reg *command.Registry,
	dispatch map[command.CommandID]command.AsyncDispatchFunc,
	claimer idempotency.Claimer,
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
	r.cmdClaimer = claimer
	return r
}

// commandDispatchFor returns the registered AsyncDispatchFunc for an entry's
// routing topic, or (nil, false) when the topic is not a registered command
// (the common event path). It is the single read point publishBatch uses to
// decide command-dispatch vs broker-publish.
//
// The discriminator is dispatcher-map MEMBERSHIP (a closed set), not the topic
// string shape: the command.* naming convention is a readability aid, not the
// gate. Correctness comes from the closed map + the generated-DispatchAsync-only
// values that COMMAND-ASYNC-DISPATCH-CALLER-01 locks (ADR §D10). An event topic
// is never in the map, so it cleanly falls through to the broker path.
func (r *Relay) commandDispatchFor(topic string) (command.AsyncDispatchFunc, bool) {
	if r.cmdDispatch == nil {
		return nil, false
	}
	fn, ok := r.cmdDispatch[command.CommandID(topic)]
	return fn, ok
}
