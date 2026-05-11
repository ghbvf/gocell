# PR #441 — Kernel-Guardian 维度审查

## 总体结论

**通过（绿）**。PR #441（commit `8c3791aa`）从 Kernel-Guardian 视角看是 GoCell 治理体系本年最干净的一次"形态升级"：

- **kernel 分层隔离严格保持**：新增的 `kernel/persistence/cell_marker.go` + `kernel/outbox/cell_marker.go` 仅依赖 `pkg/validation`（标准库 + pkg/，符合"kernel 只依赖 stdlib + pkg/ + gopkg.in/yaml.v3" 红线）；sealed marker 是 type system 工艺，**不构成对 cell 概念的污染**——marker interface 在 kernel 层定义、cells/* 与 composition root 在外部通过 wrap 注入，反向依赖零次。`kernel/cell/demo_tx_runner.go` 引入对 `kernel/persistence` 的依赖是 kernel 内部包间正常引用（同层 `kernel/* → kernel/*`），不破坏向上分层。
- **interface 稳定性**：4 子接口拆分（CellIdentity / CellLifecycle / CellStatus / CellInventory）按消费者群正交切分，复合 `Cell` 保留 io.ReadWriter 范式，调用方零代码变更。BaseCell 四段式 compile-time check 是真"违反不可表达"——缺方法编译失败精确定位到子接口（Hard 档），不是字符串约定。
- **CELLMETA-SINGLE-SOURCE-03 升级完整**：scan target 从顶层 `Cell.Metadata()` 迁移到 `CellInventory.Metadata()`，archtest gate 同步更新（`tools/archtest/cellmeta_single_source_test.go:112-158`），SOURCE-01 forbidden struct 名 `CellMetadata` 与新接口名 `CellInventory` 不冲突，命名层混淆解决。
- **契约完整性**：`kernel/outbox/emitter.go` 的 -18 行删除（`isNilEmitterDependency` file-local helper）+ `kernel/cell/observer.go` 的 -22 行删除（`IsNilHookObserver`）是把 kernel-side typed-nil helper 收敛到 `pkg/validation.IsNilInterface` 单源——按 `runtime-api.md` §"强依赖 wiring option" 的明文约束完成，净简化。
- **Phase 评审角度**：lifecycle_rollback_test.go 的新增与 ADR `202605102000` 严格一致（D2 直接落地）；sweeper 大改（+125/-33）虽然标题未明示，但是 PR 441 review 二轮的延伸修复，且通过 ADR `202605101900` D5 显式衔接到 sealed marker 主线。范围有蔓延但有 trace 可追。

5 条 finding（4 条 Cx1 / 1 条 Cx2），其中只有 F1 涉及"OUTBOX-CELL-01 文档与新分层关系存在轻微滞后"是建议性优化，其余均为后续优化方向、不阻断结论。

## Finding 列表

### F1 [Cx2] OUTBOX-CELL-01 与新 sealed marker 体系存在 scope 描述滞后 + 与 examples 切口割裂

**位置**：`tools/archtest/outbox_invariants_test.go:99-110, 180-200`。

**问题**：PR 二轮 review 已把 OUTBOX-CELL-01 文件级注释更新为分层声明（"Hard sealed marker for fields/wiring + Medium archtest pair for signature forms"），但 `isCellFile` 实现仍为 platform-only（`parts == 3 && parts[2] == "cell.go"`，注释明确"Example cells under examples/ are intentionally excluded"）。同时 `findCellFiles` 通过 `metadata.NewParser` 枚举包括 examples/* 在内的所有 cell，再被 `isCellFile` 过滤掉——一个"先收集后丢弃"的反直觉路径。

ADR `202605101900` §D6 论证"`CELL-RAW-DEPS-01` 整体删除"，但 OUTBOX-CELL-01 这个**仍然平铺扫历史 With\* 名字**的 Medium 守卫并没有显式纳入 ADR 决议体系，它的存在需要回到 `outbox_invariants_test.go` 的注释才能理解，从 ADR 角度看是 invisible carryover。

激进自审：sealed marker 已经把 raw type 通过 `WithPublisher(outbox.Publisher)` 这种签名拦死（type system 拒绝 raw → CellPublisher 赋值），即便有人复活 `WithPublisher` 这个名字、参数也必须是 `outbox.CellPublisher`——OUTBOX-CELL-01 锁的"名字"语义已被 sealed marker"形态"语义覆盖。OUTBOX-CELL-01 的存在价值现在仅是"防止历史 spelling 在新 PR 中复活引发命名混乱"，本质是 Soft 名字 convention（虽然 archtest 实施是 AST 扫描）。

**证据**：
- `tools/archtest/outbox_invariants_test.go:124-130` 测试体只 assert option 名，不检验 param 类型——名字层面唯一价值。
- ADR `202605101900` §D4 写"删除 PR-A22 引入的 CELL-RAW-DEPS-01"但未提 OUTBOX-CELL-01 的去留决议。
- `examples/todoorder/cells/ordercell/cell.go:68` 仍有 `WithOutboxWriter(writer outbox.CellWriter)`，因 platform-only 过滤而豁免——这是 isCellFile 切口割裂带来的不一致。

**建议**：
1. 短期（本 PR 后第 1 个 follow-up window）：在 ADR `202605101900` 加 §D7"OUTBOX-CELL-01 名字守卫的去留决议"，要么显式保留并论证（防止历史 spelling 在平台 cell 复活），要么删除（sealed marker + PARAM-01 已覆盖）。当前 invisible carryover 违反 ai-collab.md "PR scope 切割必须显式登记 backlog"。
2. 中期：评估是否把 OUTBOX-CELL-01 与 CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 合并为单一 type-aware 守卫（统一扫 platform + examples），消除 isCellFile 与 isCellPackageRootFile 两套 scope 函数的双源。

**AI-rebust 评级**：当前 OUTBOX-CELL-01 实现是 Medium（AST 扫描 + 名字字符串），但语义价值已退化为 Soft（名字 convention）。

**Backlog 登记建议**：新增 `OUTBOX-CELL-01-VS-SEALED-MARKER-RECONCILE`（P3/Cx2 黄）。触发条件 = 本 PR 后第 1 个 follow-up window（不要 deferred）。

### F2 [Cx2] CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 sibling 文件 scope gap 已识别但状态绿

**位置**：`tools/archtest/cell_public_option_param_test.go:83-99`（`isCellPackageRootFile`）；backlog `PR441-FU-RAW-INFRA-PARAM-SIBLING-EXPAND-01`。

**问题**：archtest 扫范围限于 cell package root（`parts == 3` 或 `parts == 5`），同包 sibling 文件如 `cells/<x>/options.go` 中的公开 With* Option 不被扫到。ADR `202605101900` §Context #2 把这个识别为"Scan range bypass"漏洞，新 archtest 沿用相同路径过滤逻辑未扩展。

**证据**：
- `cells/accesscore/` 当前已有 `cell_init.go` `cell_postgres.go` `cell_providers.go` `refresh_gc.go` `options_test.go` 等 sibling 文件——攻击面已经存在物理位置，只是当前没有被滥用。
- backlog 第 79 行登记为 🟠（条件延后），architect 决策"不立项"理由是"sealed marker Hard 主防线已覆盖核心攻击面"。
- 但 sealed marker Hard 防线锁的是**字段类型 + 赋值**层，不是签名形态；公开 With* 在 sibling 文件接受 raw param 的形态本身仍能编译——只在赋值到 cell sealed 字段那一步被拦。如果 sibling 的 With* 把 raw param 直接传给 service.NewXxx（service 接收 raw 类型），sealed marker 就管不到。

**建议**：把 backlog 条件从 (a)"出现违规实例（即使一次）" 改为主动收敛——`isCellPackageRootFile` 直接扩展为 `cells/<x>/*.go` 排除 `internal/`/`slices/`/`_test.go`/`_gen.go`。当前 sibling 文件全部是 cell-internal helpers，无 With* Option 暴露，扩展是 zero-cost regression-prevention。

**AI-rebust 评级**：当前 archtest 实现 Medium；sibling scope gap 是 Soft（路径过滤约定）。扩展后仍 Medium，但语义覆盖更完整。

**Backlog 登记**：已登记 `PR441-FU-RAW-INFRA-PARAM-SIBLING-EXPAND-01`（P3/Cx2 🟠）。建议触发条件改为"本 PR 后第 2 个 release window 主动收敛"，不要等违规实例出现。

### F3 [Cx1] kernel/command/Sweeper 改造范围超出 PR-A22 标题，AI-rebust 评级矛盾未在 ADR 中固化

**位置**：`kernel/command/sweeper.go` (+125/-33)；`kernel/command/sweeper_factory_test.go` (新 57 行)；`kernel/command/sweeper_test.go` (+127/-50)。

**问题**：PR 441 标题 `refactor(kernel/cell): ISP-split Cell interface + CELL-RAW-DEPS-01 archtest (PR-A22)` 与 sweeper 改造无明显关联。merge commit message 把它归入"reviewer agent F3 (Cx2)"——是二轮 review 的延伸修复，scope 蔓延但有 trace。

更关键的问题：`sweeper.go:92-112` godoc 自我评级"AI-rebust: **Medium (runtime fail-closed sentinel)**"，但 PR description 只列 Hard / Medium / Soft 三档，没有为 Sweeper 这条独立列项。godoc 同时承认"`var s command.Sweeper` / `&command.Sweeper{}` zero-value construction remains expressible"——也就是说现有 Hard 部分（unexported field 阻止字段写入）+ Medium 部分（runtime `built` sentinel）的混合评级没有 ADR 固化。Hard 升级路径（opaque interface return）只在 godoc 一句话提及，没有 backlog 条目。

**证据**：
- `sweeper.go:104-110` "Hard upgrade path (backlog): make Sweeper an opaque interface returned only by NewSweeper"——backlog 条目未登记。
- merge commit message 描述"reviewer agent F3-2 (Cx3 OUT_OF_SCOPE) ... 缺 negative interval 测试"——本应通过 backlog 处理而非范围扩张。

**建议**：
1. 新增 backlog `SWEEPER-OPAQUE-INTERFACE-HARD-UPGRADE-01`（P3/Cx2 黄）：把 `Sweeper` 改造为 unexported struct + `NewSweeper(...) Sweeper`（接口返回值），让 zero-value `command.Sweeper{}` 不可表达。触发条件 = 第二个走 zero-value 字面量的 caller 出现。
2. ADR `202605101900` 或新独立 ADR 显式记录 Sweeper Hard/Medium 混合评级的边界，避免 godoc 自我评级 vs PR description 评级之间的 silent drift。

**AI-rebust 评级**：当前 Medium（runtime fail-closed sentinel）；upgrade path Hard。

**Backlog 登记建议**：新增 `SWEEPER-OPAQUE-INTERFACE-HARD-UPGRADE-01`（P3/Cx2 黄，主动 ship 时机=本 PR 后第 1 个 release window）。

### F4 [Cx1] internalCellXxx Noop() 透传是隐式契约，未由静态 archtest 守卫

**位置**：`kernel/persistence/cell_marker.go:48-54`；`kernel/outbox/cell_marker.go:47-53, 64-70`。

**问题**：sealed wrapper 三处（`internalCellTxManager` / `internalCellPublisher` / `internalCellWriter`）都用相同模式实现 `Noop() bool`：

```go
func (i internalCellTxManager) Noop() bool {
    type nooper interface{ Noop() bool }
    if n, ok := i.TxRunner.(nooper); ok { return n.Noop() }
    return false
}
```

ADR `202605101900` §D3 解释为什么用 anonymous local interface（避免 `kernel/persistence → kernel/cell.Nooper` 反向依赖），但**没有 archtest 锁定这条契约**。如果未来某 sealed marker 重构遗漏了 Noop() 透传方法（例如 PR 自动生成），`cell.CheckNotNoop` / `mode_resolver.isNooperDep` / `outbox.ReportDurable` 三个调用点都会沉默地把 demo 实现视作 durable——L2 atomicity 静默丢失。

**证据**：
- `kernel/cell/demo_tx_runner.go:33-48` `DemoCellTxManager()` factory 隐式依赖 `internalCellTxManager.Noop()` 透传——一旦遗漏，`cell.CheckNotNoop` 在 DurabilityDurable 模式下不会 reject demo runner。
- ADR D3 自己写"通过 anonymous local interface 复用 Go 结构化接口语义"——结构化接口靠"方法名匹配"，没有 compile-time check。

**建议**：
1. 加 `kernel/persistence/cell_marker_test.go` 与 `kernel/outbox/cell_marker_test.go` 中的 table-driven 单测（如尚未覆盖）：构造 `Wrap*ForCell(noopImpl)` → 断言返回值的 `Noop()` 透传 inner noop signal，每个 sealed marker 一条。
2. 加 archtest `SEALED-MARKER-NOOP-TRANSPARENCY-01`（Medium）：扫 `kernel/{persistence,outbox}/cell_marker.go` 中每个 internalCell* 类型必须有 `Noop() bool` 方法；通过 AST 检查 receiver type + method name，确保未来新增 sealed marker 同样具备透传。

**AI-rebust 评级**：当前 Soft（隐式结构化契约 + godoc 解释）。目标 Medium（archtest method-set scan）。

**Backlog 登记建议**：新增 `SEALED-MARKER-NOOP-TRANSPARENCY-01`（P3/Cx1 黄）。

### F5 [Cx1] 复合 Cell 接口与 4 子接口的 method 集 hash guard 仅守 archtest 内部声明，不守 source

**位置**：`tools/archtest/cell_iface_isp_test.go:293-337` `expectedMethodSetsSHA256`。

**问题**：`TestCellIfaceISP00_MethodSetsHashGuard` 把 `(sub-interface → method-set)` 序列化后做 SHA-256 比对，但**比对对象是 archtest 自己的 `expectedSubInterfaceMethods` map**——这是 expected 数据自己 hash 自己，证明 expected 没被悄悄改。

真正"违反不可表达"的 hash guard 应该是把 source（`kernel/cell/interfaces.go` 实际声明的 4 子接口 + 方法集）hash 后比对常量。当前实现只能拦"有人改了 expected 但忘记同步常量"，拦不住"有人同时改了 expected 和常量但破坏了 source"。后者由 `TestCellIfaceISP02_SubInterfaceMethodSets`（断言 source method == expected method）兜底，但失去了 hash guard"修改不可静默"的本意。

**证据**：
- `cell_iface_isp_test.go:307-319` 测试体 `got := computeMethodSetsHash(expectedSubInterfaces, expectedSubInterfaceMethods)`——hash 输入是 expected 数据，不是 source AST。
- 文件级 godoc 自评 "AI-rebust 评级：Medium"——这条是符合实际的（不像 F1 那样自评偏乐观），但 hash guard 单独自评 "Hard (SHA-256 hash guard — silent modification impossible)" 与实际效果存在距离。

**建议**：把 hash 输入改为从 source 解析的真实 method set——由 `loadInterfaceType(t, root, name)` 读 AST，提取每个 sub-interface 的 method names，计算 hash，再与常量比对。这样改 source 必然 hash 漂移。改造工作量小（loadInterfaceType + directMethodNames 两个 helper 已存在），且能让"Hard"声明名实相符。

**AI-rebust 评级**：当前是 expected-self-hash 形式，约等于"防自己手抖"，比 Medium 略弱。目标 Hard（source-hash）。

**Backlog 登记建议**：新增 `CELL-IFACE-ISP-METHODSETS-HASH-SOURCE-DRIVEN-01`（P3/Cx1 绿，触发条件=方法集变更需走 ADR 修订时主动改造）。

## 维度评分

| 维度 | 评分 | 证据 |
|------|------|------|
| 分层隔离 | 绿 | kernel/{persistence,outbox}/cell_marker.go 仅 import pkg/validation；`kernel/cell/demo_tx_runner.go` import kernel/persistence 是同层引用合规；sealed marker 不构成 cell 概念污染（marker interface 在 kernel 定义、cells/* 经 wrap 注入，反向依赖零次）。 |
| Interface 稳定性 | 绿 | 4 子接口按消费者群正交切分（D1）；复合 Cell 保留（D2）；BaseCell 四段式 compile-time check 是真"违反不可表达"（Hard 档，缺方法编译失败精确定位到子接口）。kernel/cell/observer.go 删除仅是 typed-nil helper 收敛（IsNilHookObserver → validation.IsNilInterface），不是 observer pattern 撤回——`HookObserver` / `HookEvent` / `NopHookObserver` 全保留。 |
| 元数据合规 | 绿 | CELLMETA-SINGLE-SOURCE-03 升级到 CellInventory.Metadata() 完整；SOURCE-01 forbidden struct 名 `CellMetadata` 与新接口名 `CellInventory` 不冲突；archtest gate 从顶层 Cell 同步迁移到 CellInventory 子接口。 |
| CELL-RAW-DEPS-01 双重防线 | 黄 | sealed marker (Hard) + PARAM-01 + WRAPPER-LOCATION-01 (Medium) 三层组合在 type/signature 层覆盖完整；但 OUTBOX-CELL-01 名字层守卫的去留未在 ADR 固化（F1）+ sibling 文件 scope gap 仍是 backlog 条件延后（F2）。两点都不致命，但是治理体系的尾巴。 |
| 契约完整性 | 绿 | kernel/outbox/emitter.go -18 删除（isNilEmitterDependency）+ kernel/cell/observer.go -22 删除（IsNilHookObserver）是 typed-nil 单源化收敛，与 runtime-api.md §"强依赖 wiring option" 明文约束一致；ordercell.WithOutboxDeps + devicecell.WithOutboxDeps 改造经 cell.ResolveCellEmitter 与 platform 三 cell 的 emitter 解析路径完全对齐，emitter 生成链路正确。 |
| Phase 评审角度 | 黄 | lifecycle_rollback_test.go 与 ADR 202605102000 严格一致（D2 直接落地）；sweeper 大改虽超出 PR-A22 标题但通过 ADR 202605101900 D5 显式衔接到 sealed marker 主线，scope 蔓延有 trace。但 Sweeper 自评"Medium (runtime fail-closed)" 与 PR description 评级未对齐（F3），是流程治理的小缺口。 |
| Tech Debt 趋势 | 净减少 | 删 kernel/cell/observer.go IsNilHookObserver helper（22 行）+ kernel/outbox/emitter.go isNilEmitterDependency helper（18 行）+ ordercell.resolveOutboxDeps 私有逻辑（~30 行）；新增 sealed marker 实现（cell_marker.go × 2 共 175 行）+ 5 archtest（共 ~700 行）+ 3 ADR。架构杠杆比合理：~70 行旧代码删除换 ~875 行新规则与守卫。新增 5 条 follow-up backlog 条目均明确触发条件，无 silent carryover。 |

字数：约 2400。
