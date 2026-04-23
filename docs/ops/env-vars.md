# GoCell Environment Variables Reference

This document lists all environment variables consumed by `cmd/corebundle` at startup.
Variables without a default value are **required** in the indicated adapter mode.
Missing required variables cause fail-fast before any assembly initialization.

## JWT Configuration (required in all modes)

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_JWT_ISSUER` | JWT `iss` claim written by JWTIssuer and verified by JWTVerifier on every authenticated request | — | **All modes** | Required regardless of `GOCELL_CELL_ADAPTER_MODE` or `GOCELL_ADAPTER_MODE`; there is no dev fallback. Missing this variable causes fail-fast at startup. |
| `GOCELL_JWT_AUDIENCE` | JWT `aud` claim written by JWTIssuer and verified by JWTVerifier on every authenticated request | — | **All modes** | Required regardless of `GOCELL_CELL_ADAPTER_MODE` or `GOCELL_ADAPTER_MODE`; there is no dev fallback. Must match the value expected by all session-login/refresh token consumers. Missing this variable causes fail-fast at startup. Note: `GOCELL_JWT_AUDIENCES` (comma-separated multi-value) is not yet implemented; when introduced, migration path and priority over `GOCELL_JWT_AUDIENCE` will be defined. |

## RSA Key Set (for JWT signing and verification)

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_JWT_PRIVATE_KEY` | PEM-encoded RSA private key for JWT signing | — (ephemeral in dev) | **Real mode** |
| `GOCELL_JWT_PUBLIC_KEY` | PEM-encoded RSA public key for JWT verification | — (derived in dev) | **Real mode** |
| `GOCELL_JWT_PREV_PUBLIC_KEY` | PEM-encoded previous RSA public key (rotation) | — (optional) | No |
| `GOCELL_JWT_PREV_KEY_EXPIRES` | RFC 3339 expiry for the previous public key | — | Only when `GOCELL_JWT_PREV_PUBLIC_KEY` is set |

## Service Token / Controlplane Guard

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_SERVICE_SECRET` | HMAC-SHA256 secret (≥ 32 bytes) for `ServiceTokenMiddleware` protecting `/internal/v1/*` | — | **Real mode** | Introduced in PR #AUTH-TRUST-BOUNDARY-160 (C6). Value is used as raw UTF-8 bytes (not base64-decoded). To generate: `openssl rand -base64 32`. Empty in dev mode disables the guard (Warn logged). |
| `GOCELL_SERVICE_SECRET_PREVIOUS` | Previous HMAC secret for zero-downtime rotation | — | No | Optional; tried after current secret fails verification. |

## Per-Cell Session and Cursor Keys

Each Cell reads its own env variables. The naming pattern is `GOCELL_<CELLID>_<RESOURCE>`.

### auditcore cell

| Variable | Purpose | Default (dev) | Required |
|---|---|---|---|
| `GOCELL_AUDITCORE_HMAC_KEY` | HMAC key for session HMAC chains | `dev-hmac-key-replace-in-prod!!!!` | **Real mode** |
| `GOCELL_AUDITCORE_CURSOR_KEY` | HMAC key for audit cursor codec | `corebundle-audit-cursor-key-32b!` | **Real mode** |
| `GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY` | Previous audit cursor key (rotation) | — | No |

### configcore cell

| Variable | Purpose | Default (dev) | Required |
|---|---|---|---|
| `GOCELL_CONFIGCORE_CURSOR_KEY` | HMAC key for config cursor codec | `corebundle-cfg-cursor-key--32bb!` | **Real mode** |
| `GOCELL_CONFIGCORE_CURSOR_PREVIOUS_KEY` | Previous config cursor key (rotation) | — | No |

### accesscore cell

| Variable | Purpose | Default (dev) | Required |
|---|---|---|---|
| `GOCELL_ACCESSCORE_CURSOR_KEY` | HMAC key for access cursor codec | `corebundle-access-cursor-key32!!` | **Real mode** |
| `GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY` | Previous access cursor key (rotation) | — | No |

## Encryption Key Provider (required when GOCELL_CELL_ADAPTER_MODE=postgres)

Each Cell that uses PostgreSQL reads its own DB and encryption env variables.

### configcore cell database

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_CONFIGCORE_DATABASE_URL` | PostgreSQL DSN for configcore | — | **postgres mode** |
| `GOCELL_CONFIGCORE_DATABASE_MAX_CONNS` | Max open connections | 10 | No |
| `GOCELL_CONFIGCORE_DATABASE_IDLE_TIMEOUT` | Idle connection timeout (e.g. `5m`) | `5m` | No |
| `GOCELL_CONFIGCORE_DATABASE_MAX_LIFETIME` | Max connection lifetime (e.g. `1h`) | `1h` | No |

### configcore cell encryption

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_CONFIGCORE_KEY_PROVIDER` | Selects the encryption backend for sensitive config values | — | **postgres mode** | `"local-aes"` (dev/CI) or `"vault-transit"` (production). Must be set when `GOCELL_CELL_ADAPTER_MODE=postgres`; startup fails fast otherwise. Memory mode does not encrypt. |
| `GOCELL_CONFIGCORE_MASTER_KEY` | 32-byte hex-encoded AES key for `local-aes` provider | — | When `GOCELL_CONFIGCORE_KEY_PROVIDER=local-aes` | Generate: `openssl rand -hex 32`. Real mode rejects well-known demo keys (case-insensitive hex comparison). |
| `GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS` | Previous master key for key rotation | — | No | Optional; enables decryption of values encrypted with the prior key during rotation window. |
| `VAULT_ADDR` | Vault server address | `https://127.0.0.1:8200` | When `GOCELL_CONFIGCORE_KEY_PROVIDER=vault-transit` | Standard Vault SDK env var. |
| `VAULT_TOKEN` | Vault authentication token | — | When `GOCELL_CONFIGCORE_KEY_PROVIDER=vault-transit` | Static token path; token renewal via LifetimeWatcher is automatic when the token is renewable. Real mode will require AppRole/K8s auth in a future release (A14). |
| `GOCELL_VAULT_TRANSIT_MOUNT` | Vault Transit secrets engine mount path | `transit` | No | |
| `GOCELL_VAULT_TRANSIT_KEY` | Vault Transit key name | `gocell-config` | No | |

## Observability / Monitoring

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_METRICS_TOKEN` | Bearer token for `/metrics` scraper authentication (`X-Metrics-Token` header) | — | **Real mode** |
| `GOCELL_READYZ_VERBOSE_TOKEN` | Bearer token for `/readyz?verbose` (exposes internal topology) | — | **Real mode** |

## Adapter Mode

| Variable | Purpose | Default | Accepted Values |
|---|---|---|---|
| `GOCELL_ADAPTER_MODE` | Selects secret-loading and fail-fast behaviour | `""` (dev/in-memory) | `""` (dev), `"real"` |
| `GOCELL_CELL_ADAPTER_MODE` | Selects the storage backend for Cell repositories | `""` (in-memory) | `""`, `"memory"`, `"postgres"` |

Note: the per-cell `GOCELL_<CELLID>_DATABASE_URL` variables (e.g. `GOCELL_CONFIGCORE_DATABASE_URL`) replace the old global `GOCELL_PG_DSN`. Each cell reads its own DSN at startup.

## State

| Variable | Purpose | Default |
|---|---|---|
| `GOCELL_STATE_DIR` | Directory for stateful files (e.g. initial admin credential on first run) | `/run/gocell` |
