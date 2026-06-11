-- Migration 058: create the accesscore `policies` table — durable store for ABAC
-- Policy aggregates (EPIC #1337 PR-8, issue #1346). Until now the policy store
-- was in-memory only (PR-6 #1344 mem.PolicyRepository); this adds the PG-backed
-- implementation behind the same ports.PolicyRepository conformance contract.
--
-- Storage shape: one row per (tenant_id, policy_id); the rule list (Rules →
-- Conditions → Obligations) is stored as a single JSONB column `rules` using a
-- string-coded persistence format (effect:"allow", op:"eq", source:"subject",
-- rowScope:"self") — reorder-proof, self-describing, and fail-closed on an
-- unknown code at decode. This mirrors `roles.permissions JSONB` (migration 019)
-- and the universal policy-engine convention (Cedar text / XACML URNs / OPA Rego
-- / OpenFGA JSON), none of which persist enum ordinals. See the PR-8 ADR
-- (docs/architecture/...-abac-policy-pg-persistence.md).
--
-- Access pattern: GetByID / Delete are full-PK lookups, ListByTenant is a
-- PK-prefix scan, and the eval engine (PR-7) full-loads all of a tenant's
-- policies. The composite PRIMARY KEY (tenant_id, id) covers every path, so NO
-- secondary index is added (the F-B5 CREATE INDEX CONCURRENTLY guidance targets
-- large-table secondary indexes; a brand-new empty table with a covering PK
-- needs none, and CONCURRENTLY cannot run inside the migration transaction).
--
-- Tenancy: tenant_id TEXT NOT NULL (canonical lowercase UUID at the app layer,
-- migration-050 convention). FORCE ROW LEVEL SECURITY + the tenant_isolation
-- policy give the same fail-closed DB-kernel backstop as users/roles (migration
-- 053): the predicate `tenant_id = NULLIF(current_setting('app.tenant_id', true),
-- '')` yields 0 rows visible AND 0 rows insertable when the GUC is unset.
--
-- Non-destructive DDL (CREATE TABLE / ENABLE / FORCE / CREATE POLICY add no
-- column to an existing table, drop nothing, rewrite no row): NO
-- `+gocell forward-rebuild` annotation and NO Go destructive permit. The Down
-- section is a true reversible rollback (DROP TABLE removes the table and its
-- RLS policy together; no pre-existing data is touched).
--
-- [F-B11] DEPLOYMENT REQUIREMENT (NOT enforced by this migration — role/GRANT is
-- provisioning, not schema): the application PG role MUST NOT own this table AND
-- MUST NOT have BYPASSRLS (or be superuser). FORCE ROW LEVEL SECURITY applies RLS
-- even to the table OWNER, but a BYPASSRLS/superuser role still bypasses ALL row
-- security. Same constraint as migration 053.
--
-- ref: adapters/postgres/migrations/053_accesscore_tenant_rls_force.sql (RLS shape, copied)
-- ref: adapters/postgres/migrations/019_roles.sql (roles.permissions JSONB precedent)
-- ref: docs/plans/specs/1220-tenancy-abac-dataperm/ (tasks.md T8.1)

-- +goose Up

CREATE TABLE policies (
    tenant_id   TEXT        NOT NULL,
    id          TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    rules       JSONB       NOT NULL,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

ALTER TABLE policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE policies FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON policies
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- +goose Down
-- Reversible: dropping the table removes its tenant_isolation policy and RLS
-- flags with it. No pre-existing data is affected (the table is introduced here).
DROP TABLE IF EXISTS policies;
