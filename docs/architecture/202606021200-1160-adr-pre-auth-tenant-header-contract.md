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

## Decision

**Document now, generate later.**

1. Add YAML comment blocks to the three affected `contract.yaml` files describing
   the `X-Tenant-ID` header (type, parsing, fail-closed behaviour, codegen gap).
   YAML comments are invisible to the strict-decode parser, so governance CI is
   unaffected.

2. Do NOT modify `kernel/metadata/types.go` or the contractgen pipeline in this
   PR. Adding a `headers:` field to `EndpointsMeta` (or a sub-struct) and wiring
   it through codegen is a non-trivial change with its own blast radius (golden
   tests, archtest form-locks, codegen templates). It is deferred to a dedicated
   backlog issue.

3. Open a backlog issue (tracked externally, referenced below) to implement
   contractgen header-parameter support. Until that issue ships, callers must set
   `X-Tenant-ID` manually.

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

- Contract files are now the source of documentation truth for the header, even
  though codegen does not yet enforce it. Reviewers can audit the comment blocks
  to verify handler behaviour matches the declared semantics.
- Generated client stubs remain incomplete until the deferred issue ships. Teams
  consuming these contracts from generated code must add the header manually.
- No governance rule change in this ADR. The deferred issue must add a governance
  rule or archtest that enforces header presence when `headers:` is declared.

## Deferred backlog

Issue: #1494 — "contractgen: support header parameters (X-Tenant-ID single-source)"
Labels: `backlog`, `pri-p2`
Scope: add `headers:` to `EndpointsMeta` (or a new sub-struct) with
`yaml:"headers,omitempty"`, wire through `parser_strict_test.go` KnownFields
golden, extend contractgen templates to emit typed header accessors, update
archtest form-locks.
