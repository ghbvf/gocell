# Command Bus Guide

The **synchronous command bus** lets one in-process, first-party caller invoke a
typed business operation owned by a cell, by a stable command id, without an HTTP
round-trip. A `contract.yaml` of `kind: command` (with `codegen: true`) is the
single source: codegen derives a typed `Handler` interface plus `Register` /
`Dispatch` functions, and the cell implements the `Handler` and registers it into
a process-wide `command.Registry`.

> Design authority: ADR
> [`docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md`](../architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md).
> Enforcement index: `.claude/rules/gocell/eventbus.md` (consumer patterns) +
> the archtests named below. This guide is the consumer-facing how-to.

## When to use `kind: command`

| You want… | Use | Why |
|-----------|-----|-----|
| An external client to call an endpoint over the network | `kind: http` | Untrusted boundary: auth, JSON validation, status codes. |
| To react to something that already happened, decoupled, at-least-once | `kind: event` (subscribe) | Async fan-out; producer and consumer are independent. |
| To run a multi-step, cross-cell workflow with compensation | `kind: saga` (orchestrate) | Durable state machine + reverse-idempotent compensation. |
| One in-process caller to invoke a typed cell operation by command id, synchronously | **`kind: command`** | Typed, in-process, registry-routed; no wire, no serialization. |

The command bus is **not** a second HTTP layer and **not** an event bus:

- It is **synchronous and in-process** — `Dispatch` calls the registered handler
  directly (no JSON round-trip, no broker). Per ADR §D4/§D6, the request schema
  *sources the typed `*Request` signature*; it is **not** re-validated at runtime
  on the sync path (the caller is trusted first-party code). Value validation
  belongs at the untrusted entry points that *front* the command bus (HTTP→command,
  async outbox→command) — those bridges are later PRs.
- It is **one handler per command id** (`Register` rejects a duplicate with
  `ErrConflict`), unlike event fan-out where every consumer group gets a copy.

## The three-step flow

### 1. Declare the contract (`kind: command`, `codegen: true`)

```yaml
# examples/iotdevice/contracts/command/device-command/enqueue/v1/contract.yaml
id: command.device-command.enqueue.v1
kind: command
codegen: true          # emits command_gen.go (Handler / Register / Dispatch)
ownerCell: devicecell
consistencyLevel: L4
lifecycle: active
endpoints:
  handler: devicecell
  invokers: []
schemaRefs:
  request: request.schema.json    # sources the typed *Request DTO
  response: response.schema.json   # sources the typed *Response DTO
```

`gocell generate contract` (run `go run ./cmd/gocell generate contract --all`)
derives, into `generated/contracts/command/device-command/enqueue/v1/`:

```go
const DispatchID idutil.SafeID = "command.device-command.enqueue.v1"

type Handler interface {
    HandleEnqueue(ctx context.Context, req *Request) (*Response, error)
}

func Register(reg *command.Registry, h Handler) error
func Dispatch(ctx context.Context, reg *command.Registry, req *Request) (*Response, error)
```

The typed `Handler` is the **sole** sanctioned target of `Register`/`Dispatch`;
hand-writing an equivalent trio elsewhere is rejected by archtest
`COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`. Calling `command.Registry.RegisterHandler`
directly (bypassing the generated `Register`) is rejected by
`COMMAND-DISPATCH-REGISTER-CALLER-01`.

### 2. Implement the generated `Handler` in a cell slice

Write an adapter that bridges the generated `Handler` to your domain service —
the command-bus analog of the HTTP `Service` adapter. Reuse the same business
method the HTTP path uses; do not fork the logic.

```go
// examples/iotdevice/cells/devicecell/slices/devicecommand/command_handler.go
type EnqueueCommandAdapter struct{ S *Service }

var _ cmdenqueue.Handler = EnqueueCommandAdapter{}

func (a EnqueueCommandAdapter) HandleEnqueue(
    ctx context.Context, req *cmdenqueue.Request,
) (*cmdenqueue.Response, error) {
    entry, err := a.S.Enqueue(ctx, req.DeviceID, req.CommandType, req.Payload)
    if err != nil {
        return nil, err
    }
    return &cmdenqueue.Response{Data: toCommandEnqueueResponseData(entry)}, nil
}
```

`DEAD-CONTRACT-01` requires that an active `codegen: true` command contract has a
cell type implementing its generated `Handler` — an unimplemented codegen command
is CI-red, not dead-but-compiles.

### 3. Wire the registry: cell option + `Register` in `Init`, registry in the root

The cell holds a **required** `*command.Registry`, injected via a `With*` option,
and calls the generated `Register` during `Init`:

```go
// cell.go
func WithCommandRegistry(reg *commandruntime.Registry) Option {
    return func(c *DeviceCell) { c.commandRegistry = reg }
}

// inside initSlices, after the slice service (pubSvc) is built:
if c.commandRegistry == nil {
    return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
        "devicecell requires a command registry; from the composition root, "+
            "call WithCommandRegistry(command.NewRegistry())")
}
if err := cmdenqueue.Register(c.commandRegistry, devicecommand.EnqueueCommandAdapter{S: pubSvc}); err != nil {
    return fmt.Errorf("device-command register: %w", err)
}
```

The composition root constructs the registry and injects it:

```go
// examples/iotdevice/run.go
commandReg := commandruntime.NewRegistry()
dc := devicecell.NewDeviceCell(
    clk,
    devicecell.WithDeviceRepository(deviceRepo),
    devicecell.WithDirectPublisher(outbox.WrapPublisherForCell(directPub)),
    devicecell.WithCursorCodec(cursorCodec),
    devicecell.WithCommandRegistry(commandReg),
    devicecell.WithLogger(logger),
)
```

The registry is **required, not optional** — an `if registry != nil { Register }`
skip would let the funnel silently regress to dead-but-compiles. An assembly that
forgets to wire it fails fast in `Init`.

## Dispatching

A trusted in-process caller invokes the operation through the same registry:

```go
resp, err := cmdenqueue.Dispatch(ctx, commandReg, &cmdenqueue.Request{
    DeviceID:    "dev-1",
    CommandType: "reboot",
    Payload:     "now",
})
```

`Dispatch` returns:

- the handler's `(*Response, error)` on success;
- `ErrCommandNotFound` (`KindNotFound`) if no handler is registered;
- `ErrValidationFailed` (`KindInvalid`) for a nil registry/request.

As of this writing there is **no production `Dispatch` caller** for device-command
enqueue: the production entry points that front the command bus — an
HTTP→command bridge and an async outbox→command relay — are later #1044 PRs (ADR
§5). The wiring is exercised end-to-end by `examples/iotdevice/cells/devicecell/command_wiring_test.go`.

## How it differs from saga and event consumers

| | command (`Dispatch`) | event (subscribe) | saga (orchestrate) |
|---|---|---|---|
| Invocation | synchronous, in-process, by command id | async, broker-delivered | durable workflow steps |
| Registration | `<gen>.Register(reg, h)` (hand-written today) | `reg.Subscribe(...)` derived by cellgen from `role: subscribe` | saga definition + step funcs |
| Multiplicity | exactly one handler per command id | one per consumer group; fan-out across groups | one coordinator per saga |
| Idempotency | caller's responsibility (sync, no retry) | `ConsumerBase` Claim/Commit/Release | journal + lease CAS |
| Failure | error returned to caller | Ack / Requeue / Reject → DLX | compensation (reverse, idempotent) |
| Validation | typed struct only (sync path); JSON-schema at the fronting boundary | consumer decodes + validates payload | step output schema |

Notably, command **registration is still hand-written** in the cell's `Init`,
whereas subscribe/webhook registration is derived by cellgen from `slice.yaml`.
Folding command registration into the cellgen `role: handle` funnel (so an
unregistered codegen command becomes compile-unexpressible — upgrading
`DEAD-CONTRACT-01`'s command dimension from Medium to Hard) is tracked at
**gh #1645**.

## Checklist

- [ ] `contract.yaml`: `kind: command`, `codegen: true`, `ownerCell`, both `schemaRefs`.
- [ ] `go run ./cmd/gocell generate contract --all` and commit the generated package.
- [ ] Implement the generated `Handler` in a cell slice (adapter → domain service).
- [ ] Cell holds a required `*command.Registry`; `Init` calls `<gen>.Register`.
- [ ] Composition root constructs `command.NewRegistry()` and injects it via `With*`.
- [ ] `slice.yaml` `verify.contract` has `contract.<id>.handle`; add an executable test.
- [ ] `go run ./cmd/gocell validate` + the command archtests pass.
