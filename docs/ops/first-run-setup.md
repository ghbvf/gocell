# GoCell First-Run Admin Setup

> Operators bootstrap the first admin account by calling `POST /api/v1/access/setup/admin`. There is no mode selection — a single path handles all deployments.

This document covers the **operator-side deployment details**: environment variable configuration, Docker / Kubernetes setup, startup flow, and troubleshooting.
For the security boundary design see [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md).

## Overview

GoCell first-run admin registration uses a single path: after the service starts, the operator sends a `POST /api/v1/access/setup/admin` request to create the first admin. The endpoint is protected by HTTP Basic Auth (credentials supplied via env vars); after the admin is created it permanently returns `410 Gone`.

Required credential env vars:

```
GOCELL_BOOTSTRAP_ADMIN_USERNAME=<operator-username>
GOCELL_BOOTSTRAP_ADMIN_PASSWORD=<operator-password>
```

**Persistent operator authenticator**: these env vars are the permanent protection for the setup endpoint, not a one-time seed. After the admin is created the env vars **must not be removed**. Rotation uses a rolling K8s Secret replacement + restart (see the Credential Rotation section below).

**Empty config fail-fast**: startup fails immediately if either `GOCELL_BOOTSTRAP_ADMIN_USERNAME` or `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` is empty.

---

## Required Environment Variables

| Variable | Description |
|----------|-------------|
| `GOCELL_BOOTSTRAP_ADMIN_USERNAME` | **Required, persistent.** HTTP Basic Auth operator identity protecting the setup/admin endpoint. |
| `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` | **Required, persistent.** Minimum 8 bytes; TrimSpace strips trailing newlines automatically (handles K8s secret formatting); control characters cause fail-fast. |

---

## Startup Flow

```
[startup]
  GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
  GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!

  accesscore starts: validates env credentials (empty => fail-fast)
  => setup/admin endpoint protected by HTTP Basic Auth

[operator action]
  POST /api/v1/access/setup/admin
  Authorization: Basic <base64(ops:OpsPass123!)>
  Content-Type: application/json
  { "username": "admin", "email": "admin@corp.example", "password": "AdminPass456!" }
  => 201 Created

[subsequent call to setup/admin]
  => 410 Gone (permanent)

[log in with business credentials]
  POST /api/v1/access/sessions/login
  { "username": "admin", "password": "AdminPass456!" }
  => 201 Created + tokens
```

**Credential relationship**: `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` are the **HTTP Basic Auth operator credentials** (they verify who is authorized to initiate the setup request). The `username` / `email` / `password` fields in the request body are the **business credentials of the admin being created** — the two sets are completely independent.

---

## curl Examples

```bash
OPS_USER="ops"
OPS_PASS="OpsPass123!"
ADMIN_PASS="AdminPass456!"

# 1. Create admin (HTTP Basic Auth verifies operator identity)
curl -s -X POST http://localhost:8080/api/v1/access/setup/admin \
  -u "${OPS_USER}:${OPS_PASS}" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"email\":\"admin@corp.example\",\"password\":\"${ADMIN_PASS}\"}"
# 201 Created

# 2. Log in with business credentials
curl -s -X POST http://localhost:8080/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASS}\"}"
```

---

## Docker Compose Example

```yaml
services:
  gocell:
    image: gocell:latest
    environment:
      - GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
      - GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!
      - GOCELL_JWT_ISSUER=https://gocell.example
      - GOCELL_JWT_AUDIENCE=gocell
```

After starting, trigger the setup:

```bash
docker compose up -d
curl -s -X POST http://localhost:8080/api/v1/access/setup/admin \
  -u "ops:OpsPass123!" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","email":"admin@corp.example","password":"AdminPass456!"}'
```

---

## Kubernetes Example

Credentials are injected via a K8s Secret (never baked into the image):

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: gocell-bootstrap
type: Opaque
stringData:
  username: ops
  password: OpsPass123!
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gocell
spec:
  template:
    spec:
      containers:
        - name: gocell
          image: gocell:latest
          env:
            - name: GOCELL_BOOTSTRAP_ADMIN_USERNAME
              valueFrom:
                secretKeyRef:
                  name: gocell-bootstrap
                  key: username
            - name: GOCELL_BOOTSTRAP_ADMIN_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: gocell-bootstrap
                  key: password
```

Deployment note: the current `UserRepository` has only an in-memory implementation; an in-process `sync.Mutex` guarantees admin uniqueness. First-run admin creation must target a single pod replica. Multi-pod idempotency guarantees will be available once `ACCESSCORE-PG-USERS-MIGRATION-01` (PG `users` table + `UNIQUE(role='admin')` partial index) lands — at that point the first INSERT wins and subsequent POSTs return 409 or 410.

---

## Troubleshooting

Startup failures all return `ERR_CELL_INVALID_CONFIG` (bootstrap validation is classified as a cell config failure). Distinguish the root cause by the `message` field:

| Symptom | Message | Cause | Resolution |
|---------|---------|-------|------------|
| Startup fails | `... are required to protect setup/admin endpoint` | Both env vars are empty | Inject `GOCELL_BOOTSTRAP_ADMIN_USERNAME` and `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`; check that the K8s Secret is mounted. |
| Startup fails | `... must both be set or both be empty` | Only one of the two env vars is set | Set both or clear both. |
| Startup fails | `... USERNAME must not contain control characters` | Username contains control characters | Check secret encoding; use printable ASCII. |
| Startup fails | `... PASSWORD must be at least 8 bytes` | Password is fewer than 8 bytes after TrimSpace | Use a longer password; trailing newlines from K8s secrets are handled automatically by TrimSpace. |
| setup/admin returns 401 | — | Basic Auth credentials incorrect | Verify `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`. |
| setup/admin returns 409 | — | Requested username is already taken by another user | Use a different username and retry. |
| setup/admin returns 410 | — | Basic Auth passed but admin already exists | Admin is ready; proceed to login. |
| setup/admin returns 429 | — | Per-IP rate limit triggered (default 5 req/min, burst 10) | Wait for the rate-limit window to reset; check for unexpected request sources. |

---

## Admin Password Reset

### Scenario: current password is known (normal password change)

A logged-in admin changes their password via `POST /api/v1/access/users/{id}/password`:

```bash
ACCESS_TOKEN="<your-current-token>"
USER_ID="<your-user-id>"

curl -s -X POST "http://localhost:8080/api/v1/access/users/${USER_ID}/password" \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"oldPassword":"<current>","newPassword":"NewStr0ng@Pass!"}'
# Returns a new TokenPair
```

### Scenario: admin password forgotten (requires direct database access)

> Security note: the following operation requires direct database access and must be performed by an authorized operator.

```sql
-- 1. Generate a new bcrypt hash (cost=12, OWASP 2023 recommendation)
--    Go: bcrypt.GenerateFromPassword([]byte("NewPass!"), 12)
--    htpasswd: htpasswd -bnBC 12 "" "NewPass!" | tr -d ':\n'

-- 2. Update the admin user's password
UPDATE users
SET password_hash = '$2a$12$<your-bcrypt-hash-here>',
    password_reset_required = true,
    updated_at = NOW()
WHERE username = 'admin';

-- 3. Verify the update succeeded
SELECT id, username, password_reset_required, updated_at FROM users WHERE username = 'admin';
```

After the reset:
1. Log in with the new password via `POST /api/v1/access/sessions/login`
2. If `password_reset_required=true`, follow the "current password known" flow above to change it

---

## Security Notes

- **Env credential lifetime — persistent operator authenticator**: the bootstrap credentials are the permanent protection layer for the setup endpoint. After admin creation the env vars must be retained; they cannot be deleted while the service is running. Rotation uses a rolling K8s Secret replacement + restart (see Credential Rotation below).
- **`crypto/subtle.ConstantTimeCompare`**: HTTP Basic Auth verification uses a constant-time comparison to prevent timing side-channel leakage of operator credentials.
- **Per-IP token-bucket rate limit**: enabled by default (5 req/min, burst 10) to prevent brute-force enumeration of Basic Auth credentials; triggers return `429 ERR_RATE_LIMITED`.
- **Oracle-safe 401 envelope**: authentication failures always return `ERR_AUTH_BOOTSTRAP_FAILED` without distinguishing "wrong username" from "wrong password", preventing enumeration attacks.
- **bcrypt cost = 12** (OWASP 2023 recommendation) to resist offline brute-force attacks against the business admin password.

---

## Credential Rotation

Bootstrap credential rotation uses a rolling K8s Secret replacement + restart:

```bash
# 1. Replace the K8s Secret
kubectl create secret generic gocell-bootstrap \
  --from-literal=username=ops --from-literal=password=NewOpsPass456! \
  --dry-run=client -o yaml | kubectl apply -f -

# 2. Rolling-restart the deployment to pick up the new env vars
kubectl rollout restart deployment/gocell

# 3. Wait for the rollout to complete
kubectl rollout status deployment/gocell

# 4. Old credentials are immediately invalid; new credentials apply to all
#    subsequent setup/admin requests (if admin already exists, responses return 410)
```

Notes:

- Before rotating, ensure all operator runbooks and CI scripts have been updated to use the new credentials; old credentials become invalid as soon as the rollout completes.
- Renaming the env vars (e.g. `GOCELL_BOOTSTRAP_ADMIN_*` to another name) is a breaking change that requires a coordinated ADR update and Helm chart release.
