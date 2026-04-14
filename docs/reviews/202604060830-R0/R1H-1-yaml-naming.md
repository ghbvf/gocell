# R1H-1: YAML Metadata Naming Compliance Review

- **Auditor**: Kernel Guardian
- **Date**: 2026-04-06
- **Baseline**: `docs/architecture/naming-baseline.md`
- **Scope**: All YAML metadata files under 项目根目录

## Files Audited

| Category | Count | Files |
|----------|-------|-------|
| cell.yaml | 6 | access-core, audit-core, config-core, demo, device-cell, order-cell |
| slice.yaml | 21 | (all slices across 6 cells) |
| contract.yaml | 18 | (all contracts under contracts/) |
| assembly.yaml | 1 | core-bundle |
| journey YAML | 9 | J-sso-login, J-session-refresh, J-session-logout, J-user-onboarding, J-account-lockout, J-audit-login-trail, J-config-hot-reload, J-config-rollback, J-order-create |
| status-board.yaml | 1 | journeys/status-board.yaml |
| actors.yaml | 1 | actors.yaml |
| generated/boundary.yaml | 0 | Does not exist (OK -- no multi-assembly scenario yet) |
| **Total** | **57** | |

---

## Check 1: Prohibited Old Field Names (11 fields)

Searched all 57 YAML files for: `cellId`, `sliceId`, `contractId`, `assemblyId`, `ownedSlices`, `authoritativeData`, `producer`, `consumers`, `callsContracts`, `publishes`, `consumes`.

**Result: PASS -- zero occurrences found.**

---

## Check 2: Entity ID Format

### Cell IDs (expect kebab-case)

| File | ID Value | Verdict |
|------|----------|---------|
| cells/access-core/cell.yaml | `access-core` | PASS |
| cells/audit-core/cell.yaml | `audit-core` | PASS |
| cells/config-core/cell.yaml | `config-core` | PASS |
| cells/demo/cell.yaml | `demo` | PASS |
| cells/device-cell/cell.yaml | `device-cell` | PASS |
| cells/order-cell/cell.yaml | `order-cell` | PASS |

### Slice IDs (expect kebab-case)

All 21 slice IDs verified -- all kebab-case. No violations.

<details><summary>Full slice ID list</summary>

session-login, rbac-check, session-validate, session-logout, authorization-decide, identity-manage, session-refresh, audit-query, audit-verify, audit-archive, audit-append, config-read, config-write, config-publish, feature-flag, config-subscribe, device-command, device-register, device-status, order-create, order-query

</details>

### Assembly ID (expect kebab-case)

| File | ID Value | Verdict |
|------|----------|---------|
| assemblies/core-bundle/assembly.yaml | `core-bundle` | PASS |

### Contract IDs (expect lowercase dot-separated)

All 18 contract IDs verified. No violations.

<details><summary>Full contract ID list</summary>

- `http.auth.login.v1`
- `http.config.get.v1`
- `event.session.created.v1`
- `event.audit.appended.v1`
- `event.session.revoked.v1`
- `event.user.created.v1`
- `event.user.locked.v1`
- `event.audit.integrity-verified.v1`
- `event.config.rollback.v1`
- `http.auth.me.v1`
- `http.config.flags.v1`
- `event.config.changed.v1`
- `http.auth.refresh.v1`
- `command.device-command.v1`
- `event.device-registered.v1`
- `event.order-created.v1`
- `http.device.v1`
- `http.order.v1`

</details>

### Journey IDs (expect J-{kebab-case})

All 9 journey IDs verified -- all match `J-{kebab-case}`. No violations.

### Actor IDs (expect kebab-case)

| File | ID Value | Verdict |
|------|----------|---------|
| actors.yaml | `edge-bff` | PASS |

**Result: PASS -- all entity IDs conform to naming-baseline.md section 1.1.**

---

## Check 3: YAML Field Names (camelCase)

Searched all 57 YAML files for snake_case multi-word field names (`[a-z]+_[a-z]+:` as a YAML key).

**Result: PASS -- zero snake_case field names found.**

Spot-check of camelCase fields across files:
- `belongsToCell` -- used in all 21 slice.yaml files, correct
- `contractUsages` -- used in all 21 slice.yaml files, correct
- `consistencyLevel` -- used in all 6 cell.yaml + all 18 contract.yaml, correct
- `journeyId` -- used in status-board.yaml, correct
- `updatedAt` -- used in status-board.yaml, correct
- `ownerCell` -- used in all contract.yaml, correct
- `deliverySemantics` -- used in event contracts, correct
- `idempotencyKey` -- used in event contracts, correct
- `schemaRefs` -- used in HTTP contracts, correct
- `l0Dependencies` -- used in core cell.yaml files, correct
- `passCriteria` -- used in journey YAML, correct
- `checkRef` -- used in journey YAML, correct
- `expiresAt` -- used in waiver, correct
- `maxConsistencyLevel` -- used in actors.yaml, correct
- `deployTemplate` -- used in assembly.yaml, correct

---

## Check 4: Contract Directory Structure

Expected: `contracts/{kind}/{domain-path}/{version}/contract.yaml`

| Contract ID | Directory Path | Match | Verdict |
|-------------|---------------|-------|---------|
| http.auth.login.v1 | http/auth/login/v1/ | YES | PASS |
| http.config.get.v1 | http/config/get/v1/ | YES | PASS |
| event.session.created.v1 | event/session/created/v1/ | YES | PASS |
| event.audit.appended.v1 | event/audit/appended/v1/ | YES | PASS |
| event.session.revoked.v1 | event/session/revoked/v1/ | YES | PASS |
| event.user.created.v1 | event/user/created/v1/ | YES | PASS |
| event.user.locked.v1 | event/user/locked/v1/ | YES | PASS |
| event.audit.integrity-verified.v1 | event/audit/integrity-verified/v1/ | YES | PASS |
| event.config.rollback.v1 | event/config/rollback/v1/ | YES | PASS |
| http.auth.me.v1 | http/auth/me/v1/ | YES | PASS |
| http.config.flags.v1 | http/config/flags/v1/ | YES | PASS |
| event.config.changed.v1 | event/config/changed/v1/ | YES | PASS |
| http.auth.refresh.v1 | http/auth/refresh/v1/ | YES | PASS |
| command.device-command.v1 | command/device-command/v1/ | YES | PASS |
| event.device-registered.v1 | event/device-registered/v1/ | YES | PASS |
| event.order-created.v1 | event/order-created/v1/ | YES | PASS |
| http.device.v1 | http/device/v1/ | YES | PASS |
| http.order.v1 | http/order/v1/ | YES | PASS |

**Result: PASS -- all 18 contracts have correct directory structure matching their IDs.**

### SchemaRef File Existence (HTTP contracts only)

Six HTTP contracts declare `schemaRefs` with `request.schema.json` and `response.schema.json`. All 12 files verified to exist on disk:
- http/auth/login/v1/ -- request.schema.json, response.schema.json
- http/config/get/v1/ -- request.schema.json, response.schema.json
- http/auth/me/v1/ -- request.schema.json, response.schema.json
- http/config/flags/v1/ -- request.schema.json, response.schema.json
- http/auth/refresh/v1/ -- request.schema.json, response.schema.json

Note: `http.device.v1` and `http.order.v1` do not declare `schemaRefs`, so no files to check.

---

## Check 5: generated/boundary.yaml

File does not exist. Per naming-baseline.md section 1.5, when it is eventually generated, it must not contain the `assemblyId` field.

**Result: N/A -- file not yet generated.**

---

## Violations Summary

| # | File:Line | Violation Type | Severity | Suggested Fix |
|---|-----------|---------------|----------|---------------|
| (none) | -- | -- | -- | -- |

**Total violations: 0**

---

## Observations (non-blocking, for future consideration)

1. **device-cell and order-cell cell.yaml lack `l0Dependencies`**: The three core cells (access-core, audit-core, config-core) all declare `l0Dependencies: []`, but device-cell and order-cell omit this field entirely. While not a naming violation, this is a metadata consistency gap.

2. **status-board.yaml missing J-order-create**: The status-board lists 8 journeys but `J-order-create` is not tracked. This is not a naming issue but a metadata completeness gap.

3. **event.config.rollback.v1 lists access-core as subscriber**: No access-core slice declares a `contractUsage` for `event.config.rollback.v1` as a subscriber. This is a referential integrity concern, not a naming issue, and belongs in a separate audit (R1H-2 or similar).

---

## Conclusion

All 57 YAML metadata files in the repository pass the naming compliance checks defined in `docs/architecture/naming-baseline.md`. No prohibited field names, no snake_case YAML keys, no malformed entity IDs, and all contract directories match their IDs correctly.
