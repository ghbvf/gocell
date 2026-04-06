# GoCell

Cell-native Go Engineering Foundation.

GoCell provides Cell/Slice runtime primitives, governance toolchain, and built-in Cells for building reliable Go services with the Slice-Cell architecture.

## Quick Start (5 minutes)

```bash
git clone https://github.com/ghbvf/gocell.git
cd gocell/src
go run ./examples/todo-order
```

Open another terminal:

```bash
# Create an order
curl -s -X POST http://localhost:8082/api/v1/orders \
  -H 'Content-Type: application/json' \
  -d '{"item":"my first order"}' | jq .

# List orders
curl -s http://localhost:8082/api/v1/orders | jq .
```

Check the application logs — you should see `event.order.created consumed`.

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
| **Slice** | A single responsibility within a Cell (e.g., `session-login`, `order-create`). |
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
mkdir -p src/cells/my-cell/slices/my-action
```

Create `src/cells/my-cell/cell.yaml`:
```yaml
id: my-cell
type: core
consistencyLevel: L1
owner:
  team: my-team
  role: my-owner
schema:
  primary: my_table
verify:
  smoke:
    - my-cell/smoke
```

Create `src/cells/my-cell/slices/my-action/slice.yaml`:
```yaml
id: my-action
belongsToCell: my-cell
contractUsages:
  - contract: http.my-api.v1
    role: serve
verify:
  unit: my-action/unit
  contract: my-action/contract
```

### Step 2: Define the domain

Create `src/cells/my-cell/internal/domain/model.go`:
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

Create `src/cells/my-cell/cell.go`:
```go
package mycell

import (
    "context"
    "log/slog"
    "github.com/ghbvf/gocell/kernel/cell"
)

type MyCell struct {
    *cell.BaseCell
    logger *slog.Logger
}

func New() *MyCell {
    return &MyCell{
        BaseCell: cell.NewBaseCell(cell.CellMetadata{
            ID: "my-cell", Type: cell.CellTypeCore,
            ConsistencyLevel: cell.L1,
            Owner: cell.Owner{Team: "my-team", Role: "my-owner"},
            Schema: cell.SchemaConfig{Primary: "my_table"},
            Verify: cell.CellVerify{Smoke: []string{"my-cell/smoke"}},
        }),
        logger: slog.Default(),
    }
}

func (c *MyCell) Init(ctx context.Context, deps cell.Dependencies) error {
    return c.BaseCell.Init(ctx, deps)
}

func (c *MyCell) RegisterRoutes(mux cell.RouteMux) {
    mux.Handle("GET /api/v1/hello", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte(`{"message":"hello from my-cell"}`))
    }))
}
```

### Step 4: Create a main.go

```go
package main

import (
    "context"
    "os/signal"
    "syscall"

    mycell "github.com/ghbvf/gocell/cells/my-cell"
    "github.com/ghbvf/gocell/kernel/assembly"
    "github.com/ghbvf/gocell/runtime/bootstrap"
)

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    asm := assembly.New(assembly.Config{ID: "my-app"})
    asm.Register(mycell.New())

    app := bootstrap.New(
        bootstrap.WithAssembly(asm),
        bootstrap.WithHTTPAddr(":8080"),
    )
    app.Run(ctx)
}
```

### Step 5: Build and run

```bash
cd src && go build ./cmd/my-app && ./my-app
# In another terminal:
curl http://localhost:8080/api/v1/hello
# {"message":"hello from my-cell"}
```

## Example Projects

| Example | Complexity | What it demonstrates |
|---------|-----------|---------------------|
| [todo-order](src/examples/todo-order/) | Medium | Custom Cell, CRUD, outbox event publish, RabbitMQ consume |
| [sso-bff](src/examples/sso-bff/) | Medium-High | 3 built-in Cells composition (access + audit + config) |
| [iot-device](src/examples/iot-device/) | High | L4 DeviceLatent: command queue, ack, high-latency loop |

## Architecture

```
src/
├── kernel/       — Cell/Slice runtime + governance tools (framework core)
├── cells/        — Cell implementations (access-core / audit-core / config-core / order-cell / device-cell)
├── contracts/    — Cross-Cell boundary contracts ({kind}/{domain}/{version}/)
├── journeys/     — Journey acceptance specs + status-board.yaml
├── runtime/      — HTTP middleware, auth, worker, observability, bootstrap
├── adapters/     — External system adapters (postgres / redis / oidc / s3 / rabbitmq / websocket)
├── pkg/          — Shared utilities (errcode / ctxkeys / httputil)
├── cmd/          — CLI (gocell validate / scaffold / generate / check / verify)
├── examples/     — Example projects (sso-bff / todo-order / iot-device)
├── templates/    — Project templates (ADR / cell-design / contract-review / runbook / postmortem / grafana)
└── generated/    — Tool-generated artifacts (indexes, derived views)
```

### Layer Dependencies

```
kernel/    ← stdlib + pkg/ + gopkg.in/yaml.v3 (no runtime, adapters, cells)
runtime/   ← kernel/ + pkg/ (no cells, adapters)
cells/     ← kernel/ + runtime/ (no adapters — interface decoupling)
adapters/  ← kernel/ + runtime/ + pkg/ + external libs (no cells)
examples/  ← all layers
```

## Built-in Cells

- **access-core** — Identity management, JWT session lifecycle (RS256), RBAC authorization (7 Slices)
- **audit-core** — Tamper-proof audit trail with HMAC-SHA256 hash chain (4 Slices)
- **config-core** — Configuration management with versioning, publishing, and feature flags (5 Slices)

## Adapters

| Adapter | Capabilities | Kernel Interface |
|---------|-------------|-----------------|
| `adapters/postgres` | Pool, TxManager, Migrator, OutboxWriter, OutboxRelay | `outbox.Writer`, `outbox.Relay` |
| `adapters/redis` | Client, DistLock, IdempotencyChecker, Cache | `idempotency.Checker` |
| `adapters/oidc` | OIDC Provider, Token Exchange, JWKS Verification | — |
| `adapters/s3` | S3/MinIO Client, PresignedURL | — |
| `adapters/rabbitmq` | Publisher, Subscriber, ConsumerBase (DLQ + retry) | `outbox.Publisher`, `outbox.Subscriber` |
| `adapters/websocket` | WebSocket Hub, signal-first push | — |

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
