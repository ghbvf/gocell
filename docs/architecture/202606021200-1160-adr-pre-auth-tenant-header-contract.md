# ADR 1160 — Pre-auth tenant via X-Tenant-ID header: contract documentation and deferred codegen

Date: 2026-06-02
Status: Accepted
EPIC: #1337 (multi-tenancy PR-2a)

## Context

Three endpoints are accessed before a JWT is available:

| Contract | Path |
|---|---|
| `http.auth.login.v1` | `POST /api/v1/access/sessions/login` |
| `http.auth.setup.admin.v1` | `POST /api/v1/access/setup/admin` |
| `http.auth.setup.status.v1` | `GET /api/v1/access/setup/status` |

After migration 050 (`adapters/postgres/migrations/049_accesscore_tenant_id.sql`)
rebuilt `users` / `roles` / `role_assignments` with `tenant_id NOT NULL`, every
lookup in these pre-auth handlers requires a tenant scope that cannot come from
JWT claims (no JWT exists yet). The handler implementation reads `X-Tenant-ID`
from the HTTP request header and parses it via `tenant.ParseTenantID` before any
domain call.

The gap: the header is consumed by handler code but has no declaration in the
corresponding `contract.yaml` files. Contractgen does not yet support a
`headers:` block (the parser uses `KnownFields(true)` strict decode — adding an
unknown top-level key would cause `gocell validate` to fail). As a result, no
generated client code emits the header and no governance rule enforces its
presence.

## Amendment — 2026-06-07 (delivered in issue #1494 / PR #1707)

The deferred backlog item has shipped. The decisions and consequences below are
rewritten to describe the delivered state. The original "document now, generate
later" posture is retired.

## Decision

**Header parameters are a first-class field on `HTTPTransportMeta`.**

1. `kernel/metadata/schema_types.go` `HTTPTransportMeta` gained a
   `Headers map[string]ParamSchema` field (`yaml:"headers,omitempty"`). The three
   affected `contract.yaml` files now declare the `X-Tenant-ID` header in a
   machine-readable `endpoints.http.headers:` block (YAML comments replaced by
   structured data).

2. Contractgen merges each declared header into the generated `Request` DTO as a
   typed field populated by the generated handler via `r.Header.Get(...)` — the
   handler is the sole sanctioned reader (archtest
   `HTTP-REQUEST-HEADER-READ-FUNNEL-01`). The populate-only accessor emits no
   required/length/format gate; per-endpoint fail behavior (the matrix below)
   remains owned by the cell adapter/service.

3. Governance rule `FMT-40` (`kernel/governance/rules_fmt.go::validateFMT40`,
   `PhaseBase`) validates the `headers:` block at `gocell validate` time:
   header name must be a valid HTTP token, `type` must be a known param type, and
   `minLength`/`maxLength`/`minimum`/`maximum` are rejected because the generated
   handler emits no gate for them (`required` is accepted as declared-intent /
   client-gen metadata only).

4. Archtest `HTTP-HEADERS-FIELD-FROZEN-01` reflect-freezes the
   `HTTPTransportMeta.Headers` field set to prevent silent drift.

## Fail-closed semantics per endpoint

| Endpoint | Missing/malformed X-Tenant-ID | Reason |
|---|---|---|
| `login` | 401 (same shape as wrong-password) | Prevents tenant enumeration — attacker cannot distinguish "tenant doesn't exist" from "wrong credentials" |
| `setup/admin` | 400 ERR_AUTH_IDENTITY_INVALID_INPUT | Bootstrap path; invalid tenant UUID is a caller error, not a security probe (the setup service returns the existing identity-input errcode, not a generic validation code) |
| `setup/status` | 200 `{hasAdmin: false}` | Same shape as "not yet provisioned"; prevents tenant existence disclosure |

All three paths invoke `tenant.ParseTenantID` which rejects empty strings and
non-canonical UUIDs (Medium runtime guard; Hard gate = PR-2 typed position param
at repo boundary).

### setup/status fail-soft DELIBERATELY supersedes the fail-closed expectation (F3)

A review finding (F3) read the original requirement as **fail-closed**: a
missing/invalid `X-Tenant-ID` on `setup/status` should surface an error so a
caller cannot silently get a misleading `hasAdmin:false`. This ADR **consciously
overrides that** to fail-soft `200 {hasAdmin:false}`, and this supersession is
explicit (not implicit): `setup/status` is a **public, pre-auth probe**, so a
uniform `200 {hasAdmin:false}` for "no admin yet" / "tenant unknown" / "bad
tenant" is the anti-enumeration posture — it denies an unauthenticated attacker a
tenant-existence oracle, the same reason `login` returns a uniform 401. The
accepted cost: a caller that simply forgot the header gets `hasAdmin:false`
instead of a 4xx (a DX papercut on a bootstrap-only endpoint), which is the
deliberate trade for non-enumerability. Decision owner sign-off: keep fail-soft
(PR-2a review round-2). If the DX cost is later judged to outweigh the
enumeration risk, the bounded alternative is "missing header → 400, present-but-
invalid tenant → 200" (split), not a blanket fail-closed.

## Consequences

- Contract files are the authoritative source for header declarations. The three
  pre-auth contracts (`http.auth.login.v1`, `http.auth.setup.admin.v1`,
  `http.auth.setup.status.v1`) each declare `X-Tenant-ID` in their
  `endpoints.http.headers:` block; generated client stubs will include the header
  field automatically.
- The populate-only model is intentional: the header value is made available to the
  cell adapter, but the generated handler never rejects or validates it. This keeps
  the per-endpoint fail behavior (login→401, setup/admin→400, setup/status→200
  fail-soft) entirely under cell-adapter control.
- `gocell validate` now enforces header declaration correctness via `FMT-40`;
  undeclared length/numeric constraints on headers are a hard validation error so
  they can never silently no-op.
- The archtest `HTTP-REQUEST-HEADER-READ-FUNNEL-01` prevents business code in
  `cells/` or `examples/` from reading inbound request headers raw, directing all
  header consumption through the generated `Request` DTO field.

## Deferred backlog

No items remain deferred from the original scope of issue #1494. The full
header-parameter pipeline (metadata field, governance rule, codegen, archtest) has
been delivered in PR #1707.
