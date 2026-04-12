# todo-order Example

A minimal order management application demonstrating the GoCell "golden path":
creating a business Cell with HTTP endpoints and event publishing via the outbox pattern.

## Architecture

- **order-cell** (L2 OutboxFact): manages order lifecycle
  - **order-create** slice: POST creates an order and publishes `event.order-created.v1`
  - **order-query** slice: GET retrieves orders by ID or lists all

## Quick Start (In-Memory Mode)

No external dependencies required. Uses in-memory repository and event bus.

```bash
cd src
go run ./examples/todo-order
```

The server starts on `:8082`.

## Docker Mode

Start infrastructure services, then run the application:

```bash
cd src/examples/todo-order
docker compose up -d
cd ../..
go run ./examples/todo-order
```

## API

### Create an order

```bash
curl -X POST http://localhost:8082/api/v1/orders \
  -H 'Content-Type: application/json' \
  -d '{"item":"test"}'
```

Response (201):

```json
{"data":{"id":"ord-...","item":"test","status":"pending"}}
```

### List all orders

```bash
curl http://localhost:8082/api/v1/orders
```

Response (200):

```json
{"data":[...],"nextCursor":"...","hasMore":false}
```

### Get order by ID

```bash
curl http://localhost:8082/api/v1/orders/{id}
```

Response (200):

```json
{"data":{"id":"ord-...","item":"test","status":"pending","createdAt":"..."}}
```

### Health check

```bash
curl http://localhost:8082/healthz
curl http://localhost:8082/readyz
```
