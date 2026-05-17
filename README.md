# GoCell

Cell-native Go Engineering Foundation.

GoCell provides Cell/Slice runtime primitives, governance toolchain, and built-in Cells for building reliable Go services with the Slice-Cell architecture.

## Quick Start (5 minutes)

The todoorder example requires JWT keys and a service secret for the internal listener.
Run the commands from the repository root. If you have not cloned it yet:

```bash
git clone https://github.com/ghbvf/gocell.git
cd gocell
```

Then copy-paste the steps below in a single terminal:

```bash
# Step 1 — generate RS256 key pair
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
  -out /tmp/gocell-todoorder-jwt.key
openssl rsa -in /tmp/gocell-todoorder-jwt.key -pubout \
  -out /tmp/gocell-todoorder-jwt.pub

# Step 2 — set required env vars
export GOCELL_JWT_PRIVATE_KEY="$(cat /tmp/gocell-todoorder-jwt.key)"
export GOCELL_JWT_PUBLIC_KEY="$(cat /tmp/gocell-todoorder-jwt.pub)"
export GOCELL_JWT_ISSUER=todoorder-local
export GOCELL_JWT_AUDIENCE=gocell
export GOCELL_TODOORDER_SERVICE_SECRET="$(openssl rand -base64 32)"

# Step 3 — mint a test RS256 token (role:customer, signed by the local key)
# reads $GOCELL_JWT_PRIVATE_KEY/$GOCELL_JWT_ISSUER/$GOCELL_JWT_AUDIENCE from env
export TODOORDER_TOKEN="$(go run ./examples/todoorder/localtoken)"

# Step 4 — start the server (primary :8082, internal :9082)
go run ./examples/todoorder &

# Step 5 — wait for readiness
until curl -fsS http://localhost:8082/readyz >/dev/null; do sleep 0.2; done

# Step 6 — exercise the API
curl -s -X POST http://localhost:8082/api/v1/orders/ \
  -H "Authorization: Bearer $TODOORDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"item":"my first order"}' | jq .

curl -s http://localhost:8082/api/v1/orders/ \
  -H "Authorization: Bearer $TODOORDER_TOKEN" | jq .
```

Check the application logs — you should see `event.order.created consumed`.

For full configuration options (production hardening, real-mode adapters, multi-pod), see `examples/todoorder/README.md`.

## Core Concepts

```
┌─────────────────────────────────────────────────┐
│  Assembly        (physical deployment unit)      │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐ │
│  │ Cell       │  │ Cell       │  │ Cell       │ │
│  │ ┌────────┐ │  │ ┌────────┐ │  │ ┌────────┐ │ │
│  │ │ Slice  │ │  │ │ Slice  │ │  │ │ Slice  │ │ │
│  │ └────────┘ │  │ └────────┘ │  │ └────────┘ │ │
│  │ ┌────────┐ │  │ ┌────────┐ │  │ ┌────────┐ │ │
│  │ │ Slice  │ │  │ │ Slice  │ │  │ │ Slice  │ │ │
│  │ └────────┘ │  │ └────────┘ │  │ └────────┘ │ │
│  └──────┬─────┘  └──────┬─────┘  └──────┬─────┘ │
│         └───── Contract ─┘───── Contract ┘       │
└─────────────────────────────────────────────────┘
```

| Concept | Description |
|---------|-------------|
| **Cell** | Independent domain unit with lifecycle (Init/Start/Stop/Health). Types: core, edge, support. |
| **Slice** | A single responsibility within a Cell (e.g., `sessionlogin`, `ordercreate`). |
| **Contract** | Cross-Cell communication boundary (HTTP, event, command). Cells never import each other directly. |
| **Assembly** | Physical deployment — groups Cells into a runnable binary. |
| **Journey** | End-to-end acceptance specification spanning multiple Cells and Contracts. |

### Consistency Levels (L0-L4)

| Level | Name | Pattern | Example |
|-------|------|---------|---------|
| L0 | LocalOnly | Single slice, no side effects | Validation, computation |
| L1 | LocalTx | Single cell transaction | Session creation |
| L2 | OutboxFact | Transaction + outbox event | Order creation + event publish |
| L3 | WorkflowEventual | Cross-cell eventual consistency | Audit trail, projections |
| L4 | DeviceLatent | High-latency device loop | Command → ack with timeout |

## 30-Minute Tutorial: Create Your First Cell (codegen-driven)

GoCell uses codegen to eliminate boilerplate. The workflow is:
**define `contract.yaml` → run `gocell generate contract` → import the generated handler**.

For a deeper walkthrough see `docs/guides/codegen-new-endpoint.md`.

### Step 1: Scaffold metadata

```bash
mkdir -p contracts/http/mycell/hello/v1
mkdir -p cells/mycell/slices/myhello
```

Create `contracts/http/mycell/hello/v1/contract.yaml`:
```yaml
id: http.mycell.hello.v1
kind: http
ownerCell: mycell
consistencyLevel: L0
lifecycle: active
endpoints:
  server: mycell
  clients: []          # external callers (cell ids or actor ids); empty = open API
  http:
    method: GET
    path: /api/v1/hello
    successStatus: 200
    noContent: false   # true only for endpoints whose contract returns no body (e.g. 204 DELETE)
    auth:
      public: true     # JWT-exempt; mutually exclusive with passwordResetExempt (FMT-26)
schemaRefs:
  response: response.schema.json
```

Create `contracts/http/mycell/hello/v1/response.schema.json`:
```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": { "message": { "type": "string" } },
  "required": ["message"]
}
```

Create `cells/mycell/cell.yaml`:
```yaml
id: mycell
type: core
consistencyLevel: L0
owner:
  team: my-team
  role: my-owner
verify:
  smoke:
    - mycell/smoke
```

Create `cells/mycell/slices/myhello/slice.yaml`:
```yaml
id: myhello
belongsToCell: mycell
consistencyLevel: L1
contractUsages:
  - contract: http.mycell.hello.v1
    role: serve
verify:
  unit: myhello/unit
  contract: myhello/contract
allowedFiles:
  - handler.go
```

### Step 2: Generate the contract handler

```bash
go run ./cmd/gocell generate contract
# → generated/contracts/http/mycell/hello/v1/types_gen.go
# → generated/contracts/http/mycell/hello/v1/iface_gen.go
# → generated/contracts/http/mycell/hello/v1/handler_gen.go
```

### Step 3: Implement the Service interface

Create `cells/mycell/slices/myhello/handler.go`:
```go
package myhello

import (
    "context"

    hellog "github.com/ghbvf/gocell/generated/contracts/http/mycell/hello/v1"
    kcell "github.com/ghbvf/gocell/kernel/cell"
)

// HelloAdapter implements hellog.Service for http.mycell.hello.v1.
type HelloAdapter struct{}

func (HelloAdapter) Hello(ctx context.Context, _ *hellog.Request) (*hellog.Response, error) {
    return &hellog.Response{Message: "hello from mycell"}, nil
}

// Handler wires the generated contract handler for the myhello slice.
type Handler struct{ h *hellog.Handler }

func NewHandler() *Handler {
    return &Handler{h: hellog.NewHandler(HelloAdapter{})}
}

func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
    return h.h.RegisterRoutes(mux)
}
```

### Step 4: Implement the Cell

Cell metadata and Init wiring are produced by codegen from `cell.yaml` — set `goStructName: MyCell` in the yaml and run `go run ./cmd/gocell generate cell --all` to emit `cells/mycell/cell_gen.go` (the file holds the `metadata.CellMeta{}` literal plus a generated `Init` that drains markers).

Hand-write only `cells/mycell/cell.go`:

```go
package mycell

import (
    "context"
    "net/http"

    "github.com/ghbvf/gocell/cells/mycell/slices/myhello"
    "github.com/ghbvf/gocell/kernel/cell"
    "github.com/ghbvf/gocell/runtime/auth"
)

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type MyCell struct {
    *cell.BaseCell

    // +slice:route:slice=myhello,subPath=
    helloH *myhello.Handler
}

func New() *MyCell {
    return &MyCell{
        BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
        helloH:   myhello.NewHandler(),
    }
}

// initInternal is the hand-written init hook called from cell_gen.go after
// BaseCell.Init runs. Wire dependencies (DB, clients, workers) here.
func (c *MyCell) initInternal(ctx context.Context, reg cell.Registry) error {
    return nil
}
```

The `+cell:listener` / `+slice:route` markers tell `cellgen` how to emit `cell_gen.go::Init`, which calls `BaseCell.Init`, then `c.initInternal`, then registers each route group + slice. Re-run `gocell generate cell --all` after changing markers.

### Step 5: Create a main.go

```go
package main

import (
    "context"
    "os/signal"
    "syscall"

    mycell "github.com/ghbvf/gocell/cells/mycell"
    "github.com/ghbvf/gocell/kernel/assembly"
    "github.com/ghbvf/gocell/kernel/cell"
    "github.com/ghbvf/gocell/kernel/clock"
    "github.com/ghbvf/gocell/runtime/bootstrap"
)

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    clk := clock.Real()
    asm := assembly.New(assembly.Config{ID: "myapp", DurabilityMode: cell.DurabilityDemo, Clock: clk})
    asm.Register(mycell.New())

    app := bootstrap.New(
        bootstrap.WithAssembly(asm),
        bootstrap.WithClock(clk),
        bootstrap.WithListener(cell.PrimaryListener, ":8080",
            []cell.ListenerAuth{cell.AuthNone{}}),
        bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9090",
            []cell.ListenerAuth{cell.AuthNone{}}),
    )
    app.Run(ctx)
}
```

### Step 6: Build and run

```bash
go run ./cmd/gocell validate      # verify contracts are well-formed
go build ./cmd/myapp && ./myapp
# In another terminal:
curl http://localhost:8080/api/v1/hello
# {"message":"hello from mycell"}
```

## Scaffold（K#09）

一键创建完整 cell bundle：

```bash
gocell scaffold cell --id=foo --team=platform --role=cell-owner
```

生成 `cells/foo/` 含 cell.go + cell.yaml + slice + contract + JSON schemas，并自动 codegen 让 `go test ./cells/foo/...` 立即通过。

一键创建 assembly：

```bash
gocell scaffold assembly --id=bar --cells=foo --team=platform --role=admin --deploy=k8s
```

生成 `assemblies/bar/assembly.yaml` + `cmd/bar/{run,app,modules_gen,main}.go` + `boundary.yaml`。

详见 ADR `docs/architecture/202605101430-adr-scaffold-one-cmd-double-source-removal.md`。

## Example Projects

| Example | Complexity | What it demonstrates |
|---------|-----------|---------------------|
| [todoorder](examples/todoorder/) | Medium | Custom Cell, CRUD, outbox event publish, RabbitMQ consume |
| [ssobff](examples/ssobff/) | Medium-High | 3 built-in Cells composition (access + audit + config) |
| [iotdevice](examples/iotdevice/) | High | L4 DeviceLatent: command queue, ack, high-latency loop |

The `ssobff` example uses the initial admin bootstrap feature. On first run it writes a
temporary credential file whose location depends on the OS (Linux: `/run/gocell/`, macOS:
`~/Library/Application Support/gocell/run/`, Windows: `%LOCALAPPDATA%\gocell\run\`). Override
with `GOCELL_STATE_DIR`. `cmd/corebundle` defaults to interactive first-run setup; set
`GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE=bootstrap` for headless bootstrap, or leave it empty /
`interactive` to use `POST /api/v1/access/setup/admin`. Interactive setup passwords are 8-72
printable ASCII bytes. Unknown mode values fail fast at startup. See
`docs/operations/first-run-setup.md` for details.

## Runtime Modes

GoCell assemblies must declare a `DurabilityMode` explicitly (zero value is rejected):

| Mode | Value | Noop Allowed | Use Case |
|------|-------|-------------|----------|
| `DurabilityDemo` | 1 | Yes — `NoopWriter`, `cell.DemoTxRunner`, `DiscardPublisher` accepted; missing Tx/outbox dependencies are completed with explicit no-op defaults | Development, unit tests, examples |
| `DurabilityDurable` | 2 | No — `CheckNotNoop` rejects at `Init()` and L2 Cells require a real outbox writer + Tx runner | Production storage topologies |

```go
// Production
asm := assembly.New(assembly.Config{ID: "prod", DurabilityMode: cell.DurabilityDurable})

// Development / tests
asm := assembly.New(assembly.Config{ID: "dev", DurabilityMode: cell.DurabilityDemo})
```

`cmd/corebundle` maps PostgreSQL storage topology to `DurabilityDurable`;
development and memory storage topologies use `DurabilityDemo` so examples can
run without a database or broker. Demo mode is explicit: Cells inject
`cell.DemoTxRunner` / `NoopEmitter` when dependencies are absent, or a direct
`outbox.Emitter` when a publisher is supplied without a durable writer. Durable
mode never silently falls back to those no-op dependencies.

## Architecture

```
kernel/       — Cell/Slice runtime + governance tools (framework core)
cells/        — Platform Cell implementations (accesscore / auditcore / configcore)
contracts/    — Platform cross-Cell boundary contracts ({kind}/{domain}/{version}/)
journeys/     — Platform Journey acceptance specs + status-board.yaml
runtime/      — HTTP middleware, auth, worker, observability, bootstrap
adapters/     — External system adapters (postgres / redis / rabbitmq / websocket / s3 / oidc)
pkg/          — Shared utilities (errcode / ctxkeys / httputil / query)
cmd/          — CLI (gocell validate [--strict] / scaffold / generate / check / verify)
examples/     — Example projects; may include example-local cells/contracts/journeys
templates/    — Project templates (ADR / cell-design / contract-review / runbook / postmortem / grafana)
generated/    — Tool-generated artifacts (indexes, derived views)
```

### Layer Dependencies

```
kernel/    ← stdlib + pkg/ + gopkg.in/yaml.v3 (no runtime, adapters, cells)
runtime/   ← kernel/ + pkg/ (no cells, adapters)
cells/     ← kernel/ + runtime/ (no adapters — interface decoupling)
adapters/  ← kernel/ + runtime/ + pkg/ + external libs (no cells)
examples/  ← all layers
```

### Verification Gates

Architectural and security invariants are enforced by static gates that run in
CI (`make verify`) and can be reproduced locally:

| Gate | Script / Test | Enforces |
|------|---------------|----------|
| `PROD-CLOCK-INJECTION-01` | `tools/archtest TestProdClockInjection` | Production code must inject `kernel/clock.Clock`; stdlib `time.Now / Since / Until / NewTimer / NewTicker / After / AfterFunc / Tick / Sleep` are forbidden outside leaf adapters |
| `KERNEL-CLOCK-LEAF-FALLBACK-01` | `tools/archtest TestKernelClockLeafFallback` | Leaf code must not silently default to `clock.Real()` — composition root must inject explicitly |
| `KERNEL-CLOCK-RESET-RELATIVE-PROD-01` | `tools/archtest TestKernelClockResetRelativeProd` | Production code must use `Timer.ResetAt(deadline)` rather than `Timer.Reset(d duration)` to eliminate read-then-act race |
| `CLOCK-INJECTION-TEST-CALLSITE-01` | `tools/archtest TestClockInjectionCallsite` | Every `*_test.go` callsite of a constructor whose package exports `WithClock(Clock)` and accepts variadic Options must include `WithClock(...)` among the options. v1 covers option-pattern only; positional Clock parameters are out of scope. |
| `PROD-CLOCKMOCK-IMPORT-01` | `.golangci.yml depguard rule clockmock-test-only` | Production code must not import `kernel/clock/clockmock` (test-helper packages under `**/testutil/` and `**/storetest/` are exempt) |
| `LAYER-01..04` | `.golangci.yml depguard rules kernel/pkg/runtime/adapters-isolation` | Layered import boundaries (kernel ⇏ runtime/adapters/cells, etc.) |
| `SUPPLY-CHAIN-VULN` | `hack/verify-supply-chain-clean.sh`, `govulncheck`, `gosec`, Semgrep, CodeQL | Vulnerable dependencies + insecure code patterns |
| `SHELL-SAFETY-01` | `hack/verify-shell-safety.sh` | All `hack/*.sh` scripts use `set -euo pipefail` |

Convenience aggregator: `bash hack/verify-prod-clock-injection.sh` runs the
three D6 clock-injection tests in one shot.

## Built-in Cells

- **accesscore** — Identity management, JWT session lifecycle (RS256), RBAC authorization (9 Slices)
- **auditcore** — Tamper-proof audit trail with HMAC-SHA256 hash chain (4 Slices)
- **configcore** — Configuration management with versioning, publishing, and feature flags (6 Slices)

## Adapters

| Adapter | Capabilities | Kernel Interface |
|---------|-------------|-----------------|
| `adapters/postgres` | Pool, TxManager, Migrator (goose v3), OutboxWriter, PGOutboxStore | `outbox.Writer`, `outbox.BatchWriter`, `runtime/outbox.Store` |
| `adapters/redis` | Client, DistLock, IdempotencyClaimer, Cache | `idempotency.Claimer` |
| `adapters/oidc` | Thin go-oidc v3 wrapper (Config, Provider, Refresh, Verifier, OAuth2Config) | — |
| `adapters/s3` | Thin aws-sdk-go-v2 wrapper (Config, Upload, Health, SDK escape hatch) | — |
| `adapters/rabbitmq` | Publisher, Subscriber, ConsumerBase (DLQ + retry) | `outbox.Publisher`, `outbox.Subscriber` |
| `adapters/websocket` | WebSocket Hub, signal-first push | — |
| `adapters/otel` | OTel SDK tracer + MetricProvider + pool collector (OTLP gRPC exporter, semconv `db.client.connection.*`) | `tracing.Tracer`, `kernel/observability/metrics.Provider` |
| `adapters/prometheus` | MetricProvider (backs runtime/outbox collectors) + LifecycleHookObserver | `kernel/observability/metrics.Provider`, `cell.LifecycleHookObserver` |

### Outbox Wiring

The transactional outbox is split across three layers — Cell services depend on
`persistence.TxRunner` + `outbox.Emitter`, store + relay loop lives in
`runtime/outbox`, and persistence lives in `adapters/postgres`:

```go
// 1. Adapt the durable writer at the Cell boundary.
emitter, err := outbox.NewWriterEmitter(postgres.NewOutboxWriter())
if err != nil {
    return err
}

// 2. Service code writes business state + emits inside the same transaction.
err = txRunner.RunInTx(ctx, func(txCtx context.Context) error {
    // ... write business state ...
    return emitter.Emit(txCtx, entry)
})

// 3. Compose the relay at bootstrap (cmd/corebundle, examples, etc.)
store := postgres.NewOutboxStore(pool.DB())
relay := outbox.NewRelay(store, publisher, outbox.DefaultRelayConfig())
// relay implements worker.Worker — register with bootstrap to manage lifecycle.
```

Direct-publish demo paths use `outbox.NewDirectEmitter`; durable writer and
direct publisher paths both marshal the same `kernel/outbox` v1 wire envelope.
`runtime/outbox` owns relay/store runtime state only.

`runtime/outbox` defines the SQL-dialect-neutral `Store` interface (`ClaimPending` / `MarkPublished` / `MarkRetry` / `MarkDead` / `ReclaimStale` / `CleanupPublished` / `CleanupDead` / `OldestEligibleAt`) and the `Relay` worker that owns the poll / reclaim / cleanup goroutines. Cleanup is data-driven: it sleeps until the next published / dead row crosses its retention window, so an idle table costs zero DB cycles.

### Outbox Observability Bridge

For HTTP flows that publish through the transactional outbox, GoCell now bridges
`request_id`, `correlation_id`, and optional `trace_id` from handler context
into `outbox.Entry.Observability` on the write path. When the event is
consumed, `SubscriberWithMiddleware.SubscribeEntry` restores those keys into the
consumer handler context before business code runs.

Consumer setup now has two composition contracts. Subscription-bearing
bootstrap applications must configure `WithConsumerBase`; phase6 fails fast
without it so idempotency and broker settlement are explicit:

```go
cb, err := outbox.NewConsumerBase(
    idempotency.NewInMemClaimer(clk),
    outbox.ConsumerBaseConfig{},
    clk,
)
if err != nil {
    panic(err)
}

app := bootstrap.New(
    bootstrap.WithSubscriber(rawSub),
    bootstrap.WithConsumerBase(cb),
    bootstrap.WithTracer(tracer),
)
```

Bootstrap automatically decorates the subscriber with
`eventrouter.NewContractTracingSubscriber(rawSub, tracer)`, so consumer spans end
after final broker settlement (`ack`, `requeue`, `commit_failed`,
`retry_exhausted`). For non-bootstrap usage, call
`SubscriberWithMiddleware.SubscribeEntry` rather than the raw subscriber when
consuming business handlers, and include the same subscriber decorator when
final-settlement tracing is required:

```go
tracedSub := eventrouter.NewContractTracingSubscriber(rawSub, tracer)
wrappedSub := &outbox.SubscriberWithMiddleware{
    Inner:        tracedSub,
    Middleware:   businessMiddleware,
    ConsumerBase: cb,
}
err := wrappedSub.SubscribeEntry(ctx, sub, handler)
```

Raw `Subscriber.Subscribe` is reserved for adapter/test delivery paths; it
bypasses business middleware, `ConsumerBase`, observability restoration, and
final-settlement tracing. When HTTP tracing is enabled,
GoCell now extracts inbound `traceparent` and `b3` headers before starting the
server span so synchronous service hops preserve the same `trace_id`. Note:
`span_id` is intentionally excluded across async boundaries — spans do not
cross the outbox hop.

### Trace Propagation

When HTTP tracing is enabled via `WithTracer`, GoCell automatically extracts
inbound W3C `traceparent` and B3 headers before starting the server span.
W3C takes precedence; B3 is used only as a fallback. Invalid or missing headers
safely degrade to a new root trace.

**Enablement** — tracing is opt-in via `bootstrap.WithTracer` or
`router.WithTracer`:

```go
// bootstrap (recommended)
tracer := tracing.NewTracer("my-service")  // or adapters/otel.NewTracer(...)
app := bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithListener(cell.PrimaryListener, ":8080",
        []cell.ListenerAuth{cell.AuthNone{}}),
    bootstrap.WithTracer(tracer),
)

// router (standalone)
r := router.New(router.WithTracer(tracer))
```

**Trust assumption**: trace header propagation assumes a trusted-upstream
deployment (service-to-service behind a gateway or mesh). Public-facing edges
should sanitize or ignore inbound trace headers at the gateway layer. See
`TRUST-POLICY-01` in `docs/backlog.md` for the planned public-endpoint strategy.

Framework-emitted consumer logs pick up these fields when the process uses
GoCell's context-aware slog handler. This branch does not make plain slog JSON
handlers automatically extract `request_id`, `correlation_id`, or `trace_id`.
Values restored from broker metadata are validated for safe characters and
length before injection into context.

## Using in Your Project

```bash
# Set up Go private module access
export GOPRIVATE=github.com/ghbvf/gocell

# Add to your project
go get github.com/ghbvf/gocell@latest
```

## Project Templates

GoCell includes templates for common engineering documents:

- `templates/adr.md` — Architecture Decision Record
- `templates/cell-design.md` — Cell design document
- `templates/contract-review.md` — Contract review checklist
- `templates/runbook.md` — Operations runbook
- `templates/postmortem.md` — Incident postmortem
- `templates/grafana-dashboard.json` — Grafana monitoring dashboard

## License

[MIT](LICENSE)
