# ADR: syscore cell + aggregated cell-health endpoint (#1860)

- Status: Accepted
- Date: 2026-06-13
- Issue: #1860 (BR-001) — `GET /api/v1/admin/health/cells`
- Journey: J-systemhealth (experimental)

## Context

The frontend Health-overview page (gocell-web `/`, issue ghbvf/gocell-web#47) needs a
single endpoint that aggregates every cell's health, instead of probing each service.
Acceptance:

1. returns a `cells` array (per cell: id / live / ready / deps / adapters);
2. the contract schema lives under `contracts/http/**` so the frontend `pnpm codegen`
   can derive types;
3. the error envelope follows the canonical error-code shape.

The data the page needs already exists at runtime: `/readyz?verbose` (HealthListener)
aggregates `cells` (from `assembly.Health()`), `dependencies` (the `healthz.Aggregator`
probe snapshot) and `adapters` (static topology metadata) — but flat, token-gated, on a
no-JWT listener, and **not a published contract**. The data is fundamentally
runtime/framework-owned; **no business cell can produce cross-cell health without
framework injection** (a cell reaching across siblings violates the contract-only
boundary).

## Decision

### 1. A new `syscore` cell owns the contract

Acceptance #2 forces a real `contracts/http/**` codegen contract, and governance REF-03
requires a real `ownerCell`. Framework-owned ad-hoc endpoints (the projection-rebuild
`NewFrameworkHTTP` pattern) have **no contract.yaml / no codegen**, so they cannot satisfy
#2. Hosting the endpoint in an existing business cell (auditcore/configcore) would make a
domain cell expose cluster-wide health of its siblings — an ownership/layering violation.

So we add `corecells/syscore` — a dedicated, stateless system/observability cell (`type:
support`). It is the honest home for this and future admin/ops endpoints.

- **Consistency level L1, not L0.** TOPO-05 forbids an L0 cell from being a contract
  provider (only inbound webhook-receive is exempt) — an HTTP serving boundary is not pure
  computation. L1 is the minimum for a serving cell.
- **`schema.primary: cell_syscore` reserves a namespace it does not use.** FMT-06 requires
  non-L0 cells to declare a primary schema; syscore persists nothing today. The reserved
  schema is the framework's marker of a real boundary cell, not a DB dependency (no
  `requires: postgres`). This is the accepted cost of expressing a stateless serving cell
  in the L0/L1 model.

### 2. The cross-cell data is a framework-injected request-scoped `HealthView`

`runtime/syshealth.HealthView` is a read-only facade over the `assembly.CoreAssembly` +
`healthz.Aggregator` that **bootstrap already owns**. Bootstrap injects it into every
primary-listener request context via a middleware, mirroring the ABAC Authorizer funnel
(`WithPrimaryAuthorizer` → `AuthorizerFromContext` → `RequirePermission`). The syscore
handler reads it via `syshealth.HealthViewFromContext` and **fails closed (503)** on
absence — never a silent empty report.

syscore therefore never imports a sibling cell; it consumes a framework-provided aggregate,
exactly as cells consume adapters/clock. No new bootstrap option is added: the view is
unconditionally derivable from deps bootstrap holds by phase5.

### 3. Per-cell `deps` are structural; `adapters` are assembly-level

- **deps** (per cell): the cell's own readiness probes, taken structurally from
  `assembly.Snapshots()[cellID].Probes` (the probe set the cell registered at Init),
  joined with the aggregator snapshot for status/latency.
- **adapters** (assembly-level, NOT per-cell): the probes owned by **no** cell, computed by
  **structural set-difference** of the aggregator snapshot against the union of per-cell
  probe sets. These are the infrastructure/framework probes (`postgres_ready`,
  `redis_ready`, framework probes…).

Adapters are intentionally NOT per-cell. At runtime an adapter (e.g. the postgres pool) is a
process-global singleton with no per-cell attribution, and `requires:` is governance/CLI-only
metadata. Synthesising per-cell adapters would require either a `"postgres"→"postgres_ready"`
string mapping (a Soft carrier `ai-robust.md` forbids) or new typed registration machinery
modelling a fiction. The set-difference uses probe identity only — zero string mapping.

A cell's dep health **folds back into its own verdict** (review round 1, #1975 F1): a hard-down
dep (unhealthy/timeout) marks the cell `ready:false`/`status:unhealthy`; a degraded dep degrades
`status` but leaves lifecycle readiness intact — mirroring Kubernetes readiness, where a failed
dependency probe takes the workload out of ready rather than being a side annotation. `deps`
still lists every probe. `foldCellDeps` reuses the same severity rank as `overall`; the cell's
own `Health()` status floors `status` but does NOT flip `ready` (the ready axis is lifecycle+deps).

### 4. Response shape: a single composite resource, not a list

`{data: {overall, cells:[{id,live,ready,status,deps:[…]}], adapters:[…]}}`. `data` is an
**object**, so it is not classified as a list (governance FMT-15 would otherwise force
`hasMore`/`nextCursor` — meaningless cursor pagination over a bounded cell set). The
composite shape also carries an `overall` worst-case status the overview page needs.
(`live` is `true` for every registered cell in a started assembly — in-process liveness ≡
assembly-started; `ready`+`status` are the discriminating signals.)

### 5. Authorization: `system:read` permission

The route is gated by `auth.RequirePermission(authz.PermSystemRead())` on the PrimaryListener
(reachable by the browser via edge-bff with a JWT — the operator AdminListener is loopback
and unreachable). `system:read` is a new sealed permission in the closed registry; the PDP
baseline grants it to admin / super-admin (alongside `audit:read`). It is a first-class,
enumerable action distinct from audit-ledger access — observability access is not audit access.

## Security model

- **AuthN/AuthZ:** PrimaryListener JWT + `system:read` PDP gate. fail-closed at every step
  (no principal → 401; PDP deny / unwired → 403; HealthView absent / store down → 503).
- **Tenant isolation:** N/A by data shape — the report is runtime/global (cell + adapter
  health), carries no per-tenant rows, so there is no RowScope obligation to discharge. The
  permission gate is the sole control; there is no data-layer PEP because there is no
  tenant-scoped data. (Should syscore ever serve tenant-scoped data, RowScope governance
  applies independently, per tenancy.md.)
- **Redaction:** probe error text never reaches this wire — only `{name, status, durationMs}`
  is projected (the same wire-no-error-text invariant `/readyz?verbose` upholds).

## AI-robust enforcement

| Constraint | Carrier | Grade |
|------------|---------|-------|
| `system:read` exists + sole source | sealed `newPermission` minter + accessor func + `allPermissions` registry test | Hard |
| contract wire/types frozen | `gocell generate contract --verify` golden byte-diff | Hard |
| syscore in assembly cell closed set / metrics label | assembly.yaml → generated boundary + sealed metrics cell-label resolver | Hard |
| deps/adapters bucketing | structural probe-identity set-difference (no string mapping) — wrong bucketing fails the view unit test | Hard |
| HealthView ctx funnel — write seal | unexported `healthViewKey` (out-of-pkg write under the key is a compile error) | Hard |
| HealthView ctx funnel — callsite breadth | `SYSHEALTH-VIEW-CTX-FUNNEL-01` archtest: writer→`runtime/bootstrap`, reader→`corecells/syscore` healthread (funcs stay exported; reader has no Hard backstop) | Medium |
| HealthView fail-closed | handler returns typed 503 on ctx absence + regression test | Medium |

### Archtest registrations (new platform thing → existing allowlist/golden)

A new cell + contract + holder must enroll in the existing machine-checked registries
(extending sanctioned sets, not adding Soft mechanisms); review round 1 (#1975 F3) additionally
adds one new Medium callsite-breadth guard for the funnel:

- `goldenProbeNames()` += `corecells/syscore.ProbeRepoReady=syscore_repo_ready`
  (PROBENAME-SEALED-FUNNEL-01 inventory; the cellgen-emitted repo probe const is dead
  for this stateless cell but the golden tracks every declared ProbeName).
- `healthzHolderAllowlist` += `runtime/syshealth.view` (HEALTHZ-WRITE-01/A3 sanctioned
  Aggregator-holder set; read-only, never calls Register).
- `resourceReadProjectionCarveOut` += `http.admin.health.cells.v1`
  (RESOURCE-PROJECTION-COVERAGE-01). **Carve-out rationale:** the read is runtime/global
  observability state — non-tenant, no PII, no per-tenant rows, composite non-tabular
  body — so there is no maskable column axis. Routing it through the tenant column-masking
  funnel (`responseProjection`) would be an identity-mask fiction. The carve-out is the
  rule's sanctioned "genuinely non-maskable resource" branch; this ADR is its registry.
- `tools/archtest/syshealth_view_funnel_test.go` (**new**, review round 1 #1975 F3) owns
  `SYSHEALTH-VIEW-CTX-FUNNEL-01` — the production-callsite breadth guard for the HealthView
  funnel: writer `syshealth.WithHealthView` allowlisted to `runtime/bootstrap/phases_http.go`,
  reader `syshealth.HealthViewFromContext` to `corecells/syscore/slices/healthread/service.go`
  (`_test.go` exempt; anti-vacuity + empty-allowlist red self-check). It enforces the
  "sole injector / sole reader" claim the unexported key alone does not: the key seals the
  WRITE under it, not which production files may CALL the exported funcs. The reader side has
  no Hard backstop; the Hard-ization path (route-group-scoped injection or a sealed injector
  token that makes a production view unconstructible outside bootstrap) is backlog-tracked.

## Alternatives rejected

- **Framework-owned endpoint** (`NewFrameworkHTTP`, like projection-rebuild): no
  contract.yaml/codegen → fails acceptance #2 (frontend codegen). Rejected.
- **Host in auditcore/configcore:** a business cell exposing cluster-wide sibling health —
  ownership/layering violation. Rejected.
- **Per-cell adapters:** only achievable via a forbidden Soft string mapping or a fictional
  per-cell registration of a globally-shared resource. Rejected in favour of assembly-level
  adapters (decision §3).

## References

- Listener topology: `docs/ops/listener-topology.md`
- ABAC wiring: `docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md`,
  `.claude/rules/gocell/tenancy.md`
- readyz four-channel model: `runtime/http/health/health.go`
