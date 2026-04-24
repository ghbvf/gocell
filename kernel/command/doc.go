// Package command provides L4 (DeviceLatent) command queue primitives for
// unicast device commands with durable ack semantics.
//
// # Lifecycle
//
// Commands move through a linear state machine:
//
//	Pending → Sent → Delivered → {Succeeded / Failed / Expired / Canceled}
//
// Pending:   enqueued, awaiting send to device transport.
// Sent:      transmitted to device transport; delivery attempt counter incremented.
// Delivered: device ACK'd receipt (not execution).
// Succeeded: device confirmed successful execution.
// Failed:    permanent failure (device error or retries exhausted).
// Expired:   deadline elapsed before completion.
// Canceled:  explicitly canceled by operator/system.
//
// # Three-tier timeouts
//
// Each entry carries [Timeouts]:
//   - ScheduleToSend:  max duration from creation (Pending) to Sent.
//   - SendToComplete:  max duration from Sent to a terminal state.
//   - OverallDeadline: absolute max duration from creation to any terminal state.
//
// The [Sweeper] worker evaluates these deadlines periodically and calls
// [StateAdvancer.AdvanceStatus] to transition overdue entries to [StatusExpired].
//
// # High-level facade
//
// [Queue] is the Cell-facing interface:
//   - Enqueue stores an entry atomically; optional [EnqueueOptions.Authz] for T3 RBAC.
//   - Dequeue returns leased entries for a target device.
//   - Ack finalises a command with an [AckReason].
//   - ExtendLease renews a lease for long-running operations.
//   - Cancel aborts a non-terminal command.
//
// # Testing
//
// The commandtest sub-package provides [commandtest.InMemQueue], an in-memory
// implementation of Queue + Reader + Writer + StateAdvancer suitable for unit
// tests and examples.
//
// # References
//
// ref: ThreeDotsLabs/watermill message/router.go@master — Ack/Nack semantics adopted
// for Disposition (DispositionAck/Requeue/Reject). GoCell command Ack maps to
// broker-level Ack; AckTimeout maps to Nack+requeue.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/client-go/util/workqueue/queue.go@master
// — Add/Get/Done three-state model inspired Queue.Enqueue/Dequeue/Ack. ShutDownWithDrain
// semantics influence Sweeper.Stop idempotency.
//
// ref: nats-io/nats.go jetstream Msg — InProgress (heartbeat lease extension) maps to
// Queue.ExtendLease; NakWithDelay maps to AckTimeout+requeue; Term maps to
// AckRejected→StatusCanceled.
//
// ref: rabbitmq/amqp091-go channel.go@main — Ack/Nack(requeue=false)→DLX pattern
// mirrors AckRejected routing to dead-letter. Optimistic-lock AdvanceStatus prevents
// double-expire across replicas, consistent with RabbitMQ single-active-consumer.
//
// ref: temporal Nexus operations — ScheduleToCloseTimeout / ScheduleToStartTimeout /
// StartToCloseTimeout three-tier timeout model directly maps to Timeouts struct.
// Temporal's Nexus async operation lifecycle (SCHEDULED→STARTED→terminal) maps to
// Pending→Sent→terminal.
package command
