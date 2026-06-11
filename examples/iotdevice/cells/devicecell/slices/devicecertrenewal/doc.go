// Package devicecertrenewal is the cert-renewal producer slice: a STATELESS
// reconcile.Reconciler that, on each tick, scans the device repository for
// near-expiry certificates (cert_expires_at on the devices row, #1819) and
// enqueues a rotate-cert async command per device via runtime/command.EmitAsync
// with active-uniqueness (#1820).
//
// It is the iotdevice archetype-② reference (reconcile → command, issue #1757),
// the counterpart to the event-reactive devicebootstrap producer. The slice owns
// the cell's second usage of command.devicecommand.enqueue.v1 (role: invoke,
// declaration-only — see slice.yaml); the cert state it scans
// (cert_epoch / cert_expires_at) is persisted on the devices row by the device
// repo and seeded by the deviceregister slice.
//
// # Correctness: queue-owned active-uniqueness (F1 fix, #1820)
//
// "At most one active rotate-cert per (device,epoch)" is owned by the QUEUE, not
// by producer-side state. Each emit calls command.WithActiveUniqueness(deadline)
// so the queue admits at most one non-terminal command per (device,epoch) key;
// concurrent or duplicate emits (multiple reconcile ticks before the device
// dequeues) are coalesced by the queue to a no-op — no accumulation occurs.
//
// Un-executed commands self-expire: OverallDeadline = now+Policy.AttemptTTL.
// Once that elapses the Sweeper transitions the command to Expired (terminal),
// releasing the active-uniqueness key. The next reconcile tick then re-enqueues
// a fresh attempt — level-triggered retry without any producer-side state.
//
// # Bounded batch + requeue continuation
//
// Each Reconcile scans and emits at most Policy.BatchSize candidates. When a full
// batch is returned, Reconcile returns RequeueAfter=Policy.BatchRequeue so the
// Loop continues draining the next batch promptly, without a single-tick emit
// burst. Active-uniqueness coalesces already-queued commands to no-ops on the
// next scan, so the drain terminates monotonically.
package devicecertrenewal
