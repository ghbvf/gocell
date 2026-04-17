# sso-bff Example

A single-process SSO BFF (Backend For Frontend) demonstrating how to compose the three
built-in GoCell Cells into one assembly:

- **access-core** (L2 OutboxFact): identity management, session lifecycle (login/refresh/logout), RBAC
- **audit-core** (L3 WorkflowEventual): tamper-evident audit log with hash chain
- **config-core** (L2 OutboxFact): configuration CRUD, publish/rollback, feature flags

All dependencies are in-memory (no external services required).

## Quick Start

```bash
go run ./examples/sso-bff
```

The server starts on `:8081`.

## Seed User

On startup, a random admin password is generated and printed to the console:

```
{"level":"INFO","msg":"sso-bff: seed admin ready — use these credentials to log in","username":"admin","password":"<random>","note":"dev-only, resets on restart"}
```

Copy the `password` value from the log and use it in the walkthrough below.
The password resets every time the server restarts (in-memory only).

## API Walkthrough

### 1. Login as seed admin

```bash
curl -s -X POST http://localhost:8081/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<password from startup log>"}' | jq
```

Save the returned `accessToken` as your admin token:

```bash
export ADMIN_TOKEN="<accessToken from admin login>"
```

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

### 10. Read a config entry

```bash
curl -s http://localhost:8081/api/v1/config/site.title | jq
```

### 11. List feature flags

```bash
curl -s http://localhost:8081/api/v1/flags | jq
```

### 12. Health checks

```bash
curl -s http://localhost:8081/healthz | jq
curl -s http://localhost:8081/readyz  | jq
curl -s http://localhost:8081/readyz?verbose | jq
```

`/healthz` is liveness-only. Use `/readyz?verbose` when you need the detailed cell and dependency breakdown.

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
{"error": {"code": "ERR_CSRF_ORIGIN_DENIED", "message": "cross-origin request denied", "details": {}}}
```

Status: 403 Forbidden. The frontend should handle this by redirecting to the
login page or displaying an appropriate error.

### Integration Status

Handler-level BFF integration (login/refresh/logout setting cookies) is
tracked in the backlog. The current PR provides the middleware primitives.

## Docker Mode (Future)

Infrastructure services are provided for future adapter-based mode:

```bash
cd examples/sso-bff
docker compose up -d
cd ../..
go run ./examples/sso-bff
```
