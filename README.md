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

## 30-Minute Tutorial: Create Your First Cell

Follow these steps to create a custom Cell from scratch.

### Step 1: Create Cell directory and metadata

```bash
mkdir -p cells/mycell/slices/myaction
```

Create `cells/mycell/cell.yaml`:
```yaml
id: mycell
type: core
consistencyLevel: L1
owner:
  team: my-team
  role: my-owner
schema:
  primary: my_table
verify:
  smoke:
    - mycell/smoke
```

Create `cells/mycell/slices/myaction/slice.yaml`:
```yaml
id: myaction
belongsToCell: mycell
contractUsages:
  - contract: http.my-api.v1
    role: serve
verify:
  unit: myaction/unit
  contract: myaction/contract
```

### Step 2: Define the domain

Create `cells/mycell/internal/domain/model.go`:
```go
package domain

type Item struct {
    ID   string
    Name string
}

type ItemRepository interface {
    Create(ctx context.Context, item *Item) error
    GetByID(ctx context.Context, id string) (*Item, error)
}
```

### Step 3: Implement the Cell

Create `cells/mycell/cell.go`:
```go
package mycell

import (
    "context"
    "log/slog"
    "net/http"

    "github.com/ghbvf/gocell/kernel/cell"
    "github.com/ghbvf/gocell/kernel/wrapper"
    "github.com/ghbvf/gocell/runtime/auth"
)

type MyCell struct {
    *cell.BaseCell
    logger *slog.Logger
}

func New() *MyCell {
    return &MyCell{
        BaseCell: cell.NewBaseCell(cell.CellMetadata{
            ID: "mycell", Type: cell.CellTypeCore,
            ConsistencyLevel: cell.L1,
            Owner: cell.Owner{Team: "my-team", Role: "my-owner"},
            Schema: cell.SchemaConfig{Primary: "my_table"},
            Verify: cell.CellVerify{Smoke: []string{"mycell/smoke"}},
        }),
        logger: slog.Default(),
    }
}

func (c *MyCell) Init(ctx context.Context, deps cell.Dependencies) error {
    return c.BaseCell.Init(ctx, deps)
}

func (c *MyCell) RouteGroups() []cell.RouteGroup {
    return []cell.RouteGroup{
        cell.SingleGroup(cell.PrimaryListener, "/api/v1", func(mux cell.RouteMux) error {
            return auth.Mount(mux, auth.Route{
                Contract: wrapper.ContractSpec{
                    ID:        "http.mycell.hello.v1",
                    Kind:      "http",
                    Transport: "http",
                    Method:    "GET",
                    Path:      "/api/v1/hello",
                },
                Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                    _, _ = w.Write([]byte(`{"message":"hello from mycell"}`))
                }),
                Public: true,
            })
        }),
    }
}
```

### Step 4: Create a main.go

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

### Step 5: Build and run

```bash
go build ./cmd/myapp && ./myapp
# In another terminal:
curl http://localhost:8080/api/v1/hello
# {"message":"hello from mycell"}
```

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
| `DurabilityDemo` | 1 | Yes — `NoopWriter`, `NoopTxRunner`, `DiscardPublisher` accepted; missing Tx/outbox dependencies are completed with explicit no-op defaults | Development, unit tests, examples |
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
`NoopTxRunner` / `NoopEmitter` when dependencies are absent, or a direct
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
into `outbox.Entry.Metadata` on the PostgreSQL write path. When the event is
consumed, `ObservabilityContextMiddleware` (registered by bootstrap by default)
restores those keys into the consumer handler context before business code runs.

For non-bootstrap usage, compose `ObservabilityContextMiddleware` via
`SubscriberWithMiddleware` manually. To disable **consume-side restore**, pass
`WithDisableObservabilityRestore()` to bootstrap — the publish-side metadata
injection in the outbox writer remains active. When HTTP tracing is enabled,
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
