# ADR: ABAC policy PG persistence (string-coded JSONB + FORCE RLS)

- **Status**: Accepted
- **Date**: 2026-06-11
- **Issue**: #1346 (EPIC #1337 PR-8) — depends on PR-6 (#1344) policy model + ports + mem store, PR-7 (#1345) evaluation engine
- **Scope**: `corecells/accesscore` durable ABAC policy store

## Context

PR-6 delivered the ABAC policy domain model (`internal/abac`), the
`ports.PolicyRepository` interface, an in-memory implementation, and a
cross-implementation conformance suite plus the
`POLICYREPO-CONFORMANCE-ENROLLMENT-01` archtest (which forces any new
implementation to enroll in the suite). PR-7 made the evaluator read the store
fail-closed (store error → `Deny + KindUnavailable`). Until PR-8 the cell
hard-wired the in-memory store in both demo and PG modes, so there was no durable
policy store.

PR-8 adds the PostgreSQL implementation, routes it through the existing bundle
funnel, and closes the conformance loop. This ADR records the persistence-shape
decisions and re-states the security model for the new table.

## Decision

### D1 — Single `policies` table with a string-coded JSONB `rules` column

Each policy is one row keyed by the composite primary key `(tenant_id, id)`. The
rule list (Rules → Conditions → Obligations) is stored as a single JSONB `rules`
column using **explicit string enum codes** (`effect:"allow"`, `op:"eq"`,
`source:"subject"`, `rowScope:"self"`), produced by an adapter-local persistence
DTO + codec (`policy_codec.go`). The domain (`internal/abac`) and `pkg/authz`
enums are untouched.

**Rejected — direct `json.Marshal` of the domain structs (int ordinals).** The
`authz`/`abac` enums are iota-based `uint8`. Persisting ordinals means reordering
an iota const block silently changes the meaning of every stored policy
(`allow`↔`deny`) — a corruption that `Policy.Validate` cannot detect because both
values stay individually valid. For an authorization store this is unacceptable.

**Rejected — normalized tables** (`policies` / `policy_rules` /
`policy_conditions`). The access pattern is load-whole-aggregate (`GetByID` /
`ListByTenant`; the evaluator full-loads a tenant's policies), so joins +
multi-row reassembly add ~3× the code for no query benefit.

String codes are self-describing and reorder-proof, matching every reference
policy engine's persistence (AWS Cedar policy text, XACML XML URNs, OPA Rego,
OpenFGA JSON, Casbin text rows — none persist enum ordinals) and the in-repo
precedent (`roles.permissions` JSONB). Decode is **fail-closed**: an unknown code
errors (`ErrPGSchemaShape`), so a corrupt or forward-incompatible row fails the
policy load and the evaluator denies, rather than silently mis-deciding.

### D2 — FORCE ROW LEVEL SECURITY on `policies`

Migration 058 puts `policies` under `FORCE ROW LEVEL SECURITY` with the same
`tenant_isolation` predicate as users/roles (migration 053):
`tenant_id = NULLIF(current_setting('app.tenant_id', true), '')`. The table is
registered in `schema_guard.expectedRLSTables` (verified by `readyz` and the
`TestRLSForce_SchemaGuardVerifyRLS` integration test). Application-level
`WHERE tenant_id = $N` predicates are the primary isolation mechanism (what the
conformance suite asserts); RLS is the independent DB-kernel backstop.

### D3 — Upsert semantics

`Save` is an `INSERT … ON CONFLICT (tenant_id, id) DO UPDATE`, preserving
`created_at` and advancing `updated_at`. This matches the port contract
("upsert by (tenantID, policyID)") and the `roles` precedent. No duplicate
error code is minted.

### D4 — Readiness via composite, not a new cellgen probe

`ports.PolicyRepository` gains `RepoReady(ctx) error` (mem → nil; PG → a
lightweight `SELECT 1 FROM policies LIMIT 1`). cellgen emits exactly one
readiness probe per cell (`accesscore_repo_ready`, golden-locked by
`PROBENAME-SEALED-FUNNEL-01`), so rather than extend the codegen funnel to mint a
second probe, the cell registers a composite `healthz.RepoProber`
(`{sessionStore, policyRepo}`) through the existing `RegisterReadiness` funnel.
`accesscore_repo_ready` now means "session + policy repos ready" (fail-closed:
the first not-ready repo's error is returned). This honors observability.md
("cell repo readiness 由 cell 边界显式注册，禁止静默吞掉缺失 repo") with zero
codegen/golden change. A distinct `accesscore_policy_repo_ready` probe was
considered and rejected as a disproportionate framework-level cellgen change for
one cell.

## AI-robust ratings

| Mechanism | Carrier | Rating |
|-----------|---------|--------|
| Policy store wiring (`WithPolicyRepository` joins the bundle funnel) | sealed bundle (private fields) + unexported setter + `ACCESSCORE-BUNDLE-FUNNEL-01` forbidden-export archtest | **Hard** |
| Tenant typed parameter on every method | `tenant.TenantID` positional param (`TENANT-REPO-PARAM-FUNNEL-01`) | **Hard** |
| Durable JSON format (enum→code map + rule shape) | `policy_codec_test.go` golden bytes (diff on any code change) + exhaustive per-enum round-trip with anti-vacuity | **Medium** (exhaustiveness) + **Hard-on-diff** (golden) |
| New impl auto-joins conformance | `POLICYREPO-CONFORMANCE-ENROLLMENT-01` typed scan (pre-existing) | **Medium** (Go cannot require a test at compile time) |
| `policies` RLS shape | `schema_guard.verifyRLS` over `expectedRLSTables` + integration drop-policy negative test | **Medium** |

No new Soft mechanism is introduced; the codec's string format — the one implicit
constraint added by this PR — is closed in-PR by the golden + exhaustiveness guard.

## Security model (re-stated for the new table)

- **Cross-tenant isolation**: application `WHERE tenant_id` (primary) + FORCE RLS
  `tenant_isolation` (backstop). A GUC-unset connection sees 0 rows and can insert
  0 rows (fail-closed).
- **Corrupt/forward-incompatible row**: decode fail-closed → `ErrPGSchemaShape` →
  evaluator denies (consistent with PR-7 SC-004).
- **Deployment**: the app-serving PG role must be non-owner and lack `BYPASSRLS`
  (same requirement as migration 053; asserted by the restricted-role RLS
  integration suite).

## Consequences

- The policy store is now durable and wired in PG mode; demo mode keeps the mem
  store. Both pass an identical conformance suite (`RunPolicyRepoConformance`).
- Adding a new `abac`/`authz` enum value requires a codec entry, or the
  exhaustiveness test goes red — the format stays complete by construction.
- History/versioning remains deferred to PR-9 (policymanage); `created_at` /
  `updated_at` are infrastructure columns, not domain fields.

## References

- `corecells/accesscore/internal/adapters/postgres/policy_repo.go`, `policy_codec.go`, `policy_codec_test.go`
- `adapters/postgres/migrations/058_create_policies.sql`
- `docs/architecture/202606071300-1618-adr-audit-per-tenant-chain-rls.md` (RLS predicate lineage)
- `docs/plans/specs/1220-tenancy-abac-dataperm/` (tasks.md T8.1–T8.5)
- ref: AWS Cedar policy model; XACML 3.0 §5.8 (policy element)
