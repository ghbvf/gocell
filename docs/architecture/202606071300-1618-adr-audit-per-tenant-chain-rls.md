# ADR: audit_entries per-(namespace, tenant) hash chain + FORCE RLS

- **Issue**: #1618 (EPIC #1337 multi-tenancy; deferred out of PR-3a #1341)
- **Status**: Accepted
- **Date**: 2026-06-07

## Context

`audit_entries` is a tamper-evident HMAC-SHA256 hash chain. Pre-#1618 the chain is
**namespace-global**: `UNIQUE(namespace, seq_no)`, and the appender's tail /
prev-hash / Verify reads filter by `namespace + seq_no` across all tenants. It is
the one tenant-bearing table that could NOT go under per-tenant FORCE RLS (the
RLS geography landed for config tables in PR-3a #1341 / migration 052 and
accesscore in PR-3b #1617 / 053): under per-tenant RLS the appender (GUC = the
event's tenant) would read a tenant-filtered tail → compute `seq_no =
tenant_max+1` → collide with the global `UNIQUE(namespace, seq_no)` → append
failure / chain corruption.

Until this ADR, audit read isolation was the app-layer `AuditFilters.TenantID`
string filter (PR-2a) with no DB backstop. PR #1715 (codex review F1) further
showed `ledger.Store.GetBySeq` enforced only the OWNER obligation
(`vis.Allows(actor_id)`) and carried **no tenant axis**, so an admin
`RowScopeTenant` obligation could read a cross-tenant entry by seq_no.

## Decision

Re-architect the chain to **per-(namespace, tenant)** and put `audit_entries`
under FORCE RLS.

- **D1 — per-tenant chain.** `UNIQUE(namespace, tenant_id, seq_no)`; advisory lock
  keyed on `(namespace, tenant_id)` (two-key `pg_advisory_xact_lock(hashtext(ns),
  hashtext(tenant_id))`); per-tenant genesis (each `(namespace, tenant)` sub-chain
  restarts at seq_no=1, prev_hash=''). Idempotency dedup is per-tenant
  (`uq_audit_ns_tenant_event_id`). Migration 055 (forward-rebuild, DROP+recreate).

- **D2 — hash protocol UNCHANGED.** `seq_no` is **not** in the 12-field HMAC input
  (chain order is the `prev_hash` linkage); `tenant_id` is already field 7. So
  re-numbering seq_no per-tenant touches no hash. `protocol.go` and
  `audit_hash_input_frozen_test.go` are untouched. Cross-tenant HMAC replay was
  already prevented by tenant_id participating in the digest.

- **D3 — existing rows discarded (no backward compat).** Existing rows' prev_hash
  linkage follows the original GLOBAL append order; splitting them into per-tenant
  sub-chains would break Verify. Pre-v1.0, no production data — DROP+recreate (same
  precedent as 043 superseding 020/021). Two permits: the Up forward-rebuild
  annotation `+gocell forward-rebuild target=audit_entries` + an execution-time
  `ForwardRebuildPermit` (`tools/pg-migrate -rebuild "55:..."`; auto-passes on the
  empty table of a fresh DB); the Down `DestructiveDownPermit`.

- **D4 — `OR tenant_id = ''` policy (audit-specific divergence).** The
  `tenant_isolation` policy is `USING/WITH CHECK = (tenant_id =
  NULLIF(current_setting('app.tenant_id', true), '') OR tenant_id = '')`. The
  `OR tenant_id = ''` clause is **load-bearing**: (a) **USING** lets a tenant admin
  read tenant-less system/framework rows (bootstrap.auth.fail and other pre-auth
  events); (b) **WITH CHECK** lets the GUC-unset (pre-auth / system / bootstrap)
  appender INSERT `tenant_id=''` rows — a plain predicate yields `'' = NULL` =
  false and rejects them (42501). config/accesscore have NO such clause (strict
  equality); audit is the documented exception.

- **D5 — tenant axis source.** `Store.Query` takes a mandatory typed
  `tenant.TenantID` positional param[1] (vis → param[2], the shape
  `ROWSCOPE-REPO-PARAM-FUNNEL-01` pre-anticipates). The PR-2a Soft
  `AuditFilters.TenantID` string field is **deleted** — a tenant-less Query is no
  longer expressible (fail-closed; empty tenant → system rows only, never
  cross-tenant). `GetBySeq`/`Verify`/`Tail` (no post-auth HTTP caller — conformance
  / replay / startup only) derive the tenant chain from the ctx scope
  (`tenant.ScopeFromContext`, default `""` system chain when unscoped); they keep
  their signatures. GetBySeq's explicit `AND tenant_id` also disambiguates the now
  non-unique `(namespace, seq_no)` under the `OR tenant_id=''` clause and closes
  PR #1715 F1.

- **D6 — read-path wiring (no new funnel).** The appender (consumer, ctxkeys.TenantID
  restored from the event envelope) and auditquery (post-auth principal) both get
  the FORCE-RLS GUC via the **existing** `tenantScopeForTx` ctxkeys fallback — no
  `tenant.WithScope`, no `TENANT-TXSCOPE-WRITE-CALLER-01` allowlist change. The
  auditquery `Service` gains a `txRunner` and wraps each page fetch in `RunInTx` so
  the GUC is active (MultiStore fans out on the same ambient tx → one RunInTx scopes
  the relay + bootstrap chains).

- **D7 — dual-layer isolation (matches config/accesscore precedent).** The typed
  tenant param (compile-time Hard) + mem `tenantMatches` are the app-layer; FORCE
  RLS on the GUC is the DB-Hard primary. mem (no RLS) relies on the typed param as
  its sole isolation, so it is required, not redundant.

## AI-robust threat matrix

| Axis / layer | Mechanism | Rating |
|---|---|---|
| TENANT — DB (primary) | FORCE RLS + `gocell_app` NOBYPASSRLS (#1676); unset GUC → 0 tenant rows | **Hard** (Postgres-enforced; serving role cannot bypass) |
| TENANT — GUC injection | single `tenantScopeForTx`→`writeTenantGUC` funnel from ctxkeys / scope | **Hard** (PG-SETLOCAL-FUNNEL-01 + CTXKEYS-PRINCIPAL-WRITE-CALLER-01 + TENANT-TXSCOPE-WRITE-CALLER-01) |
| TENANT — app layer (DiD + mem) | `Store.Query` typed positional `tenant.TenantID` param | **Hard** (compile-time; omission/un-typing = compile error, also caught by ROWSCOPE obligation-slot check) |
| OWNER (actor_id) | sealed `tenant.RowVisibility` + `vis.Allows`/`SQLPredicate` | **Hard** (existing, ROWSCOPE-REPO-PARAM-FUNNEL-01) |
| Write surface | appender sole writer; `OR tenant_id=''` WITH CHECK weakening | **Hard boundary** (AUDITCORE-APPENDER-SINGLE-SOURCE-01 — no tenant-controlled INSERT path) |

**Why audit `Store` is NOT enrolled in TENANT-REPO-PARAM-FUNNEL-01:** that funnel
targets repos where *every* method is tenant-scoped (config/accesscore). The audit
`Store` is a chain primitive — only `Query` takes the typed tenant param; the
chain reads (`GetBySeq`/`Verify`/`Tail`) derive tenant from ctx. Enrolling would
need 5 carve-outs for a 1-tenant-method interface (cargo-culting). The tenant
param's Hard guarantee is the compile-time positional requirement, and
ROWSCOPE-REPO-PARAM-FUNNEL-01 already validates the obligation-slot-after-tenant
shape (so un-typing the tenant param to `string` fails the obligation-slot check).

**Write-side `OR tenant_id=''` weakening (accepted):** a tenant-scoped writer can
technically INSERT a `tenant_id=''` row. Bounded by the appender being the sole
`audit_entries` writer (Hard archtest); there is no tenant-controlled INSERT path
(auditquery is read-only). No future tenant-facing write path may be added without
revisiting this.

## Deliberate scoping (explicit, not silent)

- **Startup tail-verify coverage narrows.** `strictTailVerifyOnStartup` (relay) and
  `VerifyBootstrapTailOnStartup` (bootstrap) run UNSCOPED → now cover the `""`
  system sub-chain only. Bootstrap is fully covered (all bootstrap rows are
  tenant=''). The relay namespace's per-tenant sub-chains are verified on-demand
  (scoped Verify), NOT at startup — full per-tenant startup verify needs admin
  enumeration of tenants, which the NOBYPASSRLS serving role cannot do. Tracked as
  a follow-up backlog (admin full-chain verify tool).

- **conformance runs on superuser; RLS effectiveness on the restricted role.** The
  cross-backend conformance suite (`runtime/audit/ledger/storetest`) proves the
  per-tenant chain + typed-param filtering on mem + PG-superuser (RLS bypassed);
  `audit_rls_integration_test.go` proves RLS USING/WITH CHECK/system-rows on the
  restricted `gocell_app`-style role.

## Forward constraints

- A single `RunInTx` wrapping `MultiStore.Query` works only while the relay +
  bootstrap chains share one pool (current). If chains ever split across pools, the
  wrap must become per-store-tx.

## Ops impact

### Index renames (migration 055, forward-rebuild DROP+recreate)

| Old index name | New index name | Columns |
|---|---|---|
| `uq_audit_namespace_seq` | `uq_audit_namespace_tenant_seq` | `(namespace, tenant_id, seq_no)` |
| `uq_audit_namespace_event_id` | `uq_audit_ns_tenant_event_id` | `(namespace, tenant_id, event_id)` |

Dashboard queries or alert rules that reference old index names by string
(e.g. `pg_stat_user_indexes`) must be updated. The new names are in migration
055 DDL and reflected in `schema_guard.go` expected-index inventory.

### Advisory lock shape change

Pre-#1618 the appender held a **single-key** advisory lock:
`pg_advisory_xact_lock(hashtextextended(namespace, 0))` — one `int64` slot.

Post-#1618 the lock is **two-key**:
`pg_advisory_xact_lock(hashtext(namespace), hashtext(tenant_id))` — two `int4`
slots. PG treats `pg_advisory_xact_lock(bigint)` and
`pg_advisory_xact_lock(int4, int4)` as separate lock classes; there is no
conflict between the two forms during a rolling deploy.

Monitoring queries that inspect `pg_locks` for the old single-bigint advisory
lock class will miss the new two-int4 locks — update `classid`/`objid`
predicates accordingly (`int4` columns in `pg_locks`).

### Keyset index ordering (performance note)

The `audit_entries` keyset index remains **namespace-leading**
(`namespace, tenant_id, seq_no`). The `OR tenant_id = ''` disjunction in the
RLS predicate prevents a tenant_id-leading index from being used for full-table
scans (PG cannot use an index on one side of a disjunction without UNION ALL or
bitmapscan). Per-tenant queries therefore do a namespace-prefix scan with an
in-scan tenant_id filter. This is acceptable for current audit volumes; revisit
if per-tenant chain depth or tenant count grows significantly.

## Amendment 2026-06-07 — pre-auth scoped-emit tenant provenance (PR #1753 review F1)

The original threat matrix's **Write surface** row treated every `tenant_id=''`
row as a genuinely tenant-agnostic system/framework event, bounded by the
appender being the sole writer. Review F1 found that assumption was **false for
two tenant-bearing pre-auth events**: login's `event.session.created.v1`
(`cells/accesscore/slices/sessionlogin`) and setup's `event.user.created.v1`
(`cells/accesscore/slices/setup`) are emitted **inside** `tenant.WithScope`
(scopedtx) — so the RLS scope carries the real tenant — but the outbox principal
tenant was sourced **only** from `ctxkeys.TenantID`, which login/setup never set
(pre-auth, JWT-exempt). The appender therefore wrote those rows with
`tenant_id=''`, and the new D4 `OR tenant_id=''` USING clause made them readable
by **every** tenant — a cross-tenant audit leak introduced by this PR (pre-#1618
the read filter was `AND tenant_id=$N`, so those rows were invisible, not
all-visible).

**Compensating measure (fix, this PR):** `kernel/outbox.ContextPrincipal` now
falls back to `tenant.ScopeFromContext` for the principal `TenantID` when
`ctxkeys.TenantID` is absent, mirroring the GUC source precedence in
`adapters/postgres.tenantScopeForTx` (scope → ctxkeys). Pre-auth scoped emits
now carry their real tenant end-to-end, so the appender writes them into the
correct per-(namespace, tenant) chain — and, symmetrically, the appender's own
FORCE-RLS INSERT GUC (restored from the now-correct envelope principal) matches
the row `tenant_id`, satisfying WITH CHECK on the tenant branch rather than the
`OR tenant_id=''` branch. Genuinely tenant-less events (`bootstrap.auth.fail`,
emitted with neither scope nor ctxkeys) stay `tenant_id=''` → system chain.

**Threat-matrix re-evaluation (per ai-robust "ADR amendment 落地必查"):** no cell
flips to ⚠️/❌. The **Write surface** row stays **Hard boundary** — the invariant
it relies on ("`tenant_id=''` ⟺ genuinely tenant-less framework event") is now
*restored* rather than assumed: tenant-bearing scoped emits no longer fall into
the `''` bucket. The fix sources principal-tenant from the scope, whose write
provenance is itself **Hard**-funnel-locked (`TENANT-TXSCOPE-WRITE-CALLER-01`),
so no new forge surface is added; `CTXKEYS-PRINCIPAL-WRITE-CALLER-01` is
untouched (the change is a ctx *read* inside the single existing injection point
`ContextPrincipal`, not a new ctxkeys write). Regression guard:
`kernel/outbox.TestContextPrincipal` (scope-fallback + ctxkeys-precedence
subtests).

## Amendment 2026-06-08 — merge with PR-5 #1343 (RowScopeAll re-fail-closed)

While this PR was in review, sibling tenancy-epic **PR-5 (#1343)** merged to
`develop`. PR-5 (a) generalized the audit-query identity→RowScope derivation into
framework-level `(*auth.Principal).RowVisibility(ctx)` (non-admin→self,
device→device, admin→tenant, **super-admin→RowScopeAll**) with a mandatory FR-007
`slog.Error` cross-tenant audit, and (b) — in its standalone form, built on the
**pre-#1618** base where `audit_entries` had no RLS and tenant was the app-layer
`AuditFilters.TenantID` string — **un-fail-closed** `RowScopeAll` (deleted
`RowScopeAllUnsupportedError`, made the stores pure PEPs that read cross-tenant).

These two designs are **incompatible** at the audit `Store.Query` interface, and
PR-5's (b) is **architecturally impossible** under this PR's per-tenant FORCE RLS:
the `gocell_app` NOBYPASSRLS serving role cannot read across tenants, so "drop the
tenant filter" would be silently filtered by RLS to the GUC tenant's rows — a
cross-tenant view that silently under-delivers. The merge therefore:

- **Keeps** PR-5 (a): the audit-query handler uses `p.RowVisibility(ctx)` (the
  cell-local `auditRowVisibility` is deleted), and the FR-007 producer funnel
  `ROWSCOPEALL-AUDIT-FUNNEL-01` is unchanged.
- **Reverts** PR-5 (b) for audit: `RowScopeAllUnsupportedError` and the
  mem/PG/conformance fail-close guards are **restored**. A super-admin's
  `RowScopeAll` obligation fail-closes at the audit store (`KindInternal`/500); the
  FR-007 `slog.Error` still fires (at mint, before the store rejects).
  Cross-tenant audit read is **deferred to backlog** (same deferral as the
  startup full-chain verify above — the NOBYPASSRLS role cannot enumerate tenants).

**Threat-matrix re-evaluation (per ai-robust "ADR amendment 落地必查"):** no cell
flips to ⚠️/❌. Re-fail-closing `RowScopeAll` **strengthens** the OWNER/TENANT
boundary relative to PR-5's standalone un-fail-close (it removes a silent-partial
cross-tenant read path that FORCE RLS would have produced). The **Write surface**
and TENANT rows are unaffected. The producer funnel (`ROWSCOPEALL-AUDIT-FUNNEL-01`)
remains **Hard-downstream / Medium-upstream** — it locks where `RowScopeAll` may be
*minted* (auth/rowscope.go + conformance), orthogonal to whether a consumer
fail-closes or applies it; the merge keeps both legitimate producers. Regression
guard: `runtime/audit/ledger/storetest` `all-fail-closed` conformance (mem + PG) +
`auditquery` `TestHandleQuery_RowScopeVisibilityMatrix` (super-admin → 500 + FR-007
audit asserted).

## References

- Migration: `adapters/postgres/migrations/055_audit_entries_per_tenant_rls.sql`
- RLS templates: 052 (config) / 053 (accesscore); restricted role: ADR `202606071200-1676`
- Rule index: `.claude/rules/gocell/tenancy.md`
