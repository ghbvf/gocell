# iot-device Example

An IoT device management application demonstrating the GoCell **L4 DeviceLatent** consistency model.

## Architecture

- **device-cell** (L4 DeviceLatent): manages device lifecycle and command dispatch
  - **device-register** slice: POST registers a device and publishes `event.device-registered.v1`
  - **device-command** slice: enqueue commands, device polls pending commands, device acks execution
  - **device-status** slice: GET queries current device status

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

```bash
cd src
go run ./examples/iot-device
```

The server starts on `:8083`.

## Docker Mode

Start infrastructure services, then run the application:

```bash
cd src/examples/iot-device
docker compose up -d
cd ../..
go run ./examples/iot-device
```

## API

### Register a device

```bash
curl -X POST http://localhost:8083/api/v1/devices \
  -H 'Content-Type: application/json' \
  -d '{"name":"sensor-001"}'
```

Response (201):

```json
{"id":"dev-...","name":"sensor-001","status":"online"}
```

### Send a command to a device

```bash
curl -X POST http://localhost:8083/api/v1/devices/{id}/commands \
  -H 'Content-Type: application/json' \
  -d '{"payload":"reboot"}'
```

Response (201):

```json
{"id":"cmd-...","deviceId":"dev-...","payload":"reboot","status":"pending"}
```

### Device polls pending commands

```bash
curl http://localhost:8083/api/v1/devices/{id}/commands
```

Response (200):

```json
{"data":[{"id":"cmd-...","deviceId":"dev-...","payload":"reboot","status":"pending","createdAt":"..."}],"total":1}
```

### Device acknowledges command execution

```bash
curl -X POST http://localhost:8083/api/v1/devices/{id}/commands/{cmdId}/ack
```

Response (200):

```json
{"status":"acked"}
```

### Query device status

```bash
curl http://localhost:8083/api/v1/devices/{id}/status
```

Response (200):

```json
{"data":{"id":"dev-...","name":"sensor-001","status":"online","lastSeen":"..."}}
```

### Health check

```bash
curl http://localhost:8083/healthz
curl http://localhost:8083/readyz
curl http://localhost:8083/readyz?verbose
```

`/healthz` is liveness-only. Use `/readyz?verbose` for the detailed readiness breakdown.

## Full Walkthrough

```bash
# 1. Register a device
DEV=$(curl -s -X POST http://localhost:8083/api/v1/devices \
  -H 'Content-Type: application/json' \
  -d '{"name":"sensor-001"}')
echo "$DEV"
DEV_ID=$(echo "$DEV" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)

# 2. Send a command
CMD=$(curl -s -X POST "http://localhost:8083/api/v1/devices/${DEV_ID}/commands" \
  -H 'Content-Type: application/json' \
  -d '{"payload":"reboot"}')
echo "$CMD"
CMD_ID=$(echo "$CMD" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)

# 3. Device polls for pending commands
curl -s "http://localhost:8083/api/v1/devices/${DEV_ID}/commands"

# 4. Device acknowledges execution
curl -s -X POST "http://localhost:8083/api/v1/devices/${DEV_ID}/commands/${CMD_ID}/ack"

# 5. Verify no more pending commands
curl -s "http://localhost:8083/api/v1/devices/${DEV_ID}/commands"

# 6. Check device status
curl -s "http://localhost:8083/api/v1/devices/${DEV_ID}/status"
```
