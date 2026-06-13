-- Migration 063: create the global `webhook_sources` table — durable, encrypted
-- store for webhook source HMAC secrets (KERNEL-WEBHOOK-01 follow-up, issue #1540).
-- Until now the kernel webhook.SourceRegistry was in-memory only (eager-registered
-- at startup, immutable at runtime; secrets hardcoded at the composition root).
-- This adds the PG-backed persistence behind the same kernel webhook.SourceStore
-- seam: the composition root (cellmodules/webhooksource) loads every row at boot,
-- decrypts each secret through the sealed kernel funnel, and serves them from a
-- SourceRegistry snapshot.
--
-- Global, NOT tenant-scoped: the kernel SourceStore.Lookup(id) carries no tenant,
-- so webhook sources are platform-global identities (a sender such as "github" or
-- "stripe" is one source for the whole deployment). The source_id alone is the
-- primary key. There is therefore NO tenant_id column and NO row-level security —
-- the same posture as the global outbox_entries relay table (001). Cross-tenant
-- secret leakage is structurally impossible because there is no tenant dimension.
--
-- Encryption shape (NO plaintext column): unlike config_entries (which keeps a
-- `value` TEXT column for non-sensitive entries), every webhook source secret is
-- encrypted, so this table has NO plaintext column at all — value_cipher is
-- NOT NULL. A plaintext secret is structurally unrepresentable at rest: there is
-- nowhere to put one. The cipher tuple mirrors config_entries' envelope columns
-- (010): value_cipher (AES-GCM ciphertext), value_key_id (KEK version that wrapped
-- the DEK), value_edk (wrapped data key), value_nonce (per-encryption AEAD nonce).
-- value_edk / value_nonce are nullable because they are provider-specific (a
-- backend that embeds the nonce in the ciphertext returns none); value_cipher and
-- value_key_id are always present for an encrypted row. The encrypt-time AAD binds
-- each ciphertext to its source_id (kernel webhook.sourceAAD), so a ciphertext
-- cannot be transplanted across rows or into config_entries' AAD domain.
--
-- Access pattern: the loader full-loads every row at boot (LoadAll); writes are
-- per-source Upsert/Delete at provisioning time. The PRIMARY KEY (source_id)
-- covers every path, so NO secondary index is added (a brand-new, small table
-- with a covering PK needs none).
--
-- Non-destructive DDL (CREATE TABLE webhook_sources introduces a new relation,
-- drops nothing, rewrites no row): NO `+gocell forward-rebuild` annotation and NO
-- Go destructive permit. The Down section is a true reversible rollback (it drops
-- the table this migration introduced; no pre-existing data is touched).
--
-- [F-B11] DEPLOYMENT REQUIREMENT (provisioning, not enforced by this migration):
-- the application PG role MUST be granted read/write on webhook_sources. Same
-- grant model as the other global table outbox_entries. The startup LoadAll call
-- in cellmodules/webhooksource.LoadSourceStore serves as an implicit permission
-- smoke-test: a missing GRANT surfaces as a fail-fast startup error, not a latent
-- runtime fault.
--
-- ref: adapters/postgres/migrations/010_add_config_value_cipher.sql (envelope columns)
-- ref: adapters/postgres/migrations/001_create_outbox_entries.sql (global, no tenant/RLS)
-- ref: docs/architecture/...-1540-adr-webhook-source-persistence.md

-- +goose Up

CREATE TABLE webhook_sources (
    source_id    TEXT                     NOT NULL,
    value_cipher BYTEA                    NOT NULL,
    value_key_id VARCHAR(128)             NOT NULL,
    value_edk    BYTEA,
    value_nonce  BYTEA,
    created_at   timestamptz              NOT NULL DEFAULT now(),
    updated_at   timestamptz              NOT NULL DEFAULT now(),
    PRIMARY KEY (source_id)
);

-- +goose Down
-- Reversible: 063 INTRODUCES the table, so DROP TABLE removes it (and its encrypted
-- rows) atomically. No pre-existing data is affected. Re-seeding the secrets is the
-- operator's responsibility after a forward re-apply.
DROP TABLE IF EXISTS webhook_sources;
