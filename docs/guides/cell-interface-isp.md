# Cell 接口 ISP 拆分：消费者指南

> 本篇面向 Go 开发者（Cell 实现者 / Framework 组件作者）。  
> 完整设计决策请阅读 ADR `docs/architecture/202605101800-adr-cell-interface-isp-split.md`。

## 背景：4 个子接口

`kernel/cell` 包中的 `Cell` 接口在 PR #441 之后重定义为 4 个子接口的复合体。

```go
// kernel/cell/interfaces.go
type Cell interface {
    CellIdentity
    CellLifecycle
    CellStatus
    CellInventory
}
```

4 个子接口各自封装一类正交职责：

### CellIdentity — 静态身份

```go
type CellIdentity interface {
    ID() string
    Type() cellvocab.CellType
    ConsistencyLevel() cellvocab.Level
}
```

**消费者**：registry lookup（`kernel/cell.Assembly`）、metrics label
（`runtime/http/middleware.Metrics` 的 `cell` label）、log correlation（slog 字段）、
route attribution（`runtime/http/router.Router` 路由归属）。

### CellLifecycle — 生命周期

```go
type CellLifecycle interface {
    Init(ctx context.Context, reg Registrar) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
```

**消费者**：Assembly orchestrator（`kernel/cell.Assembly`）、bootstrap phases、
lifecycle test harnesses。

### CellStatus — 运行时探针

```go
type CellStatus interface {
    Health() HealthStatus
    Ready() bool
}
```

**消费者**：`/healthz` 与 `/readyz` HTTP handler（`runtime/http/health.Handler`）、
runtime supervision（`kernel/cell.Assembly`）。

### CellInventory — 声明态读取

```go
type CellInventory interface {
    Metadata() *metadata.CellMeta
    OwnedSlices() []Slice
    ProducedContracts() []Contract
    ConsumedContracts() []Contract
}
```

**消费者**：contract validators（`kernel/governance`）、metadata inspectors
（`cmd/gocell validate`）、codegen（`tools/codegen/contractgen`）。

这 4 个子接口在 `kernel/cell/interfaces.go` 中有完整的 godoc（含各自的 `Consumers:` 段），
拿不准某个消费场景该用哪个子接口时直接查 godoc。

## 为什么拆？

### 1. ISP（Interface Segregation Principle）

拆分前，`Cell` 是 12 方法的大接口。一个只需要读 `ID()` 的 metrics middleware
在函数签名上却被迫接受完整的 `cell.Cell`，引入了不必要的耦合。

拆分后：

```go
// runtime/http/middleware/metrics.go — 只读 cell ID，用 CellIdentity 即可
func newMetricsCollector(cells []cell.CellIdentity) *Collector { ... }
```

framework 内部组件可以精确声明自己需要的那一部分，不再依赖 12 方法全集。

### 2. 编译错误精度

**拆分前**（单条 `_ Cell` 断言）：漏实现某个方法时，编译器报出全部 12 个缺失方法，
开发者要在一大段错误里找到自己漏掉的那一个。

**拆分后**（四段式断言，见下节）：错误精确定位到子接口。例如漏实现 `Stop` 时：

```
cannot use *MyCell as cell.CellLifecycle: missing method Stop
```

立刻知道要去实现 `CellLifecycle` 接口的 `Stop` 方法，不需要在 12 方法里查找。

### 3. AI-Hard 治理

子接口是 archtest 的稳固锚点。`CELL-IFACE-ISP-COMPOSITE-01` / `METHODSETS-01` /
`BASECELL-CHECK-01` 等 archtest（Medium 评级，type-aware AST 扫描）守卫：

- `Cell` 复合接口必须嵌入且仅嵌入这 4 个子接口
- 每个子接口方法集与 ADR §D1 一致，不能增减
- `BaseCell` 在 `base.go` 必须有四段式 compile-time check

这些规则在 CI 中自动校验，AI 无法静默地把子接口改回单大接口或把方法移来移去。

### 4. 零代码变更（对 cell 实现者）

复合接口形态保留了 `Cell` 这个名字，且它等价于四子接口的全集。
`kernel/assembly` 9 处 `cell.Cell` 引用、`runtime/bootstrap/*_test.go`、
`cells/*/cell_gen.go` 全部无需修改——只要嵌了 `BaseCell`，满足四子接口 ≡ 满足 `Cell`。

## 四段式 compile-time check

cell 实现文件（通常是 `base.go` 或 `cell.go`）应使用四段式断言，而不是单条
`var _ Cell = (*MyCell)(nil)`：

```go
// cells/mycell/cell.go 或 kernel/cell/base.go
var (
    _ cell.CellIdentity  = (*MyCell)(nil)
    _ cell.CellLifecycle = (*MyCell)(nil)
    _ cell.CellStatus    = (*MyCell)(nil)
    _ cell.CellInventory = (*MyCell)(nil)
)
```

**为什么这样写更好：**

单条 `_ cell.Cell` 断言在漏方法时会同时报出所有缺失方法（最多 12 条），
让人不知道从哪里入手。四段式断言将 12 方法按职责分组，每段 3~4 个方法，
编译错误精确到子接口粒度：

```
# 漏实现 Ready() 时
./cell.go:15:6: cannot use *MyCell as cell.CellStatus:
    *MyCell does not implement cell.CellStatus (missing method Ready)
```

开发者一眼就知道要补 `CellStatus.Ready()`。

**kernel/cell/base.go 的实际形态**（由 `BASECELL-CHECK-01` archtest 守卫）：

```go
var (
    _ CellIdentity  = (*BaseCell)(nil)
    _ CellLifecycle = (*BaseCell)(nil)
    _ CellStatus    = (*BaseCell)(nil)
    _ CellInventory = (*BaseCell)(nil)
    _ Slice         = (*BaseSlice)(nil)
    _ Contract      = (*BaseContract)(nil)
)
```

`BaseCell` 实现了全部 12 方法，复合 `Cell` 接口由四子接口同时满足时自动满足，
不需要额外写 `_ Cell = (*BaseCell)(nil)`。

## 我什么时候只声明子接口依赖？

### 99% 的情况：保持嵌 BaseCell，无需单独关注子接口

如果你在开发一个新 Cell，直接嵌 `cell.BaseCell` 即可：

```go
type MyCell struct {
    cell.BaseCell
    // ... 字段
}
```

`BaseCell` 已实现全部 12 方法（`ID()` / `Type()` / `ConsistencyLevel()` /
`Init()` / `Start()` / `Stop()` / `Health()` / `Ready()` /
`Metadata()` / `OwnedSlices()` / `ProducedContracts()` / `ConsumedContracts()`）。
你只需要覆盖业务相关的方法（通常是 `Init`，以及 codegen 生成的 metadata accessor），
不需要关心哪些方法属于哪个子接口。

> 如果你不写 framework 内部组件（如自定义 middleware / metrics collector），跳过下一节即可——直接嵌 `BaseCell` 是 99% 场景的正解。

### 极少数情况：自定义 framework 组件，需按 ISP 声明最小依赖

如果你在实现 framework 内部组件（如自定义 metrics collector、自定义 middleware、
自定义 health aggregator），可以也应该按 ISP 只接受需要的子接口：

```go
// 只读 cell ID 用于 metric label — CellIdentity 已够用
func newMetricsCollector(cells []cell.CellIdentity) *Collector {
    // 不依赖 CellLifecycle / CellStatus / CellInventory
}

// 只驱动生命周期 — CellLifecycle 已够用
func startAll(ctx context.Context, cells []cell.CellLifecycle) error {
    for _, c := range cells {
        if err := c.Start(ctx); err != nil {
            return err
        }
    }
    return nil
}

// 只读健康状态 — CellStatus 已够用
func aggregateHealth(cells []cell.CellStatus) map[string]bool {
    result := make(map[string]bool)
    for _, c := range cells {
        // CellStatus 没有 ID()，这里需要 CellIdentity
        // 两者都需要时用 Cell 或声明为 interface{ CellIdentity; CellStatus }
    }
    return result
}
```

**不要"为了显得 ISP 而拆"**：如果你的函数实际上需要调用多个子接口的方法，
直接用 `cell.Cell`（复合接口）即可，不需要把参数拆成 4 个子接口类型传入。

**子接口组合**：当你需要某两个子接口时，可以用匿名复合或直接用 `Cell`：

```go
// 既需要 ID 又需要 Health 的场景
type healthObserver interface {
    cell.CellIdentity
    cell.CellStatus
}
func watchHealth(cells []healthObserver) { ... }
```

## 零代码变更承诺范围澄清

ADR `202605101800` 中的"零代码变更"有明确边界，避免在两条主线变更之间混淆：

**承诺覆盖范围**：`kernel/cell.Cell` 接口的 **消费方**（`kernel/assembly` /
`runtime/bootstrap` / `cells/*/cell_gen.go` 等）——这些代码继续用 `cell.Cell`
类型，无需改动，因为复合接口的 method set 与拆分前完全相同。

**不在承诺范围内**：**注入侧** raw infra 的变更。PR #441 同步改造了 cell Option
函数的参数类型（raw infra → sealed marker）以及所有 composition root 和测试的
调用点。这部分变更属于 ADR `202605101900`（sealed marker ADR）的范畴，与 ISP
拆分是两条独立主线，但在同一 PR 内落地。

| 变更类型 | 属于哪条主线 | 有无"零变更"承诺 |
|---------|------------|----------------|
| `kernel/assembly` 等消费 `cell.Cell` 的地方 | ISP 拆分 | 有（调用方不需改动） |
| `cells/*/cell.go` 的 `WithTxManager` 参数类型 | sealed marker | 无（主动改造，注入侧破坏性变更） |
| `cmd/*` 的 raw infra → `WrapForCell` 调用 | sealed marker | 无（主动改造，composition root 适配） |

如果你看到 PR #441 中有 `cells/*` 的 `service.go` 参数类型从 `persistence.TxRunner`
改为 `persistence.CellTxManager` 的变更，那是 sealed marker 主线的改动，
不是 ISP 主线"打破了零变更承诺"。

## 进阶阅读

- **ADR** `docs/architecture/202605101800-adr-cell-interface-isp-split.md` —
  完整设计决策，包含 ISP 拆分动机、`BaseCell` 四段式断言、Slice 接口默认不拆
  的理由（§D4），以及 AI-robust 三档分级一览。

- **接口定义** `kernel/cell/interfaces.go` — 4 个子接口的 godoc，每个接口末尾的
  `Consumers:` 段说明了各子接口的实际消费者，是选择最小子接口依赖的权威参考。

- **兄弟篇** `docs/guides/why-sealed-marker.md` — 讲解 sealed marker wrap pattern，
  即 raw infra 为什么不能直接注入 cell、应如何用 `persistence.WrapForCell` /
  `outbox.WrapPublisherForCell` / `outbox.WrapWriterForCell` 在 composition root
  包装，以及 `persistence.CellTxManager` / `outbox.CellPublisher` /
  `outbox.CellWriter` 三种 sealed marker 的使用场景。
