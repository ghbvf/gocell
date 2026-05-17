# ssobff Example

A single-process SSO BFF (Backend For Frontend) demonstrating how to compose the three
built-in GoCell Cells into one assembly:

- **accesscore** (L2 OutboxFact): identity management, session lifecycle (login/refresh/logout), RBAC
- **auditcore** (L3 WorkflowEventual): tamper-evident audit log with hash chain
- **configcore** (L2 OutboxFact): configuration CRUD, publish/rollback, feature flags

All three cells use PostgreSQL for persistence. A running PostgreSQL instance and
`DATABASE_URL` are required before starting the server.

## Quick Start

PostgreSQL is required. Start the bundled Docker Compose stack first (see
[Docker Infrastructure](#docker-infrastructure)), then:

```bash
export DATABASE_URL="postgres://gocell:gocell@localhost:5432/ssobff?sslmode=disable"
export GOCELL_SSOBFF_SERVICE_SECRET="$(openssl rand -base64 32)"
go run ./examples/ssobff
```

The server starts three listeners following `docs/ops/listener-topology.md`:

- **primary** on `:8081` — JWT-authenticated business API (`/api/v1/*`)
- **internal** on `127.0.0.1:9081` — service-token control-plane (`/internal/v1/*`), loopback by default; protected by service-token auth and the process fails fast when `GOCELL_SSOBFF_SERVICE_SECRET` is missing or shorter than 32 bytes
- **health** on `127.0.0.1:9091` — `/healthz`, `/readyz`, `/metrics`, loopback by default

Each address is overridable via environment variable (see [Environment Variables](#environment-variables)).

## Docker Infrastructure

**Required before starting ssobff.** The stack provides the PostgreSQL instance
that all three cells depend on.

```bash
cd examples/ssobff
export GOCELL_EXAMPLE_POSTGRES_PASSWORD="$(openssl rand -base64 24)"
export GOCELL_EXAMPLE_RABBITMQ_PASSWORD="$(openssl rand -base64 24)"
docker compose up -d
# wait for postgres to become healthy, then set DATABASE_URL
export DATABASE_URL="postgres://gocell:${GOCELL_EXAMPLE_POSTGRES_PASSWORD}@localhost:5432/ssobff?sslmode=disable"
```

## First Admin Provisioning

PR #392 introduced the closed `auth.bootstrap` contract: the demo's first
admin is created interactively via `POST /api/v1/access/setup/admin`, which
is protected by HTTP Basic Auth using the bootstrap operator credentials
hardcoded in `examples/ssobff/app.go`:

```go
// examples/ssobff/app.go (package main, demo-only)
const (
    ssobffBootstrapUsername = "ssobff-ops"
    ssobffBootstrapPassword = "ssobff-bootstrap-pass-1!"
)
```

These constants live in this demo binary only — production deployments
inject `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`
via env (see `docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`
§D2 + `docs/ops/first-run-setup.md`).

```bash
# Provision the admin (operator authenticates with ssobffBootstrap* creds;
# request body defines the admin identity — D5 separation).
curl -s -X POST http://localhost:8081/api/v1/access/setup/admin \
  -u 'ssobff-ops:ssobff-bootstrap-pass-1!' \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","email":"admin@local","password":"MyStr0ngP@ss!"}'
# 201 Created (subsequent calls return 410 Gone — one-shot)
```

The admin you just created uses the password from the request body —
operator-set passwords are not "initial randoms", so `passwordResetRequired`
is `false` from the start. The reset flow is exercised by the dedicated
`identitymanage` change-password endpoint (`POST /api/v1/access/users/{id}/password`),
not by the setup path.

```bash
export ADMIN_TOKEN=$(curl -s -X POST http://localhost:8081/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"MyStr0ngP@ss!"}' | jq -r '.data.accessToken')
```

After this the `ADMIN_TOKEN` works for all business endpoints.

## API Walkthrough

Every endpoint below except `POST /api/v1/access/sessions/login` and
`POST /api/v1/access/sessions/refresh` requires a `Authorization: Bearer $TOKEN`
header. Public routes are declared per-Cell via `auth.Mount(mux, auth.Route{Contract: ..., Public: true})`
inside `cells/accesscore/cell.go`; the composition root (`examples/ssobff/main.go`)
通过 `bootstrap.WithListener(..., []cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)})`
把 JWT 校验装配到 primary listener auth chain，phase4 从 `authProvider` Cell 自动发现 verifier
（不再使用顶层 Bootstrap 鉴权发现/中间件选项）。
`walkthrough_test.go` exercises the same sequence and is the authoritative
behaviour record if a curl here disagrees.

### 1. Login as admin (after completing First Login & Password Reset above)

```bash
curl -s -X POST http://localhost:8081/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"MyStr0ngP@ss!"}' | jq
```

Save the returned `accessToken` as your admin token:

```bash
export ADMIN_TOKEN="<accessToken from admin login>"
```

### Login Failure UX (uniform 401)

The public login endpoint deliberately collapses every failure mode to a
single `401 ERR_AUTH_LOGIN_FAILED` response so an attacker cannot enumerate
accounts or status from response body/timing:

```json
{
  "error": {
    "code": "ERR_AUTH_LOGIN_FAILED",
    "message": "invalid credentials",
    "details": []
  }
}
```

The same wire shape covers all four cases:

| Cause | Underlying state |
|-------|------------------|
| Missing user | Username does not exist in `users` table |
| Wrong password | bcrypt compare fails against stored hash |
| Inactive account | User exists but `status` ∈ {`locked`, `suspended`} |
| Concurrent deactivation race | User passed pre-tx active check but was locked between then and the in-tx FOR UPDATE re-fetch |

**Timing normalization**: the handler always runs a real bcrypt compare —
even on missing-user it compares against a pre-computed `dummyBcryptHash`
(cost=12) so response latency does not leak whether the user exists.

**Server-side observability**: each failure records the underlying cause via
`errcode.WithInternal` into structured `slog` (never on the wire). Operators
distinguish causes via the `internal` slog field — see
[`docs/ops/login-failure-triage.md`](../../docs/ops/login-failure-triage.md)
for query recipes and the four Internal-text templates.

**Admin path divergence**: `identitymanage.IssueForUser` (used when an admin
changes a user's password) returns `403 ERR_AUTH_USER_NOT_ACTIVE` for non-active
users rather than the uniform 401. The admin path is authenticated, so there
is no enumeration concern; surfacing the specific cause helps admin tooling.

### 2. Create a user (requires admin)

```bash
curl -s -X POST http://localhost:8081/api/v1/access/users \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"username":"alice","password":"P@ssw0rd123","email":"alice@example.com"}' | jq
```

### 3. Login as alice

```bash
curl -s -X POST http://localhost:8081/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"P@ssw0rd123"}' | jq
```

Save alice's tokens:

```bash
export ACCESS_TOKEN="<accessToken from alice login>"
export REFRESH_TOKEN="<refreshToken from alice login>"
export SESSION_ID="<sessionId from alice login>"
```

### 4. Refresh token

The refresh endpoint is public (no Authorization header required).

```bash
curl -s -X POST http://localhost:8081/api/v1/access/sessions/refresh \
  -H 'Content-Type: application/json' \
  -d "{\"refreshToken\":\"$REFRESH_TOKEN\"}" | jq
```

### 5. Get user profile

```bash
curl -s http://localhost:8081/api/v1/access/users/{userId} \
  -H "Authorization: Bearer $ACCESS_TOKEN" | jq
```

(Replace `{userId}` with the `id` from step 2's response.)

### 6. Logout (delete session)

Use the `sessionId` returned by the login response (saved as `$SESSION_ID` above).

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  http://localhost:8081/api/v1/access/sessions/$SESSION_ID
```

### 7. Query audit entries

```bash
curl -s http://localhost:8081/api/v1/audit/entries \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq '.data[] | {action: .eventType, at: .timestamp}'
```

### 8. Create a config entry

```bash
curl -s -X POST http://localhost:8081/api/v1/config/ \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"key":"site.title","value":"My SSO Portal"}' | jq
```

### 9. Update a config entry

```bash
curl -s -X PUT http://localhost:8081/api/v1/config/site.title \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"value":"SSO Portal v2"}' | jq
```

### 10. Read a config entry (admin-only)

PR-CFG-C tightened all `GET /api/v1/config/*` and `GET /api/v1/flags/*`
endpoints to `RoleAdmin` because key names + the `sensitive` flag are
themselves a recon surface — even though sensitive values are redacted,
enumerating "which secrets exist" leaks attack-surface information. Use the
admin token here, not alice's `$ACCESS_TOKEN`. Calling with a non-admin token
returns `403 Forbidden`.

```bash
curl -s http://localhost:8081/api/v1/config/site.title \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq
```

### 11. List feature flags (admin-only)

Same admin gate as config read.

```bash
curl -s http://localhost:8081/api/v1/flags/ \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq
```

### 12. Health checks

Health probes live on the dedicated health listener (loopback by default).
From the same host:

```bash
curl -s http://127.0.0.1:9091/healthz | jq
curl -s http://127.0.0.1:9091/readyz  | jq
curl -s -H "X-Readyz-Token: $GOCELL_READYZ_VERBOSE_TOKEN" \
  'http://127.0.0.1:9091/readyz?verbose' | jq
```

Responses use the project-wide envelope: success bodies are
`{"data": {"status": "healthy", ...}}`; 503 / 401 bodies are
`{"error": {"code": "ERR_READYZ_...", "message": "...", "details": []}}`
(K#08 5xx redaction policy — runtime context is emitted to server-side
`slog` instead of the wire body; see `docs/ops/readyz.md` for the full
contract).

`/healthz` is liveness-only. Use `/readyz?verbose` when you need the detailed cell and dependency breakdown — PR-A35 requires `GOCELL_READYZ_VERBOSE_TOKEN` to be set and the request to carry the matching `X-Readyz-Token` header (or set `GOCELL_READYZ_VERBOSE_DISABLED=1` to waive the endpoint).

## BFF Cookie Session Mode (Planned)

The middleware package provides CSRF protection and cookie-based session
management for browser-facing deployments. When enabled, JWT tokens are
stored in `HttpOnly; Secure; SameSite=Strict` cookies instead of being
returned in response bodies.

### Middleware Chain

```
CSRF → CookieSession → AuthMiddleware → handler
```

- **CSRF**: validates `Sec-Fetch-Site` / `Origin` / `Referer` against `TrustedOrigins`.
  Runs first to reject cross-origin requests (403) before any cookie processing.
- **CookieSession**: reads signed cookie → injects `Authorization: Bearer` header
- **AuthMiddleware**: verifies JWT → injects `Claims` into context

### CSRF Rejection

When a request is rejected by CSRF middleware, the response is:

```json
{"error": {"code": "ERR_CSRF_ORIGIN_DENIED", "message": "cross-origin request denied", "details": []}}
```

Status: 403 Forbidden. The frontend should handle this by redirecting to the
login page or displaying an appropriate error.

### Integration Status

Handler-level BFF integration (login/refresh/logout setting cookies) is
tracked in the backlog. The current PR provides the middleware primitives.

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `DATABASE_URL` | (required) | PostgreSQL DSN, e.g. `postgres://user:pass@localhost:5432/ssobff?sslmode=disable`. The process fails fast when absent. |
| `GOCELL_STATE_DIR` | (per-OS) | Override the directory holding the bootstrap admin credential file. |
| `GOCELL_SSOBFF_SERVICE_SECRET` | (required) | Internal listener service-token shared secret. ≥ 32 bytes; missing or short value fails the process at startup. |
| `GOCELL_SSOBFF_PRIMARY_ADDR` | `:8081` | Primary listener bind address (public business API). |
| `GOCELL_SSOBFF_INTERNAL_ADDR` | `127.0.0.1:9081` | Internal listener bind (control-plane / service-token). Loopback default keeps it off the public network until the operator opts in. |
| `GOCELL_SSOBFF_HEALTH_ADDR` | `127.0.0.1:9091` | Health listener bind (`/healthz`, `/readyz`, `/metrics`). Loopback default. |
| `GOCELL_READYZ_VERBOSE_TOKEN` | (unset) | When set, `/readyz?verbose=true` requires a matching `X-Readyz-Token` header. Unset leaves the verbose body disabled (recommended for demos). |

The smoke test (`make test-examples-smoke`) injects high ports
(`28081/29081/29091`) via these variables to avoid colliding with
developer dev servers.

## Development

```bash
# Build all gocell binaries
make build

# Run the demo
go run ./examples/ssobff

# Verify the demo's `main.go` startup path end-to-end (subprocess +
# /readyz probe + SIGTERM graceful shutdown). Mirrors the CI examples-
# smoke job. Required before pushing main.go / option-wiring changes.
make test-examples-smoke
```
