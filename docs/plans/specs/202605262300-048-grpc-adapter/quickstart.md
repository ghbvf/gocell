# Quickstart — add a gRPC RPC operation to a cell

Phase 1 walkthrough. The reader has finished PR 8 (first end-to-end usable handler) and now wants to add a second RPC operation to their own cell.

> **Pre-conditions** (assumed satisfied by PRs 1–8):
> - `gocell generate` understands `kind: grpc`
> - `adapters/grpc.Server` is wired via `bootstrap.WithGRPCListener(...)` in the assembly's `cmd/<assembly>/main.go`
> - `runtime/grpc/interceptor.Chain` is composed and supplied to the server
> - `buf` is installed and `Makefile` `proto-gen` target works

---

## Step 1 — declare the RPC contract

Create `contracts/grpc/<domain>/<version>/contract.yaml`:

```yaml
id: grpc.todoorder.command.v1.CreateOrder
kind: grpc
version: v1
owner: todoorder
description: Create a new order via internal RPC

grpc:
  service: todoorder.command.v1.OrderCommandService
  method: CreateOrder
  streamingType: unary
  proto: contracts/grpc/todoorder/command/v1/order_command.proto
  auth:
    public: false   # service-to-service only

verify:
  contract:
    - contract.grpc.todoorder.command.v1.CreateOrder
```

Create the proto file `contracts/grpc/todoorder/command/v1/order_command.proto`:

```protobuf
syntax = "proto3";

package todoorder.command.v1;

option go_package = "github.com/gocell/gocell/generated/contracts/grpc/todoorder/command/v1;orderv1";

service OrderCommandService {
  rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse) {}
}

message CreateOrderRequest {
  string customer_id = 1;
  repeated string item_ids = 2;
}

message CreateOrderResponse {
  string order_id = 1;
  int64 created_at_unix_nano = 2;
}
```

---

## Step 2 — declare the consuming slice

Update the cell's `slice.yaml` to add the contractUsage. Both `provider` (the cell that hosts the RPC) and `consumer` (other cells calling it) declare `contractUsages`:

```yaml
# cells/todoorder/slices/ordercommand/slice.yaml
id: ordercommand
belongsToCell: todoorder
contractUsages:
  - contract: grpc.todoorder.command.v1.CreateOrder
    role: serve              # NEW: serve / call (analogous to http server / clients)
    handler: CreateOrder     # method name on the generated interface
verify:
  contract:
    - contract.grpc.todoorder.command.v1.CreateOrder.serve
```

For a consuming cell:

```yaml
# cells/todoorder/slices/orderconsumer/slice.yaml
contractUsages:
  - contract: grpc.todoorder.command.v1.CreateOrder
    role: call
verify:
  contract:
    - contract.grpc.todoorder.command.v1.CreateOrder.call
```

---

## Step 3 — declare the cell field

The cell struct needs a `*ordercommand.Service` field so `cellgen` can resolve where to register the handler (matches existing AMQP subscribe-CU pattern):

```go
// cells/todoorder/cell.go
package todoorder

import (
    "github.com/gocell/gocell/cells/todoorder/slices/ordercommand"
)

type Cell struct {
    cell.BaseCell
    ordercommand *ordercommand.Service   // cellgen looks for this field
}
```

---

## Step 4 — generate

```bash
$ make proto-gen          # buf generates .pb.go from .proto
$ gocell generate         # contractgen reads contract.yaml + slice.yaml, emits:
                          #  - generated/contracts/grpc/todoorder/command/v1/server_gen.go
                          #  - generated/contracts/grpc/todoorder/command/v1/client_gen.go
                          #  - generated/contracts/grpc/todoorder/command/v1/methods_gen.go
                          #  - cells/todoorder/cell_gen.go (Init calls reg.GRPCService)
```

Generated `server_gen.go` declares:

```go
type OrderCommandServer interface {
    CreateOrder(ctx context.Context, req *orderv1.CreateOrderRequest) (OrderCommandCreateOrderResponseObject, error)
}

type OrderCommandCreateOrderResponseObject interface {
    visitOrderCommandCreateOrderResponse(grpc.ServerStream) error
}

type OrderCommandCreateOrder200JSONResponse orderv1.CreateOrderResponse  // success
type OrderCommandCreateOrder4xxErrorResponse errcode.Error               // error envelope
```

---

## Step 5 — implement the handler

```go
// cells/todoorder/slices/ordercommand/handler.go
package ordercommand

import (
    "context"

    "github.com/gocell/gocell/generated/contracts/grpc/todoorder/command/v1"
    "github.com/gocell/gocell/pkg/errcode"
)

type Service struct {
    repo     domain.OrderRepository       `gocell:"required"`
    txRunner persistence.CellTxManager    `gocell:"required" gocellKind:"KindInvalid" gocellCode:"ErrValidationFailed" gocellErr:"ordercommand: TxRunner required"` //nolint:lll
}

func (s *Service) CreateOrder(
    ctx context.Context,
    req *orderv1.CreateOrderRequest,
) (orderv1.OrderCommandCreateOrderResponseObject, error) {
    if req.CustomerId == "" {
        return orderv1.OrderCommandCreateOrder4xxErrorResponse(*errcode.New(
            errcode.ErrValidationFailed,
            "customer_id is required",
            errcode.WithDetails(errcode.PublicString("field", "customer_id")),
        )), nil
    }

    order, err := s.createOrderTx(ctx, req)
    if err != nil {
        return nil, err   // framework converts via runtime/grpc/interceptor/errcode_mapping.go
    }

    return orderv1.OrderCommandCreateOrder200JSONResponse{
        OrderId:           order.ID,
        CreatedAtUnixNano: order.CreatedAt.UnixNano(),
    }, nil
}
```

**Return shapes**:
- `(typed success, nil)` → 200-equivalent
- `(typed 4xx envelope, nil)` → declared business error
- `(nil, *errcode.Error)` → framework 5xx-equivalent (panic recovery or infrastructure fault)

The four-channel redaction discipline applies identically to HTTP: `errcode.Error.Internal` never reaches the gRPC trailer; for `KindInternal/Unavailable/DeadlineExceeded`, `Details` are stripped at the wire.

---

## Step 6 — call from another cell

```go
// in a consuming cell's handler
import (
    orderv1 "github.com/gocell/gocell/generated/contracts/grpc/todoorder/command/v1"
)

type Service struct {
    orders orderv1.OrderCommandClient   `gocell:"required"`
}

func (s *Service) PlaceOrder(ctx context.Context, ...) error {
    resp, err := s.orders.CreateOrder(ctx, &orderv1.CreateOrderRequest{
        CustomerId: customerID,
        ItemIds:    items,
    })
    if err != nil {
        // err is *errcode.Error — public Message + Details present, Internal absent
        return fmt.Errorf("place order: %w", err)
    }
    // resp.OrderId available
    return nil
}
```

Context (deadline, trace, correlation_id, principal envelope) propagates automatically via the generated client.

---

## Step 7 — verify

```bash
$ gocell validate                                              # contract.yaml + slice.yaml + cell.yaml
$ go test ./cells/todoorder/slices/ordercommand/...            # unit
$ gocell verify cells/todoorder/slices/ordercommand            # contract verify
$ go test ./tools/archtest/... -run GRPC                       # archtest invariants
```

If `/readyz?verbose` shows `grpc_ready: ok` and `/metrics` reports `grpc_server_requests_total{cell="todoorder",method="/todoorder.command.v1.OrderCommandService/CreateOrder",code="OK"}`, the integration is complete.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `gocell generate cell` reports `no cell.go struct field for serving slice` | Missing `*ordercommand.Service` field on cell struct | Add the field (Step 3) |
| `gocell validate` rejects `kind: grpc` | Pre-PR-1 build, or contract.yaml missing required fields | Upgrade to PR 1+ build; ensure `grpc.service`, `grpc.method`, `grpc.proto` all set |
| `make proto-gen` fails with `package mismatch` | `option go_package` not aligned with `generated/contracts/grpc/...` layout | Match the `go_package` to `generated/contracts/grpc/{domain}/{version};{shortname}v1` |
| `grpc.Server.Serve()` returns immediately at boot | Listener bind failed (port collision) | Check `/readyz?verbose`; the `grpc_ready` probe surfaces the bind error |
| Cell label shows `_runtime` instead of `todoorder` | Method not registered through generated `methods_gen.go` (hand-rolled `RegisterServer`) | Use `reg.GRPCService(...)` only; archtest `GRPC-METHOD-IN-CONTRACT-01` would normally catch this — check whether it was bypassed |
