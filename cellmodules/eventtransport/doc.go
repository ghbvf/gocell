// Package eventtransport resolves the topology-gated outbox event transport
// (outbox.Publisher + outbox.Subscriber) for a composition root: an in-process
// in-memory bus in demo topology, and a real message broker (RabbitMQ) in
// postgres topology.
//
// It is the publisher-side analog of kernel/outbox.ResolveEmitter: a single
// funnel that maps a [runtime/bootstrap.Topology] to the right transport, so the
// composition root never hand-picks an implementation. It lives in cellmodules/
// (the Composition Root helper layer, alongside cellsecrets) because resolving a
// broker requires importing adapters/rabbitmq — which runtime/composition, by
// its layering contract, must not do.
//
// # INVARIANT: COREBUNDLE-EVENTBUS-FUNNEL-01
//
// The in-memory eventbus (runtime/eventbus) is reachable ONLY through this
// resolver's demo branch. The production composition root cmd/corebundle must
// not import runtime/eventbus directly; the depguard rule
// "corebundle-no-direct-eventbus" (.golangci.yml) enforces that path-level
// import ban (ref: .claude/rules/gocell/ai-robust.md §"路径级 import ban →
// depguard"). A direct eventbus.New in the composition root would re-open the
// #1940 gap where postgres (durable) topology publishes already-persisted outbox
// entries to an in-process bus — lost across processes / restarts.
//
// In postgres topology [resolveBrokerSpec] fail-closes when no broker URL is
// configured: a missing GOCELL_AMQP_URL is a startup error, never a silent
// degrade back to in-memory.
//
// # Composition-root usage
//
// A composition root resolves the transport once and threads its three outputs
// into bootstrap:
//
//	transport, err := eventtransport.Resolve(clk, topo, eventtransport.Config{AMQPURL: os.Getenv("GOCELL_AMQP_URL")})
//	// ... handle err ...
//	opts := []bootstrap.Option{
//	    bootstrap.WithPublisher(transport.Publisher),
//	    bootstrap.WithSubscriber(transport.Subscriber),
//	    bootstrap.WithEventTransportKind(transport.Kind), // sealed broker-kind for the phase0 split gate (#2211)
//	}
//	for _, mr := range transport.Resources { // broker connection (postgres mode) for LIFO teardown
//	    opts = append(opts, bootstrap.WithManagedResource(mr))
//	}
//
// See cmd/corebundle and examples/ssobff for the two production wirings.
//
// # INVARIANT: EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01
//
// Resolve stamps each [Transport] with a sealed [runtime/bootstrap.EventTransportKind]
// (Transport.Kind): RealBrokerEventTransport() on the postgres → RabbitMQ branch,
// InMemoryEventTransport() on the demo branch. This package is the SINGLE
// sanctioned minter of the real-broker variant — the composition root threads
// Transport.Kind into bootstrap.WithEventTransportKind so the phase0
// broker-mandatory gate (validateSplitTopologyBroker) trusts a type-system fact
// instead of the older StorageBackend()=="postgres" proxy ("non-nil ≠ real
// broker" closed, #2211). bootstrap (framework module) must export the
// constructor for this package (root module) to call it, and a
// framework/.../internal/ package cannot bridge that cross-module import, so the
// caller restriction is a structural Medium ceiling enforced by archtest
// EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01 (a call-level AST scan, same family as the
// RowScopeAll minter and COMMAND-ASYNC-EMIT-CALLER-01). The production end-to-end
// guarantee remains Hard via COREBUNDLE-EVENTBUS-FUNNEL-01 above: an in-memory bus
// is import-unexpressible in the production roots, so a forged real-broker kind
// cannot be paired with one there.
//
// # Broker connection is intentionally assembly-level (not per-cell)
//
// The broker (RabbitMQ, GOCELL_AMQP_URL) is the cross-cell event bus: its
// defining semantic is cross-cell sharing, not per-cell isolation. Distinct
// per-cell broker connections — or distinct broker instances per cell — would
// sever the publish/subscribe chain between cells (cell A publishes to its own
// broker, cell B subscribes to its own broker, events never cross). Therefore,
// a per-cell broker connection seam analogous to the per-cell DB connection
// seam (cellmodules/percellpg) is intentionally not modeled here.
//
// The correct unit of broker ownership is the per-process (assembly-level)
// connection, i.e. the single GOCELL_AMQP_URL. Per-cell publisher/subscriber
// fan-out within one broker connection (e.g. per-cell exchange or routing-key
// namespacing) is a separate concern coupled to per-cell outbox relay fan-out;
// both are tracked in the per-cell infra fan-out backlog (#2152) and are not
// implemented here.
//
// ref: kernel/outbox.ResolveEmitter — the symmetric durability-gated funnel.
// ref: github.com/ThreeDotsLabs/watermill message/router.go — disabledPublisher pattern.
package eventtransport
