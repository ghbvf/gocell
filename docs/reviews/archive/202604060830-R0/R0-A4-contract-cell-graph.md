# R0-A4: Contract/Cell Dependency Graph and Declaration Integrity

**Agent**: R0-A4 (Kernel Guardian)
**Date**: 2026-04-06
**Scope**: All `cell.yaml`, `slice.yaml`, `contract.yaml`, `assembly.yaml`, journey YAML in `src/`

---

## Cell Inventory

| Cell ID | Type | ConsistencyLevel | Owner (team/role) | Slices Count | Required Fields Complete |
|---------|------|------------------|--------------------|--------------|------------------------|
| access-core | core | L2 | platform / cell-owner | 7 | YES |
| audit-core | core | L2 | platform / cell-owner | 4 | YES |
| config-core | core | L2 | platform / cell-owner | 5 | YES |
| demo | core | L1 | demo / cell-owner | 0 | YES |
| device-cell | edge | L4 | examples / device-owner | 3 | YES |
| order-cell | core | L2 | examples / order-owner | 2 | YES |

**Required fields for cell.yaml**: `id`, `type`, `consistencyLevel`, `owner{team,role}`, `schema.primary`, `verify.smoke`

All 6 cells pass required field checks. No missing fields detected.

---

## Slice Inventory

| Slice ID | BelongsToCell | ContractUsages Count | Required Fields Complete | Notes |
|----------|---------------|---------------------|------------------------|-------|
| session-login | access-core | 3 | YES | waiver for http.config.get.v1 (expires 2026-06-01) |
| rbac-check | access-core | 0 | YES | internal-only slice |
| session-validate | access-core | 0 | YES | internal-only slice |
| session-logout | access-core | 1 | YES | |
| authorization-decide | access-core | 0 | YES | internal-only slice |
| identity-manage | access-core | 3 | YES | |
| session-refresh | access-core | 1 | YES | |
| audit-query | audit-core | 0 | YES | internal-only slice |
| audit-verify | audit-core | 1 | YES | |
| audit-archive | audit-core | 0 | YES | internal-only slice |
| audit-append | audit-core | 7 | YES | major event aggregator |
| config-read | config-core | 1 | YES | |
| config-write | config-core | 1 | YES | |
| config-publish | config-core | 2 | YES | |
| feature-flag | config-core | 1 | YES | |
| config-subscribe | config-core | 1 | YES | |
| device-command | device-cell | 2 | YES | |
| device-register | device-cell | 2 | YES | |
| device-status | device-cell | 1 | YES | |
| order-create | order-cell | 2 | YES | |
| order-query | order-cell | 1 | YES | |

**Required fields for slice.yaml**: `id`, `belongsToCell`, `contractUsages`, `verify.unit`, `verify.contract`

All 21 slices pass required field checks.

---

## Contract Inventory

| Contract ID | Kind | OwnerCell | Version | ConsistencyLevel | Lifecycle | Usage Count | Has SchemaRefs Files |
|-------------|------|-----------|---------|------------------|-----------|-------------|---------------------|
| http.auth.login.v1 | http | access-core | v1 | L1 | active | 1 (serve) | YES |
| http.auth.me.v1 | http | access-core | v1 | L1 | active | 1 (serve) | YES |
| http.auth.refresh.v1 | http | access-core | v1 | L1 | active | 1 (serve) | YES |
| http.config.get.v1 | http | config-core | v1 | L1 | active | 2 (serve + call) | YES |
| http.config.flags.v1 | http | config-core | v1 | L1 | active | 1 (serve) | YES |
| event.session.created.v1 | event | access-core | v1 | L2 | active | 2 (publish + subscribe) | YES |
| event.session.revoked.v1 | event | access-core | v1 | L2 | active | 2 (publish + subscribe) | NO (contract-only) |
| event.audit.appended.v1 | event | audit-core | v1 | L2 | active | 1 (publish) | YES |
| event.audit.integrity-verified.v1 | event | audit-core | v1 | L2 | active | 1 (publish) | NO (contract-only) |
| event.user.created.v1 | event | access-core | v1 | L2 | active | 2 (publish + subscribe) | NO (contract-only) |
| event.user.locked.v1 | event | access-core | v1 | L2 | active | 2 (publish + subscribe) | NO (contract-only) |
| event.config.changed.v1 | event | config-core | v1 | L2 | active | 3 (2x publish + subscribe) | YES |
| event.config.rollback.v1 | event | config-core | v1 | L2 | active | 2 (publish + subscribe) | NO (contract-only) |
| http.device.v1 | http | device-cell | v1 | L4 | active | 3 (serve x3) | NO (contract-only) |
| http.order.v1 | http | order-cell | v1 | L2 | active | 2 (serve x2) | NO (contract-only) |
| command.device-command.v1 | command | device-cell | v1 | L4 | active | 1 (handle) | NO (contract-only) |
| event.device-registered.v1 | event | device-cell | v1 | L4 | active | 1 (publish) | NO (contract-only) |
| event.order-created.v1 | event | order-cell | v1 | L2 | active | 1 (publish) | NO (contract-only) |

**Total**: 18 contracts defined.

---

## Cell-to-Contract Dependency Graph

```
access-core
  +-- http.auth.login.v1            (serve)     [session-login]
  +-- http.auth.me.v1               (serve)     [identity-manage]
  +-- http.auth.refresh.v1          (serve)     [session-refresh]
  +-- http.config.get.v1            (call)      [session-login] (waiver: expires 2026-06-01)
  +-- event.session.created.v1      (publish)   [session-login]
  +-- event.session.revoked.v1      (publish)   [session-logout]
  +-- event.user.created.v1         (publish)   [identity-manage]
  +-- event.user.locked.v1          (publish)   [identity-manage]

audit-core
  +-- event.audit.appended.v1       (publish)   [audit-append]
  +-- event.audit.integrity-verified.v1 (publish) [audit-verify]
  +-- event.session.created.v1      (subscribe) [audit-append]
  +-- event.session.revoked.v1      (subscribe) [audit-append]
  +-- event.user.created.v1         (subscribe) [audit-append]
  +-- event.user.locked.v1          (subscribe) [audit-append]
  +-- event.config.changed.v1       (subscribe) [audit-append]
  +-- event.config.rollback.v1      (subscribe) [audit-append]

config-core
  +-- http.config.get.v1            (serve)     [config-read]
  +-- http.config.flags.v1          (serve)     [feature-flag]
  +-- event.config.changed.v1       (publish)   [config-write, config-publish]
  +-- event.config.changed.v1       (subscribe) [config-subscribe]
  +-- event.config.rollback.v1      (publish)   [config-publish]

demo
  (no contract usages -- empty cell, no slices)

device-cell
  +-- http.device.v1                (serve)     [device-command, device-register, device-status]
  +-- command.device-command.v1      (handle)    [device-command]
  +-- event.device-registered.v1     (publish)   [device-register]

order-cell
  +-- http.order.v1                 (serve)     [order-create, order-query]
  +-- event.order-created.v1        (publish)   [order-create]
```

### Cross-Cell Communication Flows

```
access-core --[event.session.created.v1]--> audit-core
access-core --[event.session.revoked.v1]--> audit-core
access-core --[event.user.created.v1]----> audit-core
access-core --[event.user.locked.v1]-----> audit-core
access-core --[http.config.get.v1]-------> config-core  (call -> serve)
config-core --[event.config.changed.v1]--> audit-core   (publish -> subscribe)
config-core --[event.config.rollback.v1]-> audit-core   (publish -> subscribe)
```

---

## Integrity Checks

### 1. Required Field Compliance

| File | Missing Fields | Severity |
|------|---------------|----------|
| (none) | -- | -- |

All cell.yaml files contain: `id`, `type`, `consistencyLevel`, `owner.team`, `owner.role`, `schema.primary`, `verify.smoke`.
All slice.yaml files contain: `id`, `belongsToCell`, `contractUsages`, `verify.unit`, `verify.contract`.

**Result: PASS** -- All required fields present across all 6 cells and 21 slices.

### 2. Forbidden Old Field Names

| File | Found Old Field | Severity |
|------|----------------|----------|
| (none) | -- | -- |

Searched all YAML files under `src/` for: `cellId`, `sliceId`, `contractId`, `assemblyId`, `ownedSlices`, `authoritativeData`, `producer`, `consumers`, `callsContracts`, `publishes`, `consumes`.

**Result: PASS** -- No forbidden old field names found.

### 3. Dangling References (slice.contractUsages -> contract definition)

| Slice | Referenced Contract | Status |
|-------|-------------------|--------|
| (none) | -- | -- |

All 18 contracts referenced by slices exist in `src/contracts/`. Every `contractUsages[].contract` dotted-ID maps to a defined `contract.yaml`.

**Result: PASS** -- No dangling references.

### 4. Orphan Contracts (defined but never used by any slice)

| Contract | Reason |
|----------|--------|
| (none) | -- |

All 18 defined contracts are referenced by at least one slice's `contractUsages`.

**Result: PASS** -- No orphan contracts.

### 5. Role Legitimacy (contractUsages.role matches kind-legal roles)

Legal roles per kind:
- `http`: serve, call
- `event`: publish, subscribe
- `command`: handle, invoke
- `projection`: provide, read

| Slice | Contract | Role | Kind | Valid |
|-------|----------|------|------|-------|
| session-login | http.auth.login.v1 | serve | http | YES |
| session-login | event.session.created.v1 | publish | event | YES |
| session-login | http.config.get.v1 | call | http | YES |
| session-logout | event.session.revoked.v1 | publish | event | YES |
| identity-manage | event.user.created.v1 | publish | event | YES |
| identity-manage | event.user.locked.v1 | publish | event | YES |
| identity-manage | http.auth.me.v1 | serve | http | YES |
| session-refresh | http.auth.refresh.v1 | serve | http | YES |
| audit-verify | event.audit.integrity-verified.v1 | publish | event | YES |
| audit-append | event.audit.appended.v1 | publish | event | YES |
| audit-append | event.session.created.v1 | subscribe | event | YES |
| audit-append | event.session.revoked.v1 | subscribe | event | YES |
| audit-append | event.user.created.v1 | subscribe | event | YES |
| audit-append | event.user.locked.v1 | subscribe | event | YES |
| audit-append | event.config.changed.v1 | subscribe | event | YES |
| audit-append | event.config.rollback.v1 | subscribe | event | YES |
| config-read | http.config.get.v1 | serve | http | YES |
| config-write | event.config.changed.v1 | publish | event | YES |
| config-publish | event.config.changed.v1 | publish | event | YES |
| config-publish | event.config.rollback.v1 | publish | event | YES |
| feature-flag | http.config.flags.v1 | serve | http | YES |
| config-subscribe | event.config.changed.v1 | subscribe | event | YES |
| device-command | http.device.v1 | serve | http | YES |
| device-command | command.device-command.v1 | handle | command | YES |
| device-register | http.device.v1 | serve | http | YES |
| device-register | event.device-registered.v1 | publish | event | YES |
| device-status | http.device.v1 | serve | http | YES |
| order-create | http.order.v1 | serve | http | YES |
| order-create | event.order-created.v1 | publish | event | YES |
| order-query | http.order.v1 | serve | http | YES |

**Result: PASS** -- All 30 contract usage roles are legitimate for their respective contract kinds.

### 6. Verify Closure (every contractUsage has verify.contract or waiver)

| Slice | Contract | Has verify.contract | Has Waiver | Status |
|-------|----------|-------------------|------------|--------|
| session-login | http.auth.login.v1 | YES | -- | OK |
| session-login | event.session.created.v1 | YES | -- | OK |
| session-login | http.config.get.v1 | NO | YES (expires 2026-06-01) | OK (waiver valid) |
| session-logout | event.session.revoked.v1 | YES | -- | OK |
| identity-manage | event.user.created.v1 | YES | -- | OK |
| identity-manage | event.user.locked.v1 | YES | -- | OK |
| identity-manage | http.auth.me.v1 | YES | -- | OK |
| session-refresh | http.auth.refresh.v1 | YES | -- | OK |
| audit-verify | event.audit.integrity-verified.v1 | YES | -- | OK |
| audit-append | event.audit.appended.v1 | YES | -- | OK |
| audit-append | event.session.created.v1 | YES | -- | OK |
| audit-append | event.session.revoked.v1 | YES | -- | OK |
| audit-append | event.user.created.v1 | YES | -- | OK |
| audit-append | event.user.locked.v1 | YES | -- | OK |
| audit-append | event.config.changed.v1 | YES | -- | OK |
| audit-append | event.config.rollback.v1 | YES | -- | OK |
| config-read | http.config.get.v1 | YES | -- | OK |
| config-write | event.config.changed.v1 | YES | -- | OK |
| config-publish | event.config.changed.v1 | YES | -- | OK |
| config-publish | event.config.rollback.v1 | YES | -- | OK |
| feature-flag | http.config.flags.v1 | YES | -- | OK |
| config-subscribe | event.config.changed.v1 | YES | -- | OK |
| device-command | http.device.v1 | YES | -- | OK |
| device-command | command.device-command.v1 | YES | -- | OK |
| device-register | http.device.v1 | YES | -- | OK |
| device-register | event.device-registered.v1 | YES | -- | OK |
| device-status | http.device.v1 | YES | -- | OK |
| order-create | http.order.v1 | YES | -- | OK |
| order-create | event.order-created.v1 | YES | -- | OK |
| order-query | http.order.v1 | YES | -- | OK |

**Result: PASS** -- All contract usages are covered by either verify.contract entries or valid waivers.

### 7. Contract Endpoint Declarations vs. Slice UsageS Cross-Check (FINDINGS)

This checks whether a contract's `endpoints` declarations (server/clients, publisher/subscribers) are consistent with actual slice-level `contractUsages`.

| Contract | Endpoint Declaration | Expected Slice Usage | Actual Slice Usage | Status |
|----------|---------------------|---------------------|-------------------|--------|
| event.config.changed.v1 | subscribers: [access-core, audit-core, config-core] | access-core should have a subscribe slice | audit-core (audit-append), config-core (config-subscribe) only | **MISMATCH** |
| event.config.rollback.v1 | subscribers: [access-core, audit-core] | access-core should have a subscribe slice | audit-core (audit-append) only | **MISMATCH** |
| http.config.flags.v1 | clients: [access-core, edge-bff] | access-core should have a call slice | config-core (feature-flag serve) only | **MISMATCH** |

**Severity: WARN (P2)** -- The contract.yaml `endpoints` fields declare `access-core` as a subscriber/client, but no slice in `access-core` has a corresponding `contractUsages` entry with `subscribe`/`call` role. This means either:
- (a) The contract.yaml endpoint declarations are stale and should remove `access-core`, OR
- (b) A slice in `access-core` is missing the `contractUsages` declaration.

### 8. SchemaRefs File Existence

Contracts that declare `schemaRefs` must have the referenced files present in the same directory.

| Contract | Declared SchemaRefs | Files Present | Status |
|----------|-------------------|---------------|--------|
| http.auth.login.v1 | request.schema.json, response.schema.json | YES, YES | OK |
| http.auth.me.v1 | request.schema.json, response.schema.json | YES, YES | OK |
| http.auth.refresh.v1 | request.schema.json, response.schema.json | YES, YES | OK |
| http.config.get.v1 | request.schema.json, response.schema.json | YES, YES | OK |
| http.config.flags.v1 | request.schema.json, response.schema.json | YES, YES | OK |

Contracts without `schemaRefs` declarations: 13 contracts (all event, command contracts). This is acceptable as event/command contracts may use different schema conventions (payload.schema.json). Three event contracts (event.session.created.v1, event.audit.appended.v1, event.config.changed.v1) have payload.schema.json and headers.schema.json files present but not declared in schemaRefs -- these appear to be convention-based rather than declared.

**Result: PASS** -- All declared schemaRefs files exist.

### 9. Missing Schema Files for Event Contracts

While not strictly a schemaRefs declaration violation, the following event contracts lack any schema files:

| Contract | Schema Files Present |
|----------|---------------------|
| event.session.revoked.v1 | NONE |
| event.user.created.v1 | NONE |
| event.user.locked.v1 | NONE |
| event.audit.integrity-verified.v1 | NONE |
| event.config.rollback.v1 | NONE |
| event.device-registered.v1 | NONE |
| event.order-created.v1 | NONE |
| command.device-command.v1 | NONE |
| http.device.v1 | NONE |
| http.order.v1 | NONE |

**Severity: INFO (P3)** -- 10 of 18 contracts have no schema files. For event contracts this may be acceptable in draft/early stages, but HTTP contracts (http.device.v1, http.order.v1) should have request/response schemas for contract testing.

---

## Assembly and Journey Analysis

### Assembly: core-bundle

**File**: `src/assemblies/core-bundle/assembly.yaml`

| Field | Value |
|-------|-------|
| id | core-bundle |
| cells | access-core, audit-core, config-core |
| build.entrypoint | src/cmd/core-bundle/main.go |
| build.binary | core-bundle |
| build.deployTemplate | k8s |

**Observations**:
- Assembly covers the 3 core cells. `demo`, `device-cell`, and `order-cell` are NOT in any assembly.
- No `boundary.yaml` exists. Per GoCell rules, multi-cell assemblies should produce a `boundary.yaml`. This is **missing**.

**Severity: WARN (P2)** -- `boundary.yaml` is absent for the core-bundle assembly which contains 3 cells.

### Journeys

| Journey ID | Goal | Cells Referenced | Contracts Referenced | Status (board) |
|------------|------|-----------------|---------------------|---------------|
| J-sso-login | SSO login + valid session | access-core, audit-core, config-core | http.auth.login.v1, event.session.created.v1 | doing |
| J-session-logout | Logout + revoke session + audit | access-core, audit-core | event.session.revoked.v1 | todo |
| J-user-onboarding | New user creation + login ability | access-core | event.user.created.v1 | todo |
| J-account-lockout | Auto-lockout + admin unlock | access-core | event.user.locked.v1 | todo |
| J-audit-login-trail | Cross-cell audit hash chain | access-core, audit-core | event.session.created.v1, event.audit.integrity-verified.v1 | todo |
| J-config-hot-reload | Config change hot reload | config-core, access-core, audit-core | event.config.changed.v1 | todo |
| J-config-rollback | Config rollback + audit | config-core, access-core, audit-core | event.config.rollback.v1 | todo |
| J-session-refresh | Refresh token flow | access-core | http.auth.refresh.v1 | todo |
| J-order-create | Create order + event publish | order-cell | http.order.v1, event.order-created.v1 | NOT on board |

**Journey Coverage of Contracts** (16 of 18 covered):

| Contract | Covered by Journey |
|----------|--------------------|
| http.auth.login.v1 | J-sso-login |
| http.auth.me.v1 | (not covered) |
| http.auth.refresh.v1 | J-session-refresh |
| http.config.get.v1 | (not covered -- but used via waiver in session-login) |
| http.config.flags.v1 | (not covered) |
| event.session.created.v1 | J-sso-login, J-audit-login-trail |
| event.session.revoked.v1 | J-session-logout |
| event.audit.appended.v1 | (not covered) |
| event.audit.integrity-verified.v1 | J-audit-login-trail |
| event.user.created.v1 | J-user-onboarding |
| event.user.locked.v1 | J-account-lockout |
| event.config.changed.v1 | J-config-hot-reload |
| event.config.rollback.v1 | J-config-rollback |
| http.device.v1 | (not covered) |
| http.order.v1 | J-order-create |
| command.device-command.v1 | (not covered) |
| event.device-registered.v1 | (not covered) |
| event.order-created.v1 | J-order-create |

**Contracts without journey coverage** (7):
1. `http.auth.me.v1` -- no journey tests the /me endpoint
2. `http.config.get.v1` -- no journey (partially tested via J-sso-login waiver)
3. `http.config.flags.v1` -- no journey for feature flags
4. `event.audit.appended.v1` -- no journey verifies audit event publication
5. `http.device.v1` -- no journey for device HTTP API
6. `command.device-command.v1` -- no journey for device command flow
7. `event.device-registered.v1` -- no journey for device registration event

### Status Board Completeness

**FINDING**: `J-order-create` is defined in `src/journeys/J-order-create.yaml` but is **not listed** in `src/journeys/status-board.yaml`.

**Severity: WARN (P2)** -- status-board.yaml is incomplete.

---

## External Actors

**File**: `src/actors.yaml`

| Actor ID | Type | MaxConsistencyLevel |
|----------|------|-------------------|
| edge-bff | external | L1 |

**Usage in contracts**:
- `http.auth.login.v1` -- clients: [edge-bff]
- `http.auth.me.v1` -- clients: [edge-bff]
- `http.config.flags.v1` -- clients: [access-core, edge-bff]

**Validation**: `edge-bff` is correctly registered as external actor. It only participates as a client (L1), which is within its declared maxConsistencyLevel. Contract ownerCells are all real Cells, not external actors.

**Result: PASS** -- Actor registration is correct.

---

## Summary of Findings

### Critical (P0): None

### High (P1): None

### Medium (P2): 4 findings

| # | Finding | Location | Recommendation |
|---|---------|----------|---------------|
| P2-1 | Contract `event.config.changed.v1` declares `access-core` as subscriber but no access-core slice subscribes | `src/contracts/event/config/changed/v1/contract.yaml` | Either add a subscribing slice to access-core or remove access-core from subscribers list |
| P2-2 | Contract `event.config.rollback.v1` declares `access-core` as subscriber but no access-core slice subscribes | `src/contracts/event/config/rollback/v1/contract.yaml` | Same as P2-1 |
| P2-3 | Contract `http.config.flags.v1` declares `access-core` as client but no access-core slice calls it | `src/contracts/http/config/flags/v1/contract.yaml` | Either add a calling slice or remove access-core from clients |
| P2-4 | `J-order-create` journey missing from `status-board.yaml` | `src/journeys/status-board.yaml` | Add J-order-create entry to status-board.yaml |

### Low (P3): 3 findings

| # | Finding | Location | Recommendation |
|---|---------|----------|---------------|
| P3-1 | `boundary.yaml` missing for core-bundle assembly (3 cells) | `src/assemblies/core-bundle/` | Generate boundary.yaml via tooling |
| P3-2 | 10 of 18 contracts lack schema files (includes 2 HTTP contracts) | Various contract directories | Prioritize http.device.v1 and http.order.v1 schema creation |
| P3-3 | 7 contracts have no journey coverage | See journey coverage table above | Create device-cell journey(s); add http.auth.me.v1 to an existing journey |

### Informational

| # | Finding | Notes |
|---|---------|-------|
| I-1 | `demo` cell has no slices and no contract usages | Appears to be a scaffold/example placeholder |
| I-2 | `device-cell` and `order-cell` are not in any assembly | These appear to be example cells; may need their own assembly |
| I-3 | Waiver for http.config.get.v1 in session-login expires 2026-06-01 | 56 days remaining; plan contract test before expiry |

---

## Statistics

| Metric | Count |
|--------|-------|
| Total Cells | 6 |
| Total Slices | 21 |
| Total Contracts | 18 |
| Total Assemblies | 1 |
| Total Journeys | 9 |
| Total External Actors | 1 |
| Contract Usages (across all slices) | 30 |
| Cross-Cell Communication Flows | 7 |
| Endpoint Declaration Mismatches | 3 |
| Forbidden Old Field Violations | 0 |
| Dangling References | 0 |
| Orphan Contracts | 0 |
| Role Legitimacy Violations | 0 |
