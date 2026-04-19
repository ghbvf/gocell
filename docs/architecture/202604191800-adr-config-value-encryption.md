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

## Fail-Closed Policy

Decryption failures return `ErrConfigDecryptFailed`. There is no fallback to returning the
raw ciphertext or empty string. This prevents silent data corruption from appearing as
"empty config" to callers.

```go
if value_cipher IS NOT NULL {
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

1. **Deploy migration 008** — adds 4 new nullable columns. Existing rows are unaffected.
2. **Dual-write period** — new writes from this PR use encrypted columns; legacy rows still have plaintext `value`.
3. **Run admin migration tool** — scans `value_cipher IS NULL AND sensitive=true`, encrypts in batches, writes cipher columns, sets `value = NULL`.
4. **Verify** — read each migrated key and confirm plaintext is returned (transparent decrypt).
5. **Rotate** (optional) — trigger `provider.Rotate()` to generate a new key version; subsequent reads detect staleness and re-encrypt lazily.

The tool is idempotent: re-running it skips rows already migrated (`value_cipher IS NOT NULL`).

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
