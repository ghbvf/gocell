# GoCell Glossary

## Core Objects

### Cell
Runtime, data sovereignty, and deployment boundary. Owns authoritative data, publishes/consumes contracts.

Two types:
- **Core Cell** — Strong-consistency state machines. Must declare `noSplitReason`.
- **Workflow Cell** — Cross-cell orchestration with eventual consistency.

Cell viability requires at least 4 of 5:
1. Independent business result
2. Independent state machine
3. Independent authoritative write model
4. Clear independent deployment benefit
5. No shared transactional requirements

### Slice
Minimum development and verification boundary. Belongs to exactly one cell. Default AI agent work unit.

A slice is NOT a data boundary. Data sovereignty belongs to its owning cell.

### Assembly
Physical packaging of cells into a deployable binary. Generated from metadata. Not a business boundary.

### Contract
Explicit interface between cells. Four kinds:

| Kind | Direction | Consistency | Example |
|------|-----------|-------------|---------|
| HTTP | Sync request/response | L0/L1 | `http.auth.login.v1` |
| Event | Async fact publication | L2 | `event.session.created.v1` |
| Command | Async action request | L2 | `command.device.enqueue.v1` |
| Projection | Read model subscription | L3 | `projection.fleet.device-list.v1` |

Schema separation principle: `contract.yaml` declares relationships (YAML), `*.schema.json` defines data formats (JSON Schema). Schema files belong to the contract version directory, not the cell implementation directory.

### Journey
User-facing business closure spanning one or more cells. The primary validation boundary.

Each journey has:
- `goal` — What it achieves
- `cells` — Which cells are involved
- `passCriteria` — How to verify success
- `fixtures` — Test data sets
- `primaryActor` — Who initiates (persona)

### Journey Catalog
Product-level registry of all journeys. The blueprint truth source.

### Status Board
Single dynamic status snapshot. Only place for `state / risk / blocker / evidenceRefs`.

## Consistency Levels

| Level | Name | Scope | Mechanism |
|-------|------|-------|-----------|
| L0 | LocalOnly | In-slice | Local computation |
| L1 | LocalTx | Single cell | Database transaction |
| L2 | OutboxFact | Single cell + publish | Transaction + outbox |
| L3 | WorkflowEventual | Cross-cell | Event consumption + projection |
| L4 | DeviceLatent | Device-dependent | Long-delay closure |

## Table Classes

| Class | Truth Source | Writer | Reader | Rebuildable |
|-------|-------------|--------|--------|-------------|
| Authoritative | Yes | Owner cell only | Via contract | No |
| Projection | No | Consumer cell | Direct query | Yes |
| Cache | No | Any cell | Direct query | Yes |
| Coordination | No | Owner cell | Owner cell | Depends |

Coordination includes: outbox, consumed markers, replay checkpoints, job leases.

## Data Rules

1. No cross-cell writes to authoritative tables
2. No cross-cell foreign keys
3. No cross-cell UPDATE/DELETE
4. No shared authoritative write models across cells
5. Projection must not upgrade to authoritative
6. Cache must not masquerade as projection
7. Outbox belongs to producer cell
8. Consumed markers belong to consumer cell
9. Migration lane owned by cell owner
10. Non-owner must not modify migration lane

## Governance Objects

### cell.yaml
Stable boundary declaration. Contains: cellId, type, consistencyLevel, owner, ownedSlices, authoritativeData, contracts, verify, noSplitReason, allowedDependencies, servedRoles.

Does NOT contain: readiness, risk, blocker, status.

### slice.yaml
Work mapping declaration. Contains: sliceId, belongsToCell, consistencyLevel, owner, journeys, callsContracts, publishes, consumes, publicEntrypoints, traceAttrs, verify, allowedFiles.

Does NOT contain: done, verified, nextAction, status.

### contract.yaml
Interface declaration. Contains: contractId, kind, version, ownerCell, producer, consumers, consistencyLevel, status, schemaRefs, compatibilityPolicy, traceRequired, idempotencyKey, deliverySemantics, replayable.

### assembly.yaml
Deployment packaging. Contains: assemblyId, cells, exportedContracts, importedContracts, smokeTargets, flags, killSwitches, generated output path.

## Toolchain Commands

| Command | Purpose |
|---------|---------|
| `gocell validate` | Validate all metadata (cell/slice/contract/assembly) |
| `gocell scaffold cell` | Generate new cell directory + metadata |
| `gocell scaffold slice` | Generate new slice in existing cell |
| `gocell scaffold contract` | Generate new contract directory + schema |
| `gocell generate assembly` | Generate Go startup code from metadata |
| `gocell check deps` | Check forbidden dependencies + one-slice-one-cell |
| `gocell check contracts` | Check unregistered contracts + compatibility |
| `gocell select targets` | Compute affected slices/journeys from file changes |
| `gocell verify slice` | Run slice-level tests (go test wrapper) |
| `gocell verify cell` | Run cell-level smoke tests |
| `gocell run journey` | Run cross-cell journey tests |
