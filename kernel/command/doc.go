// Package command provides L4 (DeviceLatent) command queue primitives for
// unicast device commands with durable ack semantics.
//
// # Lifecycle
//
// Commands move through a state machine driven by distinct Queue methods,
// each triggering exactly one transition:
//
//	Enqueue  → Pending
//	Dequeue  → Pending → Sent         (claim + lease, single atomic step)
//	Report   → Sent    → Delivered    (optional: device acknowledged receipt)
//	Ack      → any non-terminal → {Succeeded / Failed / Expired / Canceled}
//	Cancel   → any non-terminal → Canceled
//
// Pending:   enqueued, awaiting send to device transport.
// Sent:      transmitted to device transport; delivery attempt counter incremented.
// Delivered: device reported receipt and began execution (optional state).
// Succeeded: device confirmed successful execution.
// Failed:    permanent failure (device error or retries exhausted).
// Expired:   deadline elapsed before completion.
// Canceled:  explicitly canceled by operator/system.
//
// Devices MAY skip Report and go directly from Sent to Succeeded via
// Ack(Success); in that case DeliveredAt remains nil, marking the absence of
// the intermediate event.
//
// # Three-tier timeouts
//
// Each entry carries [Timeouts]:
//   - ScheduleToSend:  max duration from creation (Pending) to Sent.
//   - SendToComplete:  max duration from Sent to a terminal state.
//   - OverallDeadline: absolute max duration from creation to any terminal state.
//
// The [Sweeper] worker evaluates these deadlines periodically (via
// [ActiveScanner.ScanActive]) and calls [Queue.Ack](..., [AckTimeout], now)
// to transition overdue entries to [StatusExpired].
//
// # Facade
//
// [Queue] is the service-facing interface. All state transitions are owned
// by Queue methods; services MUST NOT mutate entry.Status directly.
//
// [ActiveScanner] is the role-based scan port used by Sweeper and by
// operational views that list non-terminal entries. It is intentionally
// distinct from Queue.Dequeue (the claim-with-lease primary consumer path).
//
// [QueueRegistrar] is an optional Cell-side interface; the runtime consumer
// lives in runtime/command (discovery + SweeperLifecycle wiring).
//
// # Testing
//
// The commandtest sub-package provides [commandtest.InMemQueue], an in-memory
// implementation of Queue + ActiveScanner + Writer suitable for unit tests
// and examples. Not suitable for production: single-process only.
//
// # References
//
// ref: kubernetes/kubernetes client-go util/workqueue/queue.go@master
// — Add/Get/Done primitive shape influences Enqueue/Dequeue/Ack; workqueue
// intentionally omits reason semantics, GoCell adds AckReason (mapped in
// SDK/runtime-equivalent layers, not app code).
//
// ref: nats-io/nats.go jetstream/message.go@main — InProgress maps to Report
// (Sent→Delivered, distinct network event); Ack/Nak/Term are single-step
// terminal dispositions; ConsumerInfo (ops view) is separate from Fetch
// (primary consume). GoCell follows the same split: ActiveScanner vs Dequeue.
//
// ref: temporalio/sdk-go internal/internal_task_handlers.go@master —
// ScheduledTime, StartedTime, CompletedTime are distinct events on the
// history; GoCell SentAt/DeliveredAt/CompletedAt mirror this per-event
// recording (not batched at Ack time).
//
// ref: temporal activity timeouts — ScheduleToStart / StartToClose /
// Heartbeat / ScheduleToClose four-tier model; GoCell simplifies to three
// tiers (ScheduleToSend / SendToComplete / OverallDeadline).
package command
