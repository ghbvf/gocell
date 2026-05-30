// Package webhook implements the inbound-webhook receiver runtime for GoCell.
//
// # Layering
//
// runtime/webhook sits in the runtime/ layer. It may import kernel/webhook,
// kernel/idempotency, kernel/clock, kernel/cell, pkg/errcode, pkg/httputil,
// and stdlib. It must NOT import cells/, adapters/, or runtime/eventbus.
//
// # Capability-token closed loop
//
// The core invariant of this package is that the verify → claim → handler
// execution sequence is enforced at the type level, not merely by convention.
// Two unexported token types — [verified] and [claimed] — act as unforgeable
// receipts:
//
//   - [verified] can only be produced by [Receiver.verify].
//   - [claimed] can only be produced by [Receiver.claim], which requires a
//     [verified] value.
//   - [Receiver.invokeHandler] requires a [claimed] value and is the only site
//     that calls the business handler.
//
// Package-external code cannot construct either token type (unexported struct
// + no exported constructor), and [Receiver.verify] / [Receiver.claim] are
// also unexported, so the ordering invariant is a compile-time guarantee
// within the package: no business handler invocation can precede a successful
// verify+claim.
//
// # Entry point
//
// [BuildRouteGroups] converts a slice of [kernel/cell.WebhookReceiverRequest]
// values (accumulated by the cell registry during Init) into
// [kernel/cell.RouteGroup] values that bootstrap mounts onto the HTTP server.
//
// # References
//
// ref: svix/svix-webhooks go/webhook.go@main — HMAC-SHA256 header parsing and
// multi-token signature matching pattern.
// ref: stripe/stripe-go webhook/client.go@master — tolerance-window timestamp
// check and constant-time signature comparison.
// ref: ThreeDotsLabs/watermill-http pkg/http/subscriber.go@master — HTTP body
// read + idempotency integration for inbound event delivery.
package webhook
