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

AI 写 `WithFoo(tx persistence.TxRunner) Option` 在 cell.go 中**函数声明本身合法编译**——type system 仅在 `WithFoo` 实现把 `tx` 写入 cell 的 sealed 字段（如 `c.txMgr = tx`）时才拒绝赋值（缺 sealed marker method）。声明形态本身的拦截由 D2 archtest `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` 完成（这正是 D2 是**必需**的 Medium 双重防线、不是 belt-and-suspenders 的根因——type system 单独无法穷尽所有"暴露 raw infra"的签名形态）。

D1 的 Hard 部分（type system 编译期不可达）覆盖：cell 字段类型 + composition root `accesscore.WithFoo(rawTxRunner)` 把 raw → CellTxManager 的赋值表达式。整个链条上 cells/* 不可能"持有 raw TxRunner 然后调 service.NewXxx"——cells/* 只持有 sealed CellTxManager（embed TxRunner，可直接传给 service）。wrap call site 进一步被 archtest D2 第二条规则 `CELL-RAW-INFRA-WRAPPER-LOCATION-01` 限定到 composition root。

**Hard（type system 字段+赋值）+ Medium（archtest 签名形态）合并构成完整防线。**

CellTxManager embed TxRunner、CellPublisher embed Publisher、CellWriter embed Writer，让 sealed wrapper 同时满足 raw 接口，cells 内部把 sealed 字段直接传给 service.NewXxx（service 接收 raw 类型，是 cell 内部、不在 sealed 约束面），service 签名零变化。

### D2. archtest 双重防线治公开 API 签名形态（必需的 Medium 守卫，非 belt-and-suspenders）

sealed marker 是字段类型与 raw→sealed 赋值层的 Hard 防线，但 type system 单独**不能根除全部签名形态**——以下两类形态在 type system 下合法编译，绕过 D1 的拦截：

1. **Inline interface embedding**：`func WithBad(p interface{ outbox.Publisher }) Option` 的参数类型在 `go/types` 下是 `*types.Interface`（匿名接口），其 `EmbeddedTypes()` 含 `outbox.Publisher`。`*types.Named`-only 匹配会落空。
2. **Dot-import wrapper call**：`import . "github.com/ghbvf/gocell/kernel/persistence"; WrapForCell(p)` 的 AST 调用形态是 `*ast.Ident`（无 SelectorExpr）。`*ast.SelectorExpr`-only 匹配会落空。

为此 PR 441 落地两条 archtest，构成 sealed marker 之外**必需的双重防线**（不是 dead weight）：

- **`tools/archtest/cell_public_option_param_test.go` (`CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01`)** — 扫 `cells/<x>/*.go` + `examples/<demo>/cells/<x>/*.go` 公开 With\* Option 参数 canonical type 不在 forbidden 集合（`persistence.TxRunner` / `outbox.Publisher` / `outbox.Writer`）。canonical 提取递归处理：pointer unwrap → `types.Unalias` → `*types.Named` 直取，或 `*types.Interface` 走 `NumEmbeddeds()`/`EmbeddedType(i)` 提取每个 embedded canonical（命中 forbidden 优先），覆盖 inline-embed 形态。
- **`tools/archtest/wrapper_location_test.go` (`CELL-RAW-INFRA-WRAPPER-LOCATION-01`)** — 守三个 wrap 函数（`persistence.WrapForCell` / `outbox.WrapPublisherForCell` / `outbox.WrapWriterForCell`）的调用方所在文件路径，仅允许：
  - `cmd/*` 任意文件（composition root）
  - `examples/<demo>/main.go` / `examples/<demo>/app.go`（example composition root）
  - `*_test.go` 任意路径（测试构造 fake）
  - `kernel/persistence/cell_marker.go` / `kernel/outbox/cell_marker.go`（marker 定义本身）
  - `kernel/cell/demo_tx_runner.go`（`DemoCellTxManager()` 工厂；cells/* demo fallback 收敛到此处，cells/* 内部不持 wrap call）
  
  调用形态识别覆盖 `*ast.SelectorExpr`（`pkg.Func()` 形态）+ `*ast.Ident`（dot-import `Func()` 形态），`info.Uses` 解析为相同 `*types.Func` 后做 canonical name 比对。

**AI-rebust 评级**：Medium（archtest type-aware via `typeseval.SharedResolver` + `go/types` Uses 解析）。type system 单独不可达签名形态空间是该问题域的客观特性，不是 archtest 实现不足；因此双重防线是该层级的 Medium 天花板，与 PII redaction / 安全语义双重防线同质（都是 type system 不可表达的横向空间）。

**架构师裁决**：本场景 D2 的 Medium 评级是该问题域的天花板，与 PII redaction 双重防线同质。Hard 化路径需要语言级 sealed-by-position 等特性，超出当前 GoCell 范围。**不进 backlog 升 Hard 跟踪**，后续 reviewer 不再质疑该评级。

scanner 检测能力由 `tools/archtest/internal/{rawparamfixture,wrapfixture/violation}/`（build tag `archtest_fixture`，不污染 `./...` 真实 repo 扫描）的 negative fixture 验证：fixture 故意写出每种攻击形态（raw param + alias bypass + inline-embed + dot-import），测试断言 scanner 报告每条 violation。Per ai-collab.md §"real source AST capture (AI 难造假)"，fixture 是真实 Go 包载入（非手 craft AST）。

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

- scanner 的旧形态（path-glob + funcName + canonicalType 三元组 allowlist + SHA-256 hash guard）治理范围已被 D2 的两条 type-aware archtest（`CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` + `CELL-RAW-INFRA-WRAPPER-LOCATION-01`）替代——后者更彻底：直接 canonical type 比对（含 `types.Unalias` + `*types.Interface` embedded walk）+ `*ast.Ident`/`*ast.SelectorExpr` 双形态识别，无需手维护 hash allowlist
- type-correctness 的 Hard 主防线由 sealed marker 提供（D1）；signature-form 的 Medium 双重防线由 D2 两条 archtest 提供（type system 单独不可达签名形态空间，参 D2）
- ai-collab.md §Soft → Hard 改造方向"hand-crafted fixture → real source AST capture (AI 难造假)" 已在 D2 两条 archtest fixture 中落实

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
- **Hard 主防线（type system）**：AI 提交 cell.go 公开 With\* Option 字段类型为 raw infra（`persistence.TxRunner` / `outbox.{Publisher,Writer}`）在 compile 期被拒；composition root 把 raw infra 直接传给接 sealed type 的 Option 也在 compile 期被拒。type alias 命中 `types.Unalias` 由 archtest 拦（D2），不在 type system 主防线内。
- **Medium 双重防线（archtest type-aware）**：inline interface embedding（`func WithBad(p interface{ outbox.Publisher })`）与 dot-import wrap call（`import . "kernel/persistence"; WrapForCell(p)`）这两类签名形态 type system 单独无法根除，由 D2 两条 archtest（`CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` + `CELL-RAW-INFRA-WRAPPER-LOCATION-01`）补足，构成必需的 Medium 双重防线（不是 dead weight）。
- archtest 文件总行数减少：`cell_raw_deps_test.go` (~470 行) + `rawdepfixture` 删除，新增 sealed marker 实现 (`cell_marker.go` × 2) + 两条新 archtest (~250 行) + 两个 fixture 包 (~100 行)，净减少 ~120 行；语义覆盖反而更宽（type alias / inline-embed / dot-import / wrapper struct 形态）
- ai-collab.md §载体决策原则"funnel + codegen → type system → archtest 平铺"分层落地范本：D1 走 type system Hard，D2 走 archtest Medium，分工清晰

负面 / 取舍：
- composition root 与测试每处 raw infra 注入多写一行 wrap（`outbox.WrapPublisherForCell(eb)` 比 `eb` 多 33 字符）。语义清晰度（"我在跨边界注入"）大于字面冗长
- internalCellXxx 的 Noop() 透传是隐式契约（inner Nooper interface 通过 anonymous local interface 反射断言），不是显式接口实现。文件级 godoc 已说明意图
- D2 archtest 是 Medium 而非 Hard——签名形态空间由 Go AST/types 模型决定，type system 不可达。Hard 化路径需要语言级 sealed-by-position 等特性，超出当前 GoCell 范围

## D7：OUTBOX-CELL-01 archtest 删除决议

**决议**：删除 `tools/archtest/outbox_invariants_test.go` 中的 `TestOUTBOX-CELL-01` 测试函数及其 helpers（`isCellFile` / `findCellFiles`）。

**理由**：OUTBOX-CELL-01 的语义是"禁止 cells/* 暴露 `WithPublisher` / `WithOutboxWriter` raw 名字 Option"，本质是名字 convention（虽实施方式是 AST 扫描）。但 sealed marker（D1）已通过类型层覆盖该语义——即便有人重新引入 `WithPublisher` 名字、参数也必须是 `outbox.CellPublisher`（unexported `sealedCellPublisher()` method 强制），raw `outbox.Publisher` 不可能被赋值到 cell 字段。OUTBOX-CELL-01 与 sealed marker 形成双源治理而无新增覆盖。

**实施**：本 ADR 落地的 PR 441 follow-up 同时删除 OUTBOX-CELL-01 测试与 helpers（commit 见 PR-560）。

**AI-rebust 评级影响**：减法（删除有效 enforcement → 无评级）。语义由 sealed marker Hard（字段层）+ `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` Medium（签名层）双重防线完整覆盖。

## ref

- `kernel/persistence/cell_marker.go` / `kernel/outbox/cell_marker.go` — sealed marker 实现
- `kernel/cell/demo_tx_runner.go` — `DemoCellTxManager()` factory
- `tools/archtest/wrapper_location_test.go` — `CELL-RAW-INFRA-WRAPPER-LOCATION-01`
- `tools/archtest/internal/wrapfixture/violation/violation.go` — negative fixture
- `.claude/rules/gocell/ai-collab.md` §AI-rebust 三档分级 / §载体决策原则 / §Soft → Hard 改造方向
- 业界 ref: Go std `database/sql.Scanner` interface (sealed-by-method 范式) / Go std `internal` package + sealed interface 复合（`net/http.RoundTripper` 风格）
