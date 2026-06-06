# webhookdemo

A minimal, runnable GoCell example for **receiving inbound webhooks** — the
end-to-end reference for `kind: webhook` / `direction: inbound`.

It proves the full path the framework already ships but no live assembly wired
up before: an HMAC-signed request reaches the handler (**200**), a forged or
unsigned request is rejected by the runtime verifier before the handler runs
(**401 / 400**), and a replayed delivery is idempotent (**200**, handler not
re-run).

## What it wires

| Piece | File |
|-------|------|
| Inbound contract (`kind: webhook`) | `contracts/webhook/demo/events/v1/contract.yaml` |
| Cell + slice (`contractUsages[role=webhook-receive]`) | `cells/hooks/{cell.yaml,cell.go}`, `cells/hooks/slices/eventreceive/{slice.yaml,service.go}` |
| `reg.RegisterWebhookReceiver(...)` (cellgen-derived) | `cells/hooks/cell_gen.go` |
| Assembly + entrypoint | `assembly.yaml`, `main.go` (generated), `run.go` (hand-written) |

The `hooks` cell and its `eventreceive` slice are **L0 LocalOnly** — the runtime
`webhook.Receiver` does HMAC verification, the timestamp window check, and
idempotency claiming, and the slice's `HandleEvent` only validates/decodes the
verified delivery and structured-logs it (no transaction, no outbox, no
persistence). Inbound webhook receive is the sanctioned `TOPO-05` exception where
the owner cell is listed as the provider endpoint but implements the contract
through `webhook-receive`.

`run.go` mounts the receiver on the dedicated **`cell.WebhookListener`** with
`auth.AuthNone{}` — the HMAC signature **is** the application-layer auth, so a JWT
chain (as on the primary listener) would 401 the request before the verifier
runs. `WithWebhookSourceStore` seeds the per-sender secret and
`WithWebhookClaimer` provides idempotency.

## Run it

```bash
go run ./examples/webhookdemo
# webhook listener  127.0.0.1:8084   (POST signed webhooks here)
# health  listener  127.0.0.1:9099   (/healthz /readyz /metrics)
```

> Both listeners default to loopback (`127.0.0.1`): the webhook listener is
> `AuthNone` (the HMAC signature is the auth) and the demo HMAC secret is public
> in this repo, so binding all interfaces would expose a signable endpoint. A real
> deployment binds it explicitly with a non-demo per-sender secret.

> Demo mode wires **no metrics provider**, so the webhook instruments listed in
> `generated/metrics-schema.yaml` (e.g. `webhook_signature_failures_total`) are
> no-ops — a real Prometheus provider must be wired to observe them.

## Signature scheme (Svix / standard-webhooks)

The header names are declared in `contract.yaml` (`signature` block) and are
**vendor-agnostic** — GoCell's on-wire format is the standard-webhooks scheme,
not any specific provider's:

| Header | Value |
|--------|-------|
| `Webhook-Delivery-Id` | per-delivery id (idempotency key) |
| `Webhook-Timestamp` | unix seconds; rejected outside ±300s |
| `Webhook-Signature` | `v1,<base64(HMAC-SHA256(secret, signedString))>` (space-separated tokens allowed for key rotation) |

Signed string: `"{deliveryID}.{timestamp}.{body}"`.

### Producing a signed request

The signer lives in the framework — `kernel/webhook.NewHMACSigner(source)`. The
**runnable, error-handled reference is `webhook_e2e_test.go` (`signReqBody`)** —
that is the canonical executable signer. The sketch below is illustrative only
(it elides error handling and the surrounding `req`/`body`/`secret` setup):

```go
// illustrative — see webhook_e2e_test.go for the complete, error-handled version
sourceID, err := webhook.NewSourceID("demosource")          // handle err
src, err := webhook.NewSource(sourceID, secret)             // handle err
signer, err := webhook.NewHMACSigner(src)                   // handle err
did, err := webhook.NewDeliveryID("delivery-1")             // handle err
headers, err := signer.Sign(body, time.Now(), did)         // handle err
req.Header.Set("Webhook-Delivery-Id", string(headers.DeliveryID))
req.Header.Set("Webhook-Timestamp", headers.Timestamp)
req.Header.Set("Webhook-Signature", headers.Signature)
```

> The demo seeds a single hardcoded source secret in `run.go` for convenience.
> A real deployment loads each sender's secret from a secret manager and seeds it
> into the `SourceStore` — never hardcodes one. There is no standalone sender CLI:
> `curl` cannot compute the HMAC inline, so the e2e test is the executable signer.

## Next steps

This demo intentionally stops at decode + log to keep the webhook plumbing in
focus. For what to *do* with a received event — persisting it and emitting a
domain event through the transactional outbox (L2) — see `examples/todoorder`.

## Test it

```bash
go -C examples/webhookdemo test ./...
```

- `webhook_e2e_test.go` — boots the real assembly on loopback and asserts
  signed→200 / forged→401 / missing→400 / replay→200.
- `run_smoke_test.go` — boots through every bootstrap phase and shuts down cleanly.
- `cells/hooks/slices/eventreceive/service_test.go` — `HandleEvent` decode paths.
