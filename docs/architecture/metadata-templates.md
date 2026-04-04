# GoCell Metadata Templates

## Naming Freeze

Use these names as the canonical field set:

- Use `id`, not `cellId / sliceId / contractId / assemblyId`
- Use `owner` object, not flat `owner: team-platform`
- Use `schema.primary`, not `authoritativeData`
- Use `slices`, not `ownedSlices`
- Use `contracts.produces / contracts.consumes`
- Use structured `verify`

Dynamic delivery state does not belong in metadata.

Do not put these fields in `cell.yaml / slice.yaml / contract.yaml / assembly.yaml`:

- `readiness`
- `risk`
- `blocker`
- `done`
- `verified`
- `nextAction`
- `lastUpdated`

Those belong in `journeys/status-board.yaml`.

## `cell.yaml`

```yaml
id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
journeys:
  - J-sso-login
  - J-session-refresh
schema:
  primary: cell_access_core
  tables:
    authoritative:
      - users
      - sessions
      - refresh_tokens
    coordination:
      - outbox_events
contracts:
  produces:
    - event.session.created.v1
    - event.session.revoked.v1
    - http.auth.login.v1
  consumes:
    - event.config.changed.v1
slices:
  - identity-manage
  - session-login
  - session-refresh
  - session-logout
  - authorization-decide
verify:
  smoke:
    - smoke.access-core.startup
  journey:
    - J-sso-login
    - J-session-refresh
noSplitReason:
  - session creation and identity verification share one transaction boundary
allowedDependencies:
  - config-core
servedRoles:
  - end-user
  - it-admin
stakeholders:
  - security
  - compliance
```

Required:

- `id`
- `type`
- `consistencyLevel`
- `owner`
- `journeys`
- `schema.primary`
- `contracts.produces`
- `contracts.consumes`
- `slices`
- `verify`

Recommended optional:

- `schema.tables.authoritative`
- `schema.tables.coordination`
- `noSplitReason`
- `allowedDependencies`
- `servedRoles`
- `stakeholders`

## `slice.yaml`

```yaml
id: session-login
belongsToCell: access-core
consistencyLevel: L2
owner:
  team: platform
  role: slice-owner
journeys:
  - J-sso-login
callsContracts:
  - http.config.get.v1
publishes:
  - event.session.created.v1
consumes: []
publicEntrypoints:
  - POST /api/v1/auth/login
  - POST /api/v1/auth/sso/callback
traceAttrs:
  required:
    - traceId
    - journeyId
    - callerCellId
    - calleeCellId
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
- `verify`
- `allowedFiles`

Recommended optional:

- `journeys`
- `publicEntrypoints`
- `traceAttrs`

## `contract.yaml`

Event example:

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
  delivery: at-least-once
outputs:
  payload: session creation fact
  headers: trace and correlation metadata
compatibilityPolicy:
  breaking:
    - remove_field
    - change_field_semantics
  nonBreaking:
    - add_optional_field
traceRequired: true
replayable: true
idempotencyKey: eventId
orderingSemantics: aggregateId+sequence
deliverySemantics: at-least-once
```

Required:

- `id`
- `kind`
- `version`
- `ownerCell`
- `producer`
- `consumers`
- `consistencyLevel`
- `status`
- `schemaRefs`
- `compatibilityPolicy`
- `traceRequired`
- `replayable`

Recommended optional:

- `summary`
- `semantics`
- `inputs`
- `outputs`
- `idempotencyKey`
- `orderingSemantics`
- `deliverySemantics`

Kind-specific `schemaRefs`:

- `http`: `request`, `response`
- `event`: `payload`, `headers`
- `command`: `request`, `ack`, `result`
- `projection`: `projection`

## `assembly.yaml`

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
flags:
  - feature.oidc.enabled
killSwitches:
  - kill.audit-consumer
generated:
  outputDir: assemblies/core-bundle/generated
```

Required:

- `id`
- `cells`
- `exportedContracts`
- `importedContracts`
- `smokeTargets`
- `generated`

Recommended optional:

- `flags`
- `killSwitches`

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
```

## Five-Layer Reminder

Metadata is only part of the overall model:

1. `journeys/catalog.yaml`
2. `journeys/*.yaml`
3. `cell.yaml`
4. `slice.yaml`
5. `journeys/status-board.yaml`

Stable governance facts live in layers 3 and 4.
Dynamic delivery state lives only in layer 5.
