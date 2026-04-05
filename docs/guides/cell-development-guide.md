# Cell Development Guide

> How to create, test, and operate Cells in the GoCell framework.

## Cell Structure

Every Cell lives under `cells/{cell-id}/` and must contain:

```
cells/my-cell/
  cell.yaml           # Cell metadata (required)
  slices/
    my-slice/
      slice.yaml      # Slice metadata (required)
      handler.go      # HTTP/event handler
      service.go      # Business logic
      repository.go   # Data access
      ...
```

## cell.yaml Reference

```yaml
id: my-cell
type: core                # core | edge | support
consistencyLevel: L2      # L0-L4
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_my_cell   # Primary DB schema
verify:
  smoke:
    - smoke.my-cell.startup
l0Dependencies: []         # Direct L0 cell dependencies
```

Required fields: `id`, `type`, `consistencyLevel`, `owner`, `schema.primary`, `verify.smoke`.

## slice.yaml Reference

```yaml
id: my-slice
belongsToCell: my-cell
consistencyLevel: L2       # Inherits from cell if omitted
owner:                     # Inherits from cell if omitted
  team: platform
  role: slice-owner
contractUsages:
  - contract: event/session/created/v1
    role: subscribe
verify:
  unit:
    - go test ./cells/my-cell/slices/my-slice/...
  contract:
    - contract-test.my-slice.session-created
allowedFiles:              # Defaults to cells/{cell-id}/slices/{slice-id}/**
  - cells/my-cell/slices/my-slice/**
```

Required fields: `id`, `belongsToCell`, `contractUsages`, `verify.unit`, `verify.contract`.

## Consistency Levels

| Level | Name | When to Use | Requirements |
|-------|------|-------------|-------------|
| L0 | LocalOnly | Pure computation, validation | No persistence |
| L1 | LocalTx | Single-cell transactions | Local DB transaction |
| L2 | OutboxFact | Events with delivery guarantee | Local TX + outbox entry |
| L3 | WorkflowEventual | Cross-cell eventual consistency | Saga/choreography |
| L4 | DeviceLatent | Long-delay device loops | Command + ack with timeout |

## Contract Communication

Cells communicate exclusively through contracts. Direct imports between cells are prohibited (except L0 cells within the same assembly).

### Contract Kinds and Roles

| Kind | Provider Role | Consumer Role |
|------|--------------|---------------|
| http | serve | call |
| event | publish | subscribe |
| command | handle | invoke |
| projection | provide | read |

### Declaring Contract Usage

In `slice.yaml`:

```yaml
contractUsages:
  - contract: event/session/created/v1
    role: publish        # This slice publishes session.created events
  - contract: http/auth/login/v1
    role: serve          # This slice serves the login HTTP endpoint
```

## Contract Testing

Contract tests verify that a cell correctly implements its contract obligations.

### Provider Contract Test

Verifies that the producing cell emits events/responses matching the contract schema:

```go
func TestContract_SessionCreatedEvent(t *testing.T) {
    // Arrange: set up the session-login slice
    // Act: trigger a login
    // Assert: the emitted event matches event/session/created/v1 schema
}
```

### Consumer Contract Test

Verifies that the consuming cell correctly handles events/requests from the contract:

```go
func TestContract_AuditAppendConsumesSessionCreated(t *testing.T) {
    // Arrange: create a session.created event matching the contract schema
    // Act: deliver the event to audit-append
    // Assert: an audit entry is written
}
```

### Waivers

Temporary exemptions from contract tests can be declared in `slice.yaml`:

```yaml
verify:
  contract:
    - contract-test.my-slice.session-created
  waivers:
    - contract: event/session/created/v1
      owner: platform-team
      reason: "Consumer implementation pending; tracked in JIRA-1234"
      expiresAt: "2026-05-01"
```

## Error Handling with errcode

All errors exposed across package boundaries must use `pkg/errcode`. Bare `errors.New` is prohibited.

### Creating Errors

```go
import "github.com/ghbvf/gocell/pkg/errcode"

// Simple error
return errcode.New(errcode.ErrValidationFailed, "session token expired")

// Wrapping an underlying error
return errcode.Wrap(errcode.ErrCellNotFound, "lookup failed", err)

// Adding details
return errcode.WithDetails(
    errcode.New(errcode.ErrValidationFailed, "invalid config key"),
    map[string]any{"key": key, "cell": cellID},
)
```

### Error Code Prefixes

| Prefix | Module |
|--------|--------|
| `ERR_AUTH_*` | Authentication |
| `ERR_VALIDATION_*` | General validation |
| `ERR_METADATA_*` | Metadata loading/parsing |
| `ERR_CELL_*` | Cell lifecycle |
| `ERR_SLICE_*` | Slice operations |
| `ERR_CONTRACT_*` | Contract operations |
| `ERR_ASSEMBLY_*` | Assembly operations |
| `ERR_LIFECYCLE_*` | Lifecycle transitions |
| `ERR_ADAPTER_*` | Adapter operations (postgres, redis, etc.) |

### Handler Error Translation

The HTTP handler layer translates `errcode.Error` to HTTP status codes. Domain code must never return HTTP status codes directly:

```go
// handler.go -- translates domain errors to HTTP responses
func handleLogin(w http.ResponseWriter, r *http.Request) {
    result, err := loginService.Execute(r.Context(), req)
    if err != nil {
        writeError(w, err) // maps errcode.Code -> HTTP status
        return
    }
    writeJSON(w, http.StatusOK, result)
}
```

## Outbox Pattern

For L2+ operations, the outbox pattern guarantees at-least-once event delivery:

### Writing with Outbox

```go
func (s *SessionLoginService) Execute(ctx context.Context, req LoginRequest) error {
    return s.db.WithTx(ctx, func(tx *sql.Tx) error {
        // 1. Write business data
        session, err := s.repo.CreateSession(ctx, tx, req)
        if err != nil {
            return fmt.Errorf("create session: %w", err)
        }

        // 2. Write outbox entry in the same transaction
        event := SessionCreatedEvent{
            EventID:   uuid.New().String(),
            SessionID: session.ID,
            UserID:    req.UserID,
            CreatedAt: time.Now(),
        }
        if err := s.outbox.Enqueue(ctx, tx, "session.created", event); err != nil {
            return fmt.Errorf("enqueue outbox: %w", err)
        }

        return nil
    })
}
```

### Outbox Poller

The outbox poller is a background worker that:

1. Reads pending entries: `SELECT ... FROM outbox WHERE delivered_at IS NULL FOR UPDATE SKIP LOCKED`
2. Publishes each event to the message broker
3. Marks delivered entries: `UPDATE outbox SET delivered_at = NOW() WHERE id = $1`

### Event Consumer with Idempotency

```go
// Consumer: cg-audit-session-created
// Idempotency key: audit:cg-audit-session-created:{event-id}, TTL 24h
// ACK timing: after business logic + idempotency key written
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter

func (c *AuditAppendConsumer) Handle(ctx context.Context, msg Message) error {
    event, err := unmarshalSessionCreated(msg)
    if err != nil {
        return deadLetter(ctx, msg, err) // permanent error, no retry
    }

    if err := c.auditRepo.Append(ctx, event); err != nil {
        return err // transient error -> NACK + backoff retry
    }

    return nil // ACK
}
```

## Prohibited Patterns

### Forbidden Field Names

The following legacy field names are prohibited in YAML metadata:

`cellId`, `sliceId`, `contractId`, `assemblyId`, `ownedSlices`, `authoritativeData`, `producer`, `consumers`, `callsContracts`, `publishes`, `consumes`

Use the v3 metadata model field names instead (see `docs/architecture/metadata-model-v3.md`).

### Forbidden Dependencies

- `kernel/` must not import `runtime/`, `adapters/`, or `cells/`
- `cells/` must not import `adapters/` (use interfaces)
- `runtime/` must not import `cells/` or `adapters/`
- Cell A must not import Cell B's `internal/` package

### Forbidden Logging

- No `fmt.Println` or `log.Printf` -- use `slog`
- No bare `slog.Error("failed")` -- must include structured fields
- No Debug-level dumps of full request/response bodies in production

## Development Workflow

1. Define the cell and slice YAML metadata
2. Create the contract YAML if new contracts are needed
3. Implement the handler, service, and repository layers
4. Write unit tests (coverage >= 80%, kernel >= 90%)
5. Write contract tests for each contractUsage
6. Run `go build ./... && go test ./... -count=1`
7. Verify with `gocell validate-meta`
