// Package outbox defines interfaces for the transactional outbox pattern:
// Writer (insert within a transaction), Emitter (write-or-direct abstraction),
// Relay (poll-and-publish), Publisher (fire-and-forget), and Subscriber (consume).
//
// Scope: L2 (OutboxFact) — local transaction + outbox publish; and
// L3 (WorkflowEventual) — cross-cell eventual consistency via event fanout.
//
// L4 (DeviceLatent) command queue semantics live in kernel/command/; outbox/
// is scoped to L2 (OutboxFact) and L3 (WorkflowEventual) event fanout.
//
// Implementations live in adapters/ (e.g., adapters/postgres, adapters/rabbitmq).
//
// ref: ThreeDotsLabs/watermill message/ -- Message unified model, Publisher/Subscriber interfaces
package outbox
