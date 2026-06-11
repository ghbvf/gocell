// Package devicecertrenewal is the cert-renewal producer slice: a
// reconcile.Reconciler that, on each tick, scans the device repository for
// near-expiry certificates (durable cert state on the devices row, #1819) and
// enqueues a rotate-cert async command per device via runtime/command.EmitAsync.
//
// It is the iotdevice archetype-② reference (reconcile → command, issue #1757),
// the counterpart to the event-reactive devicebootstrap producer. The slice owns
// the cell's second usage of command.devicecommand.enqueue.v1 (role: invoke,
// declaration-only — see slice.yaml); the cert state it scans
// (cert_epoch / cert_expires_at / renewal_requested_epoch / renewal_requested_at)
// is persisted on the devices row by the device repo and seeded by the
// deviceregister slice.
//
// # Deduplication (time-window retry release)
//
// After a successful emit, the Reconciler atomically marks BOTH
// renewal_requested_epoch and renewal_requested_at on the devices row. The scan
// then suppresses the same (device, epoch) pair for Policy.RetryInterval. Once
// that window elapses with the epoch un-advanced (terminal-failed command / device
// never executed / cert expired), the pair re-enters the candidate set and the
// same commandID is re-emitted — AT MOST ONE command per RetryInterval per epoch,
// until the epoch advances.
//
// # Bounded batch + requeue continuation
//
// Each Reconcile scans and emits at most Policy.BatchSize candidates. When a full
// batch is returned, Reconcile returns RequeueAfter=Policy.BatchRequeue so the
// Loop continues draining the next batch promptly, without a single-tick emit
// burst. Already-marked certs fall outside the retryBefore window on the next
// scan, so the drain terminates monotonically.
package devicecertrenewal
