# Cell 开发指南

## Cell 是什么

Cell 是 GoCell 的核心业务单元，封装了一组相关的 Slice（功能切片）。每个 Cell 拥有独立的数据所有权、一致性等级和生命周期。Cell 之间通过 Contract 通信，禁止直接 import 其他 Cell 的 internal 包。GoCell 内置了 3 个 Cell：configcore（配置管理）、accesscore（认证授权）、auditcore（审计追踪）。

## 创建自定义 Cell

### 1. 定义目录结构

```
cells/
└── mycell/
    ├── cell.go              # Cell 入口（实现 cell.Cell 接口）
    ├── cell.yaml            # 元数据声明（必填）
    ├── cell_test.go
    └── slices/
        └── myslice/
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
id: mycell
type: core
consistencyLevel: L1
owner:
  team: my-team
  role: mycell-owner
schema:
  primary: my_table
verify:
  smoke:
    - mycell/smoke
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
            ID:               "mycell",
            Type:             cell.CellTypeCore,
            ConsistencyLevel: cell.L1,
            Owner:            cell.Owner{Team: "my-team", Role: "mycell-owner"},
            Schema:           cell.SchemaConfig{Primary: "my_table"},
            Verify:           cell.CellVerify{Smoke: []string{"mycell/smoke"}},
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
    c.AddSlice(cell.NewBaseSlice("myslice", "mycell", cell.L1))
    return nil
}
```

### 4. 注册 HTTP 路由（可选）

实现 `cell.HTTPRegistrar` 接口，使用 `auth.Declare` 在注册点声明鉴权语义
（F3 模式，参见 [runtime-api.md](../../.claude/rules/gocell/runtime-api.md)）：

```go
var _ cell.HTTPRegistrar = (*MyCell)(nil)

func (c *MyCell) RegisterRoutes(mux cell.RouteMux) {
    mux.Route("/api/v1/my-resource", func(sub cell.RouteMux) {
        auth.Declare(sub, auth.RouteDecl{
            Method:  "GET",
            Path:    "/{id}",
            Handler: http.HandlerFunc(c.handler.Get),
            Policy:  auth.Authenticated(),
        })
    })
}
```

#### PR-A14a 双 listener 分流

`runtime` 层运行两个独立 `http.Server`：

- **primary** (`:8080` 默认) — `/api/v1/*`、`/healthz`、`/readyz`、`/metrics`、所有 public 业务路由；JWT AuthMiddleware 工作在此 listener 上。
- **internal** (`127.0.0.1:9090` 默认) — 仅 `/internal/v1/*` 控制面路由；service-token / mTLS 中间件由 composition root 通过 `bootstrap.WithInternalMiddleware(mw)` 注入作为唯一鉴权层。

Cell 在 `RegisterRoutes` 里按路径前缀声明，`Router` 自动把 `/internal/v1/*` pattern 物理挂到 internalMux：

```go
func (c *MyCell) RegisterRoutes(mux cell.RouteMux) {
    // public → publicMux
    mux.Route("/api/v1/my-resource", func(sub cell.RouteMux) {
        auth.Declare(sub, auth.RouteDecl{
            Method: "GET", Path: "/{id}",
            Handler: http.HandlerFunc(c.handler.Get),
            Policy:  auth.Authenticated(),
        })
    })
    // internal → internalMux（service-token/mTLS 是唯一鉴权层；JWT 不触达）
    mux.Route("/internal/v1/my-resource", func(sub cell.RouteMux) {
        auth.Declare(sub, auth.RouteDecl{
            Method: "POST", Path: "/admin-op",
            Handler: http.HandlerFunc(c.handler.AdminOp),
            Policy:    internalAdminPolicy,
            Delegated: true, // 必须：FinalizeAuth 在启动期断言 Delegated ⇔ /internal/v1/*
        })
    })
}
```

**约束**：

- 所有 `/internal/v1/*` 路由必须 `Delegated: true`，否则 `FinalizeAuth()` 启动期失败；反之 `Delegated: true` 只能用于 `/internal/v1/*`。
- 禁止在 `Route` / `Group` / `With` 嵌套子作用域里再次进入 `/internal/v1/*`——会触发 `chiRouterAdapter.guardNestedInternalRegistration` panic（顶层 Router 是内外 mux 分流的唯一入口）。
- `/healthz` / `/readyz` / `/metrics` 只在 primary listener，internal listener 对这些路径返回 404。

### 5. 注册事件订阅（可选）

实现 `cell.EventRegistrar` 接口，**通过 EventRouter 声明订阅意图**——
禁止手动启动 goroutine 或直接调 `Subscriber.Subscribe`，goroutine 生命周期、
错误收敛、Setup/Ready 阶段一律由 Router 统一接管。

```go
var _ cell.EventRegistrar = (*MyCell)(nil)

// RegisterSubscriptions 在启动阶段被框架调用一次。每次 AddHandler 注册
// 一个 (topic, handler, consumerGroup) 三元组：
//   - topic         : broker 路由键
//   - handler       : outbox.EntryHandler 业务处理函数
//   - consumerGroup : 通常等于 cell.ID()，作为幂等键命名空间；同 group 竞争消费，
//                     不同 group 各自一份（fanout）
//
// 框架会包装 ConsumerBase（两阶段 Claim/Commit/Release + 退避重试 + DLX 路由），
// 业务 handler 只需返回 outbox.HandleResult{Disposition: Ack/Requeue/Reject}。
func (c *MyCell) RegisterSubscriptions(r cell.EventRouter) error {
    handler := outbox.WrapLegacyHandler(c.svc.HandleEvent) // 旧签名 → EntryHandler
    r.AddHandler("my.topic.v1", handler, c.ID())
    return nil
}
```

EventRouter 在所有 cell 注册完成后按四阶段生命周期启动：
1. **Setup**：串行调 `Subscriber.Setup(sub)` 声明 broker topology；任一失败立即终止
2. **Subscribe**：每个 handler 起一个 goroutine 调 `Subscribe(ctx, sub, handler)`
3. **Ready**：等所有 `Subscriber.Ready(sub)` channel close（默认 30s 超时；
   `bootstrap.WithEventRouterReadyTimeout` 可调），任何未就绪的订阅会出现在错误信息中
4. **Block**：阻塞至 ctx cancel 或运行时错误

### 6. 注册到 Assembly

```go
asm := assembly.New(assembly.Config{ID: "myapp", DurabilityMode: cell.DurabilityDemo})
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
    c.AddSlice(cell.NewBaseSlice("myslice", "mycell", cell.L1))
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
        Config:         make(map[string]any),
        DurabilityMode: cell.DurabilityDemo,
    }

    require.NoError(t, c.Init(ctx, deps))
    require.NoError(t, c.Start(ctx))
    assert.Equal(t, "healthy", c.Health().Status)
    require.NoError(t, c.Stop(ctx))
}
```

## Integration Testing

Integration tests verify adapter behaviour against real infrastructure. They use the `//go:build integration` build tag and are excluded from the default `go test ./...`.

### Writing Integration Tests for Your Cell

1. Create `integration_test.go` in your Cell or adapter package.
2. Add `//go:build integration` as the first line.
3. Read infrastructure addresses from environment variables (see `docs/guides/integration-testing.md`).
4. Each test should be self-contained: create its own resources, run assertions, then clean up.

```go
//go:build integration

package mycell

import "testing"

func TestIntegration_MyCellSmoke(t *testing.T) {
    // Boot cell with real adapters
    c := NewMyCell(
        WithPostgresRepo(realRepo),
    )
    ctx := context.Background()
    deps := cell.Dependencies{...}

    require.NoError(t, c.Init(ctx, deps))
    require.NoError(t, c.Start(ctx))
    defer c.Stop(ctx)

    // Exercise real adapter paths
    // ...
}
```

### Running

```bash
docker compose up -d          # boot infrastructure
go test -tags integration ./adapters/postgres/... -count=1 -v
go test -tags integration ./tests/integration/... -count=1 -v
```

See `docs/guides/integration-testing.md` for full details and environment variable reference.
