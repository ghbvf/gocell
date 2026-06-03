-- Migration 051: rebuild configcore tables (config_entries / config_versions / feature_flags)
-- with tenant_id for multi-tenancy isolation (EPIC #1337 PR-2b, issue #1479).
--
-- Rationale (per CLAUDE.md "Review 和重构时不考虑向后兼容——当前只有 gocell 自身"):
--   No production deployment exists; dev data loss is acceptable. The
--   drop+recreate pattern mirrors migration 050 (accesscore tenant_id rebuild)
--   and migration 043 (audit_entries v2). All application-layer invariants
--   and constraint semantics are preserved and ported to be per-tenant-scoped.
--
-- Up deployment runbook (README §规则6 — destructive forward-rebuild):
--   This is a destructive forward-rebuild (DROP+CREATE; the old single-tenant
--   binary cannot write the new composite-keyed schema). The forward-only
--   deployment order is:
--     1. Drain traffic (stop the old binary / take the listener out of rotation).
--     2. goose up (this migration — rebuilds config_entries/config_versions/feature_flags).
--     3. Deploy the new tenant-aware binary.
--     4. Restore traffic.
--   No mixed-version window is safe: step 2 invalidates the old binary's writes,
--   so the old binary MUST be fully drained before step 2 (no rolling overlap).
--   GoCell has no production deployment today (pre-v1.0, no external consumers),
--   so in practice a dev reset = drop the DB and re-run from migration 001 up;
--   the sequence above is documented for the eventual GA deployment.
-- Down authorization + rollback order: forward-only, NO goose Down rollback.
--   The destructive-down gate is a Go-side typed permit (Migrator.Down +
--   DestructiveDownPermit, issue #1248), NOT a SQL GUC. Rollback in a
--   hypothetical prod = restore from backup; in dev = drop + migrate up from 001.
--   See the goose Down section at the bottom for the irreversibility warning.
--
-- Schema changes (config_entries):
--   - Add tenant_id TEXT NOT NULL (no DEFAULT — tables start empty after rebuild).
--   - PK stays (id TEXT) — config entries are globally unique by opaque ID.
--   - REPLACE global UNIQUE index on key with composite UNIQUE (tenant_id, key)
--     so the same config key can exist in different tenants.
--   - Tenant-prefixed keyset index: (tenant_id, key, id) replaces (key, id).
--   - Keep all 004+010 columns: value, sensitive, version, created_at, updated_at,
--     value_cipher, value_key_id, value_edk, value_nonce (cipher columns nullable).
--
-- Schema changes (config_versions):
--   - Add tenant_id TEXT NOT NULL.
--   - REPLACE global UNIQUE (config_id, version) with (tenant_id, config_id, version).
--   - FK config_id REFERENCES config_entries(id) retained (PK reference is stable).
--   - Tenant-prefixed keyset index: (tenant_id, config_id, version DESC).
--   - Retain single-column config_id index (eq-lookup optimization, migration 006).
--   - Keep all 004+010 columns: value, sensitive, published_at,
--     value_cipher, value_key_id, value_edk, value_nonce (cipher columns nullable).
--
-- Schema changes (feature_flags):
--   - Add tenant_id TEXT NOT NULL.
--   - REPLACE global UNIQUE index on key with composite UNIQUE (tenant_id, key).
--   - Tenant-prefixed keyset index: (tenant_id, key, id) replaces (key, id).
--   - Keep all 008+009 columns: enabled, rollout_percentage, description, version,
--     created_at, updated_at. rollout_percentage CHECK (0..100) preserved.
--
-- ref: adapters/postgres/migrations/050_accesscore_tenant_id.sql (drop+recreate pattern)
-- ref: adapters/postgres/migrations/043_audit_entries_v2.sql (drop+recreate pattern)
-- ref: adapters/postgres/migrations/004_create_config_entries_and_versions.sql (original schema)
-- ref: adapters/postgres/migrations/010_add_config_value_cipher.sql (cipher columns)
-- ref: adapters/postgres/migrations/008_create_feature_flags.sql (feature_flags schema)

-- +goose Up
-- Forward-rebuild gate is enforced in Go (Migrator.ForwardRebuild + ForwardRebuildPermit); see issue #1248.
-- One annotation line per rebuilt table (multi-target forward-rebuild, see ADR-1122 amendment).
-- +gocell forward-rebuild target=config_entries
-- +gocell forward-rebuild target=config_versions
-- +gocell forward-rebuild target=feature_flags

-- -----------------------------------------------------------------------
-- 1. Drop dependent objects in reverse dependency order
-- -----------------------------------------------------------------------

-- config_versions references config_entries via FK; drop it first.
-- Drop all indexes explicitly before dropping the tables.
DROP INDEX IF EXISTS idx_config_versions_config_id;
DROP INDEX IF EXISTS idx_config_versions_config_version;
DROP TABLE IF EXISTS config_versions;

DROP INDEX IF EXISTS idx_config_entries_key_id;
DROP TABLE IF EXISTS config_entries;

-- feature_flags has no FK dependencies; drop independently.
DROP INDEX IF EXISTS idx_feature_flags_key_id;
DROP TABLE IF EXISTS feature_flags;

-- -----------------------------------------------------------------------
-- 2. Recreate config_entries with tenant_id
-- -----------------------------------------------------------------------

CREATE TABLE config_entries (
    id              TEXT        NOT NULL,
    tenant_id       TEXT        NOT NULL,
    key             TEXT        NOT NULL,
    value           TEXT        NOT NULL DEFAULT '',
    sensitive       BOOLEAN     NOT NULL DEFAULT false,
    version         INT         NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Migration 010: cipher columns for sensitive value encryption (nullable).
    -- sensitive=false: value=plaintext, cipher columns=NULL.
    -- sensitive=true:  value='', cipher columns populated.
    value_cipher    BYTEA,
    value_key_id    VARCHAR(128),
    value_edk       BYTEA,
    value_nonce     BYTEA,
    CONSTRAINT config_entries_pk PRIMARY KEY (id)
);

-- Composite unique per-tenant: same config key can exist in different tenants.
CREATE UNIQUE INDEX idx_config_entries_tenant_key
    ON config_entries (tenant_id, key);

-- Tenant-prefixed keyset pagination index for LIST queries.
-- Replaces old (key, id) index from migration 004.
CREATE INDEX idx_config_entries_key_id
    ON config_entries (tenant_id, key ASC, id ASC);

-- -----------------------------------------------------------------------
-- 3. Recreate config_versions with tenant_id
-- -----------------------------------------------------------------------

CREATE TABLE config_versions (
    id              TEXT        NOT NULL,
    tenant_id       TEXT        NOT NULL,
    config_id       TEXT        NOT NULL REFERENCES config_entries(id),
    version         INT         NOT NULL,
    value           TEXT        NOT NULL DEFAULT '',
    sensitive       BOOLEAN     NOT NULL DEFAULT false,
    published_at    TIMESTAMPTZ,
    -- Migration 010: cipher columns for sensitive value encryption (nullable).
    value_cipher    BYTEA,
    value_key_id    VARCHAR(128),
    value_edk       BYTEA,
    value_nonce     BYTEA,
    CONSTRAINT config_versions_pk PRIMARY KEY (id)
);

-- Composite unique per-tenant: (tenant_id, config_id, version) replaces (config_id, version).
CREATE UNIQUE INDEX idx_config_versions_tenant_entry_version
    ON config_versions (tenant_id, config_id, version);

-- Tenant-prefixed descending version index (GetVersion + rollback history).
-- Replaces old (config_id, version DESC) index from migration 004.
CREATE INDEX idx_config_versions_config_version
    ON config_versions (tenant_id, config_id ASC, version DESC);

-- Single-column config_id eq-lookup index (migration 006 pattern, preserved).
CREATE INDEX idx_config_versions_config_id
    ON config_versions (config_id);

-- -----------------------------------------------------------------------
-- 4. Recreate feature_flags with tenant_id
-- -----------------------------------------------------------------------

CREATE TABLE feature_flags (
    id                  TEXT        NOT NULL,
    tenant_id           TEXT        NOT NULL,
    key                 TEXT        NOT NULL,
    enabled             BOOLEAN     NOT NULL DEFAULT false,
    rollout_percentage  INT         NOT NULL DEFAULT 0,
    description         TEXT        NOT NULL DEFAULT '',
    version             INT         NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_feature_flags PRIMARY KEY (id),
    CONSTRAINT feature_flags_rollout_percentage_range CHECK (rollout_percentage BETWEEN 0 AND 100)
);

-- Composite unique per-tenant: same feature flag key can exist in different tenants.
CREATE UNIQUE INDEX uq_feature_flags_tenant_key
    ON feature_flags (tenant_id, key);

-- Tenant-prefixed keyset pagination index for LIST queries.
-- Replaces old (key, id) index from migration 009.
CREATE INDEX idx_feature_flags_key_id
    ON feature_flags (tenant_id, key ASC, id ASC);

-- +goose Down
-- FORWARD-ONLY MIGRATION — NO ROLLBACK SUPPORTED.
--
-- This migration is irreversible / destructive. Rolling back to the pre-051
-- single-tenant schema is NOT supported via goose Down:
--   - The down section drops all v2 tables (config_entries / config_versions /
--     feature_flags) and ALL multi-tenant configcore data is permanently deleted.
--   - Restoring the pre-051 schema in production requires a backup restore.
--   - Dev environments must re-run from migration 001 forward (drop + migrate up).
--
-- Per CLAUDE.md "不考虑向后兼容": GoCell is pre-v1.0 with no external consumers;
-- destructive-rebuild is the accepted pattern (mirrors migration 050 accesscore,
-- migration 043 audit_entries_v2).
--
-- WARNING: Running goose Down on this migration in any environment with data
-- WILL PERMANENTLY DELETE all config_entries, config_versions, and feature_flags.
--
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP INDEX IF EXISTS idx_feature_flags_key_id;
DROP INDEX IF EXISTS uq_feature_flags_tenant_key;
DROP TABLE IF EXISTS feature_flags;

DROP INDEX IF EXISTS idx_config_versions_config_id;
DROP INDEX IF EXISTS idx_config_versions_config_version;
DROP INDEX IF EXISTS idx_config_versions_tenant_entry_version;
DROP TABLE IF EXISTS config_versions;

DROP INDEX IF EXISTS idx_config_entries_key_id;
DROP INDEX IF EXISTS idx_config_entries_tenant_key;
DROP TABLE IF EXISTS config_entries;
