# orderfulfillment Example

A multi-step order fulfillment application demonstrating GoCell's **L3 WorkflowEventual saga orchestration** with automatic compensation on failure.

## What This Example Demonstrates

- **L3 saga orchestration**: the `placeorder` slice enrolls a saga instance in the journal; the `Coordinator` drives it asynchronously through four forward steps.
- **Compensable steps**: three of the four steps (`reserveInventory`, `chargePayment`, `ship`) declare a `CompensateFunc`; the fourth (`notifyUser`) is terminal and intentionally not compensable (notifications are best-effort).
- **Automatic compensation**: when `chargePayment` fails (controlled by `paymentShouldFail=true`), the coordinator compensates **only the steps that have already completed**. On a `chargePayment` failure, only `CompensateReserveInventory` fires (releasing the inventory reservation that was acquired in step 1); `CompensateChargePayment` and `CompensateShip` do NOT run because those steps were never executed.
- **In-memory stores**: all domain stores (`OrderRepository`, `InventoryStore`, `PaymentStore`, `ShipmentStore`) are in-memory — no external dependencies required.

## Architecture

```
POST /api/v1/orders/
        │
        ▼
placeorder.Service.PlaceOrder()
  ├─ orders.Create(order)          — persist order
  └─ journal.Enqueue(instance)     — enroll saga in shared MemJournal
                                           │
        ┌──────────────────────────────────┘  (async)
        ▼
Coordinator.tickLoop()
  ├─ Step 1: reserveInventory   ──► CompensateReserveInventory (releases reservation)
  ├─ Step 2: chargePayment      ──► CompensateChargePayment (refunds payment)
  ├─ Step 3: ship               ──► CompensateShip (cancels shipment)
  └─ Step 4: notifyUser         (terminal, no compensation)
```

The `Coordinator` and `placeorder.Service` share the **same `MemJournal` instance** — enrollment by the HTTP handler is immediately visible to the coordinator's claim loop without any broker.

## The Four Steps

| Step | Forward side-effect | Compensate side-effect | Compensable |
|------|--------------------|-----------------------|-------------|
| `reserveInventory` | decrements `InventoryStore.Available(item)` | `InventoryStore.Release(orderID)` restores stock | Yes |
| `chargePayment` | records payment in `PaymentStore` | `PaymentStore.Refund(orderID)` removes payment | Yes |
| `ship` | records shipment in `ShipmentStore` | `ShipmentStore.CancelShipment(orderID)` removes shipment | Yes |
| `notifyUser` | logs notification (Notified: true) | — (best-effort; not compensable) | No |

**Compensation is selective**: the coordinator only compensates steps that have already completed. If `chargePayment` (step 2) fails, compensation runs in reverse over completed steps only: `CompensateReserveInventory` fires (step 1 completed), but `CompensateChargePayment` and `CompensateShip` do NOT fire (steps 2 and 3 were never successfully executed).

## Quick Start

No external dependencies required. Run from the **repository root** —
orderfulfillment is its own `go.work` module (#1556) and the workspace resolves
the `./examples/orderfulfillment/…` paths (including the integration-test
commands below) automatically.

```bash
go run ./examples/orderfulfillment
```

The server starts on `127.0.0.1:8083` (primary listener, loopback only). **Demo mode**: the primary listener is unauthenticated (`auth.public: true`) for local exploration only — do not expose to untrusted networks.

The health listener (`:9093`) is also bound to `127.0.0.1` (loopback only). Demo mode does not declare an internal listener.

### Place an order (happy path)

The example pre-seeds inventory with `"widget": 100` and `"gadget": 100` units.

```bash
curl -X POST http://127.0.0.1:8083/api/v1/orders/ \
  -H 'Content-Type: application/json' \
  -d '{"item":"widget","amountCents":1299,"idempotencyKey":"order-1001"}'
```

Response (202 Accepted):

```json
{"data":{"orderId":"ord-order-1001","status":"accepted"}}
```

The saga runs asynchronously. You can observe enrollment in the server logs via the `placeorder: order placed, saga enrolled` entry (contains `order_id` and `definition_id` fields). Per-step progress events are published to the outbox topic; in demo mode the `NoopEmitter` discards them — connect a real outbox emitter (see Durable Wiring Checklist) to observe individual step events.

### Trigger compensation (payment failure)

`paymentShouldFail` is a **demo-only** failure-injection field. Production services MUST NOT expose debug hooks on the HTTP API wire — drive failures from real downstream responses.

Set `paymentShouldFail: true` to force the `chargePayment` step to return a conflict error, causing the coordinator to compensate in reverse over **completed steps only**:

```bash
curl -X POST http://127.0.0.1:8083/api/v1/orders/ \
  -H 'Content-Type: application/json' \
  -d '{"item":"widget","amountCents":1299,"paymentShouldFail":true,"idempotencyKey":"order-1002"}'
```

The saga reaches `KindSagaCompensated`: `CompensateReserveInventory` releases the inventory reservation (restoring stock to 100); no payment or shipment is ever recorded because those steps were never reached.

### Request fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `item` | string | yes | Product name (1–256 chars) |
| `amountCents` | integer | yes | Price in cents (1–100,000,000) |
| `idempotencyKey` | string | yes | Client-supplied key for exactly-once placement (1–128 chars); same key = same order, no duplicate saga |
| `paymentShouldFail` | boolean | no | Demo-only failure injection; omit in production |

### Idempotency

The `idempotencyKey` maps directly to the order ID: `orderId = "ord-" + idempotencyKey`. A repeated `POST` with the same key returns the same `orderId` without re-running any saga step — safe to retry on timeout.

This mirrors the [Temporal workflow-id](https://docs.temporal.io/concepts/what-is-a-workflow-id) and [DTM GID](https://en.dtm.pub/guide/gid.html) models: the caller owns the deduplication key.

```bash
# First call — creates order and enrolls saga
curl -X POST http://127.0.0.1:8083/api/v1/orders/ \
  -H 'Content-Type: application/json' \
  -d '{"item":"widget","amountCents":1299,"idempotencyKey":"order-1001"}'

# Retry with same key — returns same orderId, no duplicate saga
curl -X POST http://127.0.0.1:8083/api/v1/orders/ \
  -H 'Content-Type: application/json' \
  -d '{"item":"widget","amountCents":1299,"idempotencyKey":"order-1001"}'
```

Both calls return `{"data":{"orderId":"ord-order-1001","status":"accepted"}}`. The server logs the second call as `placeorder: idempotent hit, returning existing order`.

### Query order status (F10 closed)

After placing an order, you can query the saga terminal state via GET:

```bash
curl http://127.0.0.1:8083/api/v1/orders/<orderId>
```

Response (200 OK, initial query immediately after placement):

```json
{"data":{"orderId":"ord-...","status":"accepted"}}
```

Query again after a moment to observe the terminal state (`succeeded` or `compensated`).

The `status` field reflects the saga state:

| Value | Meaning |
|-------|---------|
| `accepted` | Order enrolled; coordinator has not yet started forward steps |
| `running` | One or more forward steps have started |
| `succeeded` | All four steps completed (`KindSagaSucceeded`) |
| `compensated` | At least one step failed and all prior steps were rolled back (`KindSagaCompensated`) |
| `failed` | Saga failed without full compensation (`KindSagaFailed` / expired / compensation failed) |

## Health

```bash
# Liveness — always 200 when the process is running
curl http://127.0.0.1:9093/healthz

# Readiness — 200 when the saga coordinator journal is healthy
curl http://127.0.0.1:9093/readyz
```

The `/readyz` probe includes `orderfulfillmentcell_repo_ready` (coordinator journal liveness) registered via the `WithCoordinator` option.

## Durable Wiring Checklist

To move from demo mode to a durable L3 saga path, wire all of the following in the composition root (`run.go`):

1. Replace `journal.NewMemJournal` with a persistent PG-backed journal (durable saga state survives restarts).
2. Replace `outbox.DemoTxRunner{}` with a real `persistence.TxRunner` (e.g. `postgres.TxManager`) — compose via `persistence.WrapForCell`.
3. Replace `outbox.NewNoopEmitter()` with a real `outbox.Emitter` (e.g. relay-backed broker publisher) — compose via `outbox.WrapEmitterForCell`.
4. Replace the `kernelmetrics.NopProvider{}` passed to `obmetrics.NewSagaCollector` with a real Prometheus provider (e.g. from `bootstrap.MetricsProvider()`); in demo mode, saga metrics are discarded.
5. Replace `auth.AuthNone{}` + `auth.public: true` with a JWT plan (`auth.NewAuthJWTFromAssembly`) and set `auth.public: false` in both contract YAMLs — see `auth.public` comments in `contracts/http/orderfulfillment/*/v1/contract.yaml`.
6. Remove the `paymentShouldFail` field from the request schema: it is a demo-only failure-injection hook and must not be exposed on the business API wire in production — drive payment failures from real downstream payment service responses instead.

## Security

The primary listener at `127.0.0.1:8083` uses `auth.public: true` — **all requests are unauthenticated**. This is intentional for local demo exploration only. Production deployments must:
- Set `auth.public: false` and configure a JWT plan.
- Restrict access to `:8083` to trusted network segments or an API gateway.
- Replace the in-memory journal and no-op outbox with durable implementations.

The health listener (`:9093`) is already bound to `127.0.0.1` (loopback only). Demo mode does not declare an internal listener.

## Running the Integration Tests

The integration test file exercises the coordinator end-to-end with real wiring (shared `MemJournal`, real `clock.Real()`, real coordinator poll loop) and asserts terminal states:

```bash
go test -tags=integration \
  ./examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder/... \
  -run 'TestPlaceOrder_HappyPath|TestPlaceOrder_CompensateOnChargeFail' \
  -v
```

Expected output:

```
--- PASS: TestPlaceOrder_HappyPath (< 1s)
--- PASS: TestPlaceOrder_CompensateOnChargeFail (< 1s)
```

To run all tests (unit + integration) for the example:

```bash
go test -tags=integration ./examples/orderfulfillment/...
```

## Journey Specs

The `journeys/` directory contains declarative acceptance specs:

- `J-orderfulfillment-happy.yaml` — happy-path saga succeeds with all side-effects persisted
- `J-orderfulfillment-compensate.yaml` — payment-failure compensation releases all acquired resources

These specs document acceptance criteria. The `run-journey` CLI execution path is backlogged (`#967`); verification is currently covered by the integration tests above.

## File Layout

- `main.go` — generated assembly entry point (DO NOT EDIT)
- `modules_gen.go` — generated cell module factory (DO NOT EDIT)
- `run.go` — hand-written composition root: cell wiring, `MemJournal`, `Coordinator` start/stop
- `cells/orderfulfillmentcell/`
  - `internal/domain/` — `Order` struct
  - `internal/ports/` — `OrderRepository`, `InventoryStore`, `PaymentStore`, `ShipmentStore` interfaces
  - `internal/mem/` — in-memory implementations of all ports
  - `internal/sagaimpl/` — `Impl` bridging the generated `of.Impl` interface to domain ports
  - `slices/placeorder/` — `Service.PlaceOrder()` + HTTP handler + integration tests
- `contracts/saga/orderfulfillment/v1/` — saga contract YAML + step output schemas
- `journeys/` — declarative journey specs

ref: dtm-labs/dtm `sample/saga` (compensable steps + deliberate failure → reverse compensation); `examples/todoorder` + `examples/iotdevice` (in-repo) for GoCell engineering structure.
