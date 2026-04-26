# iotdevice Example

An IoT device management application demonstrating the GoCell **L4 DeviceLatent** consistency model.

## Architecture

- **devicecell** (L4 DeviceLatent): manages device lifecycle and command dispatch
  - **deviceregister** slice: POST registers a device and publishes the device registration event
  - **devicelist** slice: GET lists registered devices with cursor pagination
  - **devicecommand** slice: enqueue commands, device polls pending commands, device acks execution
  - **devicestatus** slice: GET queries current device status

## L4 DeviceLatent Model

In the L4 model, the server does **not** push commands to devices in real time.
Instead, commands are enqueued server-side and devices poll for pending commands
on their own schedule. After executing a command, the device reports an
acknowledgement (ack) back to the server.

This pattern is designed for IoT scenarios where devices have intermittent
connectivity, high latency, or constrained bandwidth.

> **Note:** L4 command primitives in v1.0 are implemented at the application
> layer. Framework-level `kernel/command` first-class support is planned for
> v1.1.

## Quick Start (In-Memory Mode)

No external dependencies required. Uses in-memory repositories and event bus.
The primary listener verifies RS256 JWTs from the `GOCELL_JWT_*` environment,
and the internal listener requires a service-token secret.

```bash
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
  -out /tmp/gocell-iotdevice-jwt.key
openssl rsa -in /tmp/gocell-iotdevice-jwt.key -pubout \
  -out /tmp/gocell-iotdevice-jwt.pub

export GOCELL_JWT_PRIVATE_KEY="$(cat /tmp/gocell-iotdevice-jwt.key)"
export GOCELL_JWT_PUBLIC_KEY="$(cat /tmp/gocell-iotdevice-jwt.pub)"
export GOCELL_JWT_ISSUER=iotdevice-local
export GOCELL_JWT_AUDIENCE=gocell
export GOCELL_IOTDEVICE_SERVICE_SECRET="$(openssl rand -base64 32)"

export IOT_ADMIN_TOKEN="$(go run ./examples/iotdevice/localtoken)"
go run ./examples/iotdevice
```

The server starts with primary listener on `:8083` (API + infra) and internal listener on `:9083` (control-plane).
`IOT_ADMIN_TOKEN` is a real RS256 access token signed by the local key above.
The helper defaults to the roles needed by the walkthrough; override with
`go run ./examples/iotdevice/localtoken -roles admin,role:operator,role:device`.

## Docker Mode

Start infrastructure services, then run the application:

```bash
cd examples/iotdevice
docker compose up -d
cd ../..
# Export the JWT and GOCELL_IOTDEVICE_SERVICE_SECRET variables from Quick Start first.
go run ./examples/iotdevice
```

## API

### Register a device

```bash
curl -X POST http://localhost:8083/api/v1/devices \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"name":"sensor-001"}'
```

Response (201):

```json
{"data":{"id":"dev-...","name":"sensor-001","status":"online"}}
```

### List devices

```bash
curl "http://localhost:8083/api/v1/devices/?limit=50" \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}"
```

Response (200):

```json
{"data":[{"id":"dev-...","name":"sensor-001","status":"online","lastSeen":"..."}],"nextCursor":"","hasMore":false}
```

### Send a command to a device

```bash
curl -X POST http://localhost:8083/api/v1/devices/{id}/commands \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"commandType":"default","payload":"reboot"}'
```

`commandType` is optional in the enqueue request (only `payload` is required).
Omitting it defaults the field to `"default"`; the device sees that value in the dequeue response.

Response (201):

```json
{"data":{"id":"cmd-...","deviceId":"dev-...","commandType":"default","payload":"reboot","status":"pending","createdAt":"..."}}
```

### Device dequeues commands

```bash
curl http://localhost:8083/api/v1/devices/{id}/commands \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}"
```

Response (200):

```json
{"data":[{"id":"cmd-...","deviceId":"dev-...","commandType":"default","payload":"reboot","status":"sent","attempt":1,"createdAt":"...","sentAt":"..."}],"nextCursor":"","hasMore":false}
```

### Device reports command receipt

```bash
curl -X POST http://localhost:8083/api/v1/devices/{id}/commands/{cmdId}/report \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}"
```

Response (200):

```json
{"data":{"id":"cmd-...","deviceId":"dev-...","commandType":"default","payload":"reboot","status":"delivered","attempt":1,"createdAt":"...","sentAt":"...","deliveredAt":"..."}}
```

### Device acknowledges command execution

```bash
curl -X POST http://localhost:8083/api/v1/devices/{id}/commands/{cmdId}/ack \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"reason":"success"}'
```

Response (200):

```json
{"data":{"id":"cmd-...","deviceId":"dev-...","commandType":"default","payload":"reboot","status":"succeeded","attempt":1,"createdAt":"...","sentAt":"...","deliveredAt":"...","completedAt":"..."}}
```

### Query device status

```bash
curl http://localhost:8083/api/v1/devices/{id}/status \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}"
```

Response (200):

```json
{"data":{"id":"dev-...","name":"sensor-001","status":"online","lastSeen":"..."}}
```

### Health check

```bash
curl http://localhost:8083/healthz
curl http://localhost:8083/readyz
curl -H "X-Readyz-Token: $GOCELL_READYZ_VERBOSE_TOKEN" 'http://localhost:8083/readyz?verbose'
```

`/healthz` is liveness-only. Use `/readyz?verbose` for the detailed readiness breakdown — PR-A35 requires `GOCELL_READYZ_VERBOSE_TOKEN` to be set and the matching `X-Readyz-Token` header on the request.

## Full Walkthrough

```bash
# 1. Register a device
DEV=$(curl -s -X POST http://localhost:8083/api/v1/devices \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"name":"sensor-001"}')
echo "$DEV"
DEV_ID=$(echo "$DEV" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)

# 2. Send a command
CMD=$(curl -s -X POST "http://localhost:8083/api/v1/devices/${DEV_ID}/commands" \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"commandType":"default","payload":"reboot"}')
echo "$CMD"
CMD_ID=$(echo "$CMD" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)

# 3. Device polls for pending commands
curl -s "http://localhost:8083/api/v1/devices/${DEV_ID}/commands" \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}"

# 4. Device acknowledges execution
curl -s -X POST "http://localhost:8083/api/v1/devices/${DEV_ID}/commands/${CMD_ID}/ack" \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"reason":"success"}'

# 5. Verify no more pending commands
curl -s "http://localhost:8083/api/v1/devices/${DEV_ID}/commands" \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}"

# 6. Check device status
curl -s "http://localhost:8083/api/v1/devices/${DEV_ID}/status" \
  -H "Authorization: Bearer ${IOT_ADMIN_TOKEN}"
```
