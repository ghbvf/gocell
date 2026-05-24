# 为什么必须 wrap raw infra：Sealed Marker 消费者指南

> 本篇面向 Go 开发者（Cell 实现者 / Example 作者）。  
> 框架内部贡献者视角请阅读 ADR `docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md`。

## 为什么我必须 wrap raw infra？

当你构建一个 Cell 时，注入依赖的 Option 函数接受的类型不是 raw infra：

```go
// cells/mycell/cell.go — 正确签名
func WithTxManager(tx persistence.CellTxManager) Option  { ... }
func WithOutboxDeps(pub outbox.CellPublisher, w outbox.CellWriter) Option { ... }
```

而不是：

```go
// 错误：不接受 raw infra
func WithTxManager(tx persistence.TxRunner) Option      { ... } // 编译期 / archtest 拦截
func WithOutboxDeps(pub outbox.Publisher, ...) Option    { ... } // 编译期 / archtest 拦截
```

**为什么这样设计？**

GoCell 要求 raw infra 只能在 composition root（`cmd/*` / `examples/*/main.go` 等）
被组装，不允许进入 cell 子树。原因有三：

1. **Composition root 单源治理**：谁在哪里拿了哪个 DB 连接池、哪个 broker
   publisher，必须在一个地方读清楚。raw infra 散入各 cell 会让这个"清单"消失。

2. **Type-system Hard 防线**：sealed marker 携带 unexported method（`sealedCellTxManager()`
   等），包外任何类型无法实现它，只能走 kernel 提供的 `Wrap*ForCell(...)` 工厂。
   这使"往 cell 注入 raw infra"在编译期不可表达，而不是靠 lint 或代码 review 拦截。

3. **AI-robust 设计原则**：GoCell 的主要实施者是 AI。字符串约定和注释可被 AI 复制
   粘贴绕过；Go 类型系统不会。这是项目治理章程（`.claude/rules/gocell/ai-robust.md`）
   要求"违反不可表达"的直接实践。

**正确做法**：在 composition root 用以下三个工厂函数包装 raw infra：

| Raw infra | Sealed marker | Wrap 函数 |
|-----------|---------------|-----------|
| `persistence.TxRunner` | `persistence.CellTxManager` | `persistence.WrapForCell(tx)` |
| `outbox.Publisher` | `outbox.CellPublisher` | `outbox.WrapPublisherForCell(pub)` |
| `outbox.Writer` | `outbox.CellWriter` | `outbox.WrapWriterForCell(w)` |

## 如果我不 wrap，编译会怎样？

尝试把 raw infra 直接传给 cell Option 时，Go 编译器输出类似：

```
cannot use myWriter (variable of type outbox.Writer) as outbox.CellWriter value
in argument to ordercell.WithOutboxWriter:
    outbox.Writer does not implement outbox.CellWriter
    (missing method sealedCellWriter)
```

或：

```
cannot use pgTxRunner (variable of type *adapters/postgres.TxManager)
as persistence.CellTxManager value in argument to mycell.WithTxManager:
    *adapters/postgres.TxManager does not implement persistence.CellTxManager
    (missing method sealedCellTxManager)
```

**如何解读这类错误：**

- `sealedCellWriter` / `sealedCellPublisher` / `sealedCellTxManager` 是 unexported
  marker method，**不要尝试在自己的类型上实现它**——这些 method 只属于 kernel
  包内部的 wrapper struct，包外实现会导致编译失败（unexported，无法在包外声明）。
- 正确修法：调用对应的 `Wrap*ForCell(...)` 工厂，它在 kernel 包内完成实现，
  返回已满足 sealed interface 的值。

同理，如果你看到：

```
cannot use myPublisher as outbox.CellPublisher (missing method sealedCellPublisher)
```

修法是 `outbox.WrapPublisherForCell(myPublisher)`，而不是给 `myPublisher` 加方法。

如果 archtest `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` 在 CI 报红，说明某个 cell
子树（`cells/<x>/**/*.go` 或 `examples/<demo>/cells/<x>/**/*.go`）有公开的 `With*`
Option 签名直接接受了 `persistence.TxRunner` / `outbox.Publisher` / `outbox.Writer`
类型（或其 inline interface embedding 形态）。修法是将该 Option 参数类型改为
对应的 sealed marker 类型。

## 三场景标准写法

### Demo 模式（in-process，无 broker / 无 DB）

开发时或单元测试中不想启动外部依赖，用内置的 noop / discard 实现：

```go
// examples/myapp/main.go 或 cmd/myserver/main.go（composition root）
import (
    "github.com/ghbvf/gocell/kernel/outbox"
    "github.com/ghbvf/gocell/kernel/persistence"
    "github.com/ghbvf/gocell/cells/mycell"
)

cell, err := mycell.New(
    mycell.WithOutboxDeps(
        outbox.WrapPublisherForCell(outbox.DiscardPublisher{}),
        outbox.WrapWriterForCell(outbox.NoopWriter{}),
    ),
    mycell.WithTxManager(persistence.WrapForCell(persistence.DemoTxRunner{})),
)
```

`outbox.DiscardPublisher{}` 丢弃所有发布调用（不报错）；`outbox.NoopWriter{}` 同理。
`persistence.DemoTxRunner{}` 提供无 DB 的事务语义（调用成功但不持久化）。
这三个类型均实现 `Noop() bool` 接口，`cell.CheckNotNoop` 在 `Init()` 时会记录
warn 日志提示当前运行在 demo 模式——这是预期行为。

### Production 模式（RabbitMQ + PostgreSQL）

在 composition root 中拿到真实的 adapter 实例后 wrap：

```go
// cmd/corebundle/access_module.go（composition root）
import (
    "github.com/ghbvf/gocell/kernel/outbox"
    "github.com/ghbvf/gocell/kernel/persistence"
    "github.com/ghbvf/gocell/cells/mycell"
    "github.com/ghbvf/gocell/adapters/postgres"
    "github.com/ghbvf/gocell/adapters/rabbitmq"
)

// rabbitPub 是 *rabbitmq.Publisher（实现 outbox.Publisher）
// pgWriter 是 *postgres.OutboxWriter（实现 outbox.Writer）
// pgTxRunner 是 *postgres.TxManager（实现 persistence.TxRunner）

cell, err := mycell.New(
    mycell.WithOutboxDeps(
        outbox.WrapPublisherForCell(rabbitPub),
        outbox.WrapWriterForCell(pgWriter),
    ),
    mycell.WithTxManager(persistence.WrapForCell(pgTxRunner)),
)
```

sealed wrapper 的 embed 设计让 cell 内部 service 方法体仍可透明调用 raw 方法
（如 `s.txRunner.RunInTx(...)`），不需要任何额外适配——embed 保留了 raw method set。

### Test 模式（构造 fake，在 `*_test.go` 任意位置）

wrap 函数在测试文件中可以任意调用：

```go
// cells/mycell/cell_test.go 或 cells/mycell/slices/xxx/service_test.go
import (
    "github.com/ghbvf/gocell/kernel/outbox"
    "github.com/ghbvf/gocell/kernel/persistence"
    "github.com/ghbvf/gocell/cells/mycell"
)

func TestMyCell_Init(t *testing.T) {
    fakePub := &outbox.FakePublisher{} // 或任意实现 outbox.Publisher 的 testdouble
    fakeWriter := outbox.NoopWriter{}
    fakeTx := persistence.DemoTxRunner{}

    c, err := mycell.New(
        mycell.WithOutboxDeps(
            outbox.WrapPublisherForCell(fakePub),
            outbox.WrapWriterForCell(fakeWriter),
        ),
        mycell.WithTxManager(persistence.WrapForCell(fakeTx)),
    )
    // ...
}
```

`*_test.go` 文件不受 `CELL-RAW-INFRA-WRAPPER-LOCATION-01` archtest 的路径限制，
可以在任意测试文件中调用 `Wrap*ForCell` 函数。

## Wrap 函数允许出现的位置

archtest `CELL-RAW-INFRA-WRAPPER-LOCATION-01` 静态守卫 wrap 函数
（`persistence.WrapForCell` / `outbox.WrapPublisherForCell` / `outbox.WrapWriterForCell`）
的调用方所在文件路径。只有以下位置允许调用：

| 位置 | 说明 |
|------|------|
| `cmd/*` 任意文件 | composition root（corebundle / CLI 入口等） |
| `examples/<demo>/main.go` | example 顶层入口 |
| `examples/<demo>/app.go` | example 应用初始化文件 |
| `examples/<demo>/run.go` | example 运行时启动文件 |
| `*_test.go` 任意路径 | 测试文件，无路径限制 |
| `kernel/persistence/cell_marker.go` | marker 定义本身（实现内部） |
| `kernel/outbox/cell_marker.go` | marker 定义本身（实现内部） |
| `kernel/outbox/demo_tx_runner.go` | `DemoCellTxManager()` 工厂（cells/* demo fallback 收敛点） |

**cells/* 内部不允许出现任何 wrap 调用**（含 cell-package root / `internal/` /
`slices/<y>/` / `postgres/` / `mem/` 等所有子目录）。cells/* 是 raw infra
的"消费侧"，只能接受已 wrap 好的 sealed marker，不负责 wrap 逻辑本身。

违反时 archtest 在 CI 报红，错误信息会指出违规的文件路径和调用形态（包括
dot-import 形态 `import . "kernel/persistence"; WrapForCell(p)` 也会被拦截）。

## 进阶阅读

- **ADR** `docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md` —
  框架内部贡献者视角，包含 sealed marker 的 type system Hard 防线 + archtest
  Medium 双重防线的完整设计决策，以及 Amendment 2026-05-12 scope 扩展说明。

- **AI 规则** `.claude/rules/gocell/cell-patterns.md` §"Sealed Marker Wrap Pattern" —
  包含完整的 Raw infra / Sealed marker / Wrapper 对照表，以及各 cell 类型的
  cell-specific Option 声明规范（`WithOutboxDeps` vs `WithOutboxWriter` vs
  `WithDirectPublisher` 的适用场景）。

- **兄弟篇** `docs/guides/cell-interface-isp.md` — 讲解 `cell.Cell` 接口 ISP
  4 子接口拆分（`CellIdentity` / `CellLifecycle` / `CellStatus` / `CellInventory`）
  以及四段式 compile-time check 写法，与本篇 sealed marker 防线并列构成
  PR #441 的两条主线改动。
