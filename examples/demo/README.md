# demo — minimal GoCell hello-world

The smallest runnable GoCell example: a single **L1 cell** (`democell`) with one
pure-compute **L0 slice** (`hello`) that serves `GET /api/v1/hello` and returns a
fixed JSON greeting. No database, no event bus, no saga — just two loopback HTTP
listeners (API + health). It is the first example a newcomer should read to learn
the GoCell **Cell → Slice → Contract → handler** shape end to end.

(The cell is L1, not L0: a cell that provides an HTTP contract is a request
boundary, which TOPO-05 requires to be ≥ L1. The slice's own processing is pure
compute, so it stays L0.)

## Run it

From the **repo root**:

```sh
go run ./examples/demo
```

(or, from inside this directory: `go run .`)

The binary binds two loopback listeners:

| Listener | Address          | Routes                       |
|----------|------------------|------------------------------|
| Primary  | `127.0.0.1:8086` | `GET /api/v1/hello`          |
| Health   | `127.0.0.1:9096` | `/healthz`, `/readyz`        |

In another terminal (responses are the standard GoCell JSON envelope):

```sh
curl -s 127.0.0.1:8086/api/v1/hello
# {"data":{"message":"hello, gocell"}}

curl -s 127.0.0.1:9096/healthz   # {"data":{"status":"healthy"}}
curl -s 127.0.0.1:9096/readyz    # {"data":{"status":"healthy"}}  (zero readiness probes registered)
```

Press `Ctrl-C` to trigger graceful shutdown.

## What's here

```
examples/demo/
  assembly.yaml                       # id: demo, cells: [democell]
  main.go            (generated)      # gocell generate assembly — entrypoint + slog seal
  modules_gen.go     (generated)      # gocell generate assembly — cell drift guard
  run.go                              # hand-written composition root (bootstrap wiring)
  cells/democell/
    cell.yaml                         # L1 cell metadata
    cell.go                           # DemoCell + +cell:listener / +slice:route markers
    cell_gen.go        (generated)    # gocell generate cell — Init + RouteGroup
    healthz_gen.go     (generated)    # gocell generate cell — readiness funnel (unused here)
    slices/hello/
      slice.yaml                      # L0 serve slice
      service.go                      # pure-compute Service.Greeting()
      handler.go                      # adapts the domain Service to the generated contract
      slice_gen.go     (generated)    # gocell generate cell — slice metadata
  contracts/http/demo/hello/v1/
    contract.yaml                     # GET /api/v1/hello, codegen: true, public
    response.schema.json              # {data:{message:string}}
```

Generated contract code (`Service` interface + typed response envelope) lives at
the repo root under `generated/contracts/http/demo/hello/v1/`. Files marked
*(generated)* are owned by `gocell generate` — do not edit them by hand.

## Going further

To regenerate after changing metadata:

```sh
go run ./cmd/gocell generate contract http.demo.hello.v1
go run ./cmd/gocell generate cell democell
go run ./cmd/gocell generate assembly --id=demo
go run ./cmd/gocell validate
```

For an example with persistence, events, and a saga, see
`examples/orderfulfillment` and `examples/todoorder`.
