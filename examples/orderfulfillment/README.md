# orderfulfillment Example

A multi-step order fulfillment application demonstrating GoCell's **L3 WorkflowEventual saga orchestration** with automatic compensation on failure.

## What This Example Demonstrates

- **L3 saga orchestration**: the `placeorder` slice enrolls a saga instance in the journal; the `Coordinator` drives it asynchronously through four forward steps.
- **Compensable steps**: three of the four steps (`reserveInventory`, `chargePayment`, `ship`) declare a `CompensateFunc`; the fourth (`notifyUser`) is terminal and intentionally not compensable (notifications are best-effort).
- **Automatic compensation**: when `chargePayment` fails (controlled by `paymentShouldFail=true`), the coordinator reverses committed steps in order: `CompensateReserveInventory` releases the inventory reservation, restoring the original stock level.
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

## Quick Start

No external dependencies required.

```bash
go run ./examples/orderfulfillment
```

The server starts on `:8083` (primary listener — no JWT required in demo mode; the primary listener is open for local exploration).

### Place an order (happy path)

The example pre-seeds inventory with `"widget": 100` units.

```bash
curl -X POST http://localhost:8083/api/v1/orders/ \
  -H 'Content-Type: application/json' \
  -d '{"item":"widget","amountCents":1299}'
```

Response (202 Accepted):

```json
{"data":{"orderId":"ord-..."}}
```

The saga runs asynchronously. Check the server logs for `saga enrolled` → `step completed` entries.

### Trigger compensation (payment failure)

Set `paymentShouldFail: true` to force the `chargePayment` step to return a conflict error, causing the coordinator to compensate in reverse:

```bash
curl -X POST http://localhost:8083/api/v1/orders/ \
  -H 'Content-Type: application/json' \
  -d '{"item":"widget","amountCents":1299,"paymentShouldFail":true}'
```

The saga reaches `KindSagaCompensated`: inventory is released, no payment or shipment is recorded.

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
  - `internal/saga/` — `Impl` bridging the generated `of.Impl` interface to domain ports
  - `slices/placeorder/` — `Service.PlaceOrder()` + HTTP handler + integration tests
- `contracts/saga/orderfulfillment/v1/` — saga contract YAML + step output schemas
- `journeys/` — declarative journey specs

ref: dtm-labs/dtm `sample/saga` (compensable steps + deliberate failure → reverse compensation); `examples/todoorder` + `examples/iotdevice` (in-repo) for GoCell engineering structure.
