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
