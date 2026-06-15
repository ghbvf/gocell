# Local Docker Deploy

This document guides a developer through starting a complete GoCell backend stack
on macOS or Linux in a single terminal session. The stack includes PostgreSQL,
Redis, database migration, and the `corebundle` binary that runs all three
platform Cells (accesscore, auditcore, configcore).

This is a development convenience, not a production blueprint. For differences
between this setup and a production deployment, see the comparison table in
[Production Differences](#production-differences) at the end of this document.


## Prerequisites

| Dependency | Minimum version | Check |
|------------|----------------|-------|
| Docker (with Compose v2) | Docker 24+, Compose v2 | `docker compose version` |
| `make` | any | `make --version` |
| `openssl` | any | `openssl version` |
| `jq` | any | `jq --version` |

`jq` is used for pretty-printing JSON responses in the curl examples below;
replace with `| python3 -m json.tool` if jq is unavailable.

Compose v2 ships bundled with Docker Desktop and with the `docker-compose-plugin`
package on Linux. The `make local-up` target calls `docker compose` (v2 syntax).


## Quick Start (5 steps)

Run these commands from the repository root in a single terminal. Each step
takes a few seconds; the whole sequence finishes in under 5 minutes.

### Step 1: Clone the repository

```bash
git clone https://github.com/ghbvf/gocell.git
cd gocell
```

### Step 2: Generate secrets

```bash
bash scripts/gen-deploy-secrets.sh
```

The script generates 14 values in `.env.local` (see §Secrets table for the
full list). The file is set to `chmod 600` automatically. The script exits
with code 1 if `.env.local` already exists, so it is safe to run without
checking first.

The JWT signing keypair is intentionally **not** written to `.env.local`. PEM
data is multiline and `docker-compose env_file` does not honour `\n` escapes —
an inline PEM in `.env.local` would arrive at `corebundle` as one literal
`\n`-containing string and fail PEM parsing. Instead, `corebundle` generates an
ephemeral RSA keypair at container start (see
`tests/e2e/Dockerfile.corebundle::start-corebundle.sh`). `make local-down &&
make local-up` rotates the keypair and invalidates any outstanding tokens — log in
again to obtain fresh credentials. For persistent keypair across restarts, see
[Production Differences](#production-differences).

### Step 3: Start the stack

```bash
make local-up
```

This runs `docker compose -f docker-compose.local.yml --env-file .env.local up
-d --wait`. Compose starts PostgreSQL and Redis, waits until both pass their
health checks, runs the database migration job to completion, then starts
`corebundle`. The `--wait` flag blocks until `corebundle` itself reports healthy
via its `/readyz` probe. Typical wall-clock time: 2-4 minutes on first run
(image build dominates); 15-30 seconds on subsequent runs with cached images.

### Step 4: Create the first admin user

> **Warning**: the password `AdminLocal!2026` below is **for local dev only**. Never reuse it in staging or production. For real deployments generate a strong password (e.g. `openssl rand -base64 16`) and rotate after first login.

```bash
source .env.local
curl -s -u "${OPS_USER}:${OPS_PASS}" \
  -X POST http://localhost:8080/api/v1/access/setup/admin \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","email":"admin@local","password":"AdminLocal!2026"}' | jq .
```

A successful response is HTTP 201 and a JSON body containing the new user's UUID.
For example:

```json
{"data": {"id": "f47ac10b-58cc-4372-a567-0e02b2c3d479", "username": "admin"}}
```

The endpoint is protected by HTTP Basic Auth using the `OPS_USER` and `OPS_PASS`
values from `.env.local`. After admin creation the endpoint permanently returns
410 Gone for any subsequent call (same or different credentials). See
[./first-run-setup.md](./first-run-setup.md) for the full protocol.

### Step 5: Log in and retrieve a JWT access token

```bash
curl -s -X POST http://localhost:8080/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"AdminLocal!2026"}' | jq .
```

A successful response is HTTP 201 and a JSON body containing an RS256-signed
access token and a refresh token. For example:

```json
{
  "data": {
    "accessToken": "eyJ...",
    "refreshToken": "eyJ...",
    "expiresAt": "2026-06-15T13:15:49Z",
    "sessionId": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "userId": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "passwordResetRequired": false
  }
}
```

Use the `accessToken` as a `Bearer` token in the `Authorization` header for all
subsequent calls to `/api/v1/*` endpoints.

### Step 6: Verify the audit hash chain

With the admin Bearer token from step 5 (admin carries the `audit:read` permission),
query the audit ledger:

```bash
TOKEN="<paste accessToken here>"
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/api/v1/audit/entries?limit=10' | jq .
```

Because the admin token carries `audit:read`, omitting `actorId` performs a
**permissioned ledger-wide read** — the response includes entries from all actors.
You should see at least two event types produced by the steps above:

- `event.user.created.v1` — published by the `system` actor during `setup/admin` (step 4)
- `event.session.created.v1` — published when you logged in (step 5)

**`actorId` query parameter semantics:**

- Omit `actorId` — permissioned ledger-wide read; requires `audit:read` permission.
- `?actorId=<your userId>` — explicit self-read; exempt from the permission gate (the
  caller is reading their own rows). Note: this exemption applies only at the routing
  gate layer; data-layer row visibility is still governed independently by the
  principal's RowScope (self → only the caller's own rows).
- `?actorId=system` — filters to system-actor events only (setup, migrations, and
  other platform operations). Unlike `?actorId=<your userId>`, this does NOT trigger
  the self-read exemption (the caller's subject is a UUID and can never equal the
  string `"system"`); the request still requires `audit:read` permission (satisfied
  here by the admin token).

To see only system-actor events (useful for verifying what setup/admin wrote):

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/api/v1/audit/entries?actorId=system&limit=10' | jq .
```


## Topology

The local stack exposes exactly one port on the Docker host: **`:8080`**
(the primary listener, JWT authentication). The internal listener (`127.0.0.1:9090`)
and health listener (`127.0.0.1:9091`) remain bound to the container's loopback
interface and are not accessible from the host.

For the authoritative three-listener topology and how listeners map to
authentication policies, see [./listener-topology.md](./listener-topology.md).

### Why `network_mode: "service:redis"`

`corebundle` joins the Redis container's network namespace. As a result, it sees
Redis at `127.0.0.1:6379` rather than a Docker bridge hostname like `redis:6379`.

This is required because `pkg/secutil.ValidateTLSEndpoint` rejects non-TLS
Redis addresses in real adapter mode unless the hostname is a loopback IP literal
(`127.0.0.1` or `::1`). The shared network namespace makes the address a
loopback literal, satisfying the exception. The primary listener port (`8080`)
is published on the Redis service definition because `corebundle` has no netns
of its own — it borrows Redis's.

This pattern is the macOS-Docker equivalent of `network_mode: host` (Linux only),
which is used by the e2e CI harness (`tests/e2e/docker-compose.e2e.yaml`).
Production deployments replace this arrangement with a managed TLS Redis URL
(see [Production Differences](#production-differences)).

### Health endpoint visibility

The health listener binds to `127.0.0.1:9091` (`GOCELL_HTTP_HEALTH_LOCAL_ONLY=1`), which is reachable **only from inside the `corebundle` container's netns**—that's what the Docker `healthcheck` directive uses. From the host machine, `curl http://localhost:9091/readyz` will fail (connection refused). To probe readiness from the host, either:

- Inspect `docker compose ps`—the `corebundle` container reports `(healthy)` when readyz is green;
- Or `docker compose -f docker-compose.local.yml --env-file .env.local exec corebundle curl -fsS http://127.0.0.1:9091/readyz`.

Primary port `:8080` is the only listener published to the host; business `/api/v1/*` traffic enters there.


## Secrets and `.env.local`

### What the script generates

`scripts/gen-deploy-secrets.sh` writes 14 values to `.env.local`:

| Variable | How generated |
|----------|---------------|
| `PG_PASSWORD` | `openssl rand -hex 16` |
| `GOCELL_APP_PASSWORD` | `openssl rand -hex 16` (hex = URL-safe; embedded in the restricted serving DSN) |
| `RABBITMQ_PASSWORD` | `openssl rand -hex 16` (hex = URL-safe; embedded in `GOCELL_AMQP_URL` userinfo, #1940) |
| `CONFIGCORE_MASTER_KEY` | `openssl rand -hex 32` (64 hex chars) |
| `CONFIGCORE_CURSOR_KEY` | `openssl rand -base64 32` |
| `AUDITCORE_HMAC_KEY` | `openssl rand -base64 32` |
| `AUDITCORE_CURSOR_KEY` | `openssl rand -base64 32` |
| `ACCESSCORE_CURSOR_KEY` | `openssl rand -base64 32` |
| `ACCESSCORE_IP_HASH_SALT` | `openssl rand -base64 32` |
| `SERVICE_SECRET` | `openssl rand -base64 32` |
| `METRICS_TOKEN` | `openssl rand -base64 32` |
| `READYZ_VERBOSE_TOKEN` | `openssl rand -hex 32` |
| `OPS_USER` | Fixed value: `ops` |
| `OPS_PASS` | `openssl rand -hex 12` |

`GOCELL_AUDIT_ADMIN_PASSWORD` and `GOCELL_AUDIT_ADMIN_DSN` are **not** auto-generated
by the script. The super-admin cross-tenant audit read capability is **optional and
off by default**: when these variables are absent, super-admin cross-tenant audit reads
return HTTP 501 (fail-closed) and `make local-up` succeeds without them.
See [§Enabling the optional audit admin pool](#enabling-the-optional-audit-admin-pool)
for the complete opt-in recipe.

### File protection

`.env.local` is listed in `.gitignore`. The script sets `chmod 600` on the
generated file. Never commit this file to version control.

`.env.local.example` (committed) is a template showing the expected variable
names and value shapes. It contains no real secrets.

### Secret rotation

To rotate all secrets at once:

```bash
rm .env.local
bash scripts/gen-deploy-secrets.sh
make local-down
make local-up
```

Rotation also invalidates any tokens issued by the previous ephemeral JWT
keypair (regenerated at container start). The new stack starts without any
admin user, so you must repeat step 4 (setup/admin) after `make local-up`
completes.

To rotate individual secrets without full teardown, edit `.env.local` manually,
then run `make local-down && make local-up`.


## Dual-role PostgreSQL (RLS runtime enforcement)

GoCell uses PostgreSQL Row Level Security (`FORCE ROW LEVEL SECURITY`) on all seven
standard tenant tables (config_entries / config_versions / feature_flags / users /
roles / role_assignments / policies, migrations 052–053 + 059; audit_entries
carries a system-rows variant, migration 055). RLS is enforced at runtime only when the
**serving pool role** is a non-owner that lacks superuser and `BYPASSRLS`. A
superuser ignores all policies regardless of schema correctness.

To enforce RLS end-to-end, the stack uses **two required PostgreSQL roles** plus one optional role:

| Role | Used by | Privileges | Required? |
|------|---------|-----------|-----------|
| `gocell` | `migrate` service (pg-migrate tool) | Superuser / table owner; runs DDL | Yes |
| `gocell_app` | `corebundle` serving pool (`GOCELL_CONFIGCORE_DATABASE_URL` / `GOCELL_AUDITCORE_DATABASE_URL` / `GOCELL_ACCESSCORE_DATABASE_URL`; per-cell seam #1964, dedup to one pool) | NOSUPERUSER, NOBYPASSRLS, non-owner; DML only via default privileges | Yes |
| `gocell_audit_admin` | `corebundle` admin read pool (`GOCELL_AUDIT_ADMIN_DSN`) — for super-admin cross-tenant audit reads (#1810) | NOSUPERUSER, NOBYPASSRLS; SELECT-only on `audit_entries` via role-scoped RLS policy `audit_admin_read_all` (migration 065) | **Optional** — skipped when `GOCELL_AUDIT_ADMIN_PASSWORD` is unset |

`gocell_app` is created by `deploy/postgres/init/10-restricted-role.sh`, which
the PostgreSQL container runs once on first data-directory init (before migrations).
Migrations are run by the admin role `gocell`, so every table created is auto-granted
to `gocell_app` via `ALTER DEFAULT PRIVILEGES`.

`gocell_audit_admin` is also created by `10-restricted-role.sh`, but only when
`GOCELL_AUDIT_ADMIN_PASSWORD` is set at initdb time. When the variable is unset,
the script prints an informational message and skips the role entirely — migration 065
is a no-op where the role is absent, and the `GOCELL_AUDIT_ADMIN_DSN` runtime variable
is left unconfigured, causing super-admin cross-tenant audit read to return HTTP 501
(fail-closed). `make local-up` succeeds without this variable.

### New env var: `GOCELL_APP_PASSWORD`

`GOCELL_APP_PASSWORD` sets the password for the restricted role. It must be set in
`.env.local`. The secret-generation script `scripts/gen-deploy-secrets.sh` writes
it automatically alongside `PG_PASSWORD`. Use `.env.local.example` as a reference.

### First-time setup (new role, new data dir)

If you already have a local stack running with the old single-role wiring:

```bash
make local-down        # stops containers AND removes the pgdata volume (-v)
# ensure .env.local contains GOCELL_APP_PASSWORD (re-run gen-deploy-secrets.sh)
make local-up          # fresh PG data dir: initdb runs 10-restricted-role.sh
```

The `make local-down` target runs `docker compose ... down -v`, which removes
the `pgdata` named volume so the next `make local-up` triggers a full initdb.
This is a one-time step; subsequent `make local-down && make local-up` cycles
reuse the initdb mechanism automatically.

### Enabling the optional audit admin pool

The super-admin cross-tenant audit read capability (`gocell_audit_admin` pool) is
**optional and off by default**. Enabling it locally requires **two variables** set in
`.env.local` — both must be present for the capability to function:

| Variable | Purpose | When set |
|----------|---------|----------|
| `GOCELL_AUDIT_ADMIN_PASSWORD` | Password for the `gocell_audit_admin` PG role | Must be present at **initdb time** (before the first `make local-up` on a fresh data directory). The init script `10-restricted-role.sh` reads it to create the role. |
| `GOCELL_AUDIT_ADMIN_DSN` | DSN corebundle uses to connect the admin read pool | Read at **runtime** by corebundle. Absent → 501; present → admin pool connected and `/readyz` gains `postgres_audit_admin_restricted_ready`. |

**Step-by-step opt-in (fresh data directory)**:

1. Generate core secrets if you have not already:

   ```bash
   bash scripts/gen-deploy-secrets.sh
   ```

2. Append the two optional variables to `.env.local` (the password must be hex/URL-safe):

   ```bash
   AUDIT_ADMIN_PW=$(openssl rand -hex 16)
   echo "GOCELL_AUDIT_ADMIN_PASSWORD=${AUDIT_ADMIN_PW}" >> .env.local
   echo "GOCELL_AUDIT_ADMIN_DSN=postgres://gocell_audit_admin:${AUDIT_ADMIN_PW}@postgres:5432/gocell?sslmode=disable" >> .env.local
   ```

3. Start the stack on a fresh data directory (initdb runs `10-restricted-role.sh`,
   which reads `GOCELL_AUDIT_ADMIN_PASSWORD` and creates the role; migration 065 then
   installs the `audit_admin_read_all` RLS policy):

   ```bash
   make local-up
   ```

   If you already have an existing `pgdata` volume (the role was not provisioned at
   initdb), you must destroy it first:

   ```bash
   make local-down    # removes pgdata volume
   make local-up
   ```

4. Verify the admin pool probe is green:

   ```bash
   docker compose -f docker-compose.local.yml --env-file .env.local exec corebundle \
     curl -fsS "http://127.0.0.1:9091/readyz?verbose" | grep audit_admin
   ```

   A healthy output contains `"postgres_audit_admin_restricted_ready": "ok"`.

**What "off" means**: when either variable is absent, `make local-up` succeeds
unchanged — corebundle does not connect an admin pool, and super-admin cross-tenant
audit read requests return HTTP 501. No manual step is needed to keep the default
behaviour.

### Probe: `postgres_app_role_restricted_ready`

After the dual-role migration, `/readyz` exposes a new probe
`postgres_app_role_restricted_ready`. It fails (503) when the serving role is a
superuser or carries `BYPASSRLS`. A green probe confirms RLS is active at runtime.
See `docs/ops/readyz.md` §Adapter-level: serving-role capability probe.


## Troubleshooting

### `make local-up` exits with `Error 1` after ~2 minutes

The log line typically reads:

```
container gocell-local-corebundle-1 exited (1)
make: *** [local-up] Error 1
```

`make local-up` runs `docker compose ... up -d --wait` and waits up to 40 retries x 3 s = 120 s for `corebundle` to report healthy. Exit 1 means the corebundle container either started and exited, or never reached `/readyz` green. Inspect logs:

```bash
docker compose -f docker-compose.local.yml --env-file .env.local logs corebundle
```

Common root causes:

- Missing env var — corebundle prints `ERR_VALIDATION_FAILED` with the offending env name. Re-run `bash scripts/gen-deploy-secrets.sh` after deleting `.env.local`.
- Wrong key format — `ERR_AUTH_KEY_INVALID` for JWT, or PEM parse errors when injecting multiline keys via `env_file` (see §Production Differences).
- PG migration incomplete — check `docker compose -f docker-compose.local.yml --env-file .env.local logs migrate`; should exit 0 with no error output.
- Bound port conflict on `:8080` — `lsof -i :8080` to find the offending process.

### `corebundle` restart loop with `ERR_ADAPTER_ENDPOINT_NOT_TLS`

The log line looks like:

```
ERR_ADAPTER_ENDPOINT_NOT_TLS: Redis address is not a TLS endpoint
```

Cause: `corebundle` is not sharing Redis's network namespace, so it resolves
`GOCELL_REDIS_ADDR=127.0.0.1:6379` against the Docker bridge network instead of
the loopback. This happens when `docker-compose.local.yml` is modified and the
`network_mode: "service:redis"` line is removed or altered.

Fix: restore `network_mode: "service:redis"` on the `corebundle` service.

### Migration exits with non-zero code

Compose logs show the `migrate` service exiting with an error and the migration
failing.

Cause: the `migrate` service started before PostgreSQL finished its health check.
This should not happen in normal operation because the migration service declares
`depends_on: postgres: condition: service_healthy`. If it does happen, it is
usually a sign that the health check interval or retry count in
`docker-compose.local.yml` was changed.

Fix: run `make local-down && make local-up`. Compose will wait for PostgreSQL to
be healthy before starting the migration service.

### setup/admin returns 401 Unauthorized

The `OPS_USER` / `OPS_PASS` credentials passed in the `curl -u` flag do not
match the values in `.env.local`.

Fix: make sure you ran `source .env.local` in the same terminal session before
the curl command, so the shell variables are populated from the generated file.

### setup/admin returns 410 Gone

An admin user was already created in a previous run of the stack. The endpoint
permanently returns 410 after the first successful admin creation.

Fix: skip setup/admin and proceed directly to login (step 5). If you want a
fresh admin with a different password, run `make local-down && make local-up`
(which destroys the database volume) then repeat from step 4.

### Port `:8080` is already in use

The `make local-up` command fails or the Redis container fails to start because
port 8080 is occupied on the host.

```bash
lsof -i :8080
```

Stop whatever process is using port 8080, or change the published port mapping
in `docker-compose.local.yml` (note: since the port is declared on the `redis`
service, both the `redis` entry and any curl commands must use the new port).


## Cleanup

To stop the stack and remove all volumes (including the PostgreSQL data directory):

```bash
make local-down
```

This runs `docker compose -f docker-compose.local.yml --env-file .env.local down -v`.

To stop the stack while keeping PostgreSQL data intact (for a faster restart):

```bash
docker compose -f docker-compose.local.yml --env-file .env.local down
```

Omitting `-v` preserves the `pgdata` named volume so the next `make local-up`
resumes from the existing database state, including any previously created users.


## Production Differences

The local stack makes several trade-offs that are appropriate for a development
machine but not for a production deployment.

| Dimension | local-docker-deploy | Production replacement |
|-----------|---------------------|----------------------|
| Redis topology | `network_mode: "service:redis"` — shared netns + loopback exception | Managed TLS Redis: `GOCELL_REDIS_ADDR=rediss://redis.example.com:6380` |
| PostgreSQL TLS | `sslmode=disable` — plaintext connection | `sslmode=verify-full` with managed CA certificate |
| JWT keypair | Ephemeral, generated per container start by the Dockerfile startup wrapper (single pod; tokens invalidate on restart) | Kubernetes Secret with PEM **mounted as a file**, not via `env_file`/multiline env (newline-encoding is brittle). Either: (a) `valueFrom.secretKeyRef` + YAML block scalar (`|`) for proper newline preservation; or (b) volume-mount the PEM and set both keys in an entrypoint wrapper: `GOCELL_JWT_PRIVATE_KEY="$(cat /run/secrets/jwt-private.pem)" GOCELL_JWT_PUBLIC_KEY="$(cat /run/secrets/jwt-public.pem)" exec /usr/local/bin/corebundle "$@"` — both `GOCELL_JWT_PRIVATE_KEY` and `GOCELL_JWT_PUBLIC_KEY` are required; omitting either causes `ERR_AUTH_KEY_MISSING` at startup. Remove the Dockerfile startup wrapper's openssl-generation block (the env-set guard already passes through env-injected keys). Shared across all pods; survives restarts. |
| Config encryption KMS | `GOCELL_CONFIGCORE_KEY_PROVIDER=local-aes`, master key in env | `GOCELL_CONFIGCORE_KEY_PROVIDER=vault-transit` + AppRole or Kubernetes auth |
| Pod count | `GOCELL_SINGLE_POD=1` — single pod, in-memory nonce store | Multi-pod: remove `GOCELL_SINGLE_POD`; nonce store backed by Redis; route via Kubernetes Service |
| Health listener bind | `127.0.0.1:9091` with `GOCELL_HTTP_HEALTH_LOCAL_ONLY=1` — container loopback only | `:9091` (Pod-reachable) for Kubelet probes and Prometheus scrapes; remove `LOCAL_ONLY`; see [./listener-topology.md](./listener-topology.md) |
| Internal listener bind | `127.0.0.1:9090` — container loopback, not reachable from Docker bridge | NetworkPolicy + caller-cell allowlist; see [./listener-topology.md](./listener-topology.md) |
| Secret rotation | `rm .env.local` + re-run script + full teardown + re-run setup/admin | Rolling K8s Secret replacement + `kubectl rollout restart`; see [./first-run-setup.md](./first-run-setup.md) |
| EventBus / AMQP broker | RabbitMQ container in the compose stack; `GOCELL_AMQP_URL=amqp://gocell:…@rabbitmq:5672/`. **postgres topology requires a real broker — `GOCELL_SINGLE_POD=1` does NOT waive it** (single-pod only waives *distributed nonce/replay*, not the durable outbox event broker); corebundle fail-closes at startup without `GOCELL_AMQP_URL` (#1940). | Managed AMQP broker (RabbitMQ / cluster) over TLS: `GOCELL_AMQP_URL=amqps://user:pass@rabbitmq.example.com:5671/`. The broker is required whenever `GOCELL_CELL_ADAPTER_MODE=postgres`, independent of pod count. |


## Related Documents

- [./listener-topology.md](./listener-topology.md) — authoritative three-listener
  topology (primary :8080 / internal :9090 / health :9091), port binding rules,
  Docker Compose and Kubernetes deployment guidance
- [./first-run-setup.md](./first-run-setup.md) — full setup/admin protocol,
  operator credential lifecycle, password reset flow, and credential rotation
- [./env-vars.md](./env-vars.md) — complete environment variable reference for
  `cmd/corebundle`
- `tests/e2e/docker-compose.e2e.yaml` — CI end-to-end fixture; uses
  `network_mode: host` (Linux only); the present document is the macOS-friendly
  equivalent
