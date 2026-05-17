# Adding a New Endpoint (codegen-driven)

This guide walks through adding a new HTTP endpoint to an existing Cell using
the GoCell codegen pipeline.

**Invariant**: Cells must never construct `wrapper.ContractSpec{}` literals
manually. All route bindings go through generated handlers in
`generated/contracts/...`. Three archtest gates enforce this:
`CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01`, `NO-MANUAL-CONTRACTSPEC-LITERAL-01`,
and `EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01`.

## Workflow overview

```
contract.yaml + schema → gocell generate contract → types_gen.go + iface_gen.go + handler_gen.go
                                                           ↓
                                        implement iface_gen.go Service in slice handler
                                                           ↓
                                        wire NewHandler into Cell.Init RouteGroup
```

## Step 1: Define the contract

Create a new directory under `contracts/http/{domain}/{action}/{version}/`:

```bash
mkdir -p contracts/http/myapp/widgets/create/v1
```

`contracts/http/myapp/widgets/create/v1/contract.yaml`:
```yaml
id: http.myapp.widgets.create.v1
kind: http
ownerCell: myapp
consistencyLevel: L1
lifecycle: active
endpoints:
  server: myapp
  http:
    method: POST
    path: /api/v1/widgets
    successStatus: 201
    responses:
      400:
        description: invalid request
        schemaRef: ../../shared/errors/error-response-v1.schema.json
      500:
        description: internal server error
        schemaRef: ../../shared/errors/error-response-v1.schema.json
schemaRefs:
  request: request.schema.json
  response: response.schema.json
```

`contracts/http/myapp/widgets/create/v1/request.schema.json`:
```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "name": { "type": "string", "minLength": 1, "maxLength": 256 }
  },
  "required": ["name"]
}
```

`contracts/http/myapp/widgets/create/v1/response.schema.json`:
```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "data": {
      "type": "object",
      "properties": {
        "id":   { "type": "string" },
        "name": { "type": "string" }
      },
      "required": ["id", "name"]
    }
  }
}
```

## Step 2: Generate

```bash
go run ./cmd/gocell generate contract
```

This writes three files under `generated/contracts/http/myapp/widgets/create/v1/`:

| File | Purpose |
|------|---------|
| `types_gen.go` | `Request`, `Response`, `ResponseData` structs + typed response envelope types (e.g. `Create201JSONResponse`, `Create400ErrorResponse`, `Create500ErrorResponse`) |
| `iface_gen.go` | `Service` interface with `Create(ctx, *Request) (CreateResponseObject, error)` |
| `handler_gen.go` | `Handler` that decodes, validates, calls Service, calls `visitCreateResponse` (unexported, package-internal) to write the typed response |

Do **not** edit these files — they are regenerated on every `gocell generate contract`.

## Step 3: Implement the Service interface

In your slice package implement the generated `Service` interface:

```go
// cells/myapp/slices/widgetcreate/handler.go
package widgetcreate

import (
    "context"

    createg "github.com/ghbvf/gocell/generated/contracts/http/myapp/widgets/create/v1"
    "github.com/ghbvf/gocell/pkg/errcode"
    kcell "github.com/ghbvf/gocell/kernel/cell"
    "github.com/ghbvf/gocell/runtime/auth"
)

// CreateAdapter implements createg.Service.
type CreateAdapter struct{ S *Service }

func (a CreateAdapter) Create(ctx context.Context, req *createg.Request) (createg.CreateResponseObject, error) {
    w, err := a.S.Create(ctx, req.Name)
    if err != nil {
        // Business 4xx: return typed error struct, not (nil, err).
        // (nil, err) is reserved for undeclared framework 5xx (panic recover,
        // infrastructure faults) — the generated handler writes those via
        // httputil.WriteError.
        if errcode.IsKind(err, errcode.KindNotFound) {
            return createg.Create404ErrorResponse{Body: *errcode.New(errcode.KindNotFound, errcode.ErrNotFound, "order not found")}, nil
        }
        return nil, err
    }
    return createg.Create201JSONResponse(createg.Response{Data: &createg.ResponseData{ID: w.ID, Name: w.Name}}), nil
}

// Handler composes the generated contract handler with a policy.
type Handler struct{ h *createg.Handler }

func NewHandler(svc *Service) *Handler {
    return &Handler{h: createg.NewHandler(CreateAdapter{svc}, auth.AnyRole(auth.RoleAdmin))}
}

func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
    return h.h.RegisterRoutes(mux)
}
```

## Step 4: Wire into the Cell

```go
// cells/myapp/cell.go (Init method excerpt)
reg.RouteGroup(cell.RouteGroup{
    Listener: cell.PrimaryListener,
    Prefix:   "/api/v1",
    Register: func(mux cell.RouteMux) error {
        return c.widgetCreateH.RegisterRoutes(mux)
    },
})
```

## Step 5: Update slice.yaml

Add the contract usage to `cells/myapp/slices/widgetcreate/slice.yaml`:

```yaml
contractUsages:
  - contract: http.myapp.widgets.create.v1
    role: serve
```

## Step 6: Validate

```bash
go run ./cmd/gocell validate --strict   # contract + metadata consistency
go build ./...                          # type-check generated code
go test ./cells/myapp/...               # unit + contract tests
```

## Adding an event subscription

For event subscriptions the workflow is the same on the contract side. After
`gocell generate contract` you get `types_gen.go`, `iface_gen.go`,
`spec_gen.go`, and `subscription_gen.go` (no handler). Wire the subscription
in `Cell.Init`:

```go
// in Cell.Init
import eventpkg "github.com/ghbvf/gocell/generated/contracts/event/my/topic/v1"

return eventpkg.NewSubscription(c.svc.HandleMyTopic, c.ID(), "myhello").Mount(reg)
```

The generated `NewSubscription(handler outbox.EntryHandler, consumerGroup, sliceID string)`
takes a standard `outbox.EntryHandler` — no custom interface to implement.

## Common mistakes

| Mistake | Effect | Fix |
|---------|--------|-----|
| `wrapper.ContractSpec{}` literal in cells/ | archtest CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01 fail | Use generated `NewHandler` / `NewSubscription` |
| Stale `codegen: false` left in contract.yaml | Generator skips the contract (explicit false overrides the default-on) | Remove the `codegen: false` line |
| Editing files in `generated/` | Overwritten on next generate | Implement the `Service` interface instead |
| Wrong `contractUsages` role | `gocell validate` ADV-06 fail | Set `role: serve` for HTTP server, `role: subscribe` for event subscriber |
| `return nil, err` for business 4xx | Generated handler falls through to `httputil.WriteError` 5xx path; business error loses its intended status code | Return typed struct: `return createg.Create404ErrorResponse{Body: *errcode.New(...)}, nil` |
| Missing `responses:` block in contract.yaml | CH-06 governance silently passes (empty set × empty set = true), but the generated typed envelope contains only the success status struct — the adapter has no typed struct to express business errors | Any POST/PUT/DELETE endpoint must declare at least `400` and `500` in `responses:` |
