# DevTools Catalog Guide

## Overview

`gocell export catalog` / `GET /api/v1/devtools/catalog` provides a unified project metadata catalog (Unified Catalog). It aggregates Cell, Slice, Contract, Journey, Assembly, and Actor entities from a GoCell project, together with the cell-level dependency graph (`cellDeps`), the package-level typed dep graph (`packageDeps`), and the status board (`statusBoard`), into a single document that can be trimmed on demand via query parameters (query / flag).

Design goals:

- **Decoupled from gocell-web**: at frontend build time, call `gocell export catalog --out=public/catalog.json`; the frontend loads it via same-origin `fetch('/catalog.json')` — zero CORS, zero live endpoint deployment coupling.
- **Single endpoint, multiple views**: CLI flags and HTTP query parameters are semantically symmetric; front-end and back-end consumers share the same wire schema.
- **Admin-gated by default**: the HTTP endpoint defaults to `admin` role access (`auth.AnyRole("admin")`), following the PR-CFG-4 fail-secure pattern.

The wire schema draws on the [Backstage Catalog Entity model](https://backstage.io/docs/features/software-catalog/descriptor-format) (no Backstage dependency is introduced); the package-level dependency graph draws on the [loov/goda](https://github.com/loov/goda) internal `pkggraph` data model.

---

## CLI Usage

The primary command is `gocell export catalog`; `gocell export metadata` is an alias (the output is byte-equal).

### Flag Reference

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--kinds` | comma list | `""` (all) | Filter by entity kind; valid values: `Cell,Slice,Contract,Journey,Assembly,Actor` |
| `--layers` | comma list | `""` (all) | Filter by layer, applied to entity and `packageDeps` node filtering; valid values: `adapters,actors,assemblies,cells,cmd,contracts,examples,generated,journeys,kernel,pkg,root,runtime,stdlib,tests,thirdparty,tools,unknown` |
| `--cells` | comma list | `""` (all) | Focus mode: output only the specified cell plus its first-order neighbours (depended-on/depended-by cells, owned slices, contractUsages) |
| `--include` | comma list | `cellDeps,packageDeps,statusBoard,relations` (all on) | Optional output blocks; valid values: `cellDeps,packageDeps,statusBoard,relations` |
| `--format` | `json\|yaml` | `json` | Output format |
| `--out` | file path | `""` (stdout) | Output file path; empty writes to stdout |
| `--root` | directory path | `""` (triggers auto-detection by walking up to the nearest `go.mod`) | GoCell project root (directory containing `cells/`, `contracts/`, `journeys/`, etc.) |

### Examples

**Example 1: Default full output (stdout, JSON)**

```bash
gocell export catalog
```

Output includes all entities, cellDeps, packageDeps, statusBoard, and relations. The CLI loads the package-level dep graph synchronously (5–10 s; acceptable in CI/Docker build contexts); the HTTP endpoint uses a build-time generated graph and does no runtime loading or waiting.

**Example 2: Filter by entity kind — only Cell and Contract**

```bash
gocell export catalog --kinds=Cell,Contract --format=yaml --out=catalog.yaml
```

`entities` contains only entries with Kind=Cell and Kind=Contract; other blocks are unaffected.

**Example 3: Focus on the accesscore cell + relations view**

```bash
gocell export catalog --cells=accesscore --include=cellDeps,relations
```

Output contains only `accesscore` and its first-order neighbour entities, plus the cellDeps graph and relations list; packageDeps and statusBoard are omitted (not declared in `--include`).

**Example 4: Export the package-level dep graph (YAML format)**

```bash
gocell export catalog --include=packageDeps --format=yaml --out=public/package-deps.yaml
```

Triggers a synchronous `tools/depgraph.Load()` call (approximately 5–10 s); output has a `dependencies.packages` block with the full package graph nodes and edges.

**Example 5: Embed in a gocell-web Dockerfile build stage**

```dockerfile
RUN gocell export catalog --include=cellDeps,packageDeps,statusBoard,relations \
    --out=public/catalog.json
```

Frontend usage:

```typescript
const catalog = await fetch('/catalog.json').then(r => r.json());
```

---

## HTTP Usage

### Endpoint

```
GET /api/v1/devtools/catalog
```

- Authentication: `admin` role (`auth.AnyRole(auth.RoleAdmin)`); non-admin returns 403, unauthenticated returns 401.
- Bootstrap-wired: registered via the `runtime/http/devtools/` package with no `contract.yaml` (framework introspection route; see Engineering Notes below). The path follows the `/api/v1/` prefix rule from api-versioning.md (consistent with business APIs), unlike the unversioned health endpoints `/healthz` / `/readyz`.
- Requires the `GOCELL_PROJECT_ROOT` environment variable pointing to the project root directory (corebundle reads it by default and validates the path at startup); if not set, the handler is not registered.

### Query Parameter Reference

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `kinds` | comma list | `""` | Same as CLI `--kinds` |
| `layers` | comma list | `""` | Same as CLI `--layers`; valid values as above |
| `cells` | comma list | `""` | Same as CLI `--cells` |
| `include` | comma list | `cellDeps,packageDeps,statusBoard,relations` | Same as CLI `--include` |
| `format` | `json\|yaml` | `json` | Response format; Content-Type is `application/yaml` when `yaml` |

### Multi-Dimensional Filter AND Semantics

When `kinds`, `layers`, and `cells` are all present, they are combined with **AND** (an entity is retained only if it satisfies all non-empty conditions). Specific behaviour:

| Scenario | Result |
|----------|--------|
| `?cells=accesscore` (single-dimension focus) | Outputs accesscore + first-order neighbours (including its Contracts/Journeys, because `layers` is unrestricted) |
| `?layers=cells&cells=accesscore` (two-dimension AND) | **Only outputs Kind=Cell entities that belong to the accesscore neighbour set**; Contract/Journey entities are filtered out because their `entityLayer` is `contracts`/`journeys`, outside the `layers=cells` set |
| `?kinds=Contract&cells=accesscore` | Outputs only the Contract entities owned by accesscore |

> **Recommendation**: to focus on a single cell and see all its neighbours (including contracts), use `?cells=foo` **without** `?layers=`. Only combine `?layers=` if you explicitly understand the filtering effect.

### curl Examples

**Example 1: Normal access with admin token**

```bash
# Obtain admin token (development environment)
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/sessions \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"YOUR_ADMIN_PASSWORD"}' \
  | jq -r '.data.accessToken')

# Fetch the full catalog
curl -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/api/v1/devtools/catalog' | jq '.schemaVersion, (.entities | length)'
```

**Example 2: Non-admin user access (403)**

```bash
curl -s -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer $NON_ADMIN_TOKEN" \
  'http://localhost:8080/api/v1/devtools/catalog'
# Output: 403
```

**Example 3: Unauthenticated access (401)**

```bash
curl -s -o /dev/null -w "%{http_code}" \
  'http://localhost:8080/api/v1/devtools/catalog'
# Output: 401
```

**Example 4: Focus cell + packageDeps combination**

```bash
curl -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/api/v1/devtools/catalog?cells=accesscore&include=packageDeps,cellDeps' \
  | jq '.dependencies.packages.graph.modules[], .dependencies.cells.nodes'
```

### Build-time packageDeps (`dependencies.packages.graph`)

The package-level dep graph is generated at **build time** by `go generate ./cmd/corebundle/` and committed as `cmd/corebundle/catalog_gen.go`. The HTTP handler reads the graph data compiled into the binary at startup — zero runtime goroutines, zero wait time.

`PackageDepsView` has only two forms: on success it returns `{"graph": {...}}`; on failure it returns `{"error": "..."}`. `Graph != nil` means ready; `Error != ""` means error. A separate `status` enum field is no longer maintained (the lazy-load approach has been replaced by build-time generation; a `loading` state cannot occur, so the redundant field has been removed).

| Scenario | `dependencies.packages` block | Behaviour |
|----------|------------------------------|-----------|
| `catalog_gen.go` has been generated (normal case) | `{"graph": {...}}` | Returns 200; package graph is always ready |
| `catalog_gen.go` not generated (fresh clone / forgot `go generate`) | absent (block does not appear) | Returns 200; other blocks (entities/cellDeps/statusBoard) return normally |
| CLI synchronous load failure | `{"error": "..."}` | CLI exits non-zero; error field contains the failure description (this form never appears in the HTTP path — HTTP uses only the build-time graph) |

**When to regenerate**: after any structural changes to `cells/`, `contracts/`, or packages, re-run:

```bash
go generate ./cmd/corebundle/
```

Then commit the updated `cmd/corebundle/catalog_gen.go`.

---

## gocell-web Integration

Call the CLI in the `gocell-web` `Dockerfile` build stage to embed a static catalog file; the frontend loads it same-origin, requiring no live endpoint:

```dockerfile
# Stage 1: Export catalog
# Requires Go toolchain: --include=packageDeps invokes tools/depgraph (go/packages)
# synchronously, taking 5–10 s. If packageDeps is not needed, remove that value
# and use a smaller builder image.
FROM golang:1.24-alpine AS catalog-builder
WORKDIR /gocell
COPY . .
RUN go install ./cmd/gocell && \
    gocell export catalog \
      --include=cellDeps,packageDeps,statusBoard,relations \
      --out=public/catalog.json

# Stage 2: Frontend build
FROM node:22-alpine AS frontend
WORKDIR /app
COPY --from=catalog-builder /gocell/public/catalog.json public/catalog.json
# ... npm install && npm run build ...
```

Frontend loading (TypeScript):

```typescript
// Same-origin load, zero CORS
const resp = await fetch('/catalog.json');
const catalog: CatalogDocument = await resp.json();
```

---

## Wire Envelope Schema Summary

Top-level structure (`schemaVersion: "v1"`, `apiVersion: "gocell.io/v1alpha1"`):

```json
{
  "schemaVersion": "v1",
  "apiVersion": "gocell.io/v1alpha1",
  "generatedAt": "2026-05-03T00:00:00Z",
  "root": "/path/to/project",
  "query": { "include": ["cellDeps", "packageDeps", "relations", "statusBoard"] },
  "entities": [
    {
      "kind": "Cell",
      "metadata": { "name": "accesscore", "owner": "...", "labels": {} },
      "spec": { "consistencyLevel": "L1", "type": "core" }
    }
  ],
  "statusBoard": [
    { "journeyId": "J-useronboarding", "state": "planned", "risk": "", "blocker": "", "updatedAt": "2026-05-03" }
  ],
  "dependencies": {
    "cells": {
      "nodes": ["accesscore", "configcore"],
      "edges": [{ "from": "accesscore", "to": "configcore" }]
    },
    "packages": {
      "graph": {
        "modules": ["github.com/ghbvf/gocell"],
        "packages": [ { "importPath": "...", "layer": "cells", "cellID": "accesscore" } ],
        "edges": [ { "from": "...", "to": "..." } ]
      }
    }
  }
}
```

Three top-level functional blocks:

| Block | Field | Description |
|-------|-------|-------------|
| `entities` | `[]Entity` | List of Cell/Slice/Contract/Journey/Assembly/Actor entities; Backstage Entity model structure |
| `statusBoard` | `[]StatusBoardEntry` | From `journeys/status-board.yaml`; entries with state `draft` or `planned` have their `risk` and `blocker` fields cleared in the output (retaining `journeyId/state/updatedAt`), to avoid exposing internal planning narrative in publicly distributed gocell-web bundles |
| `dependencies` | `*Dependencies` | Contains two sub-blocks: `cells` (cell-level dependency graph) and `packages` (package-level typed dep graph) |

`entities[*].relations` fields (e.g. hasPart/partOf/dependsOn/ownedBy) symmetrically follow the [Backstage well-known relations](https://backstage.io/docs/features/software-catalog/well-known-relations) naming convention.

### CellSpec / SliceSpec lifecycle Field

The `entities[*].spec.lifecycle` field in Cell and Slice entities represents **maturity** (production-readiness), derived from the `lifecycle` field in `cell.yaml` / `slice.yaml`:

| Field | Type | Description |
|-------|------|-------------|
| `CellSpec.Lifecycle` | `string` (omitempty) | Cell maturity; omitted values are treated as `experimental` by consumers |
| `SliceSpec.Lifecycle` | `string` (omitempty) | Slice maturity; omitted values are treated as `experimental` by consumers |

**Valid values** (Cell / Slice): `experimental` | `candidate` | `asset` | `maintenance` | `retired`

> **Note: The three artifact types use different lifecycle value domains — do not mix them:**
>
> | Artifact | lifecycle values | Semantics |
> |----------|-----------------|-----------|
> | Cell / Slice (this field) | `experimental` \| `candidate` \| `asset` \| `maintenance` \| `retired` | Maturity (production-readiness) |
> | Contract (`ContractSpec.Lifecycle`) | `draft` \| `active` \| `deprecated` | Wire stability (how safely it can be consumed) |
> | Journey (`JourneyMeta.Lifecycle`) | `active` \| `experimental` | Delivery status |
>
> The three semantic axes are orthogonal and their value domains are intentionally non-overlapping; do not infer one from another.

---

## Backstage Borrowing Note

This module draws on the Backstage Catalog entity model (Entity / kind / metadata / spec hierarchy) and relation semantics (well-known relations), but **introduces no Backstage dependency**:

- Only the documented naming conventions and wire shape are reused, meeting GoCell's cell-native governance needs
- GoCell's Entity types (Cell/Slice/Contract/Journey/Assembly/Actor) have a mapping to Backstage's Component/API/System/Domain but are not equivalent
- If future Backstage ecosystem integration is needed, the wire schema delta is minimal and adaptation cost is low

---

## Engineering Notes

### Build-time Codegen (packageDeps) — Build-tag Stub Design

The `packageDeps` graph is generated by the `gocell generate catalog` sub-command and output as `cmd/corebundle/catalog_gen.go`, but **that file is not committed to the repository** (`.gitignore` blacklist + `hack/verify-gitignore-respect.sh` guard).

#### Why Not Commit

`tools/depgraph.Load` uses `golang.org/x/tools/go/packages.Load` internally, and the handling of the `.Imports` field for production packages has minor platform differences between macOS and Linux (a known quirk: when a production package has an internal `_test.go` that imports packages not imported by the production code, platforms differ on whether to merge those into production `.Imports`). Any `catalog_gen.go` committed to the repository would drift across platforms and cause CI to fail permanently.

Reference practices: Kubernetes uses dev-containers to enforce environment consistency; buf locks tool versions; go-zero does not commit any generated files. We follow the go-zero approach plus a build-tag stub for onboarding fallback.

#### Two Files + Two Build Tags

| File | Status | Build tag | Content |
|------|--------|----------|---------|
| `cmd/corebundle/catalog_gen_stub.go` | **committed** | `//go:build !catalog_gen` (used in default builds) | Empty graph stub (`FromNodes("...", []*Node{})`); the `dependencies.packages.graph` field in the catalog endpoint is empty (all other blocks work normally) |
| `cmd/corebundle/catalog_gen.go` | **.gitignore**, regenerated each time | `//go:build catalog_gen` | Complete real graph extracted by `tools/depgraph.Load(./...)` |

#### Three Build Paths

```bash
# 1. Developer freshly git-cloned — build directly (uses stub; packageDeps is empty, all other features work)
go build ./cmd/corebundle/

# 2. Full packageDeps needed (local dev / testing the catalog endpoint)
make build                                    # Makefile auto-runs generate + -tags=catalog_gen
# Equivalent:
go generate ./cmd/corebundle/
go build -tags=catalog_gen ./cmd/corebundle/

# 3. CI / prod Docker build — same as #2
go generate ./cmd/corebundle/ && go build -tags=catalog_gen -o bin/corebundle ./cmd/corebundle/
```

The CLI `gocell export catalog --include=packageDeps` is unaffected by build tags — it calls `tools/depgraph.Load()` directly (operator context, avoiding stale data) and always outputs the full graph.

#### Preventing Accidental Commits

`hack/verify-gitignore-respect.sh` (automatically included by `make verify`) scans a whitelist of "must not be tracked" files (including `cmd/corebundle/catalog_gen.go`); if someone force-adds one with `git add -f`, verify fails. When adding new build artifacts to `.gitignore`, add them to this script's whitelist at the same time.

### Wire Envelope Exemption (No `{"data": ...}` Wrapping)

`/api/v1/devtools/catalog` is a **framework-internal admin endpoint**; its wire format returns the Backstage Catalog Entity envelope directly (`apiVersion/kind/metadata/spec` top-level structure) **without** a `{"data": ...}` wrapper.

The `{"data": ...}` envelope mandated for business APIs (see `.claude/rules/gocell/api-versioning.md`) applies only to cell-owned routes; runtime internal governance routes (this endpoint + `/healthz` `/readyz` `/metrics`) follow their own wire formats.

### Wire Type Location (`runtime/devtools/catalog/`)

Wire format types (`Document` / `Entity` / `IncludeOptions` / `ExportOptions` / `BuildDocument` / `MarshalDocument`, etc.) are declared in the `runtime/devtools/catalog/` package; CLI and HTTP handler receive `*kernel/metadata.ProjectMeta` as a parameter. `kernel/metadata` carries only the ProjectMeta parsing model; downstream consumers importing `kernel/metadata` are not locked into the wire format (see ADR `docs/architecture/202605040030-adr-wire-format-out-of-kernel.md`).

### Bootstrap-wired (No contract.yaml)

The `/api/v1/devtools/catalog` route lives in the `runtime/http/devtools/` package, wired via the bootstrap `WithDevtoolsCatalog(pm, root, generatedPackageGraph)` option — the same pattern as `runtime/http/health/` (framework introspection routes). **No cell and no contract.yaml are created**, for three reasons:

1. devtools has no business state, no events, no outbox; creating a cell would add ~5 YAML/schema files with no benefit
2. A contract describing the catalog API would itself appear in the catalog directory, described by itself — a semantic cycle
3. The sole current authentication requirement (admin-gated) is satisfied directly by bootstrap-level `auth.AnyRole(auth.RoleAdmin)`, with no need for contract-level RBAC

If a promotion trigger is met in the future (see backlog T10 DEVTOOLS-CELL-PROMOTION-01), it can be migrated to a proper cell.

### Environment Variable

`corebundle` uses `GOCELL_PROJECT_ROOT` to decide whether to enable the devtools handler:

```bash
# Local development (project root directory)
GOCELL_PROJECT_ROOT=/path/to/gocell ./bin/corebundle

# Docker deployment
ENV GOCELL_PROJECT_ROOT=/app
```

When `GOCELL_PROJECT_ROOT` is not set, `WithDevtoolsCatalog` does not register the route (service startup is unaffected).

### CLI Package-Level Load Time

CLI `--include=packageDeps` triggers a synchronous call to `tools/depgraph.Load()` (backed by `golang.org/x/tools/go/packages`), taking approximately 5–10 s. Appropriate for:

- CI build stage (single execution; acceptable)
- Docker build-time static file embedding
- Local development and debugging

When **not including `packageDeps`** (e.g. `--include=cellDeps,statusBoard,relations`), execution time is < 1 s.

The HTTP endpoint uses the build-time generated graph with zero runtime loading wait.

### Upgrade Path

If any of the following triggers are met, refer to backlog T10 DEVTOOLS-CELL-PROMOTION-01 for migration to a full cell:

- (a) A cell needs to carry cell-custom fields in the catalog with contract schema validation
- (b) devtools needs to emit events (subscribing/broadcasting catalog changes)
- (c) Fine-grained RBAC requirements emerge beyond admin-only (different roles see different fields)

---

## References (commit message ref)

The following three refs accompany the commit message for this feature:

- `ref: backstage/backstage packages/catalog-model/src/entity/Entity.ts@master`
- `ref: backstage/backstage docs/features/software-catalog/well-known-relations.md@master`
- `ref: loov/goda internal/pkggraph`
