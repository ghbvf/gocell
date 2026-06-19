# Command Bus Guide

The **command bus** lets first-party code route a typed business operation owned
by a cell through a stable command id. The synchronous fast-path calls a
registered handler in-process, without an HTTP round-trip. The async path writes a
command outbox entry and lets the relay invoke the generated dispatcher under the
same registry.

A `contract.yaml` of `kind: command` is the single source: codegen derives a
typed `Handler` interface plus `Register`, `Dispatch`, `DispatchAsync`,
`EmitAsync`, and `EmitAsyncFromIdempotencyKey` functions. The cell implements the
`Handler` and registers it into a process-wide `command.Registry`.

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
| First-party code to invoke one typed cell operation by command id | **`kind: command`** | Typed registry route; sync is in-process, async goes through outbox relay. |

The command bus is **not** a second HTTP layer and **not** an event bus:

- Its sync fast-path is **synchronous and in-process** — `Dispatch` calls the
  registered handler directly (no JSON round-trip, no broker). Per ADR §D4/§D6,
  the request schema *sources the typed `*Request` signature*; it is **not**
  re-validated at runtime on the sync path (the caller is trusted first-party
  code). Value validation belongs at untrusted entry points: generated HTTP
  handlers validate incoming bodies before service calls, and generated
  `DispatchAsync` validates outbox entry payload bytes before invoking the
  handler.
- It is **one handler per command id** (`Register` rejects a duplicate with
  `ErrConflict`), unlike event fan-out where every consumer group gets a copy.
- Its async path is **outbox-backed and relay-routed** — generated `EmitAsync`
  writes a command entry whose routing topic is the command `DispatchID`; the
  relay's `WithCommandDispatch` map routes matching entries to the generated
  `DispatchAsync` instead of publishing them to the broker.

## The three-step flow

### 1. Declare the contract (`kind: command`)

```yaml
# examples/iotdevice/contracts/command/remotecommand/v1/contract.yaml
id: command.remotecommand.v1
kind: command
ownerCell: devicecell
consistencyLevel: L4
lifecycle: active
endpoints:
  handler: devicecell
  invokers: [devicecell]
schemaRefs:
  request: request.schema.json    # sources the typed *Request DTO
  response: response.schema.json   # sources the typed *Response DTO
```

`codegen` defaults to enabled; set `codegen: false` only to opt out explicitly.

`gocell generate contract` (run `go run ./cmd/gocell generate contract --all`)
derives, into `generated/contracts/command/remotecommand/v1/`:

```go
const DispatchID idutil.SafeID = "command.remotecommand.v1"

type Handler interface {
    HandleRemotecommand(ctx context.Context, req *Request) (*Response, error)
}

func Register(reg *command.Registry, h Handler) error
func Dispatch(ctx context.Context, reg *command.Registry, req *Request) (*Response, error)
func DispatchAsync(ctx context.Context, reg *command.Registry, entry outbox.Entry) error
func EmitAsync(ctx context.Context, clk clock.Clock, emitter outbox.Emitter,
    subject, commandID string, req *Request, opts ...command.EmitOption) error
func EmitAsyncFromIdempotencyKey(ctx context.Context, clk clock.Clock,
    emitter outbox.Emitter, subject string, req *Request,
    opts ...command.EmitOption) error
```

The typed `Handler` and generated free functions are the sanctioned command
surface. Hand-writing an equivalent funnel elsewhere is rejected by archtest
`COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`. Calling `command.Registry.RegisterHandler`
directly (bypassing the generated `Register`) is rejected by
`COMMAND-DISPATCH-REGISTER-CALLER-01`; wiring relay command dispatch with anything
other than the generated `DispatchID`/`DispatchAsync` pair is rejected by
`COMMAND-ASYNC-DISPATCH-CALLER-01`; bypassing the generated emit wrappers is
rejected by `COMMAND-ASYNC-EMIT-CALLER-01`.

### 2. Implement the generated `Handler` in a cell slice

Write an adapter that bridges the generated `Handler` to your domain service —
the command-bus analog of the HTTP `Service` adapter. Reuse the same business
method the HTTP path uses; do not fork the logic.

```go
// examples/iotdevice/cells/devicecell/slices/devicecommand/command_handler.go
type RemoteCommandAdapter struct{ S *Service }

var _ cmdremote.Handler = RemoteCommandAdapter{}

func (a RemoteCommandAdapter) HandleRemotecommand(
    ctx context.Context, req *cmdremote.Request,
) (*cmdremote.Response, error) {
    entry, err := a.S.Enqueue(ctx, req.DeviceID, req.CommandType, req.Payload)
    if err != nil {
        return nil, err
    }
    return &cmdremote.Response{Data: toRemoteCommandResponseData(entry)}, nil
}
```

`DEAD-CONTRACT-01` requires that an active `codegen: true` command contract has a
cell type implementing its generated `Handler` — an unimplemented codegen command
is CI-red, not dead-but-compiles.

### 3. Wire the registry and relay

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
if err := cmdremote.Register(c.commandRegistry, devicecommand.RemoteCommandAdapter{S: pubSvc}); err != nil {
    return fmt.Errorf("devicecommand register: %w", err)
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

The async path also wires the generated dispatcher into the outbox relay:

```go
// examples/iotdevice/run.go
relay.WithCommandDispatch(commandReg, map[commandruntime.CommandID]commandruntime.AsyncDispatchFunc{
    cmdremote.DispatchID: cmdremote.DispatchAsync,
}, claimer)
```

The map key and value must come from the same generated command package. The
required `claimer` wraps command dispatch in `Claim`/`Commit`/`Release`, using the
entry's tenant, aggregate subject, and command instance id to deduplicate async
redelivery.

## Dispatching

A trusted in-process caller invokes the operation through the same registry:

```go
resp, err := cmdremote.Dispatch(ctx, commandReg, &cmdremote.Request{
    DeviceID:    "dev-1",
    CommandType: "reboot",
    Payload:     "now",
})
```

`Dispatch` returns:

- the handler's `(*Response, error)` on success;
- `ErrCommandNotFound` (`KindNotFound`) if no handler is registered;
- `ErrValidationFailed` (`KindInvalid`) for a nil registry/request.

> **Field mapping.** `Request.DeviceID` is a body field on the command contract;
> the HTTP enqueue path carries the same device identifier as the `{id}` URL path
> parameter (mapped to the HTTP `Request.ID`), not a body field. A bridge that
> translates HTTP→command must extract the path param and set `DeviceID`.

> **Authz and validation are the caller's job on the sync path.**
> Per ADR §D8, `Dispatch` does **not** authenticate, authorize, or value-validate
> the request: the registered handler delegates straight to the domain service.
> A sync production caller must enforce authn/authz, ownership/IDOR checks, and
> request value constraints before calling `Dispatch`.

There is still no production code that directly calls synchronous
`cmdremote.Dispatch`; that path is exercised by wiring tests. Production command
traffic for device enqueue is async:

- HTTP `http.device.command.enqueue-async.v1` validates the incoming request and
  calls `Service.EnqueueAsync`, which emits through
  `cmdremote.EmitAsyncFromIdempotencyKey`.
- `devicebootstrap` reacts to `event.device-registered.v1` and emits the generated
  command with `cmdremote.EmitAsync`.
- `devicecertrenewal` uses the same generated emit path for reconcile-driven
  certificate rotation commands.
- the relay dispatches matching command entries through `cmdremote.DispatchAsync`,
  which validates the outbox payload bytes before restoring context and invoking
  the registered `Handler`.

## How it differs from saga and event consumers

| | command (`Dispatch`) | event (subscribe) | saga (orchestrate) |
|---|---|---|---|
| Invocation | synchronous, in-process, by command id | async, broker-delivered | durable workflow steps |
| Registration | `<gen>.Register(reg, h)` (hand-written today) | `reg.Subscribe(...)` derived by cellgen from `role: subscribe` | saga definition + step funcs |
| Multiplicity | exactly one handler per command id | one per consumer group; fan-out across groups | one coordinator per saga |
| Idempotency | caller responsibility on sync; relay Claimer wraps async entries | `ConsumerBase` Claim/Commit/Release | journal + lease CAS |
| Failure | error returned to caller | Ack / Requeue / Reject → DLX | compensation (reverse, idempotent) |
| Validation | typed struct only (sync path); JSON-schema at the fronting boundary | consumer decodes + validates payload | step output schema |

Notably, command **registration is still hand-written** in the cell's `Init`,
whereas subscribe/webhook registration is derived by cellgen from `slice.yaml`.
Folding command registration into the cellgen `role: handle` funnel (so an
unregistered codegen command becomes compile-unexpressible — upgrading
`DEAD-CONTRACT-01`'s command dimension from Medium to Hard) is planned future
work.
See ADR `docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md` §5 for the staged command-bus roadmap.

## Checklist

- [ ] `contract.yaml`: `kind: command`, `ownerCell`, handler/invoker endpoints, both `schemaRefs`.
- [ ] `go run ./cmd/gocell generate contract --all` and commit the generated package.
- [ ] Implement the generated `Handler` in a cell slice (adapter → domain service).
- [ ] Cell holds a required `*command.Registry`; `Init` calls `<gen>.Register`.
- [ ] Composition root constructs `command.NewRegistry()` and injects it via `With*`.
- [ ] Async command producers use generated `<gen>.EmitAsync` or
      `<gen>.EmitAsyncFromIdempotencyKey`; the relay wires
      `<gen>.DispatchID: <gen>.DispatchAsync` through `WithCommandDispatch`.
- [ ] `slice.yaml` `verify.contract` has `contract.<id>.handle`; add an executable test.
- [ ] `go run ./cmd/gocell validate` + the command archtests pass.
- [ ] Sync `Dispatch` callers enforce authz + value validation before dispatch;
      async HTTP entry points rely on generated HTTP validation before emitting.
