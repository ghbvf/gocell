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
