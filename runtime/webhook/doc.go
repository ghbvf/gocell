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
// # Listener auth: dedicated WebhookListener (HMAC at the application layer)
//
// BuildRouteGroups mounts each receiver on cell.WebhookListener — a dedicated
// listener, NOT PrimaryListener. Inbound webhooks authenticate at the
// application layer via HMAC signature verification inside the Receiver, so the
// composition root configures the webhook listener with an auth.AuthNone{} chain:
//
//	bootstrap.WithListener(cell.WebhookListener, ":8090",
//	    []auth.ListenerAuth{auth.AuthNone{}})
//
// Mounting on PrimaryListener would be wrong: that listener typically carries a
// JWT auth chain that would 401 an HMAC-signed (non-JWT) webhook POST before it
// ever reached the verifier. Keeping the webhook path on its own listener (the
// canonical pattern in .claude/rules/gocell/runtime-api.md §"单 listener 单 auth
// scheme") gives it its own port and HMAC-only auth without a Public carve-out on
// the business listener. If a webhook RouteGroup names a listener the assembly
// did not declare, bootstrap fail-fasts at startup with an actionable message
// (runtime/bootstrap phase5: "add WithListener(webhook,...)").
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
