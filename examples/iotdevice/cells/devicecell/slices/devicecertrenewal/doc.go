// Package devicecertrenewal is the cert-renewal producer slice: a
// reconcile.Reconciler that, on each tick, scans the cell-internal cert store
// (internal/devicecert) for near-expiry certificates and enqueues a deduplicated
// rotate-cert async command per device via runtime/command.EmitAsync.
//
// It is the iotdevice archetype-② reference (reconcile → command, issue #1757),
// the counterpart to the event-reactive devicebootstrap producer. The slice owns
// the cell's second usage of command.devicecommand.enqueue.v1 (role: invoke,
// declaration-only — see slice.yaml); the cert Store it scans lives in the shared
// internal/devicecert package because the deviceregister slice seeds it.
package devicecertrenewal
