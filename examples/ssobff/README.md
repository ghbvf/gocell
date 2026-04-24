# sso-bff Example

A single-process SSO BFF (Backend For Frontend) demonstrating how to compose the three
built-in GoCell Cells into one assembly:

- **accesscore** (L2 OutboxFact): identity management, session lifecycle (login/refresh/logout), RBAC
- **auditcore** (L3 WorkflowEventual): tamper-evident audit log with hash chain
- **configcore** (L2 OutboxFact): configuration CRUD, publish/rollback, feature flags

All dependencies are in-memory (no external services required).

## Quick Start

```bash
go run ./examples/ssobff
```

The server starts on `:8081`.

## Seed User

On first startup, when no admin user exists, the bootstrap process creates
an initial admin account and writes the credentials to a file. Read the file:

```bash
# Linux
cat /run/gocell/initial_admin_password

# macOS
cat "$HOME/Library/Application Support/gocell/run/initial_admin_password"
```

```powershell
# Windows
Get-Content "$env:LOCALAPPDATA\gocell\run\initial_admin_password"
```

The file contains:

```
# GoCell initial admin credential
# Generated at: 2026-04-18T19:00:00Z
# Expires at:   2026-04-19T19:00:00Z
# This file is auto-deleted by the cleanup worker.
username=admin
password=<random base64 password>
expires_at=<unix timestamp>
```

You can override the default path on every platform by setting an absolute
`GOCELL_STATE_DIR` before starting:

```bash
export GOCELL_STATE_DIR=$TMPDIR/gocell
go run ./examples/ssobff
```

```powershell
$env:GOCELL_STATE_DIR = "$env:TEMP\gocell"
go run ./examples/ssobff
```

See [docs/operations/first-run-setup.md](../../docs/operations/first-run-setup.md) for full
deployment details (Docker, Kubernetes, troubleshooting).

## First Login & Password Reset

After reading the credential file, the admin token will carry
`passwordResetRequired=true`. All business endpoints return 403 until the
password is changed. Follow these steps:

```bash
# 1. Read the initial password
if [ -n "${GOCELL_STATE_DIR:-}" ]; then
  CRED_FILE="$GOCELL_STATE_DIR/initial_admin_password"
elif [ "$(uname -s)" = "Darwin" ]; then
  CRED_FILE="$HOME/Library/Application Support/gocell/run/initial_admin_password"
else
  CRED_FILE="/run/gocell/initial_admin_password"
fi
INIT_PASS=$(grep '^password=' "$CRED_FILE" | cut -d= -f2)
ADMIN_USER=$(grep '^username=' "$CRED_FILE" | cut -d= -f2)

# 2. Login (passwordResetRequired=true in response)
TOKEN_RESP=$(curl -s -X POST http://localhost:8081/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${INIT_PASS}\"}")
echo "$TOKEN_RESP" | jq .
# {"data":{"accessToken":"...","passwordResetRequired":true,...}}

BOOTSTRAP_TOKEN=$(echo "$TOKEN_RESP" | jq -r '.data.accessToken')

# 3. Extract user ID from the JWT sub claim.
USER_ID=$(BOOTSTRAP_TOKEN="$BOOTSTRAP_TOKEN" python3 - <<'PY'
import base64, json, os
payload = os.environ["BOOTSTRAP_TOKEN"].split(".")[1]
payload += "=" * (-len(payload) % 4)
print(json.loads(base64.urlsafe_b64decode(payload))["sub"])
PY
)

# 4. Change password (returns new token with passwordResetRequired=false)
NEW_TOKEN_RESP=$(curl -s -X POST "http://localhost:8081/api/v1/access/users/${USER_ID}/password" \
  -H "Authorization: Bearer $BOOTSTRAP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"oldPassword\":\"${INIT_PASS}\",\"newPassword\":\"MyStr0ngP@ss!\"}")
echo "$NEW_TOKEN_RESP" | jq .
# {"data":{"accessToken":"...","passwordResetRequired":false,...}}

export ADMIN_TOKEN=$(echo "$NEW_TOKEN_RESP" | jq -r '.data.accessToken')
```

PowerShell equivalent for Windows:

```powershell
$CredFile = if ($env:GOCELL_STATE_DIR) {
  Join-Path $env:GOCELL_STATE_DIR "initial_admin_password"
} else {
  Join-Path $env:LOCALAPPDATA "gocell\run\initial_admin_password"
}
$Cred = Get-Content $CredFile | Where-Object { $_ -match "=" } | ConvertFrom-StringData

$TokenResp = Invoke-RestMethod -Method Post http://localhost:8081/api/v1/access/sessions/login `
  -ContentType "application/json" `
  -Body (@{ username = $Cred.username; password = $Cred.password } | ConvertTo-Json)
$BootstrapToken = $TokenResp.data.accessToken

$Payload = $BootstrapToken.Split(".")[1].Replace("-", "+").Replace("_", "/")
$Payload = $Payload.PadRight($Payload.Length + (4 - $Payload.Length % 4) % 4, "=")
$UserID = ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Payload)) | ConvertFrom-Json).sub

$NewTokenResp = Invoke-RestMethod -Method Post "http://localhost:8081/api/v1/access/users/$UserID/password" `
  -Headers @{ Authorization = "Bearer $BootstrapToken" } `
  -ContentType "application/json" `
  -Body (@{ oldPassword = $Cred.password; newPassword = "MyStr0ngP@ss!" } | ConvertTo-Json)
$env:ADMIN_TOKEN = $NewTokenResp.data.accessToken
```

After this the `ADMIN_TOKEN` works for all business endpoints.

## API Walkthrough

Every endpoint below except `POST /api/v1/access/sessions/login` and
`POST /api/v1/access/sessions/refresh` requires a `Authorization: Bearer $TOKEN`
header. Public routes are declared per-Cell via `auth.Declare(mux, auth.RouteDecl{Public: true})`
inside `cells/accesscore/cell.go`; the composition root (`examples/ssobff/main.go`)
opts into verifier discovery via `bootstrap.WithAuthDiscovery()` without
hardcoding any endpoint list.
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
curl -s http://localhost:8081/api/v1/config/site.title \
  -H "Authorization: Bearer $ACCESS_TOKEN" | jq
```

### 11. List feature flags

```bash
curl -s http://localhost:8081/api/v1/flags \
  -H "Authorization: Bearer $ACCESS_TOKEN" | jq
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
cd examples/ssobff
docker compose up -d
cd ../..
go run ./examples/ssobff
```
