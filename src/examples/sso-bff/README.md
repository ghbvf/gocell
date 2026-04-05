# sso-bff Example

A single-process SSO BFF (Backend For Frontend) demonstrating how to compose the three
built-in GoCell Cells into one assembly:

- **access-core** (L2 OutboxFact): identity management, session lifecycle (login/refresh/logout), RBAC
- **audit-core** (L3 WorkflowEventual): tamper-evident audit log with hash chain
- **config-core** (L2 OutboxFact): configuration CRUD, publish/rollback, feature flags

All dependencies are in-memory (no external services required).

## Quick Start

```bash
cd src
go run ./examples/sso-bff
```

The server starts on `:8081`.

## API Walkthrough

### 1. Create a user

```bash
curl -s -X POST http://localhost:8081/api/v1/access/users \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"P@ssw0rd123","email":"alice@example.com"}' | jq
```

### 2. Login (create session)

```bash
curl -s -X POST http://localhost:8081/api/v1/access/sessions/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"P@ssw0rd123"}' | jq
```

Save the returned `token` and `sessionId` for subsequent calls.

### 3. Refresh token

```bash
curl -s -X POST http://localhost:8081/api/v1/access/sessions/refresh \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer {token}" \
  -d '{"sessionId":"{sessionId}"}' | jq
```

### 4. List users

```bash
curl -s http://localhost:8081/api/v1/access/users | jq
```

### 5. Logout (delete session)

```bash
curl -s -X DELETE http://localhost:8081/api/v1/access/sessions/{sessionId} | jq
```

### 6. Query audit entries

```bash
curl -s http://localhost:8081/api/v1/audit/entries | jq
```

### 7. Create a config entry

```bash
curl -s -X POST http://localhost:8081/api/v1/config/ \
  -H 'Content-Type: application/json' \
  -d '{"key":"site.title","value":"My SSO Portal"}' | jq
```

### 8. Update a config entry

```bash
curl -s -X PUT http://localhost:8081/api/v1/config/site.title \
  -H 'Content-Type: application/json' \
  -d '{"value":"SSO Portal v2"}' | jq
```

### 9. Read a config entry

```bash
curl -s http://localhost:8081/api/v1/config/site.title | jq
```

### 10. List feature flags

```bash
curl -s http://localhost:8081/api/v1/flags | jq
```

### 11. Verify audit trail after login/logout

```bash
# After performing login + logout, check that audit entries were recorded
curl -s http://localhost:8081/api/v1/audit/entries | jq '.[] | {action: .eventType, at: .createdAt}'
```

### 12. Health checks

```bash
curl -s http://localhost:8081/healthz | jq
curl -s http://localhost:8081/readyz  | jq
```

## Docker Mode (Future)

Infrastructure services are provided for future adapter-based mode:

```bash
cd src/examples/sso-bff
docker compose up -d
cd ../..
go run ./examples/sso-bff
```
