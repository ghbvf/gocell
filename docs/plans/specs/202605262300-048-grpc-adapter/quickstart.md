# Quickstart — add a gRPC RPC operation to a cell

Walkthrough for adding a unary gRPC RPC to a cell, written against the first
end-to-end handler landed in PR-8 (#1151, `examples/iotdevice`). Use that cell as
the working reference.

> **Pre-conditions** (live as of PR-8 #1151):
> - `gocell generate` understands `kind: grpc` (PR-1); contractgen emits **no** Go
>   for grpc (#1688) — buf's `pb.<Svc>Server` is the server contract.
> - `adapters/grpc.Server` is wired via `bootstrap.WithGRPCListener(...)` in the
>   assembly's composition root (`examples/iotdevice/run.go`); the cell author does
>   **not** touch this — only the assembly owner does. The assembly owner also sets
>   TLS in `adapters/grpc.Config.TLS`: `AllowInsecure: true` (plaintext) for the demo,
>   or `CertPEM`/`KeyPEM` (+`ClientCAPEM` for mTLS) for production.
> - `runtime/grpc/interceptor.NewUnaryChain` is composed and supplied to the server.
> - `buf` is installed and `Makefile` `proto-gen` target works.
>
> **Not yet live (planned)**: errcode→codes.Code mapping (PR-12); metrics cell
> attribution + `/metrics` export + `grpc_ready` readyz probe (PR-9 / #1383). Until
> then a returned `*errcode.Error` surfaces as `codes.Unknown`, grpc metrics are
> not exported, and the `cell` metric label is `_runtime`.

---

## Step 1 — declare the RPC contract

Create `contracts/grpc/<domain>/<version>/contract.yaml`:

```yaml
id: grpc.todoorder.command.v1   # contract id does NOT include the service name
kind: grpc
version: v1
owner: todoorder
description: Create a new order via internal RPC

endpoints:
  server: todoorder
  clients: []
  grpc:
    service: todoorder.command.v1.OrderCommandService
    proto: contracts/grpc/todoorder/command/v1/order_command.proto
# Note: the grpc block lives under `endpoints.grpc` (parallel to endpoints.http), not
# top-level. `method` and `streamingType` fields were removed in #1655 (service-level
# granularity); the method set and streaming kind derive from the .proto file (single
# source of truth). Per-RPC `auth.public` was also removed — service-level couldn't
# express per-method auth; per-method auth is deferred to #1675.

verify:
  contract:
    - contract.grpc.todoorder.command.v1.serve
```

Create the proto file `contracts/grpc/todoorder/command/v1/order_command.proto`:

```protobuf
syntax = "proto3";

package todoorder.command.v1;

option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/todoorder/command/v1;orderv1";

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
  - contract: grpc.todoorder.command.v1
    role: serve              # NEW: serve / call (analogous to http server / clients)
    # field: orderCommandServer   # optional, only needed to disambiguate multiple *ordercommand.Service fields
verify:
  contract:
    - contract.grpc.todoorder.command.v1.serve
```

For a consuming cell:

```yaml
# cells/todoorder/slices/orderconsumer/slice.yaml
contractUsages:
  - contract: grpc.todoorder.command.v1
    role: call
verify:
  contract:
    - contract.grpc.todoorder.command.v1.call
```

---

## Step 3 — declare the cell field

The cell struct needs a pointer field whose **package short-name == the slice id**
so `cellgen` can resolve where to register the handler (`fieldindex.go` matches on
package name, not field/type name). The **type name is arbitrary** — by convention
a grpc-serve slice names it `Server` to signal it implements the pb `<Svc>Server`
interface (the real `examples/iotdevice` uses `*devicecommandrpc.Server`):

```go
// cells/todoorder/cell.go
package todoorder

import (
    "github.com/ghbvf/gocell/corecells/todoorder/slices/ordercommand"
)

type Cell struct {
    cell.BaseCell
    // cellgen resolves this by "pointer-type package (ordercommand) == slice id".
    ordercommand *ordercommand.Server
}
```

---

## Step 4 — generate

```bash
$ make proto-gen          # buf generates the .pb.go from .proto:
                          #  - generated/contracts/grpc/todoorder/command/v1/order_command.pb.go      (messages)
                          #  - generated/contracts/grpc/todoorder/command/v1/order_command_grpc.pb.go  (service)
$ gocell generate         # contractgen emits NOTHING for kind=grpc (#1688); cellgen
                          # emits cells/todoorder/cell_gen.go (Init calls reg.GRPCService)
```

> **The grpc server contract is buf's generated `pb.<Svc>Server` interface — there is
> no GoCell-side generated interface (#1688).** buf's `protoc-gen-go-grpc` already
> emits, from the .proto, the proto-derived server contract that declares every RPC
> and carries `mustEmbedUnimplemented<Svc>Server()` for forward compatibility. A
> second contractgen interface would be a register-incompatible, package-colliding
> duplicate, so contractgen emits no Go file for grpc. The cell author implements
> the buf interface directly (idiomatic grpc-go / Kratos). This mirrors how the
> error model lands at the interceptor layer (Kratos `GRPCStatus()`), not at a
> per-handler typed-response envelope.

buf's `order_command_grpc.pb.go` declares (the interface your handler implements):

```go
type OrderCommandServiceServer interface {
    CreateOrder(context.Context, *CreateOrderRequest) (*CreateOrderResponse, error)
    mustEmbedUnimplementedOrderCommandServiceServer()
}

type UnimplementedOrderCommandServiceServer struct{} // embed this (by value) for forward-compat

func RegisterOrderCommandServiceServer(s grpc.ServiceRegistrar, srv OrderCommandServiceServer)
```

---

## Step 5 — implement the handler

```go
// cells/todoorder/slices/ordercommand/handler.go
package ordercommand

import (
    "context"

    orderv1 "github.com/ghbvf/gocell/generated/contracts/grpc/todoorder/command/v1"
    "github.com/ghbvf/gocell/pkg/errcode"
)

type Server struct {
    orderv1.UnimplementedOrderCommandServiceServer // by-value embed → forward-compat; satisfies the pb interface
    repo     domain.OrderRepository    `gocell:"required"`
    txRunner persistence.CellTxManager `gocell:"required" gocellKind:"KindInvalid" gocellCode:"ErrValidationFailed" gocellErr:"ordercommand: TxRunner required"` //nolint:lll
}

func (s *Server) CreateOrder(
    ctx context.Context,
    req *orderv1.CreateOrderRequest,
) (*orderv1.CreateOrderResponse, error) {
    if req.GetCustomerId() == "" {
        // errcode.Error flows out as the Go error; the gRPC interceptor maps it to a
        // status code (a returned *errcode.Error surfaces as codes.Unknown today;
        // the full errcode→codes table lands at runtime/grpc/interceptor in PR-12,
        // the Kratos GRPCStatus() model). New signature is
        // errcode.New(kind, code, message, opts...).
        return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
            "customer_id is required",
            errcode.WithDetails(errcode.PublicString("field", "customer_id")))
    }

    order, err := s.createOrderTx(ctx, req)
    if err != nil {
        return nil, err
    }

    return &orderv1.CreateOrderResponse{
        OrderId:           order.ID,
        CreatedAtUnixNano: order.CreatedAt.UnixNano(),
    }, nil
}
```

**Return shapes** (idiomatic grpc-go):
- `(*pb.Response, nil)` → success.
- `(nil, error)` → failure. Return an `*errcode.Error` for a domain error; the interceptor
  chain maps it to a gRPC status code and applies the same redaction discipline as HTTP
  (`errcode.Error.Internal` never reaches the trailer; `Details` stripped for 5xx-class codes).
  The precise errcode→codes table is PR-12.

---

## Step 6 — call from another cell

Use buf's generated standard gRPC client (`pb.New<Svc>Client`):

```go
// in a consuming cell
import (
    orderv1 "github.com/ghbvf/gocell/generated/contracts/grpc/todoorder/command/v1"
    "google.golang.org/grpc"
)

func placeOrder(ctx context.Context, conn *grpc.ClientConn, customerID string, items []string) error {
    client := orderv1.NewOrderCommandServiceClient(conn)
    resp, err := client.CreateOrder(ctx, &orderv1.CreateOrderRequest{
        CustomerId: customerID,
        ItemIds:    items,
    })
    if err != nil {
        // err is a gRPC status; the interceptor carried the errcode public Message + Details.
        return fmt.Errorf("place order: %w", err)
    }
    _ = resp.OrderId
    return nil
}
```

Context (deadline, trace, correlation_id, principal envelope) propagates automatically via the standard client + interceptor chain.

---

## Step 7 — verify

```bash
$ gocell validate                                              # contract.yaml + slice.yaml + cell.yaml
$ go test ./cells/todoorder/slices/ordercommand/...            # unit
$ gocell verify cells/todoorder/slices/ordercommand            # contract verify
$ go test ./tools/archtest/... -run GRPC                       # archtest invariants
```

A successful client `CreateOrder` round-trip (response returned, no error) confirms
the integration. Note the PR-9 / #1383 caveats from the pre-conditions: the
`grpc_ready` readyz probe is not yet wired (PR-9), grpc metrics are not yet exported
to `/metrics` (PR-9), and once exported the `cell` label is `_runtime` until cell
attribution lands (#1383) — `grpc_server_requests_total{cell="_runtime",code=...}`,
**not** `cell="todoorder"`.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `gocell generate cell` reports `no cell.go struct field for slice` | Cell struct lacks a `*<sliceID>.T` pointer field | Add a `*ordercommand.Server` field (Step 3); the pointer-type package must equal the slice id |
| `pb.Register<Svc>Server(r, c.handler)` fails to compile | Handler does not implement the pb `<Svc>Server` interface | Embed `<pb>.Unimplemented<Svc>Server` **by value** in the handler struct (forward-compat marker, Step 5) |
| `gocell validate` rejects `kind: grpc` | Pre-PR-1 build, or contract.yaml missing required fields | Upgrade to PR 1+ build; ensure `grpc.service` and `grpc.proto` are set (`grpc.method` was removed in #1655 — service-level granularity) |
| `make proto-gen` fails with `package mismatch` | `option go_package` not aligned with `generated/contracts/grpc/...` layout | Match the `go_package` to `generated/contracts/grpc/{domain}/{version};{shortname}v1`; for a satellite module add the module root to `buf.yaml` |
| `grpc.Server.Serve()` returns immediately at boot | Listener bind failed (port collision) | Check the startup logs; ensure the grpc port differs from the HTTP listeners |
