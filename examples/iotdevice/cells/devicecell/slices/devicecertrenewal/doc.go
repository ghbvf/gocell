// Package devicecertrenewal is the cert-renewal producer slice: a
// reconcile.Reconciler that, on each tick, scans the device repository for
// near-expiry certificates (durable cert state on the devices row, #1819) and
// enqueues a deduplicated rotate-cert async command per device via
// runtime/command.EmitAsync.
//
// It is the iotdevice archetype-② reference (reconcile → command, issue #1757),
// the counterpart to the event-reactive devicebootstrap producer. The slice owns
// the cell's second usage of command.devicecommand.enqueue.v1 (role: invoke,
// declaration-only — see slice.yaml); the cert state it scans
// (cert_epoch / cert_expires_at / renewal_requested_epoch) is persisted on the
// devices row by the device repo and seeded by the deviceregister slice.
package devicecertrenewal
