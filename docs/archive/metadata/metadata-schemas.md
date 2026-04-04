# GoCell Metadata Schema Reference

All cells, slices, contracts, and assemblies are governed by metadata YAML files. These are first-class artifacts, not afterthoughts.

## cell.yaml

```yaml
# Required fields
cellId: access-core                    # Unique identifier
type: core                             # core | workflow
consistencyLevel: L2                   # L0-L4
owner: team-platform                   # Responsible team/person

# Owned slices
ownedSlices:
  - identity-manage
  - session-login
  - session-refresh
  - session-logout
  - authorization-decide

# Data sovereignty
authoritativeData:
  - users
  - roles
  - sessions
  - refresh_tokens

# Contracts
contracts:
  produces:
    - event.session.created.v1
    - event.session.revoked.v1
    - http.auth.login.v1
  consumes:
    - event.config.changed.v1

# Dependency whitelist (cells this cell is allowed to call)
allowedDependencies:
  - config-core

# Verification
verify:
  smoke:
    - smoke.access-core.startup
  journey:
    - J-sso-login
    - J-session-refresh

# Core cell only: why this cell must not be decomposed
noSplitReason: "Session creation and identity verification must share the same transaction boundary"

# Persona/stakeholder (optional)
servedRoles:
  - end-user
  - it-admin
stakeholders:
  - security
  - compliance
```

**Prohibited fields (dynamic status — goes to Status Board):** readiness, risk, blocker, done, status.

## slice.yaml

```yaml
# Required fields
sliceId: session-login                 # Unique within cell
belongsToCell: access-core             # Owning cell
consistencyLevel: L2                   # L0-L4
owner: team-platform                   # Responsible team/person

# Journey mapping
journeys:
  - J-sso-login

# Contract relationships
callsContracts:
  - http.config.get.v1
publishes:
  - event.session.created.v1
consumes: []

# Public entrypoints
publicEntrypoints:
  - POST /api/v1/auth/login
  - POST /api/v1/auth/sso/callback

# Trace attributes
traceAttrs:
  - session_id
  - user_id
  - oidc_provider

# Verification
verify:
  - unit
  - contract

# File scope
allowedFiles:
  - cells/access/slices/session-login/**
```

**Prohibited fields:** done, verified, nextAction, status.

## contract.yaml

```yaml
# Required fields
contractId: event.session.created.v1   # Unique identifier
kind: event                            # http | event | command | projection
version: 1                             # Semver major
ownerCell: access-core                 # Who defines this contract
consistencyLevel: L2                   # L0-L4

# Producer / Consumer
producer: access-core
consumers:
  - audit-core
  - config-core

# Status
status: stable                         # draft | stable | deprecated

# Schema references (YAML declares relationships, JSON Schema defines format)
schemaRefs:
  payload: contracts/events/session/created/v1/payload.schema.json

# Compatibility
compatibilityPolicy:
  breaking: major-version-bump         # New major version required
  nonBreaking: additive-only           # New optional fields allowed
  deprecationWindow: 4-weeks

# Delivery semantics
deliverySemantics: at-least-once
idempotencyKey: "{consumerGroup}:{eventId}"
orderingSemantics: per-aggregate
replayable: true

# Trace
traceRequired: true
```

### Contract directory structure

```
contracts/events/session/created/v1/
├── contract.yaml              # Relationship declaration
├── payload.schema.json        # Format definition (JSON Schema)
└── examples/
    └── basic.json             # Example payload

contracts/http/auth/login/v1/
├── contract.yaml
├── request.schema.json
├── response.schema.json
└── examples/
```

**Rule:** JSON Schema files belong to the contract version directory, NOT to the cell implementation directory.

## assembly.yaml

```yaml
# Required fields
assemblyId: core-bundle                # Unique identifier
cells:
  - access-core
  - audit-core
  - config-core

# Contract boundaries
exportedContracts:
  - http.auth.login.v1
  - http.auth.me.v1
  - http.config.get.v1
importedContracts: []

# Verification
smokeTargets:
  - smoke.access-core.startup
  - smoke.audit-core.startup
  - smoke.config-core.startup

# Feature flags and kill switches
flags:
  - feature.oidc.enabled
  - feature.audit.hash-chain.v2
killSwitches:
  - kill.audit-consumer
  - kill.config-publisher

# Generated output
generated:
  entrypoint: cmd/core-bundle/main.go
  manifest: deploy/core-bundle/manifest.yaml
```

## Journey spec (journeys/*.yaml)

```yaml
id: J-sso-login
goal: user completes SSO login and receives valid session
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

## Journey Catalog (journeys/catalog.yaml)

```yaml
- id: J-sso-login
  goal: user completes SSO login and receives valid session
  owner: tech-lead
  cells: [access-core]
  spec: journeys/J-sso-login.yaml

- id: J-audit-login-trail
  goal: login event captured in tamper-evident audit trail
  owner: tech-lead
  cells: [access-core, audit-core]
  spec: journeys/J-audit-login-trail.yaml
```

## Status Board (journeys/status-board.yaml)

```yaml
- journeyId: J-sso-login
  state: todo                          # todo | doing | blocked | ready
  risk: low                            # low | medium | high
  blocker: ""
  evidenceRefs: []

- journeyId: J-audit-login-trail
  state: todo
  risk: low
  blocker: ""
  evidenceRefs: []
```
