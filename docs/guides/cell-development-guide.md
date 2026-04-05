# Cell 开发指南

## Cell 是什么

Cell 是 GoCell 的核心业务单元，封装了一组相关的 Slice（功能切片）。每个 Cell 拥有独立的数据所有权、一致性等级和生命周期。Cell 之间通过 Contract 通信，禁止直接 import 其他 Cell 的 internal 包。GoCell 内置了 3 个 Cell：config-core（配置管理）、access-core（认证授权）、audit-core（审计追踪）。

## 创建自定义 Cell

### 1. 定义目录结构

```
cells/
└── my-cell/
    ├── cell.go              # Cell 入口（实现 cell.Cell 接口）
    ├── cell.yaml            # 元数据声明（必填）
    ├── cell_test.go
    └── slices/
        └── my-slice/
            ├── slice.yaml   # Slice 元数据
            ├── service.go   # 业务逻辑
            ├── handler.go   # HTTP handler
            └── service_test.go
    └── internal/
        ├── domain/          # 领域模型
        ├── ports/           # 驱动端接口（Repository 等）
        └── mem/             # 内存实现（开发/测试用）
```

### 2. 声明 cell.yaml

```yaml
id: my-cell
type: core
consistencyLevel: L1
owner:
  team: my-team
  role: my-cell-owner
schema:
  primary: my_table
verify:
  smoke:
    - my-cell/smoke
```

### 3. 实现 Cell 接口

```go
package mycell

import (
    "context"
    "log/slog"

    "github.com/ghbvf/gocell/kernel/cell"
)

var _ cell.Cell = (*MyCell)(nil)

type MyCell struct {
    *cell.BaseCell
    logger *slog.Logger
    // ... 依赖字段
}

func NewMyCell(opts ...Option) *MyCell {
    c := &MyCell{
        BaseCell: cell.NewBaseCell(cell.CellMetadata{
            ID:               "my-cell",
            Type:             cell.CellTypeCore,
            ConsistencyLevel: cell.L1,
            Owner:            cell.Owner{Team: "my-team", Role: "my-cell-owner"},
            Schema:           cell.SchemaConfig{Primary: "my_table"},
            Verify:           cell.CellVerify{Smoke: []string{"my-cell/smoke"}},
        }),
        logger: slog.Default(),
    }
    for _, o := range opts {
        o(c)
    }
    return c
}

func (c *MyCell) Init(ctx context.Context, deps cell.Dependencies) error {
    if err := c.BaseCell.Init(ctx, deps); err != nil {
        return err
    }
    // 构造 Slice 并注册
    c.AddSlice(cell.NewBaseSlice("my-slice", "my-cell", cell.L1))
    return nil
}
```

### 4. 注册 HTTP 路由（可选）

实现 `cell.HTTPRegistrar` 接口：

```go
var _ cell.HTTPRegistrar = (*MyCell)(nil)

func (c *MyCell) RegisterRoutes(mux cell.RouteMux) {
    mux.Handle("/api/v1/my-resource/*", c.handler.Routes())
}
```

### 5. 注册事件订阅（可选）

实现 `cell.EventRegistrar` 接口：

```go
var _ cell.EventRegistrar = (*MyCell)(nil)

func (c *MyCell) RegisterSubscriptions(sub outbox.Subscriber) {
    go func() {
        ctx := context.Background()
        if err := sub.Subscribe(ctx, "my.topic", c.svc.HandleEvent); err != nil {
            c.logger.Error("subscription ended", slog.Any("error", err))
        }
    }()
}
```

### 6. 注册到 Assembly

```go
asm := assembly.New(assembly.Config{ID: "my-app"})
asm.Register(mycell.NewMyCell(...))
```

## Slice 依赖注入模式

GoCell 使用**构造时注入**：所有依赖通过 Option 函数在 `New*Cell()` 时传入，Cell 在 `Init()` 中将依赖分发给各 Slice。

```go
// Option 模式
type Option func(*MyCell)

func WithMyRepo(r ports.MyRepository) Option {
    return func(c *MyCell) { c.repo = r }
}

// Init 中分发给 Slice
func (c *MyCell) Init(ctx context.Context, deps cell.Dependencies) error {
    svc := myslice.NewService(c.repo, c.logger)
    c.handler = myslice.NewHandler(svc)
    c.AddSlice(cell.NewBaseSlice("my-slice", "my-cell", cell.L1))
    return nil
}
```

对于开发和测试，可提供 `WithInMemoryDefaults()` 选项：

```go
func WithInMemoryDefaults() Option {
    return func(c *MyCell) {
        c.repo = mem.NewMyRepository()
    }
}
```

## 测试

使用 table-driven test，kernel/ 层覆盖率 >= 90%，其他层 >= 80%。

```go
func TestMyCell_Lifecycle(t *testing.T) {
    c := NewMyCell(WithInMemoryDefaults())
    ctx := context.Background()
    deps := cell.Dependencies{
        Cells:     make(map[string]cell.Cell),
        Contracts: make(map[string]cell.Contract),
        Config:    make(map[string]any),
    }

    require.NoError(t, c.Init(ctx, deps))
    require.NoError(t, c.Start(ctx))
    assert.Equal(t, "healthy", c.Health().Status)
    require.NoError(t, c.Stop(ctx))
}
```

---

## Contract Test 编写指引

Contract tests verify that a Slice honours the interface it advertises in its
`slice.yaml` `contractUsages` field. Every contract provider Slice must have a
corresponding contract test file.

### File Naming Convention

```
cells/{cell-id}/slices/{slice-id}/contract_test.go
```

### Structure

A contract test uses only the public interface of the Slice's service and
asserts that the contract schema is satisfied. It must not access internal/
packages of other Cells.

```go
package myslice_test

import (
    "context"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    mycell "github.com/ghbvf/gocell/cells/my-cell"
    "github.com/ghbvf/gocell/cells/my-cell/slices/my-slice"
)

// TestContract_HttpMyResourceGet verifies that the Slice implements the
// http.my-resource.get.v1 contract:
//   - Response contains required fields: id, name, createdAt
//   - 404 is returned for unknown resources (ERR_MY_RESOURCE_NOT_FOUND)
func TestContract_HttpMyResourceGet(t *testing.T) {
    svc := myslice.NewService(mem.NewMyRepository(), slog.Default())

    t.Run("found", func(t *testing.T) {
        resp, err := svc.Get(context.Background(), "known-id")
        require.NoError(t, err)
        assert.NotEmpty(t, resp.ID)
        assert.NotEmpty(t, resp.Name)
        assert.False(t, resp.CreatedAt.IsZero())
    })

    t.Run("not_found", func(t *testing.T) {
        _, err := svc.Get(context.Background(), "unknown-id")
        var codeErr *errcode.Error
        require.ErrorAs(t, err, &codeErr)
        assert.Equal(t, errcode.Code("ERR_MY_RESOURCE_NOT_FOUND"), codeErr.Code)
    })
}
```

### Rules

1. Contract tests must use the in-memory port implementation — no real database.
2. Contract tests must be runnable with `go test ./...` (no special build tags
   unless integration-level, which is separate).
3. The test function name must reference the contract identifier from
   `slice.yaml` (e.g., `TestContract_HttpMyResourceGet` for
   `http.my-resource.get.v1`).
4. Each contract provider test must cover at least: happy path, not-found, and
   validation error paths.

---

## 错误处理模式 — errcode 用法

All errors crossing package boundaries must use `pkg/errcode`. Never use
`errors.New` or `fmt.Errorf` as the outermost error in a public function
signature.

### Defining Error Codes

Add domain-specific codes to `pkg/errcode/errcode.go` (or a Cell-local file
if they are Cell-internal only):

```go
const (
    ErrMyResourceNotFound errcode.Code = "ERR_MY_RESOURCE_NOT_FOUND"
    ErrMyResourceConflict errcode.Code = "ERR_MY_RESOURCE_CONFLICT"
)
```

Error code naming convention: `ERR_{DOMAIN}_{CONDITION}` in SCREAMING_SNAKE_CASE.

### Service Layer — Returning Errors

```go
import "github.com/ghbvf/gocell/pkg/errcode"

func (s *Service) Get(ctx context.Context, id string) (*MyResource, error) {
    r, err := s.repo.Find(ctx, id)
    if err != nil {
        // Wrap with context; preserve the cause for debugging.
        return nil, errcode.Wrap(errcode.ErrInternal, "get my-resource", err)
    }
    if r == nil {
        return nil, errcode.New(ErrMyResourceNotFound, "resource not found")
    }
    return r, nil
}
```

### Handler Layer — Converting to HTTP Status

```go
import (
    "github.com/ghbvf/gocell/pkg/errcode"
    "github.com/ghbvf/gocell/pkg/httputil"
)

func (h *Handler) GetResource(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    resource, err := h.svc.Get(r.Context(), id)
    if err != nil {
        var codeErr *errcode.Error
        if errors.As(err, &codeErr) {
            switch codeErr.Code {
            case ErrMyResourceNotFound:
                httputil.Error(w, http.StatusNotFound, codeErr)
            default:
                slog.ErrorContext(r.Context(), "get resource failed",
                    slog.String("id", id),
                    slog.Any("error", err),
                )
                httputil.Error(w, http.StatusInternalServerError,
                    errcode.New(errcode.ErrInternal, "internal server error"))
            }
            return
        }
        httputil.Error(w, http.StatusInternalServerError,
            errcode.New(errcode.ErrInternal, "internal server error"))
        return
    }
    httputil.JSON(w, http.StatusOK, map[string]any{"data": resource})
}
```

### Rules

- Domain layer: return `*errcode.Error`; never return HTTP status codes.
- Handler layer: translate `*errcode.Error` codes to HTTP status via a switch.
- Always log at Error level with structured context fields before returning 500.
- Never expose raw `err.Error()` strings from internal/infrastructure errors in
  HTTP responses.
- Use `errcode.Wrap` (not bare `fmt.Errorf`) to add context while preserving the
  original cause for `errors.Is` / `errors.As` chains.

### HTTP Status Code Mapping

| errcode.Code prefix | HTTP status |
|--------------------|-------------|
| `ERR_*_NOT_FOUND`  | 404 |
| `ERR_*_CONFLICT`   | 409 |
| `ERR_VALIDATION_*` | 400 |
| `ERR_AUTH_UNAUTHORIZED` | 401 |
| `ERR_AUTH_FORBIDDEN` | 403 |
| `ERR_RATE_LIMITED` | 429 |
| `ERR_BODY_TOO_LARGE` | 413 |
| `ERR_INTERNAL`     | 500 |

---

## Outbox.Writer — 事务内写入模式

Cells with consistency level L2 (OutboxFact) must publish domain events using
`kernel/outbox.Writer` inside the same database transaction as the business
state write. This is the transactional outbox pattern.

### Why Outbox?

Direct calls to `outbox.Publisher` after committing the DB transaction create
a gap: the DB commit succeeds but the publish might fail, leaving subscribers
without the event. The outbox pattern eliminates this gap by writing the event
record in the same transaction and relying on a relay (adapters/rabbitmq relay
worker) to deliver it asynchronously.

### Usage Pattern

```go
// L2 service method — session creation with outbox write
func (s *Service) CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error) {
    // 1. Begin transaction (injected from ctx or repo abstraction)
    session := &Session{
        ID:        id.New("sess"),
        UserID:    req.UserID,
        CreatedAt: time.Now(),
    }

    // 2. Write business state AND outbox entry atomically
    if err := s.repo.CreateWithOutbox(ctx, session, outbox.Entry{
        AggregateID:   session.ID,
        AggregateType: "session",
        EventType:     "event.session.created.v1",
        Payload:       mustMarshal(SessionCreatedEvent{SessionID: session.ID}),
    }); err != nil {
        return nil, errcode.Wrap(errcode.ErrInternal, "create session", err)
    }

    // 3. Do NOT call publisher.Publish here — the relay handles delivery
    return session, nil
}
```

### Repository Port Contract

The repository port must expose a combined write method for L2 Cells:

```go
// In internal/ports/session_repository.go
type SessionRepository interface {
    Create(ctx context.Context, session *domain.Session) error

    // CreateWithOutbox writes both the session and an outbox entry in a
    // single transaction. The ctx may carry a DB transaction (from adapters/).
    // Phase 2 in-memory implementations may ignore the outbox entry.
    CreateWithOutbox(ctx context.Context, session *domain.Session, event outbox.Entry) error
}
```

### Phase 2 vs Phase 3 Behaviour

| Phase | What happens to the outbox entry |
|-------|----------------------------------|
| Phase 2 | In-memory repo ignores the outbox.Entry; direct Publish call is used as a temporary substitute. See tech-debt P2-ARCH-07. |
| Phase 3 | Postgres adapter writes the entry to an `outbox_entries` table within the transaction; the RabbitMQ relay worker polls and delivers. |

> Tech-debt P2-ARCH-07 tracks migration from direct Publish to outbox.Writer
> for all L2 slices. See `docs/tech-debt-registry.md`.

### Rules

1. L2 Cells must use `outbox.Writer` in Phase 3. Phase 2 may use direct
   Publish as a documented temporary measure.
2. The `outbox.Entry.EventType` must match the event contract identifier in
   `contracts/` (e.g., `event.session.created.v1`).
3. The `outbox.Entry.AggregateID` must be the primary key of the modified
   aggregate for deduplication.
4. L3/L4 Cells that only consume events do not need outbox.Writer.
5. In tests, use `runtime/eventbus.InMemoryEventBus` as both Publisher and
   Subscriber to validate the business logic without the relay.
