// Package devicecertcompletion closes the L4 cert-renewal convergence loop
// (#1870): it records the SERVER-SIDE observation of a device's cert rotation
// completion so a successfully-renewed device leaves the near-expiry candidate
// set instead of being re-driven forever.
//
// # Why this exists
//
// The devicecertrenewal reconciler (archetype ②) emits a rotate-cert command
// when a device's cert is near expiry. The device rotates and acks. Before #1870
// the ack moved the command to a terminal status (releasing the queue
// active-uniqueness key, #1820) but NOTHING advanced the device's observed cert
// state — so the device stayed near-expiry forever and the producer re-emitted
// every Sweeper cycle. The convergence loop never closed.
//
// # Design: a reactive completion consumer (cert-manager two-controller split)
//
// This slice owns the rotation-resolved event end-to-end, decoupling the
// synchronous device-ack HTTP path from the asynchronous, idempotent cert-state
// write (cert-manager separates its issuing controller from its readiness
// controller for the same reason):
//
//   - PUBLISH (OnCommandResolved): a generic command-resolution hook the
//     devicecmd Service fires after every terminal ack. For rotate-cert commands
//     it emits event.devicecert-rotation-resolved.v1 carrying {deviceId, epoch,
//     outcome, resolvedAt}; other command types are ignored. The hook keeps
//     devicecmd generic — the cert-specific filtering lives here.
//   - SUBSCRIBE (HandleRotationResolved): consumes the event. On a succeeded
//     outcome it CAS-advances the device's cert state via
//     repo.AdvanceCertAfterRotation (epoch+1, cert_expires_at = resolvedAt +
//     domain.CertValidity) — the cert-manager loop-closing write: once the
//     expiry moves past the near-expiry window the device is no longer a
//     candidate, no explicit "remove" step needed. On a failed/rejected outcome
//     it records a structured warning (the production deployment would add a
//     bounded failure counter; the example's bar is a fail-closed-redacted log).
//
// # Idempotency
//
// repo.AdvanceCertAfterRotation is a CAS on the rotated epoch, so at-least-once
// delivery and duplicate device acks are absorbed to a no-op. The handler needs
// no per-message idempotency guard of its own.
//
// # Boundary (回执-driven, not Sweeper-driven)
//
// Only a device ACK (回执) drives the resolved event. A fully-silent device whose
// command is Sweeper-expired (StatusExpired) does NOT flow through this slice;
// that "device may be stuck" condition is observed by the reconciler's existing
// per-tick warning. Server recording of the reported completion is in scope;
// device-side cert application (firmware) remains out of scope per ADR-1044 §277.
package devicecertcompletion
