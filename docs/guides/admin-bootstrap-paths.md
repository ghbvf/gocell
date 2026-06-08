# First-Admin Setup — Single Setup-Driven Path

> This document is for **application-layer / client developers** and answers:
>
> - How does GoCell register the first-run admin?
> - What should a client do when it receives `410 Gone` from the setup endpoint?
> - How do you distinguish `400` / `401` / `409` / `410` responses from the setup endpoint in day-to-day use?
>
> For operator-side deployment details (env variables, Docker / K8s configuration, password reset procedure), see [`docs/ops/first-run-setup.md`](../ops/first-run-setup.md).
> For the security boundary ADR, see [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md).

---

## 1. Single setup-driven path

The `accesscore` Cell models "create the first admin" as a one-time fact: the admin role exists exactly once in the system, and whoever calls first owns it. GoCell provides a single path:

After the operator starts the service, the first admin is bootstrapped via `POST /api/v1/access/setup/admin`. The endpoint is protected by HTTP Basic Auth (operator credentials from env), and the `username` / `email` / `password` fields in the body are the business admin identity — the two are completely independent.

```
env  → GOCELL_BOOTSTRAP_ADMIN_USERNAME / GOCELL_BOOTSTRAP_ADMIN_PASSWORD
         = operator authenticator (who is authorized to initiate the setup request)

body → username / email / password
         = admin user identity (the account to create)
```

The password in `POST /api/v1/access/setup/admin` must be 8–72 printable ASCII bytes, consistent with bcrypt's 72-byte input limit.

Once the admin is created, the setup endpoint permanently returns 410 Gone (after Basic Auth passes). The env credentials are a persistent operator authenticator and cannot be removed after the admin is created.

---

## 2. Full flow

```
[startup]
  GOCELL_BOOTSTRAP_ADMIN_USERNAME=ops
  GOCELL_BOOTSTRAP_ADMIN_PASSWORD=OpsPass123!
  start gocell

[accesscore]
  → validates env credentials (empty = fail-fast)
  → setup/admin endpoint protected by HTTP Basic Auth

[operator action]
  POST /api/v1/access/setup/admin
  Authorization: Basic <base64(ops:OpsPass123!)>
  { "username":"admin","email":"admin@corp.example","password":"AdminPass456!" }
  → 201 Created

[calling setup/admin again]
  → 410 Gone (permanent; after Basic Auth passes; 401 if auth fails)

[login with business credentials]
  POST /api/v1/access/sessions/login
  { "username": "admin", "password": "AdminPass456!" }
  → 201 Created + access/refresh tokens
```

---

## 3. curl Examples

```bash
OPS_USER="ops"
OPS_PASS="OpsPass123!"

# Set up admin (HTTP Basic Auth verifies operator identity)
curl -sS -X POST https://gocell.example/api/v1/access/setup/admin \
  -u "${OPS_USER}:${OPS_PASS}" \
  -H 'content-type: application/json' \
  -d '{"username":"admin","email":"admin@corp.example","password":"AdminPass456!"}'
# 201 Created

# When admin already exists
curl -sS -X POST https://gocell.example/api/v1/access/setup/admin \
  -u "${OPS_USER}:${OPS_PASS}" \
  -H 'content-type: application/json' \
  -d '{"username":"root","email":"root@local","password":"SecretPass!23"}'
# {"error":{"code":"ERR_SETUP_ALREADY_INITIALIZED","message":"first-run admin already provisioned; this endpoint is retired","details":[{"key":"nextAction","value":"login"}]}}
```

## 4. K8s Secret Rolling Rotation

Rotate operator credentials via a rolling K8s Secret replacement + restart:

```bash
kubectl create secret generic gocell-bootstrap \
  --from-literal=username=ops --from-literal=password=NewOpsPass456! \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl rollout restart deployment/gocell
kubectl rollout status deployment/gocell
```

---

## 5. `410 Gone` Response Body Example

Once the setup endpoint is retired, all `POST /api/v1/access/setup/admin` requests receive a uniform error envelope:

```json
{
  "error": {
    "code": "ERR_SETUP_ALREADY_INITIALIZED",
    "message": "first-run admin already provisioned; this endpoint is retired",
    "details": [
      {"key": "nextAction", "value": "login"}
    ]
  }
}
```

- `details` is an `array<{key,value}>` (shared envelope `contracts/shared/errors/error-response-v1.schema.json`); clients match entries by key, not by map index
- `details` only exposes the semantic verb `nextAction`; no HTTP path literals are embedded
- The actual path of the login endpoint is defined by the contract (`http.auth.login.v1`); clients resolve it via OpenAPI, a contract registry, or their own routing table
- This response is stable across deployments — even if the sessions/login path changes in a future version, the 410 body fields remain unchanged

---

## 6. Go Client Pseudocode

```go
type errDetail struct {
    Key   string `json:"key"`
    Value any    `json:"value"`
}

type errEnvelope struct {
    Error struct {
        Code    string      `json:"code"`
        Message string      `json:"message"`
        Details []errDetail `json:"details"`
    } `json:"error"`
}

func (e errEnvelope) detail(key string) (any, bool) {
    for _, d := range e.Error.Details {
        if d.Key == key {
            return d.Value, true
        }
    }
    return nil, false
}

func provisionOrLogin(ctx context.Context, c *Client, in AdminSeed) error {
    resp, err := c.Post(ctx, "http.auth.setup.admin.v1", in)
    if err != nil {
        return fmt.Errorf("setup: post admin: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusCreated {
        return nil // we are the first admin
    }
    if resp.StatusCode != http.StatusGone {
        return fmt.Errorf("setup: unexpected status %d", resp.StatusCode)
    }

    var env errEnvelope
    if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
        return fmt.Errorf("setup: decode 410 envelope: %w", err)
    }
    if v, ok := env.detail("nextAction"); !ok || v != "login" {
        return fmt.Errorf("setup: 410 with unexpected nextAction %v", env.Error.Details)
    }
    // login path is provided by the contract registry, not read from the 410 body literal
    return c.LoginByContractID(ctx, "http.auth.login.v1", in.LoginCreds())
}
```

---

## 7. Distinguishing `400` / `401` / `409` / `410`

| Status | errcode | Trigger condition | Recommended client handling |
|---|---|---|---|
| **400** | `ERR_AUTH_IDENTITY_INVALID_INPUT` | Missing body fields, field too long, non-printable ASCII password, control characters | Validate input → prompt user → retry |
| **400** | `ERR_VALIDATION_FAILED` | Malformed JSON, unknown fields, wrong Content-Type | Fix request body format → retry |
| **401** | `ERR_AUTH_BOOTSTRAP_FAILED` | Basic Auth credentials incorrect | Verify env operator credentials; do not auto-retry (prevents enumeration) |
| **409** | `ERR_AUTH_USER_DUPLICATE` | Requested username is taken by another user, but that user is not yet admin | Change username → retry; do not silently retry with the same name |
| **410** | `ERR_SETUP_ALREADY_INITIALIZED` | Basic Auth passed + admin role already has a user | Proceed to login flow; **do not retry setup**; treat as terminal state |
| **429** | `ERR_RATE_LIMITED` | Per-IP rate limit triggered (default 5 req/min, burst 10) | Wait for the rate limit window; check request origin |

Decision order:

```
        ┌────────────────────────────┐
        │ POST /access/setup/admin   │
        └──────────┬─────────────────┘
                   │
       ┌───────────┴────────────┐
       │ Rate limit passed?     │
       └────┬──────────────┬────┘
            │ No            │ Yes
            ▼              ▼
         429 Too Many     ┌──────────────────┐
         Requests         │ Basic Auth OK?   │
                          └────┬──────────┬──┘
                               │ No        │ Yes
                               ▼          ▼
                          401 Unauthorized ┌────────────────────┐
                                           │ Admin exists?      │
                                           └────┬──────────┬────┘
                                                │ No        │ Yes
                                                ▼          ▼
                                       ┌────────────────┐  410 Gone (terminal)
                                       │ Input valid?   │
                                       └─┬──────────┬───┘
                                         │ No        │ Yes
                                         ▼          ▼
                                       400 Bad    ┌─────────────────┐
                                       Request    │ Username taken? │
                                                  └─┬───────────┬───┘
                                                    │ Yes        │ No
                                                    ▼           ▼
                                                 409 Conflict  201 Created
```

---

## 8. Related Documentation

- [`docs/ops/first-run-setup.md`](../ops/first-run-setup.md) — Environment variable configuration, Docker / K8s deployment details, password reset procedure
- [`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`](../architecture/202605061600-adr-bootstrap-admin-boundary.md) — Security boundary ADR (D1–D5)
- [`contracts/http/auth/setup/admin/v1/contract.yaml`](../../contracts/http/auth/setup/admin/v1/contract.yaml) — Setup admin endpoint contract (includes 400 / 401 / 409 / 410 declarations)
- [`contracts/http/auth/setup/status/v1/contract.yaml`](../../contracts/http/auth/setup/status/v1/contract.yaml) — Setup status endpoint contract
