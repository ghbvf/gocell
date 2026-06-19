# ADR: Admin full per-(namespace, tenant) audit chain verify tool (#1755)

- Status: Accepted (2026-06-19)
- Epic: #1337 (tenancy)
- Related: ADR `202606071300-1618` (per-tenant chain + FORCE RLS — this builds the
  deferred "admin full-chain verify tool"), ADR `202606131900-1810` (super-admin
  cross-tenant read via the `gocell_audit_admin` admin pool — unblocked this tool's
  enumeration prerequisite), ADR `202606071200-1676` (restricted serving pool).

## Context

After #1618, `audit_entries` is a per-`(namespace, tenant)` HMAC hash chain under
FORCE RLS. The startup tail-verify (`corecells/auditcore/cell.go::strictTailVerifyOnStartup`
+ `framework/runtime/audit/bootstrap_tail_verify.go::VerifyBootstrapTailOnStartup`)
runs UNSCOPED, so it covers only the `tenant_id=''` SYSTEM sub-chain. The relay
namespace's per-tenant sub-chains were **never integrity-verified** — the
NOBYPASSRLS `gocell_app` serving role cannot enumerate tenants (RLS hides
cross-tenant rows), so a full per-tenant verify was deferred to backlog (#1618
"Deliberate scoping"; #1810 "Scope boundaries — OUT, #1755").

#1810 shipped the enumeration prerequisite: the `gocell_audit_admin` role
(NOBYPASSRLS + role-scoped permissive `audit_admin_read_all ON audit_entries …
USING(true)` policy, migration 065) and an optional admin pool
(`GOCELL_AUDIT_ADMIN_DSN`). This ADR builds the verify tool on that pool.

## Decision

A trigger-neutral verify engine + a synchronous operator HTTP endpoint.

- **D1 — engine (transport-neutral).** `ledger.ChainVerifyStore` enumerates every
  `(namespace, tenant)` chain (one grouped `SELECT namespace, tenant_id,
  MIN(seq_no), MAX(seq_no) … GROUP BY` on the admin pool) and full-chain verifies
  each. `runtime/audit.ChainVerifier.VerifyAll` loops the fleet, verifies `[1,
  MaxSeq]` per chain, aggregates a `ChainVerifyReport`, emits aggregate metrics +
  per-chain `slog`. A run NEVER aborts on one chain's failure (the report is
  complete); the returned error is reserved for run-level infra (enumerate /
  deadline). Bounded by a fixed 30s timeout, mirroring the startup verifiers.

- **D2 — reuse the verify loop, don't duplicate it.** The PG verify loop
  (`verifyBaseline`/`verifyRange`) scopes a chain by **explicit `WHERE namespace=$1
  AND tenant_id=$2` predicates**, NOT the RLS GUC — so it runs identically on the
  admin pool (the explicit predicate does the scoping; the permissive policy only
  permits the read). It is extracted into one package function `verifyChainExec`
  with two callers: the serving-path `LedgerStore.Verify` and the admin-path
  `AuditChainVerifyStore.VerifyChain`. The security-critical HMAC/linkage logic
  exists in exactly ONE place (the same extraction is mirrored on the mem side as
  `verifyMemChain`). The existing PG verify conformance suite is the regression net.

- **D3 — no `CrossTenantVisibility` obligation.** `ChainVerifyStore` is SEPARATE
  from the sealed cross-tenant DATA-read funnel `CrossTenantQueryStore`. It returns
  only integrity verdicts `(namespace, tenant_id, valid, firstInvalidSeq, tailSeq)`
  — NEVER audit row content — so it is a system-integrity op (sibling of
  `VerifyBootstrapTailOnStartup`, which also takes no obligation), gated by
  admin-pool possession + operator auth on the endpoint, not by a
  `CrossTenantVisibility` grant. Threading the sealed obligation through it would
  either force a bogus param onto an integrity op or pollute the data-read funnel.

- **D4 — namespace→Protocol map (correctness-critical).** The grouped enumerate
  returns rows from BOTH namespace chains (relay `auditcore` + `bootstrap`), which
  have INDEPENDENT HMAC keys. Verifying with the wrong namespace's protocol would
  flag every entry as tampered. The admin store holds `protocols
  map[string]*ledger.Protocol` keyed by the `namespace` column; an unregistered
  namespace FAILS CLOSED (error, not `valid=false`). The composition root keys the
  map by each protocol's OWN `Namespace()` so the key cannot drift from the protocol.

- **D5 — synchronous operator HTTP endpoint.** `POST /admin/v1/audit/chains/verify`
  on the AdminListener (framework-owned RouteGroup, no contract.yaml — the
  projection-rebuild pattern), AuthOperator (HTTP Basic + per-IP rate limit,
  loopback). A run that COMPLETES — even with tampered chains — is **HTTP 200** with
  `allValid:false`; alerting is driven by the `audit_chain_verify_invalid_chains`
  gauge + per-chain `slog.Error`. Only a run-level failure is a framework 5xx. The
  response lists only invalid/errored chains (valid summarized by count → bounded
  body); errored chains carry `errored:true` with the infra reason kept off the wire
  (server-log only).

- **D6 — split-option wiring → no #1810 regression.** The verifier is injected
  unconditionally when the admin pool is present (`WithAuditChainVerifier` via the
  auditcore module's `ModuleResult.Opts`); the endpoint is ENABLED separately
  (`WithAuditChainVerifyEndpoint`) only in corebundle's operator-credentials block,
  alongside a conditional AdminListener declaration (mirroring `examples/todoorder`).
  So provisioning `GOCELL_AUDIT_ADMIN_DSN` (which already enables #1810 super-admin
  reads on the PrimaryListener) WITHOUT operator credentials → verifier injected,
  endpoint dormant, no AdminListener — #1810 reads unaffected. corebundle gains
  `GOCELL_OPERATOR_ADMIN_USERNAME` / `_PASSWORD` / `GOCELL_ADMIN_HTTP_ADDR`.

- **D7 — metric cardinality (minimal, frozen).** `tenant_id` is unbounded → MUST
  NOT be a metric label (`observability.md`). Per-chain detail → report body +
  `slog` only. Aggregate metrics only: `audit_chain_verify_runs_total{outcome}`
  (outcome ∈ frozen `{success,invalid_found,error}`), label-free gauges
  `audit_chain_verify_invalid_chains` / `_errored_chains`, histogram
  `audit_chain_verify_duration_seconds`. The whole metric-name + label set is
  value-golden frozen; `tenant_id` never reaches a label.

## AI-robust enforcement (every constraint Hard or Medium; no Soft)

| Constraint | Grade | Mechanism |
|---|---|---|
| Verifier never returns cross-tenant audit **content** | **Hard** (by construction) + Medium drift-guard | `ChainVerifyResult` field set is verdict-scalars-only; reflect field-freeze test (`TestChainVerifyResult_FieldSetFrozen`) |
| Verify store reads the **admin pool**, never the serving pool | **Medium** (fail-closed) | `NewAuditChainVerifyStore` requires `pool.AuditAdminReadyCheck` at construction (rejects the NOBYPASSRLS serving pool, which would silently RLS-under-enumerate); integration test `…_ConstructorRejectsServingPool`. **Hard-upgrade path:** a sealed `AuditAdminPool` marker constructed only after the preflight, shared with `NewAuditCrossTenantStore`, so "verify store on a non-admin pool" is unexpressible (gh follow-up) |
| Unknown namespace → no false tamper | **Medium** (fail-closed) | `VerifyChain` errors (not `valid=false`) on an unregistered namespace; map keyed by `protocol.Namespace()`; integration test `…_UnknownNamespace` |
| No `tenant_id` (or any unbounded) metric label | **Medium** | metric-name+label-set value-golden (`TestChainVerifyMetrics_FrozenSet`) + label-hygiene unit test (`TestVerifyAll_MetricLabelHygiene`) |
| Enabling endpoint requires AdminListener **and** verifier | **Medium** (fail-fast) | phase0 `validateAuditChainVerifyEndpoint` (mirrors `validateProjectionRebuildEndpoint`) |
| No #1810 super-admin-read regression | **Medium** | split-option design + corebundle test (`operatorAuthFromEnv` absent → endpoint gated off, no AdminListener) |

The verify-loop single-source (one `verifyChainExec`, two callers) is a refactor
pinned by the existing PG verify conformance suite, not a new enforced constraint.

## Threat-matrix re-evaluation (per ai-robust "ADR amendment 落地必查")

No cell flips to ⚠️/❌. The tool is a **SELECT-only integrity read** on the EXISTING
`gocell_audit_admin` admin pool (no new role, no new DB grant, no migration). The
**TENANT** boundary is unchanged for the serving role (the admin pool's permissive
policy was already added by #1810; an integration test asserts `gocell_app` still
cannot read cross-tenant). The **Write surface** is unaffected (read-only; no DML).
No new forge surface: the verifier carries no obligation BECAUSE it exposes no
tenant audit content (verdicts only — Hard by the result type's field set), so the
`CrossTenantVisibility` sealed funnel (#1760) is not widened. The new operator HTTP
surface is gated by AuthOperator (Basic + per-IP rate limit) on a loopback
AdminListener that is declared only when operator credentials are provisioned.

## Scope boundaries (explicit)

- **IN:** on-demand full per-`(namespace, tenant)` chain integrity verify via an
  operator HTTP endpoint + metrics/report; usable in corebundle (operator-creds
  gated) and demo (mem-backed verifier).
- **OUT:** startup full-fleet verify (every boot) — rejected as too slow as tenant
  count grows; the engine is programmatically callable if a future cell wants it.
- **OUT:** per-chain `tenant_id` as a metric label (cardinality); async/job-mode
  endpoint (synchronous is adequate at current audit volumes — revisit with a
  configurable timeout if real fleets exceed 30s).

## References

- Code: `framework/runtime/audit/ledger/{chain_verify_store,mem_chain_verify_store}.go`,
  `framework/runtime/audit/chain_verify_runner.go`,
  `adapters/postgres/{audit_verify_core,audit_chain_verify_store}.go`,
  `framework/runtime/bootstrap/{audit_chain_verify,options_audit_verify}.go`,
  `cellmodules/auditcore/module.go`, `cmd/corebundle/{admin_listener,bundle_options}.go`
- Amends: ADR `202606071300-1618` (Deliberate scoping — verify tool shipped),
  ADR `202606131900-1810` (Scope boundaries — #1755 shipped)
- Rule index: `.claude/rules/gocell/tenancy.md`
