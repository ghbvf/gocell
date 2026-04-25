# GoCell Environment Variables Reference

This document lists all environment variables consumed by `cmd/corebundle` at startup.
Variables without a default value are **required** in the indicated adapter mode.
Missing required variables cause fail-fast before any assembly initialization.

## JWT Configuration (required in all modes)

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_JWT_ISSUER` | JWT `iss` claim written by JWTIssuer and verified by JWTVerifier on every authenticated request | — | **All modes** | Required regardless of `GOCELL_CELL_ADAPTER_MODE` or `GOCELL_ADAPTER_MODE`; there is no dev fallback. Missing this variable causes fail-fast at startup. |
| `GOCELL_JWT_AUDIENCE` | JWT `aud` claim written by JWTIssuer and verified by JWTVerifier on every authenticated request | — | **All modes** | Required regardless of `GOCELL_CELL_ADAPTER_MODE` or `GOCELL_ADAPTER_MODE`; there is no dev fallback. Must match the value expected by all `sessionlogin` and `sessionrefresh` token consumers. Missing this variable causes fail-fast at startup. Note: `GOCELL_JWT_AUDIENCES` (comma-separated multi-value) is not yet implemented; when introduced, migration path and priority over `GOCELL_JWT_AUDIENCE` will be defined. |

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
| `GOCELL_SERVICE_SECRET` | HMAC-SHA256 secret (≥ 32 bytes) for `ServiceTokenMiddleware` protecting `/internal/v1/*` | — | **Real mode** | Introduced in PR #AUTH-TRUST-BOUNDARY-160 (C6). Value is used as raw UTF-8 bytes (not base64-decoded); any UTF-8 string of ≥ 32 bytes is acceptable. Recommended generators: `openssl rand -base64 32` → 44 printable chars (base64 padded), used as raw bytes; `openssl rand -hex 32` → 64 hex chars, used as raw bytes. Both meet the 32-byte minimum. Empty in dev mode disables the guard (Warn logged). PR-A25: when the guard is installed, an in-memory replay-defense `NonceStore` is wired automatically so a captured token cannot be replayed within its 5 min validity window. Real-mode startup fails fast with `ERR_CONTROLPLANE_SERVICE_SECRET_MISSING` if the env var is empty, or `ERR_CONTROLPLANE_NONCE_STORE_MISSING` if the guard was somehow wired without a replay-safe store. Multi-pod deployments must inject a shared store (e.g. Redis) via `auth.WithServiceTokenNonceStore`; the in-memory default only protects against replay on a single pod. |
| `GOCELL_SERVICE_SECRET_PREVIOUS` | Previous HMAC secret for zero-downtime rotation | — | No | Optional; tried after current secret fails verification. |
| `GOCELL_SINGLE_POD` | Acknowledges that the deployment is single-pod and in-memory replay protection is sufficient | — | **Real mode** (when using default in-memory NonceStore) | Must be `1` in single-pod real-mode deployments to acknowledge in-memory replay defence scope; otherwise startup fails fast with `ERR_CONTROLPLANE_NONCE_STORE_MISSING`. Multi-pod deployments leave unset and inject a distributed NonceStore via `auth.WithServiceTokenNonceStore` (e.g. Redis). |

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

### accesscore first-admin provisioning

| Variable | Purpose | Default | Accepted Values |
|---|---|---|---|
| `GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE` | Selects first-admin ownership in `cmd/corebundle`. `interactive` leaves `/api/v1/access/setup/admin` as the one-shot owner; `bootstrap` enables the lifecycle that creates a random initial admin password and writes the credential file under `GOCELL_STATE_DIR`. Unknown non-empty values fail fast at startup. | `interactive` | `""`, `"interactive"`, `"bootstrap"` |

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
| `VAULT_ADDR` | Vault server address | — | When `GOCELL_CONFIGCORE_KEY_PROVIDER=vault-transit` | Standard Vault SDK env var. No default; missing value fails fast in **all modes** when the vault-transit provider is selected (not just real mode). |
| `VAULT_NAMESPACE` | Vault namespace (HCP Vault / Vault Enterprise multi-tenancy) | — | No (default = root namespace) | Standard Vault SDK env var. Applied via `client.SetNamespace` before any Vault I/O so Login + datakey + decrypt + key reads + rotate all carry the `X-Vault-Namespace` header. |
| `VAULT_AUTH_METHOD` | Vault auth method | — | When `GOCELL_CONFIGCORE_KEY_PROVIDER=vault-transit` | **Required, no default.** Accepted values: `token` (dev/CI only, rejected in real mode), `approle`, `kubernetes`. |
| `VAULT_TOKEN` | Static Vault token for `VAULT_AUTH_METHOD=token` | — | When `VAULT_AUTH_METHOD=token` | Dev/CI only. Rejected when `GOCELL_ADAPTER_MODE=real`. |
| `VAULT_ROLE_ID` | AppRole role ID | — | When `VAULT_AUTH_METHOD=approle` | |
| `VAULT_SECRET_ID` | AppRole secret ID (direct mode) | — | When `VAULT_AUTH_METHOD=approle` and `VAULT_SECRET_ID_TYPE=direct` | |
| `VAULT_SECRET_ID_TYPE` | How the secret ID is supplied | `direct` | No | `direct` (env), `wrapped` (wrapping token), or `file` (projected volume). |
| `VAULT_SECRET_ID_WRAPPING_TOKEN` | Wrapping token for `VAULT_SECRET_ID_TYPE=wrapped` | — | When `VAULT_SECRET_ID_TYPE=wrapped` | Consumed on first use. |
| `VAULT_SECRET_ID_FILE` | File path for `VAULT_SECRET_ID_TYPE=file` | — | When `VAULT_SECRET_ID_TYPE=file` | Typically a K8s projected volume. |
| `VAULT_K8S_ROLE` | Vault Kubernetes auth role name | — | When `VAULT_AUTH_METHOD=kubernetes` | |
| `VAULT_K8S_JWT_PATH` | Path to K8s projected service account JWT | `/var/run/secrets/kubernetes.io/serviceaccount/token` | No | |
| `VAULT_K8S_MOUNT` | Vault Kubernetes auth mount path | `kubernetes` | No | |
| `GOCELL_VAULT_TRANSIT_MOUNT` | Vault Transit secrets engine mount path | `transit` | No | |
| `GOCELL_VAULT_TRANSIT_KEY` | Vault Transit key name | `gocell-config` | No | |
| `GOCELL_VAULT_STARTUP_TIMEOUT` | Total startup I/O deadline (auth Login + optional unwrap + initial key metadata read) | `30s` | No | `time.ParseDuration` format (e.g. `45s`, `2m`). Must be positive; malformed or non-positive values fail fast. Increase for high-latency networks or wrapped-token paths that require multiple TLS round-trips. |

### Required Vault transit policy

The provider needs `read` on the key metadata, `update` on `datakey/plaintext` (the encrypt path), `update` on `decrypt`, and `update` on `rotate`. Apply this HCL at the role's policy:

```hcl
path "transit/keys/<keyname>"               { capabilities = ["read"] }
path "transit/keys/<keyname>/rotate"        { capabilities = ["create","update"] }
path "transit/datakey/plaintext/<keyname>"  { capabilities = ["create","update"] }
path "transit/decrypt/<keyname>"            { capabilities = ["create","update"] }
```

Substitute `<keyname>` with the value of `GOCELL_VAULT_TRANSIT_KEY` (default `gocell-config`). The startup readiness check only exercises `transit/keys/<keyname>` (the `read` cap), so a missing `datakey/plaintext` capability slips past startup and surfaces as `ErrKeyProviderEncryptFailed` on the first encrypt — apply the policy before the first deploy.

> Migration note: pre-PR-A18 deployments granted `transit/encrypt/<keyname>` instead of `transit/datakey/plaintext/<keyname>`. The legacy `encrypt` path is no longer used; the new policy above replaces it.

## HTTP Listeners (PR-A14b three-listener topology)

> **Breaking change (PR-A14b):** `/healthz`、`/readyz`、`/metrics` 从 primary 端口迁到 health listener，更新 k8s probe + Prometheus 配置。详见 [listener-topology](listener-topology.md)。

`cmd/corebundle` binds three HTTP servers. See `docs/ops/listener-topology.md` for the full topology diagram and k8s probe migration notes.

- **primary** — `/api/v1/*` public business routes. Exposed to the public / edge network. JWT authentication middleware runs here. Explicitly 404s `/internal/v1/*` so the internal prefix never leaks to the public network.
- **internal** — `/internal/v1/*` control-plane routes only. Must be bound to an internal network segment; service-token / mTLS middleware is the sole authentication layer.
- **health** — `/healthz`, `/readyz`, `/metrics` only. Dedicated listener so infra endpoints are never mixed with business traffic. Bind to loopback or an internal segment and point k8s probes here.

| Variable | Purpose | Default | Accepted Values |
|---|---|---|---|
| `GOCELL_HTTP_PRIMARY_ADDR` | Primary listener bind address (public / API) | `:8080` | Any `host:port` accepted by `net.Listen("tcp", …)`. Use `0.0.0.0:8080` or a specific interface in production. |
| `GOCELL_HTTP_INTERNAL_ADDR` | Internal listener bind address (`/internal/v1/*`) | `127.0.0.1:9090` | Same format as primary. **Default is loopback** so a dev deployment without a service-token guard is not trivially reachable across the network. Production deployments binding to an internal VPC interface (e.g. `10.0.0.10:9090`) must set this variable explicitly so service-token / mTLS enforcement is the primary defence. |
| `GOCELL_HTTP_HEALTH_ADDR` | Health listener bind address (`/healthz`, `/readyz`, `/metrics`) | `127.0.0.1:9091` | Same format as primary. Point k8s liveness/readiness probes to this address. **Not exposed** to external traffic by default. |

All three addresses must be non-empty and distinct; startup fails fast otherwise.

## Observability / Monitoring

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_METRICS_TOKEN` | Bearer token for `/metrics` scraper authentication (`X-Metrics-Token` header) | — | **Real mode** |
| `GOCELL_READYZ_VERBOSE_TOKEN` | Bearer token for `/readyz?verbose` (exposes internal topology). After PR-A35 required in every mode unless `GOCELL_READYZ_VERBOSE_DISABLED=1` is set; verbose requests without a matching token return 401 `ERR_READYZ_VERBOSE_DENIED`. See `docs/ops/readyz.md`. | — | **All modes** |
| `GOCELL_READYZ_VERBOSE_DISABLED` | Set to `1` to waive the `/readyz?verbose` endpoint entirely. Lets ephemeral deployments (test harnesses, single-node demos) satisfy the PR-A35 invariant without minting a token. Rejected when `GOCELL_ADAPTER_MODE=real`. | `0` | Optional |

## Adapter Mode

| Variable | Purpose | Default | Accepted Values |
|---|---|---|---|
| `GOCELL_ADAPTER_MODE` | Selects secret-loading and fail-fast behaviour | `""` (dev/in-memory) | `""` (dev), `"real"` |
| `GOCELL_CELL_ADAPTER_MODE` | Selects the storage backend for Cell repositories | `""` (in-memory) | `""`, `"memory"`, `"postgres"` |

Note: the per-cell `GOCELL_<CELLID>_DATABASE_URL` variables (e.g. `GOCELL_CONFIGCORE_DATABASE_URL`) replace the old global `GOCELL_PG_DSN`. Each cell reads its own DSN at startup.

## State

| Variable | Purpose | Default |
|---|---|---|
| `GOCELL_STATE_DIR` | Directory for stateful files (e.g. initial admin credential on first run) | Platform-specific (see below) |

### Per-OS defaults for `GOCELL_STATE_DIR`

When `GOCELL_STATE_DIR` is not set, GoCell selects the default state directory based on the operating system:

| OS | Default path |
|----|-------------|
| Linux | `/run/gocell` (systemd `RuntimeDirectory` convention; tmpfs, not written to disk on reboot) |
| macOS | `~/Library/Application Support/gocell/run` |
| Windows | `%LOCALAPPDATA%\gocell\run` |

Set `GOCELL_STATE_DIR` to override the platform default for all stateful files.

## Migration from pre-T6 env names

Old names are removed in GoCell PR-A3 / T6. Operators must update environment configuration before upgrading.

| Old name (pre-T6, removed) | New name |
|---|---|
| `GOCELL_PG_DSN` | `GOCELL_CONFIGCORE_DATABASE_URL` |
| `GOCELL_PG_MAX_CONNS` | `GOCELL_CONFIGCORE_DATABASE_MAX_CONNS` |
| `GOCELL_PG_IDLE_TIMEOUT` | `GOCELL_CONFIGCORE_DATABASE_IDLE_TIMEOUT` |
| `GOCELL_PG_MAX_LIFETIME` | `GOCELL_CONFIGCORE_DATABASE_MAX_LIFETIME` |
| `GOCELL_MASTER_KEY` | `GOCELL_CONFIGCORE_MASTER_KEY` |
| `GOCELL_MASTER_KEY_PREVIOUS` | `GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS` |
| `GOCELL_KEY_PROVIDER` | `GOCELL_CONFIGCORE_KEY_PROVIDER` |
| `GOCELL_AUDIT_CURSOR_KEY` | `GOCELL_AUDITCORE_CURSOR_KEY` |
| `GOCELL_AUDIT_CURSOR_PREVIOUS_KEY` | `GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY` |
| `GOCELL_HMAC_KEY` | `GOCELL_AUDITCORE_HMAC_KEY` |
| `GOCELL_CONFIG_CURSOR_KEY` | `GOCELL_CONFIGCORE_CURSOR_KEY` |
| `GOCELL_CONFIG_CURSOR_PREVIOUS_KEY` | `GOCELL_CONFIGCORE_CURSOR_PREVIOUS_KEY` |
| `GOCELL_ACCESS_CURSOR_KEY` | `GOCELL_ACCESSCORE_CURSOR_KEY` |
| `GOCELL_ACCESS_CURSOR_PREVIOUS_KEY` | `GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY` |
