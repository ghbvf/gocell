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
codegen: true
endpoints:
  server: myapp
  http:
    method: POST
    path: /api/v1/widgets
    successStatus: 201
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
| `types_gen.go` | `Request`, `Response`, `ResponseData` structs |
| `iface_gen.go` | `Service` interface with `Create(ctx, *Request) (*Response, error)` |
| `handler_gen.go` | `Handler` that decodes, validates, calls Service, encodes |

Do **not** edit these files — they are regenerated on every `gocell generate contract`.

## Step 3: Implement the Service interface

In your slice package implement the generated `Service` interface:

```go
// cells/myapp/slices/widgetcreate/handler.go
package widgetcreate

import (
    "context"

    createg "github.com/ghbvf/gocell/generated/contracts/http/myapp/widgets/create/v1"
    kcell "github.com/ghbvf/gocell/kernel/cell"
    "github.com/ghbvf/gocell/runtime/auth"
)

// CreateAdapter implements createg.Service.
type CreateAdapter struct{ S *Service }

func (a CreateAdapter) Create(ctx context.Context, req *createg.Request) (*createg.Response, error) {
    w, err := a.S.Create(ctx, req.Name)
    if err != nil {
        return nil, err
    }
    return &createg.Response{Data: &createg.ResponseData{ID: w.ID, Name: w.Name}}, nil
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
| Forgetting `codegen: true` in contract.yaml | Generator skips the contract | Add `codegen: true` |
| Editing files in `generated/` | Overwritten on next generate | Implement the `Service` interface instead |
| Wrong `contractUsages` role | `gocell validate` ADV-06 fail | Set `role: serve` for HTTP server, `role: subscribe` for event subscriber |
