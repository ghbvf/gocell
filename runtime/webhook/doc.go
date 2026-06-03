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
// # Integrating a new receiver
//
// The wiring is single-sourced from slice.yaml + contract.yaml; this package is
// never edited to add a receiver. The full authoring flow (contract.yaml
// signature/endpoints fields, slice.yaml contractUsages[role=webhook-receive],
// the cell.go struct field cellgen resolves the handler from, and the cellgen
// derivation shape) lives in contracts/webhook/README.md — that is the single
// source, not duplicated here. The runtime steps this package adds on top:
//
//   - composition root passes [bootstrap.WithWebhookSourceStore] (seeded with a
//     [kernel/webhook.Source] per sender) and [bootstrap.WithWebhookClaimer].
//   - bootstrap phase5 drains RegistrySnapshot.WebhookReceivers through
//     [BuildRouteGroups] and mounts the resulting route groups.
//
// # Troubleshooting
//
//   - bootstrap fails with "webhook receiver declared but no SourceStore /
//     Claimer wired": a cell registered a receiver but the composition root did
//     not call WithWebhookSourceStore / WithWebhookClaimer (the dependency is
//     mandatory only once a receiver exists — see options_webhook.go).
//   - all deliveries return 401: the SourceStore has no [kernel/webhook.Source]
//     for the receiver's sourceID, or the secret differs from the sender's. The
//     wire 401 is intentionally identical to a signature mismatch (anti-
//     enumeration); the real reason is in the server-side slog "reason" field.
//   - cellgen "no cell.go struct field for slice" / "ambiguous field": see the
//     contracts/webhook/README.md troubleshooting section (same resolution as
//     the subscribe path).
//
// # Observability
//
// Metrics are auto-wired by bootstrap: when WithMetricsProvider is configured,
// phase5 injects the shared kwh.Metrics collector into each Receiver (recording
// webhook_signature_failures_total and webhook_idempotency_hits_total) and
// phase6 into each Dispatcher (webhook_deliveries_total and
// webhook_delivery_duration_seconds). A nil/NopProvider leaves the zero
// kwh.Metrics, which records as a no-op — callers do nothing. See
// kernel/webhook/metrics.go.
//
// # Known gap: listener auth (KERNEL-WEBHOOK-01 follow-up)
//
// BuildRouteGroups mounts the receiver on cell.PrimaryListener WITHOUT a Public
// marker. In any assembly whose PrimaryListener carries a JWT auth chain, an
// external webhook POST (which carries an HMAC signature, not a JWT) is rejected
// by the JWT middleware with 401 before reaching the HMAC verifier. A runnable
// deployment therefore needs the webhook path marked Public or a dedicated
// webhook ListenerRef (see .claude/rules/gocell/runtime-api.md §"单 listener 单
// auth scheme"). Tracked as a KERNEL-WEBHOOK-01 follow-up; out of PR-6 scope.
//
// # Deferred capabilities
//
// Healthz probes were evaluated and dropped (vacuous/synonymous with the
// adapter _ready probes under today's immutable source store; tracked for when
// the store becomes runtime-mutable). Per-contract Claimer TTL configuration is
// deferred (the 24h done-TTL is a fixed constant today — see receiver.go).
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
