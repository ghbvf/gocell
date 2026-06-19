// Package devicebootstrap implements the device-bootstrap slice: an
// event-reactive producer that subscribes to event.device-registered.v1 and,
// for every newly registered device, reactively emits a
// command.remotecommand.v1 async command outbox entry (a "bootstrap"
// command) via the generated cmdremote.EmitAsync wrapper.
//
// The slice owns no domain state: it decodes the source event payload, builds a
// typed enqueue Request, and emits the command. The command_id carried in the
// emitted entry's idempotency slot is the SOURCE event's entry.ID() so that an
// at-least-once redelivery of the same device-registered event deterministically
// dedups to the same bootstrap command instance.
package devicebootstrap
