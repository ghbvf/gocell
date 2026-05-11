# PR #441 — Product-Manager 维度审查

> 视角：GoCell 框架消费者（Go 开发者：cell 实现者 / cell 消费者 / framework 集成者）。
> 基线：develop @ 8c3791aa（PR #441 已 MERGED 进 PR #449 之前的 develop）。
> 审查面：消费者 DX / 学习曲线 / 错误信息可读性 / 验收标准对齐 / ADR 价值传达 / 跨示例一致性。

## 总体结论

**有条件通过 / 多处对外口径与实物偏差**。从 develop 实物核验，PR #441 在内部架构语义上是干净的（ISP 子接口拆分 + sealed marker 把 raw infra 在 type system 强制不可入 cells/*），与 ADR 三件套的概念模型一致；但**对外向消费者展示的"零代码变更"叙事不准确**，并且**面向消费者的入口文档（README / docs/guides/cell-development-guide.md / docs/guides/codegen-new-endpoint.md）零更新**——README 仍直接 `var _ cell.Cell = (*MyCell)(nil)` 教 12-方法整体接口，cell-development-guide 仍展示旧 outbox wiring 形态，新消费者按当前 README 走根本接触不到 PR #441 引入的新心智模型。叠加 example cell godoc 内残留旧 ADR 引用（`ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D6` 指向已被 amends 删除的 D6 archtest 实体），按 P1 验收口径（开发者文档闭环 + 对外口径准确）判为**产品 FAIL**。功能本体（type system Hard 防线）实现优雅，但消费者发现 / 学习 / 排错路径 4 处闭环缺失。

---

## Finding 列表

### F1 [Cx2] [开发者体验] PR body "调用方零代码变更" framing 与实物冲突
**位置**：PR #441 description；ADR `docs/architecture/202605101800-adr-cell-interface-isp-split.md:42` "调用方零代码变更" 与 §"范围澄清"。
**问题**：PR 标题 / ADR §D2 同一段同时声明：
- "调用方零代码变更（kernel/assembly 9 处 cell.Cell 引用 / runtime/bootstrap/*_test.go / cells/*/cell_gen.go 全部继续工作）"
- 紧接着 §"范围澄清" 列举两处破坏性变更：`ordercell.WithOutboxDeps(nil, w) → WithOutboxWriter(w)`、`devicecell.WithOutboxDeps(eb, nil) → WithDirectPublisher(eb)`。

ADR 在第 152 行用"仅适用于 kernel/cell.Cell 接口的 consumers"做了内部澄清，但 PR title / body 上层口径未做同样澄清。**对消费者（看 PR title / commit message / changelog 的人）的 framing 是不准确的**：
- platform cells（accesscore / auditcore / configcore）3 处全部从 `WithOutboxDeps(outbox.Publisher, outbox.Writer)` 切到 `WithOutboxDeps(outbox.CellPublisher, outbox.CellWriter)`——签名兼容，但**调用方必须在 composition root 用 `outbox.WrapPublisherForCell(eb)` / `outbox.WrapWriterForCell(nw)` 包装**，否则 compile error。`examples/ssobff/app.go:218,235,250` 三处 cell wiring 全改。
- `examples/iotdevice/main.go:60` 从 `devicecell.WithPublisher(eb)` → `devicecell.WithDirectPublisher(outbox.WrapPublisherForCell(eb))`：option name + 参数类型双重变化。
- `examples/todoorder/main.go:69-70` 从 `ordercell.WithOutboxDeps(...)` → `ordercell.WithOutboxWriter(outbox.WrapWriterForCell(...))` + `ordercell.WithTxManager(persistence.WrapForCell(...))`：option name + 参数类型双重变化。
- 11 处测试 wiring 同步改造（`examples/todoorder/cells/ordercell/cell_test.go` 多处出现 `WrapWriterForCell` / `WrapForCell`）。

ADR §D6 自陈"composition root 6 处 + 11 处测试一次性迁移"，与 "零代码变更" 的对外口径直接矛盾。
**证据**：
- `examples/ssobff/app.go:218` `accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw))`
- `examples/iotdevice/main.go:60` `devicecell.WithDirectPublisher(outbox.WrapPublisherForCell(eb))`
- `examples/todoorder/main.go:69` `ordercell.WithOutboxWriter(outbox.WrapWriterForCell(outbox.NoopWriter{}))`
- ADR `docs/architecture/202605101800-adr-cell-interface-isp-split.md:42` vs `:152-164`

**建议**：PR body / changelog / commit message 改口径为"对 `kernel/cell.Cell` interface 的 consumers 零变更（assembly / bootstrap 不动）；对 cell 注入侧消费者破坏性变更——所有 `cells.With*Deps/Writer/Publisher` 调用必须改用 `outbox.Wrap*ForCell(...)` / `persistence.WrapForCell(...)` 包装。"。同时建议加一节 **Migration Snippet**（before/after 对照），让 framework 集成者一眼能看到"我项目里需要改什么"。

**Backlog 登记**：建议补 `PR441-DOC-CONSUMER-MIGRATION-NOTE-01`（doc 类，P2/Cx1，触发条件即时——changelog/release note 改口径 + 贴 migration snippet）。

---

### F2 [Cx3] [开发者体验] 面向消费者的入口文档零同步
**位置**：`README.md` 全文；`docs/guides/cell-development-guide.md`；`docs/guides/codegen-new-endpoint.md`。
**问题**：PR #441 引入了消费者必须理解的 4 个新概念（`CellIdentity` / `CellLifecycle` / `CellStatus` / `CellInventory` 子接口、`CellTxManager` / `CellPublisher` / `CellWriter` sealed marker、`Wrap*ForCell` 工厂、composition-root-only allowlist），但消费者发现路径全部断裂：
- `Grep "CellIdentity|CellLifecycle|CellStatus|CellInventory|CellTxManager|CellPublisher|CellWriter|WrapForCell|WrapPublisherForCell|WrapWriterForCell|sealed" README.md` → **0 命中**
- `Grep` 同关键词 `docs/guides/cell-development-guide.md` → **0 命中**
- `Grep` 同关键词 `docs/guides/codegen-new-endpoint.md` → **0 命中**

具体偏差：
- `README.md:88` Tutorial 仍写 `var _ cell.Cell = (*MyCell)(nil)` 的 12-方法整体断言形态——而 ADR §D3 要求"四段式独立断言（缺方法精确报哪个子接口）"。新用户照 README 学到的是被 ADR 反对的写法。
- `docs/guides/cell-development-guide.md:88` 同问题：`var _ cell.Cell = (*MyCell)(nil)`。
- 整本 cell-development-guide 没有"raw infra 不能入 cell.go"的描述；新消费者在 `WithMyOutbox(p outbox.Publisher) Option` 这种代码上看到一个不友好的 archtest fail，而 guide 没解释为什么。
- README "Quick Start" + "30-Minute Tutorial" 路径无任何引导消费者去看 ADR 202605101800/202605101900。

文档侧唯一同步的是 `.claude/rules/gocell/cell-patterns.md` 的 §"Sealed Marker Wrap Pattern"（写得相当完整），但 .claude/ 是给 AI agent 看的规则，**不是面向消费者的开发文档**。Go 开发者按 `README.md` → `docs/guides/` 的常规路径走根本接触不到。

**证据**：上述 grep 结果 + README/guides 的现状字面量。
**建议**：
1. README §"30-Minute Tutorial" Step 4 把 `var _ cell.Cell = (*MyCell)(nil)` 改为四段式 `_ cell.CellIdentity / CellLifecycle / CellStatus / CellInventory = (*MyCell)(nil)`，inline 注释引用 ADR 202605101800 §D3。
2. `cell-development-guide.md` 同步改写，并新增 §"Outbox 注入：sealed marker 模式"，给 demo / production 两个最小 cell.go + composition root 对照例（覆盖 `WithOutboxDeps` / `WithTxManager` / `Wrap*ForCell`）。
3. 新增 `docs/guides/cell-interface-isp.md`（短篇，~200 行）解释"为什么拆四子接口"+"我什么时候只声明子接口依赖（metrics middleware / health handler 场景）"——纯消费者视角。
4. `README.md` Tutorial 末尾加一行 "进阶阅读" 链接到 ADR 202605101800/202605101900。

**Backlog 登记**：建议补 `PR441-DOC-INTERFACE-ISP-CONSUMER-DOCS-01`（doc 类，P1/Cx2，触发条件即时——文档闭环是 P1 验收必须项）。

---

### F3 [Cx2] [开发者体验] 错误信息可读性 vs ADR 承诺有 gap
**位置**：`kernel/cell/base.go:24-31` 四段式 compile-time check；`kernel/persistence/cell_marker.go:11-17` sealed marker godoc；`examples/iotdevice/cells/devicecell/cell.go:222-227` runtime guard。
**问题**：
**优点**（值得肯定）：
- BaseCell 四段式 check（`base.go:24-31`）+ inline godoc 例子 `"missing Stop() fails exactly: (*BaseCell) does not implement CellLifecycle (missing method Stop)"` 的 DX 设计是优秀的——消费者忘实现某个方法时编译错误精确指出哪个子接口缺哪个方法，比单 `_ Cell = ...` 的笼统 12-方法报错好很多。
- `devicecell/cell.go:222-227` 给出**含修复建议的运行时错误**：`"devicecell requires publisher; use WithDirectPublisher(outbox.WrapPublisherForCell(&outbox.DiscardPublisher{})) from composition root for demo mode"` —— 这是消费者友好的错误信息典范。
- `kernel/persistence/cell_marker.go:11-17` godoc 解释了"为什么不能写 `WithFoo(tx persistence.TxRunner)`"+ 错误时机 + 修复路径（composition root 必须 WrapForCell），消费者在编辑器 hover 就能看到。

**问题**：
- 消费者首次从 `examples/todoorder/main.go` 复制粘贴写新示例时，把 `outbox.WrapWriterForCell(...)` 漏掉，写 `ordercell.WithOutboxWriter(myWriter)` 直接传 raw `outbox.Writer`。**编译错误是 Go 类型系统原生的**：`"cannot use myWriter (variable of type outbox.Writer) as outbox.CellWriter value in argument to ordercell.WithOutboxWriter: outbox.Writer does not implement outbox.CellWriter (missing method sealedCellWriter)"`。这条错误对人类**不友好**：
  1. `sealedCellWriter` 是 unexported method，消费者无法实现，错误暗示"我应该实现这个方法"是误导。
  2. 没有任何线索说"应该调 `outbox.WrapWriterForCell()`"——消费者要去 grep 整个 codebase 才能找到 wrap 函数。
- ADR `202605101900 §D1` 自陈"AI 写 `WithFoo(tx persistence.TxRunner) Option` 在 cell.go 中**函数声明本身合法编译**——type system 仅在 `WithFoo` 实现把 `tx` 写入 cell 的 sealed 字段时才拒绝赋值"。这意味着：消费者在 cell author 角色（写 cell.go）和 framework integrator 角色（写 main.go）拿到完全不同形态的报错（cell author 看到的是 archtest fail，integrator 看到的是 type error），文档侧没有给出"如果你看到 X 报错，应该怎么修"的对照表。

**证据**：sealed interface 类型 + Go compile error 现状；ADR 自陈分层。
**建议**：
1. `kernel/persistence/cell_marker.go` / `kernel/outbox/cell_marker.go` 在 `sealedCellTxManager()` / `sealedCellPublisher()` / `sealedCellWriter()` 上方加 `// MARKER: do not implement; this method is the sealing marker — call persistence.WrapForCell(...) / outbox.WrapPublisherForCell(...) / outbox.WrapWriterForCell(...) from your composition root instead.` godoc——消费者 hover unimplemented method 名时直接看到 wrap 函数名。
2. `docs/guides/troubleshooting-cell.md`（新建）增加 "Common errors" 章节，列举 6 类错误形态（4 子接口缺方法 + 3 类 sealed marker missing）+ 对应修复 snippet。

**Backlog 登记**：建议补 `PR441-DOC-CELL-ERROR-MAP-01`（doc 类，P2/Cx2，触发条件即时）+ `PR441-CODE-SEALED-MARKER-GODOC-HINT-01`（code 类，P3/Cx1，touch-when-edit）。

---

### F4 [Cx2] [验收标准缺失] 验收标准未对齐"消费者旅程"
**位置**：PR #441 test plan / Wave breakdown。
**问题**：PR test plan（自陈）以 archtest（type-aware sealed marker / wrapper-location）+ unit test（ISP mock）+ runtime test（`TestSweeperLifecycle_StartupFailRollback`）+ integration（rollback）为主——全是**框架内部不变量验证**，无一条覆盖消费者旅程：
- 消费者写一个新 cell（含 outbox wiring）从 0 到通过 `go test`，5 分钟能完成？
- 消费者改造一个旧 cell（旧的 raw outbox.Publisher 形态）到新形态，平均改几行？
- 消费者在 IDE 里写错（漏 wrap）后能从错误信息独立修好？

按产品验收 P1（"核心功能"）口径，"框架消费者能 onboard" 是核心功能；本 PR 改造了"消费者写 cell 的形态"，但验收标准全在 framework 内部不变量层。无 e2e journey test、无消费者旅程的 onboarding test。
**证据**：PR test plan checklist 全部为 archtest + unit + integration，缺消费者旅程层。
**建议**：
1. 新增 `examples/onboarding/cells/<minimal>/` 一个最小 cell（仅 outbox + log），同步在 `examples/onboarding/README.md` 给出 "5-minute tutorial: build a cell with outbox in 30 lines"——consumer-journey 形态的活文档，CI build 守护其可编译可跑。
2. 验收标准分级中显式登记 P1：`AC-PR441-P1-001 — 新建 outbox cell 的最小 cell.go + main.go 总行数 ≤ 50, 且 go build + go test 一次通过（基于 examples/onboarding fixture）`。
3. P2：`AC-PR441-P2-002 — 旧 cell 改造（WithOutboxWriter raw → CellWriter）平均 diff ≤ 3 行/cell（基于 git history 抽样）`。

**Backlog 登记**：建议补 `PR441-AC-CONSUMER-JOURNEY-COVERAGE-01`（test 类，P1/Cx2，触发条件即时）。

---

### F5 [Cx2] [范围偏移] ADR 价值传达：3 份 ADR 全是 framework 维护者视角
**位置**：`docs/architecture/202605101800-adr-cell-interface-isp-split.md`、`docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md`、`docs/architecture/202605102000-adr-lifecycle-hook-ctx-semantics.md`。
**问题**：3 份 ADR 信息密度极高、决策追溯完整，但**全部以"framework 维护者"视角写作**：
- 202605101800 §D1 列子接口的 "Consumers" 字段——给的是 `kernel/assembly` / `runtime/http/middleware.Metrics` / `kernel/governance` 这种**框架内部消费者**，不是写 cell 的 Go 开发者。
- 202605101900 §"Context" 大段讨论 "type alias bypass / Scan range bypass / Interface embedding" 等 archtest 实现细节——这些是 framework 维护者关心的"如何防止 AI 绕过"。
- 202605102000 §D1 "维持 context.WithCancel(context.Background())" 完全是 framework 内部 lifecycle 协议——消费者完全不需要知道。

消费者读这 3 份 ADR **几乎无法回答**"我作为 cell 写 outbox 应该怎么写"+"我为什么要用 sealed marker（对我有什么用）"。ADR 价值传达不到消费者层。
**证据**：3 份 ADR 全文 godoc / Consumers 列表均面向 framework 内部组件。
**建议**：
1. 不动 ADR（ADR 是决策记录，不是用户文档）。
2. 新增 `docs/guides/why-sealed-marker.md` 短篇 FAQ，以消费者视角问答："为什么我要 wrap"、"如果我不 wrap 会怎样"、"Demo / Production / Test 三场景的标准写法"——把 ADR 内容**翻译**成消费者语言。
3. 在 ADR 顶部加一行 `> 面向：framework 内部贡献者。框架使用者请先看 docs/guides/why-sealed-marker.md。`

**Backlog 登记**：与 F2 合并到 `PR441-DOC-INTERFACE-ISP-CONSUMER-DOCS-01`。

---

### F6 [Cx1] [兼容性风险 / 范围偏移] example godoc 残留旧 ADR section 引用
**位置**：`examples/iotdevice/cells/devicecell/cell.go:72`。
**问题**：`WithDirectPublisher` godoc 写 `// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D6`。但 ADR 202605101800 §D6 已被 ADR 202605101900 §D4 显式 amends 删除（"D6 落地实体已删除"），现在 §D6 只剩 amend 提示和历史描述。消费者点击该 ref 进入 D6 看到的是"已删除"提示，找不到关于 sealed marker 的实质性解释。
**证据**：`examples/iotdevice/cells/devicecell/cell.go:72` ref 字面 + ADR 202605101800:82 自陈"D6 落地实体已删除"。
**建议**：把 ref 改为 `// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1`（sealed marker 实际定义所在）。本 PR 已合并，建议在下次 touch-when-edit 时一并修，不另开 PR。
**Backlog 登记**：Cx1 直接修，不必 backlog；登记到 user memory `feedback_correct_attribution`（"引用规则要核实出处"）的应用案例。

---

### F7 [Cx2] [验收标准缺失 / 范围偏移] backlog carve-out 闭环但未把"文档闭环"列为 follow-up
**位置**：`docs/backlog.md:79`（PR441-FU-RAW-INFRA-PARAM-SIBLING-EXPAND-01）、`:326`（PR245-F10 ✅ marker）、`:447`（SWEEPER-OBSERVABLE-01 部分落地）、`:448`（LIFECYCLE-OWNER-CTX-PROPAGATION-01）、`:453`（CELL-PUBLIC-OPTION-NAMED-IFACE-EMBED-01）、`:454`（ADR-CELL-RAW-INFRA-WORDING-01）。
**问题**：PR #441 的 carve-out 在 backlog 登记是相对认真的——上述 6 条都明确 source 指向 PR #441。但**消费者面文档（README / cell-development-guide / codegen-new-endpoint）的更新缺口**未登记任何 backlog 条目（grep 全 backlog 无"README"/"cell-development-guide"/"codegen-new-endpoint" 关键词）。这违反 user memory `feedback_pr_scope_carveouts_must_backlog`「PR 范围切割必须显式 backlog，不能 silent」。

具体未登记项：
- README §"30-Minute Tutorial" 仍展示 12-方法整体断言旧形态。
- cell-development-guide.md 仍展示旧 outbox wiring 形态。
- codegen-new-endpoint.md 完全无 sealed marker 内容。

**证据**：上述 grep 结果。
**建议**：补登 3 条 backlog 条目（合并入 F2 建议的 `PR441-DOC-INTERFACE-ISP-CONSUMER-DOCS-01`，或拆为：
- `PR441-DOC-README-TUTORIAL-ISP-01`（README 改 4 段式断言）
- `PR441-DOC-CELL-DEV-GUIDE-OUTBOX-WIRING-01`（guide 改 sealed marker 形态）
- `PR441-DOC-CODEGEN-GUIDE-WRAP-FUNCTION-01`（codegen guide 加 wrap 函数说明）。

**Backlog 登记**：F2 + F7 合并出 1-3 条新 backlog（按拆分粒度选）。

---

### F8 [Cx1] [开发者体验] examples 跨示例一致性：3 个 example 给出 3 种不同形态
**位置**：`examples/ssobff/app.go:218,235,250`、`examples/todoorder/main.go:69-70`、`examples/iotdevice/main.go:60`。
**问题**：3 个 example 现在向消费者展示 3 种不同的 outbox 注入形态：
- `ssobff`（platform cell L1/L2 三连）：`WithOutboxDeps(WrapPublisherForCell(eb), WrapWriterForCell(nw))` + `WithTxManager(WrapForCell(demo))`——pub + writer + tx 三件套。
- `todoorder`（L2 OutboxFact，仅 writer 路径）：`WithOutboxWriter(WrapWriterForCell(...))` + `WithTxManager(WrapForCell(...))`——writer + tx 两件套，**option 名带后缀 Writer**。
- `iotdevice`（L4 DeviceLatent，仅 publisher 路径）：`WithDirectPublisher(WrapPublisherForCell(eb))`——只 pub 一件，**option 名带后缀 DirectPublisher**。

设计上 3 种形态都"按 cell 真实能力"是合理的（ADR 202605101800 §D6 表格明确），但**对消费者学习曲线是高负担**：消费者要理解"我写新 cell 时该用哪种 option 形态"+"为什么 ssobff 用 WithOutboxDeps 而 ordercell 用 WithOutboxWriter"。当前 cell-patterns.md（AI 规则文件）有解释，但消费者文档（README / guides）无任何引导。
**证据**：上述 3 个文件不同形态。
**建议**：
- `docs/guides/cell-development-guide.md` 新增 §"按 cell 能力选 outbox option 形态" decision table（L1/L2 platform → WithOutboxDeps；L2 outbox-only → WithOutboxWriter；L4 publish-only → WithDirectPublisher）。
- 每个 example README 顶部加一行 "本示例展示的是 L? 一致性级别下的 outbox 注入形态"。

**Backlog 登记**：可合并到 F2 `PR441-DOC-INTERFACE-ISP-CONSUMER-DOCS-01`。

---

### F9 [Cx1] [开发者体验] godoc "Consumers:" 字段对消费者无误导 + 高价值
**位置**：`kernel/cell/interfaces.go:30-44, 56-70, 78-83, 93-103`。
**问题**：这是**正面 finding**——4 个子接口 godoc 都精确写了 `Consumers: <谁>`，符合 ADR §D1 "Soft 引导"约定，对 framework 内部组件作者很有价值。**对消费者无误导**：消费者实现 cell 时只需要嵌 BaseCell + 实现 initInternal，不需要决定声明哪个子接口，所以 Consumers 段不会让消费者误以为"我必须按 Consumers 列表选子接口"。
**证据**：上述 godoc 文本。
**建议**：保持。可考虑在 `interfaces.go` 文件头加一段简短 godoc 说明"99% 的 cell 作者直接用 Cell 复合接口（嵌 BaseCell）；只有 framework 内部组件（assembly / metrics middleware 等）才按 Consumers 列表选子接口"——避免少数消费者过度解读。
**Backlog 登记**：N/A。

---

## 评审维度评分

| 维度 | 评级 | 证据 |
|------|------|------|
| A. 验收标准覆盖率 | 红 | F4 — 消费者旅程 0 覆盖；P1 验收无证据 |
| B. UI 合规检查（CLI / API surface 角度） | 绿 | sealed marker API 设计完整、错误时机清晰、demo/production 场景全覆盖 |
| C. 错误路径覆盖率 | 黄 | F3 — type system 错误对人不友好（`sealedCellWriter` missing 提示无法引导到 wrap 函数）；BaseCell 四段式 check 错误信息优秀 |
| D. 文档链路完整性 | 红 | F2 + F7 — README / cell-development-guide / codegen-new-endpoint 三处主文档 0 同步；.claude/rules 已同步但不属于消费者文档 |
| E. 功能完整度 | 绿 | sealed marker 主防线 + double archtest 双重防线 + 3 ADR 决策记录完整 |
| F. 成功标准达成度 | 黄 | framework 内部 ISP 单源化目标达成；消费者 onboarding 易用性目标未验证 |
| G. 产品 Tech Debt | 黄 | F6 example godoc ref 漂移 + F1 PR body framing 漂移 + F7 文档 carve-out 未登记 |

## 产品验收确认结果

| 检查项 | 状态 |
|--------|------|
| 产品上下文已定义（消费者 persona + 成功标准） | PASS（CLAUDE.md 已定义"框架消费者 = Go 开发者"） |
| 验收标准已分级（P1/P2/P3） | FAIL（PR test plan 未按 P1/P2/P3 分级） |
| P1 验收 100% PASS | FAIL（F4 消费者旅程 / F2 文档闭环 / F1 对外口径 三项 P1 未达成） |
| P2 无 FAIL | FAIL（F3 错误信息可读性 / F8 跨示例一致性 黄） |
| 评审无红色维度 | FAIL（A / D 红） |

**判定：产品 FAIL**。最小修复闭环：
1. **F1**：PR description / changelog 改口径，加 migration snippet（before/after 对照）。
2. **F2 + F7**：README §Tutorial 4 段式断言改写 + cell-development-guide.md 加 sealed marker 章节 + 新建 `docs/guides/why-sealed-marker.md` 消费者 FAQ；同步登记 backlog。
3. **F4**：补 `examples/onboarding/` 最小 cell + e2e journey test，作为消费者旅程 P1 验收 oracle。
4. **F3**：在 sealed interface marker method godoc 上加 `// call XxxWrapForCell(...) from composition root` hint，让 IDE hover 能引导到正确修复路径。

完成 1-3 后可重审；F3 / F6 / F8 列入 follow-up。框架内部架构变更（ISP + sealed marker）本身无需返工，**问题全部在"对外承诺与消费者发现路径"层**。
