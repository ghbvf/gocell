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

实现 `cell.RouteGroupContributor` 接口，Cell 通过 `RouteGroups()` 声明
每组路由所属的物理 listener，并在 `Register` 闭包里使用 `auth.Mount`
声明鉴权语义（F3 模式，参见
[runtime-api.md](../../.claude/rules/gocell/runtime-api.md)）：

```go
var _ cell.RouteGroupContributor = (*MyCell)(nil)

func (c *MyCell) RouteGroups() []cell.RouteGroup {
    return []cell.RouteGroup{
        cell.SingleGroup(cell.PrimaryListener, "/api/v1/my-resource", func(mux cell.RouteMux) error {
            return auth.Mount(mux, auth.Route{
                Contract: wrapper.ContractSpec{
                    ID:        "http.mycell.my-resource.get.v1",
                    Kind:      "http",
                    Transport: "http",
                    Method:    "GET",
                    Path:      "/api/v1/my-resource/{id}",
                },
                Handler: http.HandlerFunc(c.handler.Get),
                Policy:  auth.Authenticated(),
            })
        }),
    }
}
```

#### Listener 分流

`runtime` 层运行三个独立 `http.Server`：

- **primary** (`:8080` 默认) — `/api/v1/*` 和所有 public 业务路由；JWT AuthMiddleware 通过 `bootstrap.WithListener(cell.PrimaryListener, ..., []cell.ListenerAuth{...})` 装配在此 listener 上。
- **internal** (`127.0.0.1:9090` 默认) — 仅 `/internal/v1/*` 控制面路由；service-token / mTLS 通过 `bootstrap.WithListener(cell.InternalListener, ..., []cell.ListenerAuth{...})` 装配为 listener 级鉴权层。
- **health** (`127.0.0.1:9091` local/dev 默认；生产 PodIP/Service probe 用 `:9091`) — 仅 `/healthz`、`/readyz`、`/metrics`。

Cell 在 `RouteGroups()` 里按 listener + 路径前缀声明，bootstrap 会把每组路由挂到对应 listener 的独立 router：

```go
func (c *MyCell) RouteGroups() []cell.RouteGroup {
    return []cell.RouteGroup{
        cell.SingleGroup(cell.PrimaryListener, "/api/v1/my-resource", func(mux cell.RouteMux) error {
            return auth.Mount(mux, auth.Route{
                Contract: wrapper.ContractSpec{
                    ID:        "http.mycell.my-resource.get.v1",
                    Kind:      "http",
                    Transport: "http",
                    Method:    "GET",
                    Path:      "/api/v1/my-resource/{id}",
                },
                Handler: http.HandlerFunc(c.handler.Get),
                Policy:  auth.Authenticated(),
            })
        }),
        cell.SingleGroup(cell.InternalListener, "/internal/v1/my-resource", func(mux cell.RouteMux) error {
            return auth.Mount(mux, auth.Route{
                Contract: wrapper.ContractSpec{
                    ID:        "http.mycell.my-resource.admin-op.v1",
                    Kind:      "http",
                    Transport: "http",
                    Method:    "POST",
                    Path:      "/internal/v1/my-resource/admin-op",
                },
                Handler: http.HandlerFunc(c.handler.AdminOp),
                Policy:  auth.AnyRole(auth.RoleInternalAdmin),
            })
        }),
    }
}
```

**约束**：

- 所有 `/internal/v1/*` 路由必须挂在 `cell.InternalListener`；`FinalizeAuth()` 会在启动期校验内部前缀与 listener 归属一致。
- 禁止在 `Route` / `Group` / `With` 嵌套子作用域里再次进入 `/internal/v1/*`——会触发 `chiRouterAdapter.guardNestedInternalRegistration` panic（顶层 Router 是内外 mux 分流的唯一入口）。
- `/healthz` / `/readyz` / `/metrics` 只在 health listener；未声明 health listener 时才 fallback 到 primary。

### 5. 注册事件订阅（可选）

实现 `cell.EventRegistrar` 接口，**通过 EventRouter 声明订阅意图**——
禁止手动启动 goroutine 或直接调 `Subscriber.Subscribe`，goroutine 生命周期、
错误收敛、Setup/Ready 阶段一律由 Router 统一接管。

```go
var _ cell.EventRegistrar = (*MyCell)(nil)

// RegisterSubscriptions 在启动阶段被框架调用一次。每次 AddContractHandler 注册
// 一个 (contract, handler, consumerGroup) 三元组：
//   - contract      : contract id、broker 路由键、observability metadata
//   - handler       : outbox.EntryHandler 业务处理函数
//   - consumerGroup : 通常等于 cell.ID()，作为幂等键命名空间；同 group 竞争消费，
//                     不同 group 各自一份（fanout）
//
// 框架会包装 ConsumerBase（两阶段 Claim/Commit/Release + 退避重试 + DLX 路由），
// 业务 handler 只需返回 outbox.HandleResult{Disposition: Ack/Requeue/Reject}。
func (c *MyCell) RegisterSubscriptions(r cell.EventRouter) error {
    handler := outbox.WrapLegacyHandler(c.svc.HandleEvent) // 旧签名 → EntryHandler
    // EventSpec(id, transport) helper for the common case Topic == ID.
    // FMT-18 cross-checks both ContractSpec{} literals and EventSpec(...) calls
    // against contracts/**/contract.yaml — strict-only: the check fires under
    // `gocell validate --strict` (and the strict CI job), not plain `validate`.
    // The id argument must be a string literal so the AST can resolve it;
    // computed ids surface as a FMT-18 WARNING.
    r.AddContractHandler(wrapper.EventSpec("event.my.topic.v1", "amqp"), handler, c.ID())
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

## Schema 演化

`contracts/{kind}/{domain}/v{N}/*.schema.json` 是跨 cell 的唯一契约。schema
按方向分三种政策（详见
[ADR-202605031600](../architecture/202605031600-adr-v1-schema-evolution.md)
和 [api-versioning.md](../../.claude/rules/gocell/api-versioning.md)）：

| 政策 | 适用 schema | 约束 | 守护 |
|------|-------------|------|------|
| **strict (request)** | `request.schema.json`、`error-response-v1.schema.json` | 必须声明 `additionalProperties: false`（含嵌套） | FMT-20 + `verify-schema-policy.sh` |
| **lenient (response/event)** | `response.schema.json`、`payload.schema.json`、`headers.schema.json` | **禁止**声明 `additionalProperties: false`（允许 v1 加 optional 字段不破坏 client/consumer） | `verify-schema-policy.sh --check` |
| **metaonly (whitelist)** | metadata-only event payload（载体只携带 identifier，不携带 state，如 `event.config.entry-upserted.v1`） | 必须声明 `unevaluatedProperties: false`，新增字段必须显式加到 `properties`（防止状态字段误传） | `verify-schema-policy.sh --check` |

### 加新字段的工作流

| 场景 | 步骤 |
|------|------|
| 给 v1 response/event payload 加 optional 字段 | 1) 改 schema `properties`；2) 改 typed struct + JSON tag；3) contract test 通过；CI 自动校验 |
| 给 v1 request 加可选字段 | 同上；schema 仍 strict（FMT-20），所以必须更新 `properties` 列表 |
| 给 metadata-only event 加 identifier 字段 | 同上；`unevaluatedProperties: false` 要求 `properties` 必须列出所有合法字段 |
| 删字段 / 改字段类型 / 改字段含义 | 必须 v2 — 不在 v1 演化范围内 |

### 检查命令

```bash
bash hack/verify-schema-policy.sh           # check 全部策略
bash hack/verify-schema-policy.sh --fix     # auto-strip lenient 违规（误加 additionalProperties:false 时）
go run ./cmd/gocell validate                # gocell 元数据 + FMT-20 + 全部规则
```

### contract test 怎么用

`pkg/contracttest` 提供两组对称 API：`Validate*` 检验合规、`MustReject*`
断言负向。lenient schema 下，`MustReject*` 仅捕获 `required`/`type`/`pattern`
等非加性违规；要断言"额外字段被拒"，schema 必须 metaonly 或 strict。

```go
c := contracttest.LoadByID(t, root, "event.config.entry-upserted.v1")
c.ValidatePayload(t, payload)                                          // 正向
c.MustRejectPayload(t, []byte(`{"key":"k"}`))                         // 缺 required → 拒
c.MustRejectPayload(t, []byte(`{"key":"k","version":1,"actorId":"a","value":"x"}`))
                                                                       // metaonly: extra "value" → 拒
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
3. Start required infrastructure with testcontainers and call `testutil.RequireDocker(t)` before the first container start.
4. Pass container-returned DSNs directly into the adapter or Cell under test; do not skip on missing external DSN environment variables.
5. Each test should be self-contained: create its own resources, run assertions, then clean up.

```go
//go:build integration

package mycell

import (
    "context"
    "testing"

    "github.com/ghbvf/gocell/tests/testutil"
    tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestIntegration_MyCellSmoke(t *testing.T) {
    testutil.RequireDocker(t)

    ctx := context.Background()
    container, err := tcpostgres.Run(ctx, testutil.PostgresImage)
    require.NoError(t, err)
    t.Cleanup(func() { _ = container.Terminate(context.Background()) })

    dsn, err := container.ConnectionString(ctx, "sslmode=disable")
    require.NoError(t, err)

    repo := newPostgresRepo(dsn)
    c := NewMyCell(
        WithPostgresRepo(repo),
    )
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
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags integration ./adapters/postgres/... -count=1 -v
GOCELL_TEST_DOCKER_REQUIRED=1 go test -tags integration ./tests/integration/... -count=1 -v
```

See `docs/guides/integration-testing.md` for full details and environment variable reference.
