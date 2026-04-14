# R1H-3: Go Naming Convention Review (adapters/ + cells/ + examples/ + cmd/)

Reviewer: R1H-3 Agent (Seat 5 DX/Maintainability focus)
Scope: `adapters/**/*.go`, `cells/**/*.go`, `examples/**/*.go`, `cmd/**/*.go` (non-test)
Baseline: `docs/architecture/naming-baseline.md`
Date: 2026-04-06

---

## 1. Go Abbreviation Violations (Id -> ID)

### 1.1 URL Path Parameter `{cmdId}` (should be `{cmdID}`)

This is a chi URL path parameter. While chi does not enforce casing, project consistency requires `ID` form.

| # | File | Line | Current | Should Be |
|---|------|------|---------|-----------|
| 1 | `cells/device-cell/cell.go` | 129 | `{cmdId}` | `{cmdID}` |
| 2 | `cells/device-cell/slices/device-command/handler.go` | 70 | `{cmdId}` (comment) | `{cmdID}` |
| 3 | `cells/device-cell/slices/device-command/handler.go` | 73 | `chi.URLParam(r, "cmdId")` | `chi.URLParam(r, "cmdID")` |

Severity: P2 (URL path params are external API surface but chi params are internal routing keys)

Note: If `cmdId` is changed in the route pattern, all `chi.URLParam(r, "cmdId")` calls and tests must be updated together.

### 1.2 JSON Response Map Key `"deviceId"` (should be `"deviceID"`)

Per naming-baseline 2.1, JSON fields should be camelCase. However, the Go abbreviation rule (`ID` not `Id`) applies to Go identifiers, not JSON field names. The JSON convention is `"deviceId"` (camelCase) which is actually the standard JSON convention -- `"deviceID"` would be unusual in JSON.

**Decision: NOT a violation.** JSON field `"deviceId"` follows standard JSON camelCase convention. The Go struct field `DeviceID` correctly uses `ID`, and the JSON tag maps it to `"deviceId"` which is correct. No action needed.

Affected locations (all COMPLIANT):
- `cells/device-cell/internal/domain/device.go:23` -- `DeviceID string \`json:"deviceId"\``
- `cells/device-cell/slices/device-command/handler.go:47` -- `"deviceId": cmd.DeviceID`

---

## 2. JSON Tag snake_case Violations

### 2.1 OIDC Adapter -- External Protocol (EXEMPT)

The following use snake_case in JSON tags because they map to the OIDC/OAuth2 protocol specification (RFC 6749, OpenID Connect Discovery 1.0). These are NOT violations.

| File | Line | Tag | Reason |
|------|------|-----|--------|
| `adapters/oidc/token.go` | 18 | `json:"access_token"` | OAuth2 RFC 6749 |
| `adapters/oidc/token.go` | 19 | `json:"token_type"` | OAuth2 RFC 6749 |
| `adapters/oidc/token.go` | 20 | `json:"expires_in"` | OAuth2 RFC 6749 |
| `adapters/oidc/token.go` | 21 | `json:"refresh_token"` | OAuth2 RFC 6749 |
| `adapters/oidc/token.go` | 22 | `json:"id_token"` | OIDC Core |
| `adapters/oidc/provider.go` | 20 | `json:"authorization_endpoint"` | OIDC Discovery |
| `adapters/oidc/provider.go` | 21 | `json:"token_endpoint"` | OIDC Discovery |
| `adapters/oidc/provider.go` | 22 | `json:"userinfo_endpoint"` | OIDC Discovery |
| `adapters/oidc/provider.go` | 23 | `json:"jwks_uri"` | OIDC Discovery |
| `adapters/oidc/provider.go` | 24 | `json:"scopes_supported"` | OIDC Discovery |
| `adapters/oidc/provider.go` | 25 | `json:"id_token_signing_alg_values_supported"` | OIDC Discovery |
| `adapters/oidc/userinfo.go` | 19 | `json:"email_verified"` | OIDC UserInfo |

**Status: EXEMPT** -- External protocol fields, naming-baseline 2.1 allows external protocol overrides.

### 2.2 Audit Append -- Internal Payload Struct

| # | File | Line | Current | Should Be |
|---|------|------|---------|-----------|
| 1 | `cells/audit-core/slices/auditappend/service.go` | 93 | `json:"user_id"` | `json:"userId"` |

Severity: P2 (this struct unmarshals event payloads; the JSON field name is part of the internal event schema)

Note: This struct reads from event payloads produced by `identitymanage/service.go:91` (`"user_id"`) and `sessionlogin/service.go:153` (`"user_id"`). All producers and consumers must change together if this is fixed.

---

## 3. Event Payload JSON Keys (snake_case)

Event payloads use `map[string]any` with snake_case keys. Per naming-baseline 2.1, JSON fields should prefer camelCase. However, event payloads are internal protocol between producers and consumers, not external API. This is a borderline area.

**Listing for completeness (all P2 -- suggestion, not blocking):**

| # | File | Line | Key(s) |
|---|------|------|--------|
| 1 | `cells/access-core/slices/identitymanage/service.go` | 91 | `"user_id"`, `"username"` |
| 2 | `cells/access-core/slices/identitymanage/service.go` | 192 | `"user_id"` |
| 3 | `cells/access-core/slices/sessionlogin/service.go` | 153 | `"session_id"`, `"user_id"` |
| 4 | `cells/access-core/slices/sessionlogout/service.go` | 88 | `"session_id"`, `"user_id"` |
| 5 | `cells/audit-core/slices/auditappend/service.go` | 108-109 | `"audit_entry_id"`, `"event_type"` |
| 6 | `cells/audit-core/slices/auditverify/service.go` | 95-96 | `"first_invalid_index"`, `"entries_checked"` |
| 7 | `cells/config-core/slices/configpublish/service.go` | 92 | `"config_id"` |
| 8 | `cells/config-core/slices/configpublish/service.go` | 134-135 | `"target_version"`, `"new_version"` |

Severity: P2 (event payloads are internal, but naming-baseline says JSON should prefer camelCase)

---

## 4. Query Parameter Naming (snake_case -> camelCase)

Per naming-baseline 2.1: "JSON / Query / Path fields should use camelCase".

| # | File | Line | Current | Should Be |
|---|------|------|---------|-----------|
| 1 | `cells/audit-core/slices/auditquery/handler.go` | 29 | `r.URL.Query().Get("event_type")` | `"eventType"` |
| 2 | `cells/audit-core/slices/auditquery/handler.go` | 30 | `r.URL.Query().Get("actor_id")` | `"actorId"` |

Severity: P1 (external API surface, directly affects client integration)

---

## 5. DB Field Naming

All SQL queries use snake_case for column names. Verified in:
- `adapters/postgres/migrator.go` -- `version`, `applied_at`, `name`
- `adapters/postgres/outbox_writer.go` -- `aggregate_id`, `aggregate_type`, `event_type`, `payload`, `metadata`
- `adapters/postgres/outbox_relay.go` -- `id`, `aggregate_id`, `aggregate_type`, `event_type`, `payload`, `metadata`, `published`, `published_at`, `created_at`
- `cells/config-core/internal/adapters/postgres/config_repo.go` -- `id`, `key`, `value`, `version`, `created_at`, `updated_at`, `config_id`, `published_at`
- `cells/audit-core/internal/adapters/postgres/audit_repo.go` -- `id`, `event_id`, `event_type`, `actor_id`, `timestamp`, `payload`, `prev_hash`, `hash`

**Status: COMPLIANT** -- All DB column names use snake_case as required.

---

## 6. Environment Variable Naming

| # | File | Line | Variable | Status |
|---|------|------|----------|--------|
| 1 | `adapters/postgres/pool.go` | 49 | `GOCELL_PG_DSN` | COMPLIANT |
| 2 | `adapters/postgres/pool.go` | 55 | `GOCELL_PG_MAX_CONNS` | COMPLIANT |
| 3 | `adapters/postgres/pool.go` | 60 | `GOCELL_PG_IDLE_TIMEOUT` | COMPLIANT |
| 4 | `adapters/postgres/pool.go` | 65 | `GOCELL_PG_MAX_LIFETIME` | COMPLIANT |
| 5 | `cmd/core-bundle/main.go` | 33 | `GOCELL_SIGNING_KEY` | COMPLIANT |
| 6 | `cmd/core-bundle/main.go` | 34 | `GOCELL_HMAC_KEY` | COMPLIANT |
| 7 | `cmd/core-bundle/main.go` | 37 | `GOCELL_ADAPTER_MODE` | COMPLIANT |
| 8 | `adapters/s3/client.go` | 48 | `GOCELL_S3_ENDPOINT` | COMPLIANT |
| 9 | `adapters/s3/client.go` | 49 | `GOCELL_S3_REGION` | COMPLIANT |
| 10 | `adapters/s3/client.go` | 50 | `GOCELL_S3_BUCKET` | COMPLIANT |
| 11 | `adapters/s3/client.go` | 51 | `GOCELL_S3_ACCESS_KEY` | COMPLIANT |
| 12 | `adapters/s3/client.go` | 52 | `GOCELL_S3_SECRET_KEY` | COMPLIANT |
| 13 | `adapters/s3/client.go` | 53 | `GOCELL_S3_USE_PATH_STYLE` | COMPLIANT |

Legacy S3_* fallback variables are documented as deprecated with slog.Warn -- acceptable.

**Note (test file only, out of scope):** `adapters/postgres/pool_test.go:123` uses `PG_INTEGRATION` (no `GOCELL_` prefix). This is a test-only gate variable, not a production config. Not counted as a violation but noted for awareness.

**Status: COMPLIANT** -- All production env vars use `GOCELL_*` prefix with SCREAMING_SNAKE_CASE.

---

## 7. Slice Dual-Directory Violations

Naming-baseline 1.4 states: "Not allowed to have parallel sibling directories, e.g. `session-login/` and `sessionlogin/` coexisting."

The following dual-directory pairs exist:

| # | Cell | kebab-case dir (slice.yaml) | Go package dir (code) | Violation? |
|---|------|-----------------------------|-----------------------|------------|
| 1 | access-core | `slices/session-login/` | `slices/sessionlogin/` | YES |
| 2 | access-core | `slices/session-logout/` | `slices/sessionlogout/` | (inferred) |
| 3 | access-core | `slices/session-refresh/` | `slices/sessionrefresh/` | (inferred) |
| 4 | access-core | `slices/session-validate/` | `slices/sessionvalidate/` | (inferred from code path) |
| 5 | access-core | `slices/identity-manage/` | `slices/identitymanage/` | YES |
| 6 | access-core | `slices/rbac-check/` | `slices/rbaccheck/` | YES |
| 7 | access-core | `slices/authorization-decide/` | `slices/authorizationdecide/` | YES |
| 8 | audit-core | `slices/audit-append/` | `slices/auditappend/` | YES |
| 9 | audit-core | `slices/audit-query/` | `slices/auditquery/` | YES |
| 10 | audit-core | `slices/audit-verify/` | `slices/auditverify/` | YES |
| 11 | audit-core | `slices/audit-archive/` | `slices/auditarchive/` | YES |
| 12 | config-core | `slices/config-read/` | `slices/configread/` | YES |
| 13 | config-core | `slices/config-write/` | `slices/configwrite/` | YES |
| 14 | config-core | `slices/config-publish/` | `slices/configpublish/` | YES |
| 15 | config-core | `slices/config-subscribe/` | `slices/configsubscribe/` | YES |
| 16 | config-core | `slices/feature-flag/` | `slices/featureflag/` | YES |

Evidence: `cells/access-core/slices/` contains both `session-login/slice.yaml` and `sessionlogin/handler.go` as separate directories. Same pattern repeats across all three core Cells.

Severity: P1 (naming-baseline 1.4 explicitly prohibits this; section 3 lists it as a required migration item)

Note: `device-cell` and `order-cell` do NOT have this problem -- they use a single kebab-case directory for both slice.yaml and Go code, with the Go package name derived from the directory. This is the correct pattern.

---

## 8. errcode Format

All error codes in scope follow `ERR_*` + SCREAMING_SNAKE_CASE format.

Verified across:
- `adapters/postgres/errors.go` -- `ERR_ADAPTER_PG_*`
- `adapters/redis/client.go` -- `ERR_ADAPTER_REDIS_*`
- `adapters/rabbitmq/connection.go` -- `ERR_ADAPTER_AMQP_*`
- `adapters/s3/errors.go` -- `ERR_ADAPTER_S3_*`
- `adapters/websocket/errors.go` -- `ERR_ADAPTER_WS_*`
- `adapters/oidc/errors.go` -- `ERR_ADAPTER_OIDC_*`
- `cells/access-core/` -- `ERR_AUTH_*`
- `cells/audit-core/` -- `ERR_AUDIT_*`, `ERR_NOT_IMPLEMENTED`, `ERR_ARCHIVE_*`
- `cells/config-core/` -- `ERR_CONFIG_*`, `ERR_FLAG_*`

**Status: COMPLIANT**

---

## Summary

| Category | Findings | Severity |
|----------|----------|----------|
| Go abbreviations (URL param `cmdId`) | 3 locations | P2 |
| JSON tag snake_case (internal struct) | 1 location | P2 |
| Event payload JSON keys (snake_case) | 8 code sites | P2 |
| Query param snake_case (external API) | 2 locations | P1 |
| DB field naming | 0 | COMPLIANT |
| Environment variables | 0 | COMPLIANT |
| Slice dual-directory | 16 pairs | P1 |
| errcode format | 0 | COMPLIANT |

### Action Items

**P1 (should fix):**
1. **Query parameters**: Change `event_type` -> `eventType` and `actor_id` -> `actorId` in `cells/audit-core/slices/auditquery/handler.go:29-30`. Update corresponding tests in `handler_test.go`.
2. **Slice dual-directory consolidation**: Merge 16 dual-directory pairs so each slice has one canonical directory. Go code and slice.yaml must coexist in the kebab-case directory. The Go package name inside a `session-login/` directory should be `sessionlogin` (Go does not allow hyphens in package names, but the *directory* should be kebab-case). This is tracked in naming-baseline section 3 as a migration item.

**P2 (suggestion):**
3. **URL path param `cmdId`**: Consider renaming to `cmdID` in route pattern and all `chi.URLParam` calls. Low priority since it is an internal routing key.
4. **Event payload keys**: Consider migrating internal event payload JSON keys from snake_case to camelCase for consistency with the naming baseline. This requires coordinated changes across all producers and consumers.
5. **JSON tag `"user_id"`**: In `auditappend/service.go:93`, consider changing to `"userId"` if event producers are also updated.
