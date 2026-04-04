# GoCell Architecture Overview

## What is GoCell

GoCell is a cell-native Go engineering foundation. It provides:

1. **Cell/Slice Runtime** — Interfaces and base implementations for building services as composable cells
2. **Governance Toolchain** — Metadata validation, assembly generation, contract registry, impact analysis
3. **Built-in Cells** — access-core (auth), audit-core (audit trail), config-core (hot-reload + feature flags)
4. **Adapters** — PostgreSQL, Redis, OIDC, S3, VictoriaMetrics, RabbitMQ, WebSocket

## Core Concepts

### Cell

A **Cell** is the runtime, data sovereignty, and deployment boundary.

- Owns authoritative data (tables that define business truth)
- Publishes and consumes contracts
- Has a consistency level (L0-L4)
- Contains one or more slices

Two types:
- **Core Cell** — Owns strong-consistency state machines (e.g., access-core, audit-core)
- **Workflow Cell** — Orchestrates across cores with eventual consistency (e.g., fleet-query-cell)

### Slice

A **Slice** is the minimum development and verification boundary.

- Belongs to exactly one cell (one-slice-one-cell)
- Is the default work unit for AI agents
- Has its own unit + contract tests
- Does NOT own data — data sovereignty belongs to the cell

### Assembly

An **Assembly** is the physical packaging of one or more cells into a deployable binary.

- Generated from assembly.yaml + cell.yaml
- Manages cell startup/shutdown order
- Not a business boundary — only a deployment boundary

### Contract

A **Contract** is the explicit interface between cells.

Four types:
- **HTTP** — Synchronous request/response
- **Event** — Asynchronous fact publication (via outbox)
- **Command** — Asynchronous action request
- **Projection** — Read model consumed by workflow cells

Contracts live in versioned directories with their own JSON Schema files:
```
contracts/events/session/created/v1/
├── contract.yaml      # Relationship declaration (who produces/consumes)
├── payload.schema.json # Format definition
└── examples/
```

### Journey

A **Journey** is a user-facing business closure that spans one or more cells.

- Defined in Journey Catalog (product-level truth)
- Each journey has pass criteria and verification fixtures
- Journey tests are the ultimate validation boundary

## Five-Layer Information Model

| Layer | File | Responsibility | Fact Type |
|-------|------|----------------|-----------|
| Journey Catalog | `journeys/catalog.yaml` | All product journeys | Blueprint |
| Journey Spec | `journeys/*.yaml` | Single journey acceptance spec | Acceptance |
| cell.yaml | `cells/*/cell.yaml` | Stable boundary + governance facts | Boundary |
| slice.yaml | `cells/*/slices/*/slice.yaml` | Work mapping + impact routing | Mapping |
| Status Board | `journeys/status-board.yaml` | Dynamic delivery status | Dynamic |

**Rule: Stable governance facts stay in cell/slice. Dynamic status only in Status Board.**

## Consistency Levels (L0-L4)

| Level | Meaning | Example | Verification |
|-------|---------|---------|-------------|
| L0 LocalOnly | In-slice local processing | Validation, computation | Unit test |
| L1 LocalTx | Single-cell local transaction | Session creation, audit write | Transaction test |
| L2 OutboxFact | Local tx + outbox publish | session.created event | Outbox + consumer test |
| L3 WorkflowEventual | Cross-cell eventual consistency | Query projection, compliance | Replay + projection test |
| L4 DeviceLatent | Device-dependent, long-delay closure | Command ACK, cert renewal | Timeout + late-arrival test |

## Design Principles

1. One-slice-one-cell — A slice belongs to exactly one cell
2. Data sovereignty belongs to cells, not slices
3. Cross-cell communication only via contracts
4. No shared authoritative tables across cells
5. Metadata-first — No code before validate-meta passes
6. Assembly is generated, not hand-written
7. Journey is the primary validation boundary
8. Projections can be discarded and rebuilt
9. Core cells declare noSplitReason — why they must not be decomposed
10. Dynamic status lives only in Status Board

## Table Classifications

| Class | Owner | Can Write | Can Read | Rebuildable |
|-------|-------|-----------|----------|-------------|
| Authoritative | Owner cell only | Owner cell only | Via contract | No — is the truth |
| Projection | Consumer cell | Consumer cell | Direct | Yes — from events |
| Cache | Any cell | Any cell | Direct | Yes — from source |
| Coordination | Owner cell | Owner cell | Owner cell | Depends (outbox yes, lease no) |

## Directory Structure

```
gocell/
├── kernel/                    # Cell/Slice runtime + governance tools
│   ├── cell.go / slice.go / assembly.go
│   ├── metadata/
│   ├── consistency/
│   ├── outbox/ / consumed/ / idempotency/ / replay/ / reconcile/
│   ├── verify/ / trace/ / governance/ / catalog/
│   ├── scaffold/ / selector/ / wrapper/ / generator/
│   ├── webhook/ / rollback/ / support/
│   └── ...
├── cells/                     # Built-in cells
│   ├── access/                # SSO/auth
│   ├── audit/                 # Audit trail
│   └── config/                # Hot-reload + feature flags
├── runtime/                   # Shared runtime
│   ├── http/ / auth/ / worker/ / scheduler/ / retry/
│   ├── security/ / observability/ / config/ / bootstrap/ / shutdown/
│   └── audit/
├── adapters/                  # External system adapters
│   ├── postgres/ / redis/ / oidc/ / s3/ / victoriametrics/
│   ├── family/{rabbitmq,websocket}/
│   └── optional/{mysql,kafka,sqlite,...}/
├── contracts/                 # Contract definitions (shared)
│   ├── http/{access,config}/
│   ├── events/{session,user,config,audit}/
│   ├── commands/
│   ├── projections/
│   └── shared/components/{headers,ids,envelopes}/
├── pkg/                       # Shared utilities
├── cmd/gocell/                # CLI
├── journeys/                  # Journey catalog + status board
├── examples/                  # Example projects
├── templates/                 # ADR, runbook, cell-design templates
├── schemas/                   # Metadata schema reference (renamed from specs/)
└── docs/
```
