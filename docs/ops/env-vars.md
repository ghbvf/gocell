# GoCell Environment Variables Reference

This document lists all environment variables consumed by `cmd/corebundle` at startup.
Variables without a default value are **required** in the indicated adapter mode.
Missing required variables cause fail-fast before any assembly initialization.

> **Exception:** the [webhook source secret encryption](#webhook-source-secret-encryption)
> section is consumed by **assemblies that call `cellmodules/webhooksource.LoadSourceStore`**,
> not by `cmd/corebundle` (which declares no webhook receivers today). It is flagged inline.

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
| `GOCELL_SERVICE_SECRET` | **Master mode (monolith):** HMAC-SHA256 master secret (≥ 32 bytes) from which per-cell service-token subkeys are derived (HKDF, #2153) for `/internal/v1/*` | — | Master mode (monolith); **mutually exclusive** with split provisioned vars below | Value is used as raw UTF-8 bytes (not base64-decoded); any UTF-8 string of ≥ 32 bytes is acceptable. Recommended generators: `openssl rand -base64 32` → 44 printable chars (base64 padded), used as raw bytes; `openssl rand -hex 32` → 64 hex chars, used as raw bytes. Both meet the 32-byte minimum. The composition root requires **exactly one** keying mode: this master var **XOR** the split provisioned vars below — setting both (a split cell must not also hold the master) or neither fails fast with `ERR_CONTROLPLANE_SERVICE_SECRET_MISSING`. Master mode is a single trust domain (monolith): a process compromise yields the master, so it does **not** provide per-cell isolation — use the split provisioned vars for cross-trust-boundary deployments. When the guard is installed, a replay-defense `NonceStore` is wired automatically so a captured token cannot be replayed within `auth.ServiceTokenNonceTTL` (currently 5 min 30 sec). Real-mode startup also fails fast with `ERR_CONTROLPLANE_NONCE_STORE_MISSING` if the guard was somehow wired without a replay-safe store. Single-pod real deployments may use the in-memory store by setting `GOCELL_SINGLE_POD=1`; real multi-pod deployments must configure Redis with `GOCELL_REDIS_ADDR` so nonce replay protection and outbox idempotency are distributed across pods. |
| `GOCELL_SERVICE_SECRET_PREVIOUS` | Previous HMAC master secret for zero-downtime rotation (master mode) | — | No | Optional; per-cell subkeys derived from it are tried after the current secret fails verification. |
| `GOCELL_SERVICE_CELL` | **Split mode (#2153):** this process's own cell id (the cell it signs as) | — | Split mode | Set together with `GOCELL_SERVICE_SIGNING_KEY` + `GOCELL_SERVICE_VERIFY_KEYS`. A split cell process holds only its own subkeys, **never the master**. Produce all three via `gocell derive-service-keys --cell <id>`. |
| `GOCELL_SERVICE_SIGNING_KEY` | **Split mode:** this cell's signing subkey, hex-encoded (32 bytes) | — | Split mode | `HKDF(master, <cell>)`, emitted by `gocell derive-service-keys`. |
| `GOCELL_SERVICE_SIGNING_KEY_PREVIOUS` | **Split mode:** previous-generation signing subkey (hex), rotation overlap | — | No | Optional; covers a master-rotation overlap window. |
| `GOCELL_SERVICE_VERIFY_KEYS` | **Split mode:** this cell's declared callers' verify subkeys, `callerA:<hex>,callerB:<hex>` | — | Split mode | The callee verifies only the callers in this set (least-privilege, fail-closed). Emitted by `gocell derive-service-keys`. |
| `GOCELL_SERVICE_VERIFY_KEYS_PREVIOUS` | **Split mode:** previous-generation verify map (same encoding), rotation overlap | — | No | Optional; covers a master-rotation overlap window. |
| `GOCELL_SINGLE_POD` | Acknowledges that the deployment is single-pod and in-memory replay protection is sufficient | — | **Real mode** (when using default in-memory NonceStore) | Must be `1` in single-pod real-mode deployments to acknowledge in-memory replay defence scope; otherwise startup fails fast with `ERR_CONTROLPLANE_NONCE_STORE_MISSING`. Multi-pod deployments leave unset and configure Redis via `GOCELL_REDIS_ADDR` or `GOCELL_REDIS_CLUSTER_ADDRS` instead. |

## Redis (required for real multi-pod deployments)

`cmd/corebundle` uses Redis as the shared coordination backend for real multi-pod deployments. When `GOCELL_ADAPTER_MODE=real` and `GOCELL_SINGLE_POD` is not set, **either** `GOCELL_REDIS_ADDR` (standalone) **or** `GOCELL_REDIS_CLUSTER_ADDRS` (Redis Cluster) is required at startup. The same Redis client backs three consumers, so missing Redis fails fast before any listener binds:

1. **Service-token nonce replay protection** (`servicetoken-nonce` namespace): prevents captured service tokens from being replayed within `auth.ServiceTokenNonceTTL` (~5 min 30 sec).
2. **Outbox idempotency claiming** (`_runtime` namespace): distributed `Claim/Commit/Release` fencing for event consumer deduplication across pods.
3. **HTTP idempotency replay store** (`_runtime` namespace, key-space `<tenantID>:{<key>}:lease` / `:resp` / `:fp`): records response blobs for `Idempotency-Key` header replay (≤ 256 KiB per unique request, 24h TTL). **Memory model**: plan Redis memory for `peak_QPS × 86400s × avg_response_size` (24h TTL window at peak request rate times average recorded response size). Activated default-ON when Redis is present; inactive in single-pod / memory mode where no cross-pod replay is needed.

Sentinel mode is supported by the adapter but not yet wired into corebundle env loading.

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_REDIS_ADDR` | Redis standalone address for distributed nonce and idempotency state | — | **Real mode, multi-pod** (one of `GOCELL_REDIS_ADDR` / `GOCELL_REDIS_CLUSTER_ADDRS`) | Required when `GOCELL_ADAPTER_MODE=real`, `GOCELL_SINGLE_POD` is unset, and `GOCELL_REDIS_CLUSTER_ADDRS` is unset. Remote deployments must use a TLS URL such as `rediss://redis.example.internal:6379`; bare `host:port` is accepted only for loopback dev/CI addresses such as `127.0.0.1:6379` or `localhost:6379`. Mutually exclusive with `GOCELL_REDIS_CLUSTER_ADDRS`. |
| `GOCELL_REDIS_CLUSTER_ADDRS` | Comma-separated list of Redis Cluster node addresses (AWS ElastiCache Cluster, Azure Cache Cluster, self-hosted Redis Cluster) | — | **Real mode, multi-pod** (alternative to `GOCELL_REDIS_ADDR`) | Selects Redis Cluster mode. Each entry is a plain `host:port` for loopback/dev or a TLS URL `rediss://host:port`. Mixing URL and plain forms within a single value is rejected. Leading/trailing whitespace per entry is trimmed; exact-duplicate entries are deduplicated. Empty entries (trailing or double commas) fail fast. `GOCELL_REDIS_DB` must be `0` or unset (Redis Cluster has no `SELECT` command). Mutually exclusive with `GOCELL_REDIS_ADDR`. |
| `GOCELL_REDIS_PASSWORD` | Redis password (applies to all modes) | — | **Real mode** (yes); **dev mode** (no) | Passed directly to the Redis client. Fail-closed behavior: in **real** mode (`GOCELL_ADAPTER_MODE=real` requiring production control plane) the password is **mandatory** — startup fails fast with `ERR_ADAPTER_REDIS_CONNECT` ("`connection credential required`") when both `GOCELL_REDIS_PASSWORD` is unset and the deployment is not single-pod. In **dev** mode (`GOCELL_ADAPTER_MODE=dev` or single-pod) the corebundle automatically enables the `Config.AllowUnsafeNoPassword` opt-in so unauthenticated local Redis instances (testcontainers, `127.0.0.1` dev) work without a password. URL-embedded credentials in cluster URLs (`rediss://user:pass@host`) must equal `GOCELL_REDIS_PASSWORD` if both are set; conflicting values fail fast at startup with `ERR_ADAPTER_REDIS_CONNECT`. |
| `GOCELL_REDIS_DB` | Redis database number | `0` | No | Must be a non-negative integer. Invalid values fail fast at startup. **Cluster mode forbids non-zero values** (Redis Cluster has no `SELECT` command). |

## Event Transport (RabbitMQ — required for postgres topology)

In postgres (durable) topology the outbox event transport is a **real message broker** (RabbitMQ), not the in-process bus: the relay publishes already-persisted outbox entries to the broker and consumers subscribe from it, so events survive process restarts and cross pod boundaries (#1940). In demo / memory topology the in-process eventbus is used and this variable is ignored. The transport is selected by `cellmodules/eventtransport.Resolve` from `Topology`.

| Variable | Purpose | Default | Required? | Notes |
|----------|---------|---------|-----------|-------|
| `GOCELL_<CELLID>_AMQP_URL` | Per-cell RabbitMQ connection URL for a broker cell (#2152 PR-2). Read for each `generatedBrokerCells()` broker cell — the codegen-derived set of cells that produce or consume an amqp-transported contract (#2365; currently `ACCESSCORE` / `AUDITCORE` / `CONFIGCORE`, but that set is **derived from `contractUsages`, not defined as the postgres cell set** — a DB-only cell with no amqp contract is excluded) — and deduped by `eventtransport.dedupBrokerURL` | falls back to `GOCELL_AMQP_URL` when unset | optional | **Colocated** assemblies share one broker — leave these unset and set only `GOCELL_AMQP_URL` (every cell falls back to it → one connection, behavior-preserving). **Distinct** per-cell URLs are currently **fail-closed** (egress-only: only the relay/publisher fans out today; a single subscriber cannot consume N brokers). True N-broker fan-out is gated on the ingress N-router (#2366). Note: per-cell DB pool/relay fan-out has landed independently (#2341) but does **not** lift the broker distinct-URL fail-closed — DB and broker resources are not in lockstep. **Ignored** in demo / memory topology. |
| `GOCELL_AMQP_URL` | RabbitMQ (AMQP 0-9-1) connection URL the relay publishes to and consumers subscribe from; assembly-wide fallback for the per-cell `GOCELL_<CELLID>_AMQP_URL` above | — | **postgres topology** (`GOCELL_CELL_ADAPTER_MODE=postgres`), unless every broker cell sets its own `GOCELL_<CELLID>_AMQP_URL` | Startup **fails fast** when a broker cell has neither its per-cell URL nor this fallback in postgres topology — there is **no silent in-memory fallback** (a process-local bus would lose durable outbox entries across processes / restarts). The connection dials eagerly, so an unreachable broker also fails fast at startup. Use a TLS URL (`amqps://…`) for remote brokers; `amqp://guest:guest@localhost:5672/` is dev/CI only. **Ignored** in demo / memory topology. |

**Dead-letter exchange (broker topology contract):** the RabbitMQ subscriber declares a single dead-letter exchange **`gocell.events.dlx`** at subscription setup. Messages rejected past the retry budget (`outbox.Reject`) are routed there instead of being silently dropped, retaining their original routing key (topic) so a DLX consumer can route by source topic. This name is a stable operations contract — renaming it requires migrating in-flight dead letters. Operators configuring vhost ACLs or dead-letter monitoring should account for `gocell.events.dlx`.

## Per-Cell Session and Cursor Keys

Each Cell reads its own env variables. The naming pattern is `GOCELL_<CELLID>_<RESOURCE>`.

### auditcore cell

| Variable | Purpose | Default (dev) | Required |
|---|---|---|---|
| `GOCELL_AUDITCORE_HMAC_KEY` | HMAC key for session HMAC chains | `dev-hmac-key-replace-in-prod!!!!` | **Real mode** |
| `GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY` | HMAC key for the bootstrap audit hash chain | — | **Real mode** |
| `GOCELL_AUDITCORE_CURSOR_KEY` | HMAC key for audit cursor codec | `corebundle-audit-cursor-key-32b!` | **Real mode** |
| `GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY` | Previous audit cursor key (rotation) | — | No |
| `GOCELL_AUDIT_ADMIN_DSN` | PostgreSQL DSN for the dedicated `gocell_audit_admin` read pool used for super-admin cross-tenant audit reads (#1810). Format: `postgres://gocell_audit_admin:<pw>@host:5432/<db>?sslmode=require`. **Optional** (postgres mode only). When absent, super-admin cross-tenant audit read returns HTTP 501 (fail-closed). The role must be provisioned first via `deploy/postgres/init/10-restricted-role.sh` (requires `GOCELL_AUDIT_ADMIN_PASSWORD` at initdb time). | — | No |
| `GOCELL_AUDIT_ADMIN_PASSWORD` | Password for the optional `gocell_audit_admin` PostgreSQL role, used only by `deploy/postgres/init/10-restricted-role.sh` at initdb time to provision the role. **Optional** (deploy/init layer only; not read by `cmd/corebundle` at runtime). When unset, the role is not created and the cross-tenant audit read capability is disabled. Must be URL-safe (RFC 3986 unreserved: `A-Za-z0-9._~-`). Recommended: `openssl rand -hex 16`. | — | No |

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
| `GOCELL_ACCESSCORE_IP_HASH_SALT` | HMAC salt for the bootstrap-failed event client-IP hash (≥32 bytes). Keeps plaintext IP off outbox/broker/DLX + the audit ledger. Real mode fails fast if unset or a well-known demo value. | `dev-ip-hash-salt-accesscore-32b!` | **Real mode** |

### accesscore first-admin provisioning

Both variables are **required, persistent operator Basic Auth credentials** protecting the setup/admin endpoint for the lifetime of the deployment. Empty values cause fail-fast at startup. See `docs/ops/first-run-setup.md` for deployment examples and `docs/architecture/202605061600-adr-bootstrap-admin-boundary.md` for the security boundary ADR.

| Variable | Purpose | Default | Notes |
|---|---|---|---|
| `GOCELL_BOOTSTRAP_ADMIN_USERNAME` | HTTP Basic Auth username protecting the setup/admin endpoint. Persistent operator authenticator — env is the authenticator (who may trigger setup), not the business admin identity (which comes from the POST body). Must be non-empty; empty value fails fast. | — | **Required, persistent (lifetime of deployment)** |
| `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` | HTTP Basic Auth password protecting the setup/admin endpoint. Minimum 8 bytes after TrimSpace (handles K8s secret trailing newlines). Control characters fail fast. Persistent operator authenticator — not the business admin password. | — | **Required, persistent (lifetime of deployment)** |

> **Operator control-plane (admin listener)**: `cmd/corebundle` does **not** wire
> `cell.AdminListener` / `AuthOperator`, so it reads no `GOCELL_OPERATOR_ADMIN_*`
> variables — setting them on corebundle has no effect. The operator
> control-plane (`/admin/v1/*`, e.g. projection rebuild) is demonstrated in
> `examples/todoorder` (see `examples/todoorder/auth.go::newOperatorAuthFromEnv`,
> which reads `GOCELL_OPERATOR_ADMIN_USERNAME` / `_PASSWORD`). Design:
> `docs/architecture/202606041200-1505-adr-operator-control-plane-auth.md` and
> `docs/ops/listener-topology.md` §"Admin Listener".

## Encryption Key Provider (required when GOCELL_CELL_ADAPTER_MODE=postgres)

Each Cell that uses PostgreSQL reads its own DB and encryption env variables.

### Per-cell database DSNs (#1964, #2341)

Each postgres-requiring cell reads its own DSN via `GOCELL_<CELLID>_DATABASE_URL`.
`percellpg.Resolve` groups cells by DSN and opens **one independent pool per distinct
DSN** plus one relay per pool (split topology, landed #2341). A missing DSN for any
cell fails fast at startup naming the cell and the expected env var.

- **Colocated** (all cells share one DSN): all three vars are identical;
  `percellpg.Resolve` deduplicates to a single shared pool. One relay services all
  outbox traffic.
- **Split** (each cell on its own DB server): each distinct DSN gets its own pool
  and relay instance. Cell IDs are grouped by DSN; the pool/relay for a group is keyed
  by the group's alphabetically-first cell (`InfraInstanceKey`).

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_CONFIGCORE_DATABASE_URL` | PostgreSQL DSN for configcore; role must be `gocell_app` (restricted, NOSUPERUSER NOBYPASSRLS) so that `FORCE ROW LEVEL SECURITY` is enforced at runtime | — | **postgres mode** |
| `GOCELL_AUDITCORE_DATABASE_URL` | PostgreSQL DSN for auditcore; same role requirement as configcore | — | **postgres mode** |
| `GOCELL_ACCESSCORE_DATABASE_URL` | PostgreSQL DSN for accesscore; same role requirement as configcore | — | **postgres mode** |
| `GOCELL_ACCESSCORE_DATABASE_MAX_CONNS` | Max open connections for a pool. **Pool knobs are read from the alphabetically-first cell in each DSN group** (`accesscore` leads the colocated group since `a < au < c`). In colocated mode all cells share ONE pool — only the knobs of the first sorted cell apply; the other cells' knob vars are read but ignored. In split mode each distinct-DSN group takes its own pool sized by the group's alphabetically-first cell. Cells within the same DSN group MUST set identical knob values; a mismatch is fail-closed by `percellpg.Resolve` (#2341). | 10 | No |
| `GOCELL_ACCESSCORE_DATABASE_IDLE_TIMEOUT` | Idle connection timeout for the pool (same ordering/group rule as `MAX_CONNS` above; cells sharing a DSN group must set this identically or `percellpg.Resolve` fails closed) | `5m` | No |
| `GOCELL_ACCESSCORE_DATABASE_MAX_LIFETIME` | Max connection lifetime for the pool (same ordering/group rule as `MAX_CONNS` above; cells sharing a DSN group must set this identically or `percellpg.Resolve` fails closed) | `1h` | No |

> **F15 — Split topology: each per-cell DB must carry the full platform schema.**
> Every database referenced by a distinct `GOCELL_<CELLID>_DATABASE_URL` must have the
> complete platform migration set applied (the same migrations as a colocated DB). There
> is no per-cell schema subset: `verifyPGPreconditions` runs on each pool at startup and
> fails fast if any expected table or role is missing. Per-cell schema subsetting (only
> the owning cell's tables) is future work.

> **F16 — Partial-split: relay metric `cell` label uses the DSN-group representative.**
> In a partial-split assembly (some cells share a DSN, others have their own), each relay
> instance's `cell` metric label is set to the **alphabetically-first cell in its DSN
> group** (the group representative, `InfraInstanceKey`). A single relay label may
> therefore cover the outbox traffic of multiple cells in the same group. Operations
> dashboards and alerts must account for this: a relay labelled `accesscore` may be
> draining outbox entries for all cells that share the `accesscore` DSN.

### Restricted serving-role credential

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_APP_PASSWORD` | Password for the restricted PostgreSQL role `gocell_app` (NOSUPERUSER, NOBYPASSRLS, non-owner). Used by per-cell `GOCELL_*_DATABASE_URL` vars and by `deploy/postgres/init/10-restricted-role.sh` which creates the role on first data-dir init. Must be URL-safe — RFC 3986 unreserved characters only (`A-Za-z0-9._~-`); it is interpolated into a SQL heredoc AND the serving DSN userinfo, so `'` `/` `+` `=` `@` and spaces would break init or connection. Recommended: `openssl rand -hex 16`. | — | **postgres mode** |

See `docs/ops/local-docker-deploy.md` §Dual-role PostgreSQL for first-time setup steps.
Probe `postgres_app_role_restricted_ready` verifies at runtime that the serving role is not a superuser / `BYPASSRLS`.

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

### webhook source secret encryption

Persistent, encrypted webhook source HMAC secrets (#1540). Same envelope-encryption
backends as configcore; vault-transit reuses the shared `VAULT_*` / `GOCELL_VAULT_*`
settings above.

> **Not consumed by `cmd/corebundle` today** (unlike the rest of this document).
> These variables are read by `cellmodules/webhooksource.LoadSourceStore`, which a
> webhook-serving **assembly** calls at boot to load the persistent encrypted
> `SourceStore`. `cmd/corebundle` declares no webhook receivers and does not invoke
> the loader, so setting `GOCELL_WEBHOOK_*` has no effect on the current corebundle.
> Required only for assemblies that persist webhook sources (postgres storage) —
> memory/demo deployments seed the in-memory `kwh.SourceRegistry` directly.

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_WEBHOOK_KEY_PROVIDER` | Selects the encryption backend for persisted webhook source secrets | — | **postgres mode (when persisting webhook sources)** | `"local-aes"` (dev/CI) or `"vault-transit"` (production). The persistent SourceStore loader fails fast if unset; there is no plaintext fallback (the `webhook_sources` table has no plaintext column). |
| `GOCELL_WEBHOOK_MASTER_KEY` | 32-byte hex-encoded AES key for `local-aes` provider | — | When `GOCELL_WEBHOOK_KEY_PROVIDER=local-aes` | Generate: `openssl rand -hex 32`. Real mode rejects well-known demo keys. |
| `GOCELL_WEBHOOK_MASTER_KEY_PREVIOUS` | Previous master key for key rotation | — | No | Optional; enables decryption of secrets encrypted with the prior key during the rotation window (`local-aes` only). |

### Required Vault transit policy

The provider needs `read` on the key metadata, `update` on `datakey/plaintext` (the encrypt path), `update` on `decrypt`, and `update` on `rotate`. Apply this HCL at the role's policy:

```hcl
path "transit/keys/<keyname>"               { capabilities = ["read"] }
path "transit/keys/<keyname>/rotate"        { capabilities = ["create","update"] }
path "transit/datakey/plaintext/<keyname>"  { capabilities = ["create","update"] }
path "transit/decrypt/<keyname>"            { capabilities = ["create","update"] }
```

Substitute `<keyname>` with the value of `GOCELL_VAULT_TRANSIT_KEY` (default `gocell-config`). The startup readiness check only exercises `transit/keys/<keyname>` (the `read` cap), so a missing `datakey/plaintext` capability slips past startup and surfaces as `ErrKeyProviderEncryptFailed` on the first encrypt — apply the policy before the first deploy.

> Migration note: older deployments granted `transit/encrypt/<keyname>` instead of `transit/datakey/plaintext/<keyname>`. The legacy `encrypt` path is no longer used; the new policy above replaces it.

## Split 拓扑 mTLS 传输层安全（#2263，ZT-1）

非 loopback `topology.groups` group endpoint 的 split 跨 cell 调用现强制 mTLS（`celltls.Resolve`
在启动期 fail-fast）。以下四个变量**全有或全无（all-or-nothing）**：只设部分等同于全部未设，在
topology 含非 loopback group endpoint 时 `celltls.Resolve` 启动 fail-fast，不降级明文。

每个 cell 进程需要：一张携带 `spiffe://<trustDomain>/cell/<cellID>` URI SAN + 双 EKU
（ServerAuth + ClientAuth）的 leaf cert、配套私钥，以及签发所有 cell cert 的 trust-root CA bundle。
完整证书要求、SPIFFE-ID 格式和操作步骤见 `docs/guides/deployment-topology.md` §Split mTLS 配置 checklist。

| 变量 | 用途 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `GOCELL_TRANSPORT_TLS_CERT_FILE` | 本 cell 的 leaf cert PEM **文件路径**（URI SAN `spiffe://<trustDomain>/cell/<cellID>`，双 EKU）| — | topology 含非 loopback remote cell 时必填（all-or-nothing） | 框架在启动时读取文件内容到内存，不在请求路径重读。cert 必须同时声明 `ExtKeyUsageServerAuth` + `ExtKeyUsageClientAuth`——兼作 server cert 和 client cert。TLS 1.3 强制，cert 签名算法须兼容（ECDSA P-256+ 或 RSA 2048+）。|
| `GOCELL_TRANSPORT_TLS_KEY_FILE`  | `GOCELL_TRANSPORT_TLS_CERT_FILE` 配套的私钥 PEM **文件路径** | — | 同上（all-or-nothing） | 私钥必须与 cert 中的公钥匹配；不匹配导致 `tls.LoadX509KeyPair` 报错，启动 fail-fast。|
| `GOCELL_TRANSPORT_TLS_CA_FILE`   | trust-root CA bundle PEM **文件路径**（签发所有 cell leaf cert 的单根 CA） | — | 同上（all-or-nothing） | 同时作为客户端 `RootCAs`（验证 server 证书链）和服务端 `ClientCAs`（验证 client 证书链）。支持多 CA 的 bundle PEM（多个 `-----BEGIN CERTIFICATE-----` 块），但所有 leaf cert 须在同一信任根下。|
| `GOCELL_SPIFFE_TRUST_DOMAIN`     | SPIFFE trust domain，不含 `spiffe://` 前缀（如 `gocell.internal`）| — | 同上（all-or-nothing） | 用于构造和验证 SPIFFE-ID：客户端 `VerifyConnection` 要求 server cert 的 URI SAN 以 `spiffe://<trustDomain>/cell/` 开头；服务端 cross-bind middleware 同样以本 env 作为 trust domain 过滤。非空 + 不含 `spiffe://` 前缀 + 不含 `/` 尾缀；违反格式启动 fail-fast。|

**Fail-closed 行为要点**：
- topology 含非 loopback remote cell + 任一变量缺失 → **启动 fail-fast**。
- 四变量全设但无 remote cell → 仍 honor（loopback remote 亦升 mTLS）。
- cert chain 验证失败 / SPIFFE cell ID 不匹配目标 cell → TLS 握手拒绝（client 侧 `VerifyConnection` 报错）。
- client cert SPIFFE cell ID 与 service-token callerCell 不一致 → **401**（server 侧 cross-bind middleware）。

> **Warning — loopback remote 亦强制 mTLS（反直觉行为）：**
> 即使 remote peer 端点是 loopback 地址（`localhost` / `127.x.x.x` / `::1`），只要四个 TLS
> 环境变量已设置，`celltls.Resolve` 就会 honor 该 TLS 材料并强制 mTLS。这意味着：
>
> **demo / dev 环境不应设置这四个变量，除非已准备好有效的证书。**
>
> 错误后果：在本地 Docker Compose 多进程 dev 中同时配置了 loopback remote peer 和 TLS 变量
> 但未提供有效 cert/key/CA → TLS 握手失败，peer 不可达，`<peer>_remote_ready` probe 报
> `unhealthy`，进程 `/readyz` 503。解决方法：dev 不设置这四个变量（plaintext loopback），
> 或提供真实的自签 CA + leaf cert（参见 `docs/guides/deployment-topology.md` §Split mTLS
> 配置 checklist）。

**轮换注意**：本 PR 使用静态文件，轮换需替换文件 + 重启进程（hot-reload 是 follow-up）。
证书自动颁发/续期追踪在 `runtime/certlifecycle` reconciler 独立 roadmap；SPIFFE Workload API
集成（ZT-4）是另一独立 roadmap。完整说明见 ADR
`docs/architecture/202606171200-2263-adr-cross-cell-transport-mtls.md` §推迟项。

**TLS 握手失败的可观测性（metrics vs trace）：**
TLS 握手失败（cert chain 验证错误、SPIFFE-ID 不匹配、证书过期）与普通 TCP 不可达，在 metric
层面都记为 `cell_transport_requests_total{outcome="dial_error"}`——`outcome` label 是有界低基数
闭值集，不细分 TLS 内部原因。

需要区分"cert 信任链错误"与"host 不可达"时，应查**链路追踪 span**：每次 dial 失败后
transport 均调用 `span.RecordError(err)`，携带完整的 Go TLS 错误字符串（`error.type` 属性）。
这是有意为之——保持 metric label 低基数（避免证书 CN/SAN 等高 cardinality 信息进 label），
同时在 trace 侧保留完整诊断上下文。

操作员排查流程：
1. 看 `/readyz?verbose` 确认 `<peer>_remote_ready` probe 状态。
2. 用 Grafana/Prometheus 查 `cell_transport_requests_total{outcome="dial_error"}` 确认失败量级。
3. 用 Jaeger/Zipkin 按 trace 的 `error.type` 属性过滤，定位是 TLS 握手错误还是连接拒绝。
4. 对照四个 `GOCELL_TRANSPORT_TLS_*` / `GOCELL_SPIFFE_TRUST_DOMAIN` 变量及 cert 内容排查根因。

## HTTP Listeners (three-listener topology)

> **Breaking change:** `/healthz`, `/readyz`, and `/metrics` have moved from the primary port to the health listener. Update your k8s probes and Prometheus scrape configuration accordingly. See [listener-topology](listener-topology.md) for details.

`cmd/corebundle` binds three HTTP servers. See `docs/ops/listener-topology.md` for the full topology diagram and k8s probe migration notes.

- **primary** — `/api/v1/*` public business routes. Exposed to the public / edge network. JWT authentication middleware runs here. Explicitly 404s `/internal/v1/*` so the internal prefix never leaks to the public network.
- **internal** — `/internal/v1/*` control-plane routes only. Must be bound to an internal network segment; service-token / mTLS middleware is the sole authentication layer.
- **health** — `/healthz`, `/readyz`, `/metrics` only. Dedicated listener so infra endpoints are never mixed with business traffic. Bind to loopback or an internal segment and point k8s probes here.

| Variable | Purpose | Default | Accepted Values |
|---|---|---|---|
| `GOCELL_HTTP_PRIMARY_ADDR` | Primary listener bind address (public / API) | `:8080` | Any `host:port` accepted by `net.Listen("tcp", …)`. Use `0.0.0.0:8080` or a specific interface in production. |
| `GOCELL_HTTP_INTERNAL_ADDR` | Internal listener bind address (`/internal/v1/*`) | `127.0.0.1:9090` | Same format as primary. **Default is loopback** for local development; startup still requires `GOCELL_SERVICE_SECRET` so the internal listener is service-token guarded in every mode. Production deployments binding to an internal VPC interface (e.g. `10.0.0.10:9090`) must set this variable explicitly. |
| `GOCELL_HTTP_HEALTH_ADDR` | Health listener bind address (`/healthz`, `/readyz`, `/metrics`) | `127.0.0.1:9091` | Same format as primary. Default is local/dev only. Use `:9091` or another Pod-reachable address for kubelet HTTP probes and Prometheus PodIP/Service scrapes. |
| `GOCELL_HTTP_HEALTH_LOCAL_ONLY` | Explicit waiver for loopback health listener in `GOCELL_ADAPTER_MODE=real` | unset | Set to `1` only when health/metrics are reached from the same network namespace, such as local dev, same-Pod sidecar, or exec-probe style checks. |

All three addresses must be non-empty and distinct; startup fails fast otherwise.

## gRPC Listener (PR-11 #1154)

`cmd/corebundle` binds **one always-on gRPC listener** on `cell.PrimaryListener` (the same
listener ROLE as the HTTP primary listener — independent sockets, not one socket serving both
protocols). It is required because accesscore serves the `grpc.auth.session.verify.v1`
admin/super-admin JWT token introspection contract there (the first platform-cell gRPC service);
the cell registers the service unconditionally, so bootstrap fails fast if no gRPC listener is
wired. There is no per-slice toggle — only the bind address and transport security are
configurable. Note: true service-to-service (service-principal + InternalListener-gRPC +
service-token channel) is a future capability tracked as #2290.

| Variable | Purpose | Default | Accepted Values |
|---|---|---|---|
| `GOCELL_GRPC_ADDR` | gRPC listener bind address (admin/operator JWT token introspection) | `:9095` | Any `host:port` accepted by `net.Listen("tcp", …)`. Use a specific interface in production. |
| `GOCELL_GRPC_TLS_CERT_FILE` | PEM server certificate path (enables server TLS) | unset | Must be set together with `GOCELL_GRPC_TLS_KEY_FILE`. |
| `GOCELL_GRPC_TLS_KEY_FILE` | PEM server private-key path (enables server TLS) | unset | Must be set together with `GOCELL_GRPC_TLS_CERT_FILE`. |
| `GOCELL_GRPC_TLS_CLIENT_CA_FILE` | PEM client-CA path (enables mTLS — verifies client certs) | unset | Optional; only consumed when cert+key are set. |
| `GOCELL_GRPC_ALLOW_INSECURE` | Explicit opt-in to run plaintext gRPC in durable/real mode | unset | Set to `true` only behind a TLS-terminating sidecar. In durable mode, startup **fails fast** if no TLS material is set and this is not `true` (demo mode runs plaintext by default; the adapter logs a Warn on a non-loopback plaintext bind). |

## Observability / Monitoring

| Variable | Purpose | Default | Required |
|---|---|---|---|
| `GOCELL_METRICS_TOKEN` | Bearer token for `/metrics` scraper authentication (`X-Metrics-Token` header) | — | **Real mode** |
| `GOCELL_READYZ_VERBOSE_TOKEN` | Bearer token for `/readyz?verbose` (exposes internal topology). Required in every mode unless `GOCELL_READYZ_VERBOSE_DISABLED=1` is set; verbose requests without a matching token return 401 `ERR_READYZ_VERBOSE_DENIED`. See `docs/ops/readyz.md`. | — | **All modes** |
| `GOCELL_READYZ_VERBOSE_DISABLED` | Set to `1` to waive the `/readyz?verbose` endpoint entirely. Lets ephemeral deployments (test harnesses, single-node demos) satisfy the verbose-readiness invariant without minting a token. Rejected when `GOCELL_ADAPTER_MODE=real`. | `0` | Optional |

## System Information Endpoint

`GET /api/v1/admin/system` returns a security-filtered build/runtime summary for
authenticated administrators with `system:read`. The response intentionally omits
hostnames, process IDs, listener bind addresses, remote topology, and environment
dumps.

| Variable | Purpose | Default | Required | Notes |
|---|---|---|---|---|
| `GOCELL_ENV` | Deployment environment label surfaced in the system information response | `unknown` | No | Free-form operator label such as `dev`, `staging`, or `prod`; it is TrimSpace-normalized and does not change startup mode or security posture. |
| `GOCELL_DEPLOYED_AT` | Deployment timestamp surfaced in the system information response | — | No | Use an RFC 3339 timestamp such as `2026-06-19T08:30:00Z`. When unset, the endpoint reports deployment metadata as unavailable instead of guessing. |

## Adapter Mode

| Variable | Purpose | Default | Accepted Values |
|---|---|---|---|
| `GOCELL_ADAPTER_MODE` | Selects secret-loading and fail-fast behaviour | `""` (dev/in-memory) | `""` (dev), `"real"` |
| `GOCELL_CELL_ADAPTER_MODE` | Selects the storage backend for Cell repositories | `""` (in-memory) | `""`, `"memory"`, `"postgres"` |

Note: the per-cell `GOCELL_<CELLID>_DATABASE_URL` variables replace the old global `GOCELL_PG_DSN`. Each postgres cell reads its own DSN at startup. In colocated deployments all three DSNs are identical; `percellpg.Resolve` deduplicates to one pool (#1964).

## State

| Variable | Purpose | Default |
|---|---|---|
| `GOCELL_STATE_DIR` | Directory for stateful files | Platform-specific (see below) |

### Per-OS defaults for `GOCELL_STATE_DIR`

When `GOCELL_STATE_DIR` is not set, GoCell selects the default state directory based on the operating system:

| OS | Default path |
|----|-------------|
| Linux | `/run/gocell` (systemd `RuntimeDirectory` convention; tmpfs, not written to disk on reboot) |
| macOS | `~/Library/Application Support/gocell/run` |
| Windows | `%LOCALAPPDATA%\gocell\run` |

Set `GOCELL_STATE_DIR` to override the platform default for all stateful files.

## Migration from pre-T6 env names

The old global PostgreSQL env names for the **serving pool** (`cmd/corebundle`) have been removed. Operators must update environment configuration before upgrading.

> **Note:** `GOCELL_PG_DSN` is NOT fully removed from the codebase. It is still
> the conventional DSN variable for the **`tools/pg-migrate` migration-admin tool**
> (owner/superuser role, distinct from the restricted `gocell_app` serving role).
> `tools/pg-migrate` and the associated `pg-migrate` service in
> `deploy/docker-compose.local.yml` and `tests/e2e/docker-compose.e2e.yaml` continue to
> use `GOCELL_PG_DSN`. The migration-admin tool and the `cmd/corebundle` serving
> pool may point at the **same database** but with **different roles** (superuser vs
> restricted `gocell_app`).

| Old name (pre-T6, removed from serving pool) | New name |
|---|---|
| `GOCELL_PG_DSN` (**serving pool only — removed**; migration tool still uses it) | `GOCELL_CONFIGCORE_DATABASE_URL` + `GOCELL_AUDITCORE_DATABASE_URL` + `GOCELL_ACCESSCORE_DATABASE_URL` (same value, per-cell seam #1964) |
| `GOCELL_PG_MAX_CONNS` | `GOCELL_{ACCESSCORE,AUDITCORE,CONFIGCORE}_DATABASE_MAX_CONNS` — **colocated/shared-DSN: set all three to the same value, or leave all three unset.** Cells sharing a DSN group MUST declare identical pool knobs or `percellpg.Resolve` fails closed at startup; setting only `accesscore` makes `audit`/`config` keep the default and trips the mismatch gate. Partial-split: identical within each DSN group. See §Per-cell database DSNs |
| `GOCELL_PG_IDLE_TIMEOUT` | `GOCELL_{ACCESSCORE,AUDITCORE,CONFIGCORE}_DATABASE_IDLE_TIMEOUT` — same value across the DSN group, or all unset (same fail-closed rule as `MAX_CONNS`) |
| `GOCELL_PG_MAX_LIFETIME` | `GOCELL_{ACCESSCORE,AUDITCORE,CONFIGCORE}_DATABASE_MAX_LIFETIME` — same value across the DSN group, or all unset (same fail-closed rule as `MAX_CONNS`) |
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
