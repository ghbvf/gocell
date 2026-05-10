# ADR: Cell Raw-Infra Sealed Marker（升级 CELL-RAW-DEPS-01 为 AI-HARD type-system 强制）

> Status: Accepted
> Date: 2026-05-10
> Amends: docs/architecture/202605101800-adr-cell-interface-isp-split.md §D6
> Implementation: PR #441（review round）

## Context

ADR `202605101800 §D6` 在 PR-A22 (PR-V1-CELL-IFACE-ISP-SPLIT) 引入 `CELL-RAW-DEPS-01` archtest，扫 `cells/<x>/cell.go` + `examples/<demo>/cells/<x>/cell.go` 公开 With* Option 不得暴露 raw infra 类型 (`persistence.TxRunner` / `outbox.{Publisher,Writer}`)，三元组 (file glob, funcName, canonicalType) allowlist + SHA-256 hash guard。

PR 441 review 暴露这条 archtest 仍可被多类形态绕过：

1. **Type alias bypass** — Go 1.23+ 默认 materialize `go/types.Alias`；`type Tx = persistence.TxRunner` 在本地 alias 名下暴露 forbidden type，`canonicalTypeName` 仅 `*types.Named` 断言会落到 alias 名而非 canonical name
2. **Scan range bypass** — `isAnyCellGoFile` 限定 `parts[-1] == "cell.go"`，跳过同包 `options.go` / `helpers.go` 等 sibling 文件中的公开 With* Option
3. **Interface embedding / wrapper struct / variadic / functional closure 等其他形态** — `canonicalTypeName` 同样无法识别

按 `.claude/rules/gocell/ai-collab.md` 严格定义，`CELL-RAW-DEPS-01` 是 Medium（archtest type-aware；违反在 build time 被检测但完全可表达），并非该文件 godoc 自称的 "Hard"。激进自审"违反不可表达"原则要求改用 type system 做 Hard 强制。

## Decisions

### D1. 引入 sealed marker interface 在 type system 强制 raw infra 不可入 cell.go With* Option

`kernel/persistence` 与 `kernel/outbox` 各暴露 sealed wrapper interface：

- `persistence.CellTxManager interface { TxRunner; sealedCellTxManager() }`
- `outbox.CellPublisher interface { Publisher; sealedCellPublisher() }`
- `outbox.CellWriter interface { Writer; sealedCellWriter() }`

`sealedXxx()` 是 unexported method，使外部包无法实现 sealed interface；唯一实现是 kernel 包内 `internalCellTxManager` 等结构体，通过 `WrapForCell` / `WrapPublisherForCell` / `WrapWriterForCell` 工厂构造。

cells/<x>/cell.go 公开 With* Option 接收 sealed type：

```go
// cells/accesscore/cell.go
func WithTxManager(tx persistence.CellTxManager) Option { ... }
func WithOutboxDeps(pub outbox.CellPublisher, w outbox.CellWriter) Option { ... }
```

AI 写 `WithFoo(tx persistence.TxRunner) Option` 在 cell.go 编译期被拒：composition root 调用 `accesscore.WithFoo(rawTxRunner)` 时 type 系统拒绝 `TxRunner → CellTxManager` 直接赋值（缺 sealed marker method），强迫调用方走 `WrapForCell`，而 wrap call site 又被 archtest 限定到 composition root（详见 D2）。整个链条上 cells/* 不可能"持有 raw TxRunner 然后调 service.NewXxx"——cells/* 只持有 sealed CellTxManager（embed TxRunner，可直接传给 service）。

**违反不可表达 → AI-HARD 达成。**

CellTxManager embed TxRunner、CellPublisher embed Publisher、CellWriter embed Writer，让 sealed wrapper 同时满足 raw 接口，cells 内部把 sealed 字段直接传给 service.NewXxx（service 接收 raw 类型，是 cell 内部、不在 sealed 约束面），service 签名零变化。

### D2. Wrap*ForCell 调用站点由 wrapper-location archtest 限定（belt-and-suspenders）

新建 `tools/archtest/wrapper_location_test.go` (`CELL-RAW-INFRA-WRAPPER-LOCATION-01`) 守三个 wrap 函数 `persistence.WrapForCell` / `outbox.WrapPublisherForCell` / `outbox.WrapWriterForCell` 的调用方所在文件路径，仅允许：

- `cmd/*` 任意文件（composition root）
- `examples/<demo>/main.go` / `examples/<demo>/app.go`（example composition root）
- `*_test.go` 任意路径（测试构造 fake）
- `kernel/persistence/cell_marker.go` / `kernel/outbox/cell_marker.go`（marker 定义本身）
- `kernel/cell/demo_tx_runner.go`（`DemoCellTxManager()` 工厂；cells/* demo fallback 收敛到此处，cells/* 内部不持 wrap call）

**AI-rebust 评级**：Medium（archtest type-aware，via `typeseval.SharedResolver` + `go/types` Uses 解析 caller package）。这是 sealed marker (Hard 主防线) 之外的独立语义守卫——防止 cells/* 偷偷 import kernel/persistence 然后自己 wrap raw infra（cells/ 不依赖 adapters/ 的现有分层 archtest 已切断大部分获取 raw 类型的路径，本规则补足"即使 cells 拿到 raw type 也不能 wrap"）。

scanner 检测能力由 `tools/archtest/internal/wrapfixture/violation/violation.go`（build tag `archtest_fixture`，不污染 `./...` 真实 repo 扫描）的 negative fixture 验证：fixture 故意从非 allowlist 路径调 `persistence.WrapForCell`，测试断言 scanner 报告 ≥1 violation。Per ai-collab.md §"real source AST capture (AI 难造假)"，fixture 是真实 Go 包载入（非手 craft AST）。

### D3. internalCellXxx 透传 Nooper 接口

`cell.CheckNotNoop`（`kernel/cell/durability.go`）与 `kernel/cell/mode_resolver.go::isNooperDep` / `kernel/outbox/emitter.go::ReportDurable` 都通过 `dep.(Nooper)` 类型断言识别 demo/noop 实现（`cell.DemoTxRunner` / `outbox.NoopWriter` / `outbox.DiscardPublisher` 等）。

sealed wrapper 默认会隐藏 inner type 的 `Noop()` 方法（embed 的是 interface 字段而非 struct，方法不被 promote）。各 internalCellXxx 显式定义 `Noop() bool` 透传到 inner Nooper：

```go
// kernel/persistence/cell_marker.go
func (i internalCellTxManager) Noop() bool {
    type nooper interface{ Noop() bool }
    if n, ok := i.TxRunner.(nooper); ok { return n.Noop() }
    return false
}
```

通过 anonymous local interface 复用 Go 结构化接口语义，无需 import `kernel/cell.Nooper`（避免 cell ↔ persistence/outbox 反向依赖）。

### D4. 删除 PR-A22 引入的 CELL-RAW-DEPS-01 archtest scanner（原 §D6 落地实体）

`tools/archtest/cell_raw_deps_test.go` (~470 行) + `tools/archtest/internal/rawdepfixture/cell.go` 整体删除：

- scanner 的角色（"cells/<x>/cell.go With* Option 不得暴露 raw infra 类型"）已被 sealed marker 在 type system 完整覆盖
- 保留 scanner 等于把 Hard 已拦的违反再 Medium 检测一遍，是 dead weight 而非"双重防线"——双重防线适用于 PII / 安全等"漏一次代价极大"的语义；此处违反在 compile 期就出错，不需要 build-time scanner 兜底
- ai-collab.md §Soft → Hard 改造方向"hand-crafted fixture → real source AST capture (AI 难造假)" 已在 wrapper_location_test.go 落实

### D5. cell-internal demo fallback 收敛到 kernel/cell.DemoCellTxManager()

cells/<x>/cell_init.go 原本 `c.txRunner = cell.DemoTxRunner{}` 在 sealed type 下会编译失败（`DemoTxRunner` 不实现 `CellTxManager`）。两个候选：

A. cells/* 内部调 `persistence.WrapForCell(cell.DemoTxRunner{})`——但 cells/* 不在 wrapper-location archtest allowlist 内，违反 D2
B. **kernel/cell** 暴露 `DemoCellTxManager() persistence.CellTxManager` 工厂（内部 wrap），cells/* 调 `cell.DemoCellTxManager()`——wrap call 收敛到 kernel/cell

选 B：wrap call site 列表多 1 个文件（kernel/cell/demo_tx_runner.go），cells/* 完全不持 wrap call，archtest allowlist 简洁清晰，cells/* 与 raw infra 完全解耦。

### D6. composition root 6 处 + 11 处测试一次性迁移到 Wrap*ForCell

旧 `cmd/corebundle/{access,audit}_module.go` + `cmd/corebundle/bundle.go` + `examples/{iotdevice/main.go, todoorder/main.go, ssobff/app.go}` 共 6 处直接传 raw infra 给 cell With*；旧 cells 与 examples 测试 11 处用 fake publisher/writer/txRunner 直接传 cell With*。Wave 3 GREEN 一次性 wrap 所有 call site，无 deprecation 别名、无 compat shim、不留双路径——符合"不向后兼容、优雅简洁"原则。

`outbox.WrapPublisherForCell(nil)` / `WrapWriterForCell(nil)` / `WrapForCell(nil)` 返 nil interface，保留 `WithOutboxDeps` 等 builder option 的 typed-nil 累加语义（`if pub != nil { c.pendingOutboxPub = pub }` 在 wrap 后仍正确识别 nil 不覆盖）。

## Consequences

正面：
- AI 提交 cell.go 公开 With* Option 接收 raw infra 类型在 compile 期被拒，违反"不可表达"
- archtest 文件减少 ~470 行（cell_raw_deps_test.go 整体删），新增 ~150 行 (cell_marker.go × 2 + wrapper_location_test.go)，净减少
- type alias / interface embedding / wrapper struct / variadic / functional closure 等所有 bypass 形态在 type system 层一次性根除
- ai-collab.md §载体决策原则首选项（type system）落地范本，可作 Hard 转化模板

负面 / 取舍：
- composition root 与测试每处 raw infra 注入多写一行 wrap（`outbox.WrapPublisherForCell(eb)` 比 `eb` 多 33 字符）。语义清晰度（"我在跨边界注入"）大于字面冗长
- internalCellXxx 的 Noop() 透传是隐式契约（inner Nooper interface 通过 anonymous local interface 反射断言），不是显式接口实现。文件级 godoc 已说明意图
- wrapper-location archtest 是 Medium 而非 Hard——若未来出现 cells 内部强需要 wrap 的场景（极不可能，因为 cells 拿不到 raw infra），需要扩 allowlist 而非更深层 type system 护栏

## ref

- `kernel/persistence/cell_marker.go` / `kernel/outbox/cell_marker.go` — sealed marker 实现
- `kernel/cell/demo_tx_runner.go` — `DemoCellTxManager()` factory
- `tools/archtest/wrapper_location_test.go` — `CELL-RAW-INFRA-WRAPPER-LOCATION-01`
- `tools/archtest/internal/wrapfixture/violation/violation.go` — negative fixture
- `.claude/rules/gocell/ai-collab.md` §AI-rebust 三档分级 / §载体决策原则 / §Soft → Hard 改造方向
- 业界 ref: Go std `database/sql.Scanner` interface (sealed-by-method 范式) / Go std `internal` package + sealed interface 复合（`net/http.RoundTripper` 风格）
