package command

import (
	"context"

	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
)

// AsyncDispatchFunc is the signature of a generated command DispatchAsync free
// function: it decodes a claimed command outbox.Entry's payload into the typed
// *Request, looks the handler up in reg, and invokes it — the async sibling of
// the synchronous generated Dispatch (ADR
// docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md §5 ④).
//
// The outbox relay routes a command entry (an entry whose RoutingTopic equals a
// command DispatchID) to the registered AsyncDispatchFunc via
// Relay.WithCommandDispatch, instead of publishing it to the broker.
//
// Every generated command package compile-asserts
// `var _ command.AsyncDispatchFunc = DispatchAsync`, so the generated
// DispatchAsync is the only value with this exact identity. archtest
// COMMAND-ASYNC-DISPATCH-CALLER-01 locks the values bound into a
// WithCommandDispatch map to generated DispatchAsync symbols, the async-path
// downstream half of the dispatch funnel double-lock (the synchronous half is
// COMMAND-DISPATCH-REGISTER-CALLER-01). reg is carried as a parameter (rather
// than captured in a composition-root closure) so the map values stay direct
// generated-symbol references the archtest can resolve via go/types.
type AsyncDispatchFunc func(ctx context.Context, reg *Registry, entry kout.Entry) error
