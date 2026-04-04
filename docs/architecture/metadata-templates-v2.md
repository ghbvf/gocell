# GoCell Metadata Templates V2

## Design Goals

This version is derived from first principles:

1. A fact should be authored once.
2. Stable governance facts belong in metadata.
3. Dynamic delivery status does not belong in metadata.
4. Contract structure belongs in versioned schema files, not in implementation directories.
5. The model must be sufficient for:
   - `validate-meta`
   - `generate-assembly`
   - `select-targets`
   - `verify-slice`
   - `verify-cell`
   - `run-journey`
6. Derivable facts should be generated or validated, not hand-maintained.

## Canonical Truth Rules

Use these names as the canonical field set:

- `id`
- `owner`
- `schema.primary`
- `belongsToCell`
- `callsContracts / publishes / consumes`
- structured `verify`

Do not use these legacy names in new metadata:

- `cellId`
- `sliceId`
- `contractId`
- `assemblyId`
- `ownedSlices`
- `authoritativeData`

Do not put dynamic status into metadata:

- `readiness`
- `risk`
- `blocker`
- `done`
- `verified`
- `nextAction`
- `lastUpdated`

Those belong only in `journeys/status-board.yaml`.

## Five-Layer Model

| Layer | File | Canonical Responsibility |
|------|------|--------------------------|
| 1 | `journeys/catalog.yaml` | Minimal journey index |
| 2 | `journeys/*.yaml` | Single-journey acceptance spec |
| 3 | `cells/*/cell.yaml` | Runtime boundary and data sovereignty |
| 4 | `cells/*/slices/*/slice.yaml` | Work mapping and impact routing |
| 5 | `journeys/status-board.yaml` | Dynamic delivery state |

Contracts are cross-cutting boundary assets under `contracts/**/contract.yaml`.

They are referenced by layers 2-4, so they are documented separately instead of being forced into a numbered product-information layer.

Cells within the same assembly may interact via Go interfaces without a contract. Contracts are only required when the interaction crosses a cell boundary that needs independent evolvability, versioning, or consumer allowlisting.

## Relationship Ownership

To avoid drift, these facts are canonical in exactly one place:

| Fact | Canonical Owner | Notes |
|------|------------------|-------|
| Which journeys exist and where their specs live | `journeys/catalog.yaml` | Minimal index only; it may be generated |
| What a journey means | `journeys/*.yaml` | `goal`, `owner`, `cells`, `contracts`, and acceptance criteria live here |
| Which cell a slice belongs to | `slice.belongsToCell` | `cell.slices` should be generated, not hand-maintained |
| Which journeys a slice affects | `journeys/*.yaml` via `cells` + `slice.belongsToCell` | Derived by tooling, not hand-maintained in slice |
| Which cells participate in a journey | `journeys/*.yaml` | Catalog does not repeat journey content |
| Which contracts a slice calls/publishes/consumes | `slice.yaml` | Implementation mapping for impact routing and test targeting |
| Which runtime actor owns a contract boundary and which actors may consume it | `contract.yaml` | Slice-level use must validate against this boundary declaration |
| Which cells are packaged together | `assembly.yaml` | Physical packaging only |
| Which contracts cross an assembly boundary | `assembly.yaml` | Boundary surface, not the union of all internal contract usage |

Implication:

- `cell.yaml` should not be the hand-maintained home of `slices`, `journeys`, or `contracts`.
- If those summaries are useful, generate them into a registry or derived view.
- `journeys/catalog.yaml` should stay a minimal index, not a second hand-maintained copy of journey metadata.

## Core Metadata vs Extensions

Core metadata is required to make the system executable.

Extensions may be useful, but they are not part of the minimum stable model.

Examples of extensions:

- `allowedDependencies`
- `servedRoles`
- `stakeholders`
- extra domain-specific trace attributes
- tool-specific generator configuration

## Layer 1: `journeys/catalog.yaml`

```yaml
- id: J-sso-login
  spec: journeys/J-sso-login.yaml

- id: J-audit-login-trail
  spec: journeys/J-audit-login-trail.yaml
```

`catalog.yaml` is a minimal registry.

It should not duplicate `goal`, `owner`, `cells`, `contracts`, or acceptance criteria from the journey spec.

If the repo prefers discovery by globbing `journeys/J-*.yaml`, `catalog.yaml` can be generated.

Required:

- `id`
- `spec`

## Layer 2: `journeys/*.yaml`

```yaml
id: J-sso-login
goal: user completes SSO login and receives valid session
owner:
  team: platform
  role: journey-owner
primaryActor: end-user
cells:
  - access-core
fixtures:
  - fixture-oidc-provider
  - fixture-user-basic
contracts:
  - http.auth.login.v1
  - event.session.created.v1
passCriteria:
  - OIDC redirect completes
  - callback token exchanged
  - session created in DB
  - JWT cookie set
  - user info accessible via /me
```

Required:

- `id`
- `goal`
- `owner`
- `cells`
- `contracts`
- `passCriteria`

Recommended optional:

- `primaryActor`
- `fixtures`

## Layer 5: `journeys/status-board.yaml`

```yaml
- id: J-sso-login
  state: doing
  risk: low
  blocker: ""
  evidenceRefs:
    - tests/journey/J-sso-login.log

- id: J-audit-login-trail
  state: todo
  risk: medium
  blocker: waiting for audit-core scaffolding
  evidenceRefs: []
```

Required:

- `id`
- `state`
- `risk`
- `blocker`
- `evidenceRefs`

## `cell.yaml`

`cell.yaml` owns runtime-boundary facts, not reverse indexes.

```yaml
id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
noSplitReason:
  - session creation and identity verification share one transaction boundary
```

Required:

- `id`
- `type`
- `consistencyLevel`
- `owner`
- `schema.primary`
- `verify.smoke`

Recommended optional:

- `noSplitReason`
- `schema.tables`

Extension examples:

```yaml
allowedDependencies:
  - config-core
servedRoles:
  - end-user
stakeholders:
  - security
```

## `slice.yaml`

`slice.yaml` is the canonical source for work mapping.

It does not redefine contract ownership.

It only records which contracts this slice actually touches so tools can route impact, verification, and review.

```yaml
id: session-login
belongsToCell: access-core
consistencyLevel: L2
owner:
  team: platform
  role: slice-owner
callsContracts:
  - http.config.get.v1
publishes:
  - event.session.created.v1
consumes: []
publicEntrypoints:
  - POST /api/v1/auth/login
traceAttrs:
  extra:
    - session_id
    - user_id
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.producer
allowedFiles:
  - cells/access/slices/session-login/**
```

Required:

- `id`
- `belongsToCell`
- `consistencyLevel`
- `owner`
- `callsContracts`
- `publishes`
- `consumes`
- `verify.unit`
- `verify.contract`
- `allowedFiles`

Recommended optional:

- `publicEntrypoints`
- `traceAttrs.extra`

Rule:

- Platform trace envelope fields such as `traceId`, `journeyId`, `callerCellId`, and `calleeCellId` are runtime standards, not per-slice metadata.
- `traceAttrs` should only describe additional domain attributes worth attaching to traces.

## `contract.yaml`

Common required fields for all contract kinds:

- `id`
- `kind`
- `version`
- `ownerCell`
- `producer`
- `consumers`
- `consistencyLevel`
- `status`
- `schemaRefs`

Common recommended fields:

- `summary`
- `semantics`

### `status` values

| Value | Meaning |
|-------|---------|
| `draft` | Contract defined but not yet serving traffic. `consumers` may be `[]`. |
| `active` | Contract is live. At least one consumer expected. |
| `deprecated` | Contract is scheduled for removal. Consumers should migrate. |

### `consumers` and draft contracts

`consumers` is required but may be an empty list `[]` when `status: draft`.

Once `status` becomes `active`, at least one consumer should be present.

### Contract defaults

Unless overridden in an individual contract:

- `compatibilityPolicy`: `{ breaking: [remove_field, change_field_semantics], nonBreaking: [add_optional_field] }`
- `traceRequired`: `true`

Contracts only need to declare these fields when deviating from defaults.

### Relationship fields

- `ownerCell` is the cell responsible for the contract definition and lifecycle.
- `producer` is the runtime actor that emits, serves, or initiates this contract.
- `consumers` is the allowlist of runtime actors expected to consume it.

`ownerCell` and `producer` are often the same cell, but not always. See the Command Contract example below where `producer` is the caller, not the owner.

`slice.yaml` may reference a contract, but it should not restate these boundary facts.

Instead, slice-level usage must validate against the declarations in `contract.yaml`.

### HTTP Contract

```yaml
id: http.auth.login.v1
kind: http
version: v1
ownerCell: access-core
producer: access-core
consumers:
  - edge-bff
consistencyLevel: L1
status: active
summary: authenticate user and create login session
schemaRefs:
  request: request.schema.json
  response: response.schema.json
```

### Event Contract

```yaml
id: event.session.created.v1
kind: event
version: v1
ownerCell: access-core
producer: access-core
consumers:
  - audit-core
  - config-core
consistencyLevel: L2
status: active
summary: session creation finalized and visible to downstream consumers
schemaRefs:
  payload: payload.schema.json
  headers: headers.schema.json
semantics:
  fact: session creation completed
replayable: true
idempotencyKey: eventId
orderingSemantics: aggregateId+sequence
deliverySemantics: at-least-once
```

Additional required for `event`:

- `replayable`

### Command Contract

```yaml
id: command.device.enqueue.v1
kind: command
version: v1
ownerCell: device-command-core
producer: edge-bff
consumers:
  - device-command-core
consistencyLevel: L2
status: active
summary: request command execution on target device
schemaRefs:
  request: request.schema.json
  ack: ack.schema.json
  result: result.schema.json
semantics:
  action: enqueue device command
```

Note: `producer: edge-bff` differs from `ownerCell: device-command-core`. The caller initiates the command, but the owning cell defines and processes it.

### Projection Contract

```yaml
id: projection.audit.timeline.v1
kind: projection
version: v1
ownerCell: audit-core
producer: audit-core
consumers:
  - edge-bff
consistencyLevel: L3
status: active
summary: read-only audit timeline view
schemaRefs:
  projection: projection.schema.json
replayable: true
```

Additional required for `projection`:

- `replayable`

## `assembly.yaml`

`assembly.yaml` owns physical packaging facts only.

`exportedContracts` and `importedContracts` describe the contract surface that crosses the assembly boundary.

They are not intended to be a mechanical dump of every contract touched by cells inside the assembly.

```yaml
id: core-bundle
cells:
  - access-core
  - audit-core
  - config-core
exportedContracts:
  - http.auth.login.v1
  - http.auth.me.v1
  - http.config.get.v1
importedContracts: []
smokeTargets:
  - smoke.access-core.startup
  - smoke.audit-core.startup
  - smoke.config-core.startup
killSwitches:
  - kill.audit-consumer
```

Required:

- `id`
- `cells`
- `exportedContracts`
- `importedContracts`
- `smokeTargets`

Recommended optional:

- `killSwitches`
- `flags`

Tool-specific generator configuration should be optional:

```yaml
generated:
  outputDir: assemblies/core-bundle/generated
```

## Schema Placement Rule

Cross-boundary schemas belong to the contract version directory, not to cell implementation directories.

Example:

```text
contracts/events/session/created/v1/
  contract.yaml
  payload.schema.json
  headers.schema.json
  examples/

contracts/http/auth/login/v1/
  contract.yaml
  request.schema.json
  response.schema.json
  examples/

contracts/commands/device/enqueue/v1/
  contract.yaml
  request.schema.json
  ack.schema.json
  result.schema.json

contracts/projections/audit/timeline/v1/
  contract.yaml
  projection.schema.json
```

## Validation Expectations

`validate-meta` should enforce at least these rules:

1. Every `slice.belongsToCell` points to an existing cell.
2. Every `journeys/catalog.yaml` entry points to an existing journey spec, and `catalog.id` must equal `journey.id`.
3. Every `journeys/*.yaml` `cells` entry points to an existing cell, and every `contracts` entry points to an existing contract.
4. Every contract referenced by a slice exists.
5. If a slice publishes contract `C`, `contract.producer` for `C` must equal `slice.belongsToCell`.
6. If a slice calls or consumes contract `C`, `slice.belongsToCell` must be allowed by `contract.consumers` for `C`.
7. Every contract `producer` and `consumers` entry refers to an existing runtime actor.
8. `contract.version` must match the version segment in `id` and directory path.
9. Every `assembly.exportedContracts` and `assembly.importedContracts` entry points to an existing contract.
10. A contract listed in `assembly.exportedContracts` or `assembly.importedContracts` must cross the assembly boundary rather than stay fully internal.
11. `slice.consistencyLevel` must not exceed `cell.consistencyLevel` of the cell it belongs to.
12. Contract `kind` and `consistencyLevel` must be a valid combination: `event` requires `>= L2`, `projection` requires `>= L3`, `command` requires `>= L2`.
13. Slice `verify` fields must satisfy the minimum verification level required by its `consistencyLevel` (as defined in `consistency.md`).
14. Every contract referenced by a `journeys/*.yaml` must have its `producer` or at least one `consumer` present in that journey's `cells` list.
15. No two slices may have overlapping `allowedFiles` glob patterns.
16. Dynamic status fields must not appear in `cell.yaml / slice.yaml / contract.yaml / assembly.yaml`.
17. Derived indexes, summaries, and generated registries must match canonical sources.
