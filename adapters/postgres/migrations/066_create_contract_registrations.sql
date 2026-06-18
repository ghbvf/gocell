-- Migration 066: create contract_registrations (+ contract_registration_events)
-- — the durable store for the runtime contract registry (303-US5, #2236). Until
-- now the registrycore cell backed submit/list with the in-mem kernel
-- registry.ContractRegistrar (US4 #2235), which persists nothing. This adds the
-- PG-backed store behind ports.Registry (the in-mem implementation conforms to the
-- same contract).
--
-- Two-table shape mirrors the kernel's two-truth model:
--   * contract_registrations         — projection of the CURRENT state per
--     registration (mutable: state/approver/updated_at advance on each
--     transition). One row per (tenant_id, id).
--   * contract_registration_events   — the APPEND-ONLY migration history (source
--     of truth). One row per state transition; seq is 1-based contiguous per
--     registration. from_state = '' is the zero sentinel of the initial submit
--     event (folding from '' reproduces the lifecycle). Never UPDATEd/DELETEd.
--
-- payload_schema is an opaque schema reference/hash recorded at submit (the
-- kernel does not interpret it) — TEXT, not JSONB.
--
-- Tenancy: tenant_id TEXT NOT NULL (canonical lowercase UUID at the app layer,
-- migration-050 convention). A registration id is unique only within a tenant, so
-- the composite PRIMARY KEY (tenant_id, id) — and (tenant_id, registration_id, seq)
-- for the history — covers Get (full PK), List (PK-prefix range scan
-- `WHERE tenant_id=$1 AND id > $2 ORDER BY id`) and History (PK-prefix
-- `WHERE tenant_id=$1 AND registration_id=$2 ORDER BY seq`), so NO secondary index
-- is added (same rationale as migration 059). FORCE ROW LEVEL SECURITY + the
-- tenant_isolation policy give the DB-kernel backstop: the predicate
-- `tenant_id = NULLIF(current_setting('app.tenant_id', true), '')` yields 0 rows
-- visible AND 0 rows insertable when the GUC is unset (fail-closed), the same shape
-- as policies/users/roles (migrations 059/053).
--
-- Append-only enforcement (history table): the serving role gocell_app must never
-- UPDATE or DELETE contract_registration_events — the DB engine, not application
-- discipline, is the unbypassable guard. deploy/postgres/init/10-restricted-role.sh
-- runs BEFORE migrations and default-grants gocell_app SELECT/INSERT/UPDATE/DELETE
-- on every future table, so this table is born with the two destructive privileges;
-- revoke them here. The role keeps SELECT (replay/History read) + INSERT (the
-- same-transaction append). The projection table stays fully mutable (transitions
-- UPDATE state/approver). The IF EXISTS guard makes the REVOKE a no-op where
-- gocell_app is not provisioned (dev/memory mode, template-build migration in
-- testmain). Mirrors migration 058 (projection_events).
--
-- [F-B11] DEPLOYMENT REQUIREMENT (provisioning, not schema): the application PG
-- role MUST NOT own these tables AND MUST NOT have BYPASSRLS (or be superuser).
-- FORCE ROW LEVEL SECURITY applies RLS even to the table OWNER, but a
-- BYPASSRLS/superuser role still bypasses ALL row security. Same as migration 059.
--
-- Non-destructive DDL (CREATE TABLE / ENABLE / FORCE / CREATE POLICY / REVOKE add
-- no column, drop nothing, rewrite no row): NO `+gocell forward-rebuild` annotation
-- and NO Go destructive permit. The Down section is a true reversible rollback.
--
-- ref: adapters/postgres/migrations/059_create_policies.sql (tenant_id + FORCE RLS shape, copied)
-- ref: adapters/postgres/migrations/058_create_projection_events.sql (append-only REVOKE shape, copied)
-- ref: confluent/schema-registry (append-only log) · pact-foundation/pact (immutable verification record)

-- +goose Up

CREATE TABLE contract_registrations (
    tenant_id      TEXT        NOT NULL,
    id             TEXT        NOT NULL,
    kind           TEXT        NOT NULL,
    payload_schema TEXT        NOT NULL DEFAULT '',
    submitter      TEXT        NOT NULL,
    approver       TEXT        NOT NULL DEFAULT '',
    state          TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

ALTER TABLE contract_registrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE contract_registrations FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON contract_registrations
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

CREATE TABLE contract_registration_events (
    tenant_id       TEXT        NOT NULL,
    registration_id TEXT        NOT NULL,
    seq             INTEGER     NOT NULL,
    from_state      TEXT        NOT NULL DEFAULT '',
    to_state        TEXT        NOT NULL,
    actor           TEXT        NOT NULL,
    reason          TEXT        NOT NULL DEFAULT '',
    occurred_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, registration_id, seq)
);

ALTER TABLE contract_registration_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE contract_registration_events FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON contract_registration_events
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- Append-only enforcement at the DB-engine layer for the history table. No-op
-- where gocell_app is absent (dev/memory/template); real REVOKE in deploy/e2e.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gocell_app') THEN
    REVOKE UPDATE, DELETE ON contract_registration_events FROM gocell_app;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Reversible: 066 INTRODUCES both tables, so DROP TABLE removes each tenant_isolation
-- policy and the RLS flags atomically with it. No pre-existing data is affected.
DROP TABLE IF EXISTS contract_registration_events;
DROP TABLE IF EXISTS contract_registrations;
