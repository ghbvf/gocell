# PR #441 — Doc-Engineer 维度审查

## 总体结论

文档工作量大、质量整体可接受，ADR 三篇均有 Context / Decisions / Consequences 完整结构，godoc 覆盖充分，backlog 同步认真。主要缺陷集中在四处：(1) `cell_iface_isp_test.go` 含 3 条 INVARIANT 但文件名未使用 `{theme}_invariants_test.go` 形式，违反 ai-collab.md 命名规则；(2) CHANGELOG +20 完整覆盖了 sealed marker API 变更，但缺少 Cell ISP 接口拆分（4 个新导出子接口）这一 kernel 层新 API 的条目；(3) 两个 `cell_marker.go` 文件的 godoc 引用 ADR 路径使用了尖括号占位符 `<adr-cell-raw-infra-sealed-marker>`，未替换为实际文件名；(4) CHANGELOG 提及「Sweeper 不透明接口 Hard 升级路径 tracked as backlog」但无对应 backlog 条目，是 ai-collab.md 要求的显式登记缺口。三篇 ADR 均缺少独立的 Alternatives Considered 章节，替代方案分析散落在 Decision 段落内，可接受但不符合标准 ADR 格式。

---

## Finding 列表

### F1 [Cx1] `cell_iface_isp_test.go` 含 3 条 INVARIANT 但文件名未使用 invariants 形式

**位置**：`tools/archtest/cell_iface_isp_test.go`

**问题**：ai-collab.md 规定「同主题规则 ≥ 3 → `{theme}_invariants_test.go` 主题文件；已有单文件升到第 3 条时，重命名为 `{theme}_invariants_test.go`」。该文件在 PR #441 建立时即含 3 条 INVARIANT：

```
//   - INVARIANT: CELL-IFACE-ISP-COMPOSITE-01
//   - INVARIANT: CELL-IFACE-ISP-METHODSETS-01
//   - INVARIANT: CELL-IFACE-ISP-BASECELL-CHECK-01
```

按命名规则，应命名为 `cell_iface_isp_invariants_test.go`，当前文件名 `cell_iface_isp_test.go` 仅适用于单条独立规则。

**证据**：`tools/archtest/cell_iface_isp_test.go` 文件头 CommentGroup 使用列表续行格式（`//   - INVARIANT: <ID>`），明确表示多规则；ai-collab.md §archtest 文件命名原文已引用上方。

**建议**：将 `tools/archtest/cell_iface_isp_test.go` 重命名为 `tools/archtest/cell_iface_isp_invariants_test.go`。无逻辑变更，仅文件名修正。

**Backlog 登记建议**：`ARCHTEST-NAMING-ISP-RENAME-01` — 重命名 `cell_iface_isp_test.go` → `cell_iface_isp_invariants_test.go`，P2/Cx1，来源：PR #441 doc-engineer review F1。

---

### F2 [Cx2] CHANGELOG 缺少 Cell ISP 接口拆分（4 新导出子接口）条目

**位置**：`CHANGELOG.md`（PR #441 新增的 +20 行）

**问题**：PR #441 最核心的变更是 `kernel/cell/interfaces.go` 新增 4 个导出子接口 `CellIdentity` / `CellLifecycle` / `CellStatus` / `CellInventory`，并将 `Cell` 重定义为这 4 个接口的复合。这是 kernel 层新增的公开 API 面（4 个新的 exported interface type），且对编写测试桩的调用方有影响（测试 mock 现在可以只实现单个子接口而非全量 `Cell`）。

CHANGELOG +20 行完整描述了 sealed marker API 变更（Breaking Changes）和 Sweeper 构造 API 变更（Breaking Changes），但没有任何条目提及：
- 4 个新导出子接口的存在
- `Cell` 现在是复合接口，调用方可以按需声明 `CellIdentity` / `CellLifecycle` 等更窄的依赖
- `BaseCell` compile-time check 升级为四段式

这不是 Breaking Change（`*BaseCell` 仍满足 `Cell` + 4 子接口，调用方代码零变更），但属于新增 API（Added 类别），应当记录，尤其是对于后续 AI 实施者理解最小依赖原则至关重要。

**证据**：`CHANGELOG.md` 全文中 `grep "CellIdentity\|CellLifecycle\|CellStatus\|CellInventory\|ISP\|4 sub"` 输出为空；ADR 202605101800 §D1 明确将子接口 godoc 的 `Consumers:` 段作为引导 AI 实施「按消费者群声明最小子接口依赖」的文档机制，但 CHANGELOG 中无对应条目供 AI 实施者发现新 API。

**建议**：在 CHANGELOG `### Added` 段补充一个条目，说明：`kernel/cell` 新增 `CellIdentity` / `CellLifecycle` / `CellStatus` / `CellInventory` 四个子接口；`Cell` 现为复合接口；调用方测试桩和注入点可按需声明更窄子接口依赖。引用 ADR `docs/architecture/202605101800-adr-cell-interface-isp-split.md` §D1/D2。

**Backlog 登记建议**：`CHANGELOG-ISP-IFACE-MISSING-01` — 补录 Cell ISP 子接口新增条目，P2/Cx1，来源：PR #441 doc-engineer review F2。

---

### F3 [Cx1] `cell_marker.go` godoc 中 ADR 路径使用尖括号占位符

**位置**：
- `/Users/shengming/Documents/code/gocell/kernel/outbox/cell_marker.go` 第 22 行
- `/Users/shengming/Documents/code/gocell/kernel/persistence/cell_marker.go` 第 26 行

**问题**：两个文件的 godoc 均写：

```go
// ref: docs/architecture/<adr-cell-raw-infra-sealed-marker>.md §D1
```

实际文件名为 `202605101900-adr-cell-raw-infra-sealed-marker.md`，尖括号 `<...>` 是占位符未替换。读者（含 AI 实施者）无法直接从 godoc 定位到 ADR 文件，需要额外搜索。对比 `kernel/cell/interfaces.go` 的同类 godoc 写法（`ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D1`），使用了完整路径。

**证据**：
```
grep "adr-cell-raw-infra" kernel/outbox/cell_marker.go
# → // ref: docs/architecture/<adr-cell-raw-infra-sealed-marker>.md §D1
grep "adr-cell-interface" kernel/cell/interfaces.go
# → // ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D1
```

**建议**：将两处 `<adr-cell-raw-infra-sealed-marker>` 替换为完整文件名 `202605101900-adr-cell-raw-infra-sealed-marker`，与 `interfaces.go` 的引用格式保持一致。

**Backlog 登记建议**：无需单独登记，属于下次触及该文件时顺带修复的 Cx1 质量改进。

---

### F4 [Cx2] CHANGELOG 提及「Sweeper opaque-interface Hard 升级 tracked as backlog」但无对应 backlog 条目

**位置**：`CHANGELOG.md` Sweeper 构造 API 条目末段

**问题**：CHANGELOG 写道：

> Hard upgrade path is opaque-interface return from NewSweeper, tracked as backlog.

但 `docs/backlog.md` 中不存在描述「NewSweeper 返回不透明接口（opaque interface）以消除零值攻击面」的专项条目。现有的 `SWEEPER-OBSERVABLE-01` 条目提及了 nil-receiver guard 等剩余项，但其描述范围与「opaque-interface Hard 升级路径」不对应。

按 ai-collab.md 要求：「暂不做 / 等触发条件 / archtest allowlist」必须同步登记 backlog 条目，不能 silent carryover。CHANGELOG 的 "tracked as backlog" 用语明示有登记意图，但实际条目缺失。

**证据**：
```
grep -n "opaque\|OPAQUE" docs/backlog.md   # → 无输出
grep -n "SWEEPER-OBSERVABLE-01" docs/backlog.md  # → 存在，描述 nil-receiver guard 等，未提 opaque-interface
```

**建议**：在 `docs/backlog.md` 补充独立条目，描述「`NewSweeper` Hard 升级路径：改为返回不透明接口 `type sweeperHandle interface{ Start/Stop }` 消除 `*Sweeper` 零值可表达性；当前 `built` sentinel 是 Medium runtime fail-closed」，触发条件：出现通过零值 `*Sweeper` 绕过 guard 的实测事故，或 governance review 升级要求。也可在现有 `SWEEPER-OBSERVABLE-01` 条目中追加描述，但需让「opaque-interface Hard 升级路径」明显可搜索。

**Backlog 登记建议**：`SWEEPER-OPAQUE-IFACE-HARD-01` — NewSweeper 返回不透明接口消除零值攻击面，P3/Cx2，来源：CHANGELOG PR #441 + doc-engineer review F4。

---

### F5 [Cx2] ADR 202605101800 §D6 allowlist 表使用 raw 类型，被修改注解覆盖但仍可误导

**位置**：`docs/architecture/202605101800-adr-cell-interface-isp-split.md` §D6（第 94–112 行）

**问题**：§D6 保留了 W1/W2 阶段的 CELL-RAW-DEPS-01 archtest 三元组 allowlist 表，其中 `CanonicalType` 列使用 raw 类型（`kernel/persistence.TxRunner` / `kernel/outbox.Writer` / `kernel/outbox.Publisher`）：

```markdown
| cells/*/cell.go | WithTxManager | kernel/persistence.TxRunner | ...
| cells/*/cell.go | WithOutboxDeps | kernel/outbox.Publisher | ...
```

PR #441 review 阶段，§D6 被修改注解（Amend）覆盖并声明「实施细节以新 ADR 为准」，但旧表仍原样保留在文档中。实际代码中，cells/* 现在接受的是 sealed 类型（`persistence.CellTxManager` / `outbox.CellPublisher` / `outbox.CellWriter`），与表格中的类型完全不同。

新人或 AI 实施者读到 §D6 allowlist 表（在 amend 注解之前），可能认为 `WithTxManager(persistence.TxRunner)` 是当前合规的 platform cell 函数签名，而实际上这会导致编译失败。

**证据**：ADR §D6 allowlist 表第 3 列（CanonicalType）全部为 raw 类型；`cells/accesscore/cell.go` 实际代码为 `WithTxManager(tx persistence.CellTxManager)`；ADR amend 注记在表格之前，但表格体仍呈现旧内容。

**建议**：在 §D6 allowlist 表上方增加更醒目的遮盖标记（如在表格标题行改为 ~~删除线~~ 或将整段表格内容缩进并注明「此表已作废，实际类型见 ADR 202605101900 §D1」）。或者直接将表格中的 raw 类型列更新为 sealed 类型，保留 allowlist 形态仅作历史参考（但函数→类型映射本身仍有参考价值）。

**Backlog 登记建议**：`ADR-202605101800-D6-TABLE-STALE-01` — 更新 §D6 allowlist 表的 CanonicalType 列为实际 sealed 类型，或增加醒目废弃标注；P2/Cx1，来源：PR #441 doc-engineer review F5。

---

### F6 [Cx1] 三篇 ADR 均缺少独立的「Alternatives Considered」章节

**位置**：
- `docs/architecture/202605101800-adr-cell-interface-isp-split.md`
- `docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md`
- `docs/architecture/202605102000-adr-lifecycle-hook-ctx-semantics.md`

**问题**：标准 ADR 格式包含 Context / Decision / Consequences / Alternatives Considered 四节。三篇 ADR 均有 Context + Decisions + Consequences，但没有独立的「Alternatives Considered」章节。替代方案分析分散在各 Decision 内部正文中：

- ADR 202605101800 §D4 在 Slice 接口决策中隐含了「不拆」的理由
- ADR 202605101900 §D2 中解释了为什么不能只用 type system 而必须加 archtest
- ADR 202605102000 §D1 中对 controller-runtime 主流贯穿模式有比较

这些内容是有效的替代方案分析，但散落在决策段中，不便于后续审查者快速定位「曾经考虑过什么、为何放弃」。

对比 ADR 202605051300 (`adr-kernel-cellmeta-single-source.md`) 同样没有独立 Alternatives 节；这是项目 ADR 的普遍模式，而非本 PR 特有问题。

**建议**：建议在下次新建 ADR 时，将替代方案段从 Decision 正文中提炼为独立的 `## Alternatives Considered` 小节，哪怕是一段短文字也比嵌入 Decision 更易查找。对本 PR 三篇 ADR 不强制要求回补（一致性修改应整个 docs/architecture/ 统一推进）。

**Backlog 登记建议**：`ADR-FORMAT-ALTERNATIVES-SECTION-01` — 推动 docs/architecture/ 下 ADR 增加独立 Alternatives Considered 小节，或在 ADR 模板中明确该节为可选但推荐；P3/Cx1，来源：PR #441 doc-engineer review F6。

---

### F7 [Cx1] 未跟踪的新 roadmap `202605101839-029-master-roadmap.md` 中 #13 状态为「待办」

**位置**：`docs/plans/202605101839-029-master-roadmap.md`（工作区 untracked 文件）

**问题**：该文件是工作区未提交的新 roadmap 草稿，其中 #13 PR-V1-CELL-IFACE-ISP-SPLIT 仍显示「待办」状态，而已归档的 `docs/plans/202605011500-029-master-roadmap.md`（PR #441 中已更新）的 #13 已标记为 ✅ done。此问题与 PR #442 doc-engineer review F4 的模式完全相同（K#09 状态不一致），说明新 roadmap 草稿是以旧基线生成的，合并后未同步已完成项状态。

**证据**：`git status` 显示 `?? docs/plans/202605101839-029-master-roadmap.md`；该文件第 41 行 #13 列「待办」；归档版 `docs/plans/archive/202605011500-029-master-roadmap.md` 第 41 行 #13 已为 ✅ done（含 PR #441 commit refs）。

**建议**：提交该文件前，将 #13 状态从「待办」更新为 ✅ done，并引用 PR #441 合入记录；同时核查是否还有其他在 HEAD develop 中已完成但在新 roadmap 中仍显示「待办」的条目（如 K#09 同样需要对齐）。

**Backlog 登记建议**：无需单独登记，属于新 roadmap 归档操作的必要校对步骤，与 PR #442 review F4 同性质。

---

### F8 [Cx1] ADR 202605102000 缺少明确的「Consequences — 负向」对 bootstrap LIFO 完整性作为隐式前提的文档化

**位置**：`docs/architecture/202605102000-adr-lifecycle-hook-ctx-semantics.md` Consequences 段

**问题**：ADR §D1 决议「维持 `context.WithCancel(context.Background())` 派生 worker ctx」的核心前提是「bootstrap LIFO rollback 完整性是强契约」——即 bootstrap 保证对已 Start 的 cell 反向遍历调用 OnStop。ADR Consequences 段「负向 / 取舍」已写：「bootstrap LIFO rollback 完整性是隐式契约（runtime/bootstrap 单独保证），lifecycle hook 协议层不可见」。

但 ADR 没有说明：如果未来 `runtime/bootstrap` 的 LIFO 反向遍历逻辑被修改（例如为支持并行 Stop 而跳过某些 cell），`lifecycle_rollback_test.go` 将是唯一检测手段——而该测试属于 `runtime/command` 包，不属于 `runtime/bootstrap` 包，测试覆盖的是「给定 Start 后紧接 Stop 能干净退出」而非「bootstrap 一定调用 Stop」。

这是文档层面的完整性缺口：ADR 做了关键的「隐式依赖」声明，但没有指出这个依赖是否有机制保护，以及谁负责维护 bootstrap LIFO 这个强契约。

**建议**：在 ADR Consequences「负向 / 取舍」段补充一句：「`runtime/bootstrap` 的 LIFO 完整性由 `runtime/bootstrap` 包内的 phase 编排逻辑保证；如未来改变 Stop 调用策略，需同步审查本 ADR D1 依赖前提是否仍成立」。两行文字，不影响决策。

**Backlog 登记建议**：无需单独登记，属于 ADR 质量的 Cx1 改进，下次触及该文件时顺带修复。

---

## 跨文档一致性核查

| 检查项 | 结论 |
|---|---|
| ADR 202605101800 §D1 四子接口命名 vs `interfaces.go` 实际代码 | 一致：CellIdentity / CellLifecycle / CellStatus / CellInventory ✓ |
| ADR 202605101800 §D2 Cell 复合形 vs `interfaces.go` `type Cell interface` | 一致：`interface { CellIdentity; CellLifecycle; CellStatus; CellInventory }` ✓ |
| ADR 202605101900 D1 sealed marker 三种类型 vs `cell_marker.go` 实际类型 | 一致：`CellTxManager` / `CellPublisher` / `CellWriter` ✓ |
| `cell-patterns.md` 描述的 With* Option 按 cell 分化 vs 实际代码 | 一致：platform `WithOutboxDeps(CellPublisher, CellWriter)` + ordercell `WithOutboxWriter(CellWriter)` + devicecell `WithDirectPublisher(CellPublisher)` ✓ |
| `pg-cell-template.md` 模板代码中 WrapPublisherForCell / WrapForCell 调用 vs 实际 API | 一致 ✓ |
| `examples/todoorder/README.md` 对 WrapWriterForCell / WrapForCell 的使用说明 vs 实际 API | 一致 ✓，且明确说明了 composition root 是唯一合法调用点，引用了 ADR |
| ADR 202605101900 §D4 删除的 `CELL-RAW-DEPS-01` archtest vs 当前 `tools/archtest/` 目录 | 一致：`cell_raw_deps_test.go` 已不存在，由 `cell_public_option_param_test.go` + `wrapper_location_test.go` 替代 ✓ |
| ADR 202605102000 引用的 `lifecycle_rollback_test.go` | 文件实际存在于 `runtime/command/` ✓ |
| `interfaces.go` 每个子接口 godoc 含 `Consumers:` 段（ADR §D1 Soft 引导要求） | 全覆盖 ✓ |
| sealed marker godoc 含「AI-HARD per ai-collab.md」注解（对应 ADR §D1） | 覆盖 ✓，但 ADR 引用路径使用占位符（见 F3） |
| backlog.md `LIFECYCLE-OWNER-CTX-PROPAGATION-01` 是否登记（ADR 202605102000 §D3 要求） | 已登记 ✓ |
| backlog.md `SLICE-ISP-DEFERRED` 是否登记（ADR 202605101800 §D4 要求） | 已登记 ✓ |
| CHANGELOG.md「Sweeper opaque-interface Hard 升级 tracked as backlog」对应 backlog 条目 | 缺失 ✗（见 F4） |
