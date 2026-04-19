# ADR: Config Value Encryption at Repository Boundary

**Date**: 2026-04-19  
**Status**: Accepted  
**Deciders**: platform team  
**Scope**: config-core sensitive=true values; extensible to access-core / audit-core

---

## Context

`config_entries.value` and `config_versions.value` store all config values — including
sensitive ones (API keys, passwords, tokens) — as plaintext in PostgreSQL. Any actor with
SELECT on those tables (replica reader, DBA, backup restore) reads secrets without audit.
PR-CC-FLAG-DURABLE (PR2) introduces durable flag storage; this PR (PR3) closes the remaining
plaintext storage gap.

### Threat model

| Threat | Current | Post-PR3 |
|--------|---------|----------|
| DB dump / backup restore leaks secrets | YES | No (ciphertext only) |
| Read-replica exposes secrets | YES | No |
| DBA SELECT on config_entries leaks secrets | YES | No |
| App-level SELECT (correct auth path) leaks secrets | No (handler redacts) | No |
| Key compromise exposes all values | N/A | Bounded to key version — rotation is lazy |

---

## Decision

**Option C** — KeyProvider abstraction + LocalAES (dev/CI) + VaultTransit (production)
dual implementation land in the same PR. AWS-KMS and GCP-KMS remain as interface-compatible
follow-up items (see backlog S14a).

### Rejected alternatives

| Option | Reason rejected |
|--------|----------------|
| A — application-level AES (static key, no rotation) | No rotation path; key compromise requires re-deploy |
| B — database-level pgcrypto | No rotation; requires DB privileges for all read paths; poor portability |
| C (chosen) | Clean rotation via DEK re-wrap; pluggable backend; production-grade Vault path |
| D — external KMS only (no local fallback) | Breaks dev/CI with no vault; requires real infra from day one |

---

## Architecture

### Envelope Encryption (per-row DEK + master KEK)

Each sensitive row gets an independently generated 32-byte Data Encryption Key (DEK).
The DEK encrypts the plaintext value using AES-GCM-256 producing `(ciphertext, nonce)`.
The master Key Encryption Key (KEK) then encrypts the DEK producing the encrypted DEK (`edk`).
All four artifacts are stored as separate columns.

```
plaintext value
      │
      ▼
[crypto/rand 32B DEK] ──AES-GCM-256──► (ciphertext, nonce)
      │
      ▼ KEK (master key, version-tagged)
[encrypted DEK (edk)]

Stored columns: value_cipher, value_nonce, value_edk, value_key_id
```

Benefits:
- Rotating the master KEK only requires re-wrapping `edk` (not re-encrypting the value).
- Each row has a unique nonce — even identical plaintexts produce distinct ciphertexts.
- `value_key_id` identifies which KEK version encrypted the DEK, enabling multi-key keyring.

ref: hashicorp/vault vault/barrier_aes_gcm.go@main  
ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/transformer.go

### KeyProvider interface

```go
// KeyProvider abstracts any KMS backend.
// ref: kubernetes/kubernetes staging/.../storage/value/transformer.go@master:L1-L40
type KeyProvider interface {
    Current(ctx context.Context) (KeyHandle, error)
    ByID(ctx context.Context, keyID string) (KeyHandle, error)
    Rotate(ctx context.Context) (newKeyID string, err error)
}

type KeyHandle interface {
    ID() string
    Encrypt(ctx context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, err error)
    Decrypt(ctx context.Context, ciphertext, nonce, edk, aad []byte) (plaintext []byte, err error)
}
```

Implementations:
- `LocalAESKeyProvider` — master KEK from `GOCELL_MASTER_KEY` env (32B hex/base64); per-row DEK from `crypto/rand`.
- `VaultTransitKeyProvider` — delegates encrypt/decrypt to Vault's transit engine; `Rotate()` calls the Vault rotate API.

### ValueTransformer thin wrapper

`ValueTransformer` calls `KeyProvider.Current()` for writes and `KeyProvider.ByID(keyID)` for reads.
It also computes the AAD (`cell:config-core/key:{configKey}`) to prevent ciphertext transplant attacks.

```go
type ValueTransformer interface {
    Encrypt(ctx context.Context, plaintext []byte, aad []byte) (ciphertext, keyID string, nonce, edk []byte, err error)
    Decrypt(ctx context.Context, ciphertext []byte, keyID string, nonce, edk, aad []byte) (plaintext []byte, err error)
}
```

`NoopTransformer` passes plaintext through unchanged — used for sensitive=false values.

---

## DDL Impact: Migration 008

```sql
ALTER TABLE config_entries
    ADD COLUMN IF NOT EXISTS value_cipher BYTEA,
    ADD COLUMN IF NOT EXISTS value_key_id VARCHAR(128),
    ADD COLUMN IF NOT EXISTS value_edk    BYTEA,
    ADD COLUMN IF NOT EXISTS value_nonce  BYTEA;
ALTER TABLE config_versions
    ADD COLUMN IF NOT EXISTS value_cipher BYTEA,
    ADD COLUMN IF NOT EXISTS value_key_id VARCHAR(128),
    ADD COLUMN IF NOT EXISTS value_edk    BYTEA,
    ADD COLUMN IF NOT EXISTS value_nonce  BYTEA;
```

The existing `value` column is retained as the plaintext channel for `sensitive=false`
entries and as a transitional buffer during the plaintext→encrypted migration. Once all
sensitive rows have been migrated and verified, `value` for sensitive rows stays NULL.

### Column semantics

| sensitive | value | value_cipher |
|-----------|-------|-------------|
| false | plaintext | NULL |
| true (new write) | NULL | ciphertext |
| true (legacy row, pre-migration) | plaintext | NULL |

---

## dev/prod Switching

```
GOCELL_KEY_PROVIDER=local-aes    → LocalAESKeyProvider (default dev/CI)
GOCELL_KEY_PROVIDER=vault-transit → VaultTransitKeyProvider (production)
(unset) + memory mode              → LocalAES with ephemeral random key (slog.Warn)
(unset) + postgres mode            → fail-fast (no silent plaintext in production)
```

When `GOCELL_KEY_PROVIDER=local-aes`:
- `GOCELL_MASTER_KEY` — required in postgres mode, 32 bytes hex or base64.
- `GOCELL_MASTER_KEY_PREVIOUS` — optional; enables decryption of values encrypted with the prior key.

When `GOCELL_KEY_PROVIDER=vault-transit`:
- `VAULT_ADDR`, `VAULT_TOKEN` (or AppRole) — standard Vault SDK env vars.
- `GOCELL_VAULT_TRANSIT_MOUNT` — default `transit`.
- `GOCELL_VAULT_TRANSIT_KEY` — default `gocell-config`.

---

## Security Guarantees: AAD Binding Equivalence

Both provider implementations bind the ciphertext to a row-specific context (AAD) to prevent
cross-row replay attacks where an attacker transplants the ciphertext from one row to another.

### LocalAESKeyProvider
Passes the AAD bytes directly as the AES-GCM additional authenticated data parameter.
AES-GCM authentication fails with a non-nil error if the AAD does not match.

### VaultTransitKeyProvider
Passes the AAD as the `context` field (base64-encoded) in the Vault Transit encrypt/decrypt
API call. Vault uses this context for HMAC binding — the decrypt call fails if the context
does not match what was provided during encryption.

Both providers therefore provide equivalent cross-row replay prevention:
- AAD = `cell:config-core/key:{configKey}` (constructed by `crypto.AADForConfig`)
- Mismatched AAD (e.g. transplanted ciphertext from key "db_password" to "api_key") → decrypt error → `ErrConfigDecryptFailed`

ref: hashicorp/vault builtin/logical/transit/path_encrypt.go — "context" field semantics

---

## Fail-Closed Policy

Decryption failures return `ErrConfigDecryptFailed`. There is no fallback to returning the
raw ciphertext or empty string. This prevents silent data corruption from appearing as
"empty config" to callers.

Legacy sensitive rows (sensitive=true, value_cipher IS NULL) also return `ErrConfigDecryptFailed`
to enforce fail-closed semantics — plaintext is never returned from un-migrated rows.

```go
if e.Sensitive {
    if len(valueCipher) == 0 || valueKeyID == nil {
        return nil, ErrConfigDecryptFailed  // legacy row — run plaintext_migration
    }
    plaintext, err = transformer.Decrypt(...)
    if err != nil {
        return nil, ErrConfigDecryptFailed  // never nil, never ciphertext
    }
}
```

ref: Kubernetes envelope.go — transformer failures are returned to caller, not silently skipped.

---

## Staleness Signal + Lazy Re-encrypt

When `entry.KeyID != provider.Current().ID()`, the entry was encrypted with an older key version.
The repository sets `entry.Stale = true` in the returned domain object. Upper layers can use this
signal to schedule lazy re-encryption without blocking the read path.

```go
currentID, _ := provider.Current(ctx)
if entry.KeyID != currentID.ID() {
    entry.Stale = true
}
```

ref: Kubernetes KMSv2 envelope.go L237-L252 — staleness-driven lazy rotation.  
ref: hashicorp/vault vault/barrier_aes_gcm.go — term/keyID carried alongside ciphertext for rotation awareness.

Bulk re-encryption is handled by `plaintext_migration.go` (admin CLI tool), which also
covers legacy rows where `value_cipher IS NULL AND sensitive=true`.

---

## Plaintext Migration Path

1. **Deploy migration 010** — adds 4 new nullable columns. Existing rows are unaffected.
2. **Dual-write period** — new writes from this PR use encrypted columns; legacy rows still have plaintext `value`.
3. **Run admin migration tool** — scans `value_cipher IS NULL AND sensitive=true`, encrypts in batches (deterministic `ORDER BY id`), writes cipher columns, sets `value = NULL`.
4. **Verify** — read each migrated key and confirm plaintext is returned (transparent decrypt).
5. **Rotate** (optional) — trigger `provider.Rotate()` to generate a new key version; subsequent reads detect staleness and re-encrypt lazily.

The tool is idempotent: re-running it skips rows already migrated (`value_cipher IS NOT NULL`).

---

## Rollback & Recovery Runbook

The migration is intentionally **not reversible in-place**: the `Down` section
of migration 010 raises a PostgreSQL exception instead of dropping the cipher
columns, because a `DROP COLUMN value_cipher` would destroy encrypted
sensitive values with no recovery path.

### If the migration's forward application fails mid-deploy

* Transactional safety: `ADD COLUMN IF NOT EXISTS` is idempotent. Simply
  re-run `migrator.Up(ctx)` after fixing the trigger (disk space, transient
  DDL lock) — no data loss.
* Confirm schema state: `SELECT column_name FROM information_schema.columns
  WHERE table_name = 'config_entries' AND column_name IN ('value_cipher',
  'value_key_id', 'value_edk', 'value_nonce')` — all four must appear.

### If you must undo migration 010 in a dev/CI environment

The embedded `RAISE EXCEPTION` blocks the goose `Down` path by design.
To reset a dev environment (for iterative testing only; never in production):

```sql
-- Run outside a transaction (goose no_transaction) to match migration 010.
ALTER TABLE config_entries
    RENAME COLUMN value_cipher TO value_cipher_deprecated_20260419;
ALTER TABLE config_entries
    RENAME COLUMN value_key_id TO value_key_id_deprecated_20260419;
ALTER TABLE config_entries
    RENAME COLUMN value_edk    TO value_edk_deprecated_20260419;
ALTER TABLE config_entries
    RENAME COLUMN value_nonce  TO value_nonce_deprecated_20260419;
-- Repeat for config_versions.
-- Then manually `DELETE FROM schema_migrations WHERE version = 10`.
```

Renaming (rather than dropping) preserves encrypted payload for post-mortem
while removing the column from the active schema — matching the "no silent
data loss" invariant.

### If a production deploy needs to be rolled forward instead of back

* Stop the `plaintext_migration` admin tool (it is resumable: `ORDER BY id`
  + `value_cipher IS NULL` filter makes successive runs deterministic).
* Re-deploy the previous binary (pre-010 code path). Pre-010 writes read the
  plaintext `value` column and ignore the new cipher columns — no data is
  lost, only newly-encrypted values become unreadable until the new binary
  is re-deployed.
* Fix the forward problem and re-deploy the new binary. The admin migration
  tool resumes where it stopped.

### Data-loss risk matrix

| Scenario | Data-loss risk | Action |
|---|---|---|
| Forward apply fails mid-migration | None (DDL idempotent) | Re-run `Up` |
| Need to undo in dev/CI | None (rename preserves) | Use rename SQL above |
| Roll back binary only | None | Re-deploy old binary; new-encrypted rows temporarily unreadable |
| Drop cipher columns in production | **TOTAL LOSS** of encrypted values | **Disallowed** — RAISE EXCEPTION enforces |

---

## Rotation Strategy

### VaultTransitKeyProvider (production)
`VaultTransitKeyProvider.Rotate()` calls the Vault Transit `transit/keys/{name}/rotate` API.
Vault persists all key versions server-side. Historical ciphertext (encoded with `vault:vN:...`)
remains decryptable after rotation because Vault routes decryption to the correct version via
the ciphertext prefix.

### LocalAESKeyProvider (dev/CI only)

`LocalAESKeyProvider.Rotate()` returns `ErrNotImplemented`. LocalAES key rotation is not
persistent — a new in-memory key is lost on restart, making all values encrypted with the
rotated key permanently unreadable. For rotation scenarios, use VaultTransitKeyProvider.

This design prevents accidental production rotation via LocalAES while keeping the dev
path simple (restart = fresh key).

#### LocalAES key-ID / version semantics

LocalAES uses a **two-version keyring** with fixed labels:

| Label | Env var | Role |
|---|---|---|
| `local-aes-v1` | `GOCELL_MASTER_KEY` (required) | Current (writes encrypt with this) |
| `local-aes-v0` | `GOCELL_MASTER_KEY_PREVIOUS` (optional) | Historical (reads only) |

The labels are intentionally **not** versioned beyond `v0/v1` — they are
opaque identifiers that bind a ciphertext row to the KEK material it was
encrypted under. Key material change lifecycle:

1. **Steady state**: writes use `v1`; reads accept `v1` only.
2. **Rotation prep**: operator copies current `GOCELL_MASTER_KEY` to
   `GOCELL_MASTER_KEY_PREVIOUS` and sets a fresh 32-byte key in
   `GOCELL_MASTER_KEY`, then restarts. Reads now accept both `v0` and `v1`.
3. **Historic decrypt window**: rows carrying `value_key_id = "local-aes-v0"`
   decrypt against the previous KEK; rows carrying `"local-aes-v1"` decrypt
   against the current. The `Stale=true` signal is emitted for any row still
   on `v0` so callers can trigger lazy re-encryption.
4. **Cleanup**: once all rows report `Stale=false` (i.e. have been rewritten
   on `v1`), the operator removes `GOCELL_MASTER_KEY_PREVIOUS` on the next
   restart. Any row still on `v0` at that point becomes permanently unreadable
   — the same failure mode as losing a production KEK.

**Limits vs production KMS**:
- No server-side key persistence. Losing the env var loses the key.
- No third version. An accidental two-step rotation (v1 → v2 → v3) within
  a single deploy window is not supported; rotate, wait for all rows to
  migrate, then rotate again.
- No rewrap API: the `Rotate()` method is `ErrNotImplemented` by design.
  Rotation is driven by operator env-var surgery + restart, not a runtime call.

**Why this is acceptable for dev/CI**: The threat model in dev/CI is
"prove the cell wiring, not production key custody". Production must use
VaultTransit (backlog S14a tracks full production wiring and AWS/GCP KMS
adapters).

---

## Future KMS Backends

The `KeyProvider` interface is designed to accommodate cloud KMS backends without changes:

| Backend | Status | Backlog Item |
|---------|--------|-------------|
| LocalAES | Implemented (dev/CI) | — |
| VaultTransit | Implemented (production) | — |
| AWS-KMS | Interface placeholder | S14a CONFIG-VALUE-KMS-AWS-PROVIDER-01 |
| GCP-KMS | Interface placeholder | (future) |

AWS-KMS implementation sketch:
```go
// AWSKMSKeyProvider wraps AWS KMS GenerateDataKey + Decrypt.
// Envelope encryption: AWS KMS generates DEK + returns encrypted copy.
// ref: aws/aws-sdk-go-v2 service/kms GenerateDataKeyInput
type AWSKMSKeyProvider struct {
    client  *kms.Client
    keyARN  string
    keyring map[string]string // keyID → keyARN (for historical decryption)
}
```

The `KeyHandle.Encrypt` for AWS-KMS calls `GenerateDataKey` (which returns both the plaintext
DEK and the encrypted DEK); `KeyHandle.Decrypt` calls `Decrypt` to recover the plaintext DEK,
then uses it for AES-GCM decryption locally. This matches the Vault Transit pattern exactly
(nonce/edk transparent to ValueTransformer).

---

## VaultTransit Adapter Design

Because Vault Transit manages encryption entirely server-side, it does not expose the raw nonce
or DEK. The `KeyHandle` contract allows `nonce` and `edk` to be nil or empty byte slices —
`ValueTransformer` treats them as opaque and stores them verbatim in the cipher columns.

For VaultTransit:
- `value_cipher` = Vault ciphertext string (`vault:vN:base64...`), stored as bytes.
- `value_key_id` = Vault key version extracted from the ciphertext prefix (e.g. `vault-transit:v2`).
- `value_edk` = nil (empty).
- `value_nonce` = nil (empty).

`ByID(keyID)` for VaultTransit: the full ciphertext already contains the key version, so
Vault routes decryption to the correct key automatically. The `keyID` parameter is used only
for staleness comparison.

ref: hashicorp/vault builtin/logical/transit/path_rewrap.go — Rewrap API for batch key rotation.

---

## Testing Strategy

| Layer | Coverage |
|-------|---------|
| Unit (`runtime/crypto/`) | KeyProvider interface contract, LocalAES envelope round-trip, AAD binding, nonce uniqueness, VaultTransit interface contract (mocked) |
| Integration (`//go:build integration`) | VaultTransit with real vault testcontainer; LocalAES with real pgx session |
| e2e (`//go:build e2e`) | 3-container compose (PG + RMQ + vault); sensitive write → DB verification → relay → subscriber |

---

## References

- `ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/transformer.go@master`
- `ref: hashicorp/vault vault/barrier_aes_gcm.go@main:L1199-L1233`
- `ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main`
- `ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127` (keyID version prefix)
