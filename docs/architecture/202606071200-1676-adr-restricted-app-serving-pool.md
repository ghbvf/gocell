# ADR: Restricted App-Serving PostgreSQL Pool (Dual-Role Deployment)

**Date**: 2026-06-07
**Status**: Accepted
**Issue**: #1676
**Relates to**: #1617 (PR-3b, FORCE RLS on access tables), EPIC #1337 (multi-tenant isolation)
**Spec**: `docs/plans/specs/1220-tenancy-abac-dataperm/` [F-B11]

---

## Context

PR-3a (#1341) and PR-3b (#1617) applied `FORCE ROW LEVEL SECURITY` and
`tenant_isolation` policies to all six tenant tables:

- config_entries / config_versions / feature_flags (migration 052)
- users / roles / role_assignments (migration 053)

The `tenant_isolation` policy evaluates `tenant_id = NULLIF(current_setting('app.tenant_id',
true), '')` on every row read or write. This is fail-closed: an unset or empty
`app.tenant_id` GUC matches no rows.

**Critical gap**: PostgreSQL ignores `FORCE ROW LEVEL SECURITY` for:

1. The table **owner** (the role that ran `CREATE TABLE`).
2. Any role that is a **superuser**.
3. Any role that has the **`BYPASSRLS`** attribute.

GoCell's existing single-role setup uses the admin role `gocell`, which is
the table owner and typically a superuser in development. The FORCE RLS
policies are correctly defined in the schema but are a **runtime no-op** for
this role. PR-3b explicitly deferred this as `[F-B11]`, guarded by integration
tests asserting `rolbypassrls=false` on the restricted role fixture, and tracked
in backlog #1676.

`schema_guard.verifyRLS` (the /readyz shape probe) validates that RLS policies
exist and are correctly formed on the expected tables. It does **not** verify
that the **serving connection role** lacks the ability to bypass those policies —
because the shape check runs under the migration/admin pool. Folding the role
capability check into `VerifyExpectedShape` would falsely fail every development
environment that uses the admin pool for convenience.

---

## Decision

### 1. Dual-role deployment model

Introduce two distinct PostgreSQL roles with separate concerns:

| Role | Used by | Attributes |
|------|---------|-----------|
| `gocell` | `pg-migrate` (admin/DDL pool) | Superuser, table owner; runs all migrations |
| `gocell_app` | `corebundle` serving pool | `NOSUPERUSER`, `NOBYPASSRLS`, non-owner; DML only |

`gocell_app` receives DML privileges via `ALTER DEFAULT PRIVILEGES FOR ROLE gocell
IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO gocell_app`
and `GRANT USAGE, SELECT ON SEQUENCES`. Because `gocell` creates every table (via
migrations), all current and future tables are automatically accessible to
`gocell_app` without per-table grants.

`gocell_app` is **not** the table owner and is **not** a superuser → PostgreSQL
applies `FORCE ROW LEVEL SECURITY` to every query it makes.

The restricted role is created by `deploy/postgres/init/10-restricted-role.sh`,
which the PostgreSQL container runs once on first data-directory initialisation
(via the `docker-entrypoint-initdb.d/` mechanism), before migrations.

`GOCELL_APP_PASSWORD` controls the role password. It must be set in the
environment before compose-up.

### 2. New readyz probe: `postgres_app_role_restricted_ready`

A new adapter-level probe in `adapters/postgres` queries:

```sql
SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user
```

The probe fails (unhealthy) when either `rolsuper` or `rolbypassrls` is true.

This probe is registered with a typed `healthz.ProbeName` constant
(`postgres.ProbeAppRoleRestrictedReady`, declared in `adapters/postgres/pool.go`)
and is included in the `PROBENAME-SEALED-FUNNEL-01` archtest golden inventory. It is an adapter-level probe (not cell-level), with a
distinct failure domain from:

- `postgres_ready` — connection liveness (bare pool Ping)
- `schema_guard.verifyRLS` — RLS policy shape (defined and correctly formed)
- `postgres_app_role_restricted_ready` — serving role capability (runtime bypass or not)

All three probes must be green to guarantee end-to-end tenant isolation.

---

## Why separate dimension from `VerifyExpectedShape` / `verifyRLS`

`schema_guard.VerifyExpectedShape` is called at startup and during /readyz under the
**migration pool** (`GOCELL_PG_DSN`, admin role `gocell`). Running the role-capability
check there would always return "superuser" in development and CI environments, producing
a permanent red /readyz for any developer who hasn't configured dual-role locally.

The serving-pool probe must execute under the **serving pool** connection
(per-cell DSNs `GOCELL_CONFIGCORE_DATABASE_URL` / `GOCELL_AUDITCORE_DATABASE_URL` /
`GOCELL_ACCESSCORE_DATABASE_URL`; deduped to one pool by `percellpg.Resolve`, #1964),
which uses the restricted role in dual-role deployments. This is why the probe is
registered in `adapters/postgres` for the serving pool specifically, not folded into
the shared admin pool health check.

---

## AI-robust tier

The probe itself is **Medium**:

- The role-capability check executes a real SQL query against `pg_roles` at
  readyz time — this is a runtime guard, not a type-system constraint.
- There is no purely compile-time mechanism to express "the serving DSN role must
  lack BYPASSRLS"; it requires a live DB query.

**Backstops that raise effective assurance**:

1. **`PROBENAME-SEALED-FUNNEL-01`** (Hard): probe name is a typed `healthz.ProbeName`
   constant; bare string registration is compile-time impossible.
2. **Integration tests** assert `rolbypassrls=false` on the test DB role fixture,
   ensuring the e2e harness always runs under the correct privilege model.
3. **Compose wiring**: `deploy/docker-compose.local.yml` and `tests/e2e/docker-compose.e2e.yaml`
   configure all three per-cell DSNs (`GOCELL_CONFIGCORE_DATABASE_URL`,
   `GOCELL_AUDITCORE_DATABASE_URL`, `GOCELL_ACCESSCORE_DATABASE_URL`) to use `gocell_app`,
   so the probe would immediately fail in CI if the restricted role were not created or
   misconfigured.

---

## Consequences

### Developer impact

- **New env var `GOCELL_APP_PASSWORD`** must be set alongside `PG_PASSWORD` in
  `.env.local` and in CI. `hack/scripts/gen-deploy-secrets.sh` emits it as a fresh
  `openssl rand -hex 16` value (URL-safe, like `PG_PASSWORD`: both are embedded in
  DSN userinfo where base64 `/`/`+`/`=` would break URL parsing).
- **One-time volume reset**: existing local environments have a `pgdata` volume
  created without the restricted role. Run `make local-down` (which passes `-v` to
  remove volumes) then `make local-up`. The initdb script runs on the fresh data dir.
- **Superuser dev /readyz**: if any per-cell DSN (`GOCELL_CONFIGCORE_DATABASE_URL`,
  `GOCELL_AUDITCORE_DATABASE_URL`, `GOCELL_ACCESSCORE_DATABASE_URL`) still points to the
  admin role `gocell`, the new probe turns red. This is the intended behaviour.

### Production deployment

- The serving application pool must connect as `gocell_app` (or an equivalent
  NOSUPERUSER+NOBYPASSRLS role). The admin role `gocell` is reserved for migration
  runs only.
- `deploy/postgres/init/10-restricted-role.sh` is a reference implementation for
  the role-creation step. Managed deployments (Kubernetes, RDS) should use their
  own equivalent (e.g. Terraform `aws_db_instance` + a one-time `CREATE ROLE`
  migration, or a Vault dynamic-credentials role with limited privileges).

### Monitoring

Alert `GoCellPostgresAppRoleNotRestricted` fires when `postgres_app_role_restricted_ready`
is down. This indicates RLS is not being enforced at runtime and is a **critical**
tenant-isolation regression. See `docs/ops/alerting-rules.md` §GoCellPostgresAppRoleNotRestricted.

---

## Alternatives considered

**A. Run the role-capability check inside `schema_guard.VerifyExpectedShape`**

Rejected: `VerifyExpectedShape` runs under the admin pool. It would always report
superuser in development, permanently breaking local /readyz. The serving-pool probe
must execute under the serving pool connection.

**B. Add a `NOBYPASSRLS` migration that demotes `gocell`**

Rejected: demoting the migration role is self-defeating — migrations require elevated
privileges for DDL. The dual-role model is the correct solution.

**C. Use PostgreSQL `SECURITY LABEL` to express the requirement**

Out of scope; adds infrastructure complexity for marginal benefit. The probe +
compose wiring is sufficient.

---

## Amendment 2026-06-13 — third role `gocell_audit_admin` (cross-tenant audit read, #1810)

#1810 adds a THIRD deployment role for the sanctioned super-admin cross-tenant
audit read. It is a **deliberate, bounded** addition to the dual-role model above
and does **not** weaken the core invariant ("the serving role has no way to read
across tenants").

| Role | Used by | Attributes |
|------|---------|-----------|
| `gocell_audit_admin` | auditcore admin read pool (`GOCELL_AUDIT_ADMIN_DSN`) | `NOSUPERUSER`, **`NOBYPASSRLS`**, non-owner; **SELECT on `audit_entries` only** |

**Why this is NOT a no-BYPASSRLS-invariant violation.** The role is itself
`NOBYPASSRLS`. Its cross-tenant visibility comes from a **role-scoped permissive
RLS policy** `audit_admin_read_all ON audit_entries FOR SELECT TO gocell_audit_admin
USING (true)` (migration 065), not from the `BYPASSRLS` attribute the §Context
threat model bans. The distinction is load-bearing:

- `BYPASSRLS` disables RLS for **every table and every query** the role makes —
  an opaque, blanket bypass invisible in `pg_policy`.
- A role-scoped permissive policy is **explicit, auditable, and narrow**: it is a
  row in `pg_policy` (so `schema_guard.verifyRLS` pins its exact shape), it applies
  to **exactly one table** (`audit_entries`) and **one command** (`SELECT`), and
  PostgreSQL still evaluates RLS for this role — the policy just evaluates to
  `true`. The role has no INSERT/UPDATE/DELETE and no access to any other RLS table.

`gocell_app`'s effective predicate and `FORCE ROW LEVEL SECURITY` are **byte-for-byte
unchanged** — a role-listed permissive policy is invisible to other roles. An
integration test asserts `gocell_app` still cannot read another tenant's audit rows
after migration 065.

**Provisioning.** `deploy/postgres/init/10-restricted-role.sh` also creates
`gocell_audit_admin` (password via `GOCELL_AUDIT_ADMIN_PASSWORD`, no fallback). The
role is **optional**: where it is not provisioned, migration 065 is a no-op
(role-guarded) and super-admin cross-tenant reads stay fail-closed at HTTP 501. The
admin pool is `NOBYPASSRLS`, so the existing `postgres_app_role_restricted_ready`
probe's intent (no BYPASSRLS in the deployment) is upheld for it too; the admin
pool contributes no `/readyz` probe of its own (close-only managed resource) to
avoid a `postgres_ready` probe-name collision with the serving pool.

**Threat-matrix re-evaluation (per ai-robust "ADR amendment 落地必查"):** the
§Context threat (1)(2)(3) enumeration is unchanged — (3) "any role with BYPASSRLS"
still bans BYPASSRLS, and `gocell_audit_admin` is NOBYPASSRLS, so it is outside
that threat by construction. The new privileged read is contained to one table /
one command on a separate role, gated upstream by the Hard sealed
`tenant.CrossTenantVisibility` funnel (#1760) + the mandatory FR-007 audit. See ADR
`202606071300-1618` Amendment 2026-06-13 and ADR
`202606131900-1810-adr-super-admin-cross-tenant-audit-read`.

## References

- `deploy/postgres/init/10-restricted-role.sh` — role-creation initdb script
- ADR `202606071300-1618` Amendment 2026-06-13 + ADR `202606131900-1810` — cross-tenant audit read (#1810)
- `docs/ops/readyz.md` §Adapter-level: serving-role capability probe
- `docs/ops/local-docker-deploy.md` §Dual-role PostgreSQL
- `docs/ops/alerting-rules.md` §GoCellPostgresAppRoleNotRestricted
- `docs/ops/env-vars.md` §Restricted serving-role credential
- `.claude/rules/gocell/tenancy.md` §#1676 (PR increment, Enforcement index)
- ADR `202605271100-adr-probename-sealed-funnel.md` — PROBENAME-SEALED-FUNNEL-01
- ADR `202605161030-adr-cell-repo-readyz-probe.md` — cell-level repo probe rationale
- PR-3a ADR (migration 052) + PR-3b ADR (migration 053/054) for FORCE RLS context
