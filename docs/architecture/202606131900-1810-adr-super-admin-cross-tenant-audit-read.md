# ADR: Super-admin cross-tenant audit read via role-scoped admin pool (#1810)

- Status: Accepted (2026-06-13)
- Epic: #1337 (tenancy)
- Related: #1760 (sealed `RowScopeAll` construction), ADR `202606071300-1618`
  (per-tenant chain + FORCE RLS), ADR `202606071200-1676` (restricted serving pool),
  #1761 (slog-vs-ledger audit signal — OUT of scope here), #1755 (startup full-chain
  verify — OUT of scope here)

## Context

PR-5 (#1343) landed the framework derivation `(*auth.Principal).RowVisibility(ctx)`
including `super-admin → RowScopeAll`, with a mandatory FR-007 `slog.Error` audit
before constructing the cross-tenant obligation. PR #1753 (#1618) then put
`audit_entries` under per-(namespace, tenant) `FORCE ROW LEVEL SECURITY`. During
merge, `RowScopeAll` on audit reads was deliberately **re-fail-closed** to HTTP 501
(`RowScopeAllUnsupportedError`): the single `gocell_app` NOBYPASSRLS serving role
(ADR #1676) cannot enumerate tenants or read across them, so "drop the tenant
filter" would be silently RLS-filtered to the GUC tenant — a cross-tenant view that
under-delivers. Safe, but the #1337 FR-007 / PR-5 super-admin cross-tenant audit
read capability was never actually usable.

The blocker is purely **the serving role cannot read cross-tenant** — not a missing
algorithm. Granting `gocell_app` more power would violate ADR #1676.

## Decision

Serve the super-admin cross-tenant audit read from a **dedicated, separate
privileged-read path**, leaving the serving role untouched.

1. **Role-scoped permissive RLS policy (no BYPASSRLS).** A new role
   `gocell_audit_admin` (NOSUPERUSER, **NOBYPASSRLS**, non-owner, SELECT-only) plus
   migration 064:
   `CREATE POLICY audit_admin_read_all ON audit_entries FOR SELECT TO
   gocell_audit_admin USING (true)` + `GRANT SELECT`. PostgreSQL permissive policies
   are OR-ed and a role-listed policy applies only to that role, so this role reads
   every tenant while `gocell_app`'s effective predicate and FORCE RLS are unchanged.
   Cross-tenant visibility is an explicit, `schema_guard`-pinned `pg_policy` row, not
   a blanket BYPASSRLS bypass (see ADR #1676 amendment). Migration 064 is a
   role-guarded **no-op** where the role is not provisioned.

2. **Sealed Hard typed funnel (#1760).** The read API
   `ledger.CrossTenantQueryStore.QueryCrossTenant(ctx, ctv tenant.CrossTenantVisibility,
   …)` takes the **sealed** `tenant.CrossTenantVisibility` positional param, whose
   sole producer is the audited `(*auth.Principal).CrossTenantVisibility` derivation.
   The read is therefore uncallable without routing through the mandatory-FR-007-audit
   funnel — forget = compile error, forge = compile error. `NewRowVisibility` rejects
   `RowScopeAll` (general path provably cannot mint it).

3. **Single predicate-free query.** The PG impl (`adapters/postgres.AuditCrossTenantStore`,
   dedicated admin pool) issues one `SELECT … FROM audit_entries` with **no namespace
   and no tenant predicate** — the permissive policy returns all tenants and dropping
   the namespace filter returns both the relay and bootstrap chains in one scan (what
   `MultiStore` aggregates across two serving stores). PG orders by `timestamp DESC,
   id ASC` (= `QuerySort`) with keyset pagination (limit ≤ 500). No `SET LOCAL`, no
   `RunInTx`. A mem impl backs demo/test.

4. **Defense in depth + graceful optionality.** The serving-pool `Store.Query` /
   `GetBySeq` **keep** fail-closing `RowScopeAll`. The admin pool is **optional**
   (`GOCELL_AUDIT_ADMIN_DSN`): absent → super-admin reads stay fail-closed at HTTP
   501; present-but-broken → fail-fast at composition (no silent fallback). The
   auditquery handler branches on `HasRole(SuperAdmin)` → `CrossTenantVisibility(ctx)`
   (the single FR-007 mint) → `Service.QueryCrossTenant`; ordinary tenant/admin/self
   paths are unchanged.

## AI-robust threat matrix

| Surface | Before #1810 | After #1810 | Δ |
|---|---|---|---|
| `gocell_app` cross-tenant read | RLS-blocked (501) | RLS-blocked (unchanged) | — (integration-test asserted) |
| Super-admin cross-tenant read | 501 (capability absent) | served via `gocell_audit_admin` role | New, contained capability |
| `RowScopeAll` mint | archtest allowlist (Medium) | **Hard** general-path closure + sealed type; minter caller-restriction Medium ceiling | Strengthened (#1760) |
| Cross-tenant read call gate | n/a | **Hard** typed funnel (sealed `CrossTenantVisibility` param) | New Hard |
| FR-007 audit | at mint (1×) | at mint, exactly 1× per request (handler single-branch; test-gated) | Preserved |
| Write surface | SELECT-only admin role | unchanged (admin role has no DML) | — |
| schema_guard "no extra permissive policy" (#1622-F1) | 1 policy on `audit_entries` | expects 2 **only when** role provisioned; unexpected extra still fails | Preserved (synthetic-red tested) |

No cell flips to ⚠️/❌.

## Scope boundaries (explicit, no loose TODOs)

- **IN:** cross-tenant audit **list** read (`GET /api/v1/audit/entries` for super-admin).
- **OUT — #1761:** a tamper-evident `ledger.Append` breadcrumb of the cross-tenant
  read (slog-vs-ledger audit signal is #1761's concern; FR-007 slog is preserved here).
- **OUT — #1755:** startup full per-tenant chain verify. #1810 unblocks its
  enumeration prerequisite (the admin role can `SELECT DISTINCT tenant_id`) but does
  not build the verify tool.
- **OUT (and complete):** `GetBySeq` / `Tail` / `Verify` cross-tenant — no HTTP
  surface exposes them; they stay fail-closed by design.

## Consequences

- Operators who want super-admin cross-tenant audit read provision the
  `gocell_audit_admin` role (`GOCELL_AUDIT_ADMIN_PASSWORD`) + admin pool DSN
  (`GOCELL_AUDIT_ADMIN_DSN`); otherwise the capability is gracefully absent (501).
- A third deployment role + an additional managed pool (closed LIFO on shutdown).
- `schema_guard` now has role-conditional RLS expectations on `audit_entries`.

## References

- Migration: `adapters/postgres/migrations/064_audit_admin_read_role.sql`
- Code: `runtime/audit/ledger/cross_tenant_store.go`,
  `adapters/postgres/audit_cross_tenant_store.go`,
  `runtime/audit/ledger/mem_cross_tenant_store.go`,
  `corecells/auditcore/slices/auditquery/{handler,service}.go`,
  `cellmodules/auditcore/module.go`
- Sealed obligation (#1760): `pkg/tenant/rowvisibility.go`,
  `runtime/auth/rowscope.go`, `tools/archtest/rowscopeall_audit_funnel_test.go`
- Amends: ADR `202606071300-1618` (Amendment 2026-06-13), ADR `202606071200-1676`
  (Amendment 2026-06-13)
- Rule index: `.claude/rules/gocell/tenancy.md`
