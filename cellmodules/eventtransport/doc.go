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
// ref: kernel/outbox.ResolveEmitter — the symmetric durability-gated funnel.
// ref: github.com/ThreeDotsLabs/watermill message/router.go — disabledPublisher pattern.
package eventtransport
