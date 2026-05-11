# PR #441 — Architect 维度审查

## 总体结论

**通过**。PR 在三个维度上做了真正的架构提升：

1. **ISP 拆分**：把 12 方法的 `Cell` 收敛到 4 个语义正交、消费者群清晰的子接口（CellIdentity / CellLifecycle / CellStatus / CellInventory），并保留复合 `Cell` 接口实现 zero-touch 调用方迁移；
2. **Raw-infra 防线由 archtest scanner 升级到 type-system sealed marker**：把原 `CELL-RAW-DEPS-01` archtest（自评 Hard，但 ADR 202605101900 §Context 自承 type alias / scan-range / interface embed 多类绕过 → 实测 Medium）替换为 `kernel/persistence.CellTxManager` + `kernel/outbox.{CellPublisher,CellWriter}` sealed 接口，配两条 type-aware archtest 双重防线（`CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` / `CELL-RAW-INFRA-WRAPPER-LOCATION-01`）治签名形态空间；
3. **lifecycle hook ctx 语义文档化**：第三份 ADR 明确不改 `context.Background()` 派生的现状，理由对齐 uber-go/fx 范式 + 加 rollback 集成测试钉死 OnStop 契约。

三份 ADR 互相承接、概念自洽：202605101800 提出 ISP+CELL-RAW-DEPS-01；202605101900 在合并 review round 中显式 amend D6（删除原 archtest，升 sealed marker）；202605102000 处理 review 的另一条 P1 finding。架构决策的演进路径在 ADR 里 paper trail 完整。

但有四条架构层面的 finding 需要后续跟踪。其中一条（F1）涉及概念模型一致性；其余三条是评级标定 / 双重防线必要性 / Slice 接口对称性张力，均不阻塞本次合入。

---

## Finding 列表

### F1 [Cx2] CellInventory.Metadata 防御复制语义与 ISP 切分目标存在张力
**位置**：`kernel/cell/interfaces.go:93-103` + `kernel/cell/base.go:141`
**问题**：`CellInventory.Metadata()` godoc 注明返回 "independent deep copy"（`b.meta.Clone()` 在每次 `Metadata()` 调用都执行）。CellInventory 的 Consumers 是 contract validators / `gocell validate` / codegen，**全部是高频元数据自省路径**。把读路径设计为防御复制是 fail-closed 的合理选择（前置 PR 的 ADR `202605051300` 一致），但在 ISP 拆出"一个专属于元数据自省的 sub-interface"之后，把高 cost 的 deep-copy 留在 hot path 上，是把数据层单源（ADR `202605051300`）的代价集中到了被 ISP 单独命名出来的接口上——形成 "consumer 们正因为 ISP 才大胆按 sub-interface 直接调 Metadata()" → "调用次数 × deep-copy cost" 的放大路径。
**证据**：
- `kernel/cell/interfaces.go:93-103` CellInventory godoc："defensive copies (callers may freely mutate returned values)"
- `kernel/cell/base.go:141` `Metadata() *metadata.CellMeta { return b.meta.Clone() }`
- ADR 202605101800 §D1 表 CellInventory 消费者列表 = "contract validators / metadata inspectors / `gocell validate` / codegen"——这些都是 batch scan 路径
**建议**：要么 (a) 在 CellInventory godoc 显式标 "Metadata() returns a deep copy on every call; cache the result if you call it more than once per cell"，让消费者按 ISP 的最小依赖原则**主动持有副本**；要么 (b) 把 `Metadata()` 改为 `MetadataView() metadata.CellMetaView` 返回只读 view，把不可变性下沉到 type system（与 ADR 202605101900 sealed marker 同精神：让违反不可表达）。当前 PR 选 deep-copy + 文字承诺，是 ISP 收益的对冲项。
**AI-rebust 评级**：建议 (b) = Hard（type-system sealed read-only view）；建议 (a) = Soft（godoc 警示），不立项门槛要求 ≥ Medium，所以建议 (a) 单独不能立项，须组合 archtest 守"调用方持有副本"才到 Medium。
**Backlog 登记建议**：`PR441-FU-CELLINVENTORY-METADATA-READONLY-VIEW-01`（触发：`Metadata()` 出现在 hot path / batch scan benchmark p99 退化 ≥ 10%）。

### F2 [Cx3] sealed marker D2 archtest 双重防线必要性的 architect 裁决：Medium 是该问题域天花板，但与 ai-collab.md 「架构师做 Soft 升 Medium 的最终裁决」要求需明确写入 ADR
**位置**：`docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md` §D2 + `tools/archtest/cell_public_option_param_test.go` + `tools/archtest/wrapper_location_test.go`
**问题**：ADR 202605101900 §D2 明文承认 "type system 单独无法穷尽所有'暴露 raw infra'的签名形态"——inline interface embedding (`func WithBad(p interface{ outbox.Publisher })`) 与 dot-import wrap call (`import . "kernel/persistence"; WrapForCell(p)`) 这两类**type system 不可达**。这是 type system + archtest 双重防线**必需**而非 belt-and-suspenders 的根因。但 ADR §D2 把这两条 archtest 评为 Medium（type-aware via `typeseval.SharedResolver`）。架构师裁决：这是该问题域的 Medium 天花板，**升 Hard 的路径（语言级 sealed-by-position）超出当前 GoCell 范围**，应在 ADR 中显式记录"为什么 Medium 在此场景下是终点而不是过渡态"。当前 ADR §D2 末段只说 "Hard 化路径需要语言级 sealed-by-position 等特性，超出当前 GoCell 范围"——表述够清楚，但缺一句架构师的"裁决为终点态，不进 follow-up backlog"。否则按 `.claude/rules/gocell/ai-collab.md` Review checklist「Medium 若有低成本升 Hard 的路径，开 follow-up」会被后续 reviewer 误读为"还要继续找升 Hard 路径"。
**证据**：
- ADR §D2 倒数第二段 "type system 不可达签名形态空间"
- ADR §D2 末段 "Hard 化路径需要语言级 sealed-by-position 等特性，超出当前 GoCell 范围"
- `.claude/rules/gocell/ai-collab.md` Review checklist Medium 行
**建议**：在 ADR 202605101900 §D2 末尾追加一句架构裁决："架构师裁决：本场景 D2 的 Medium 评级是该问题域的天花板，与 PII redaction 双重防线同质，**不进 backlog 升 Hard 跟踪**。后续 reviewer 不再质疑该评级。"——避免将来 review 反复挑战。
**AI-rebust 评级**：D2 两条 archtest 本身 Medium 评级合理，ADR 表述需补充。
**Backlog 登记建议**：不需要——本 finding 是要求"显式不入 backlog"。

### F3 [Cx2] CELL-IFACE-ISP-METHODSETS-01 SHA-256 hash guard 评级未在 ADR 露面
**位置**：`tools/archtest/cell_iface_isp_test.go:297-319` + `docs/architecture/202605101800-adr-cell-interface-isp-split.md` §D1/§"AI-HARD 三档分级一览"
**问题**：`tools/archtest/cell_iface_isp_test.go` 中 `expectedMethodSetsSHA256` 常量 + `TestCellIfaceISP00_MethodSetsHashGuard` 是教科书 Hard 模式（修改 expectedSubInterfaces 或 expectedSubInterfaceMethods 立刻 hash 漂移）。但 ADR 202605101800 §"AI-HARD 三档分级一览"只列出 4 段式 compile-time check 与 allowlist hash guard 两个 Hard 项，**没列出 method-set hash guard**。代码层做到了 Hard，ADR 文字没认领——后续 reviewer 看 ADR 会以为 ISP 拆分 + 子接口方法集只有 archtest Medium 守，不知道有 hash guard 锁字面量漂移。
**证据**：
- `tools/archtest/cell_iface_isp_test.go:297` const `expectedMethodSetsSHA256 = "a2cf7188..."`
- ADR §"AI-HARD 三档分级一览" Hard 行未列 `CELL-IFACE-ISP-METHODSETS-01` hash guard
**建议**：补 ADR 202605101800 §"AI-HARD 三档分级一览" Hard 行：`TestCellIfaceISP00_MethodSetsHashGuard`（method-set SHA-256 hash guard，修改 expectedSubInterfaces / expectedSubInterfaceMethods 必须同步更新 hex 常量 + ADR amendment）。让 hash guard 显式 + ADR 承诺一致。
**AI-rebust 评级**：hash guard = Hard（已落地）；ADR 表述补全 = doc 类，不涉评级。
**Backlog 登记建议**：`PR441-FU-ADR-202605101800-AMEND-HASH-GUARD-LIST`（doc 类，可顺手在下一个 ISP 类 PR 一并修）。

### F4 [Cx3] Slice 接口默认不拆决议（D4）的"不预设未来需求" vs "对称性收益" 张力未做边界声明
**位置**：`docs/architecture/202605101800-adr-cell-interface-isp-split.md` §D4 + `kernel/cell/interfaces.go:126-143`
**问题**：ADR §D4 说"Slice 默认不拆"，理由 (a) cells/* 全部嵌 BaseSlice，无第三方实现；(b) Slice 字段为简单值，无 metadata.SliceMeta 单源化驱动；(c) "拆 Slice 只有形态对称收益，无 ISP 实际收益"。激进自审"不预设未来需求"原则是对的。**但 §D4 的触发条件**——"首次出现需替换 BaseSlice 7 方法之一的第三方 Slice 实现"——本质是"等事故发生再拆"。ISP 的核心价值不是"已经有 N 个实现者所以要拆"，而是"调用方按消费者群声明最小依赖"——这一点 Slice 当前同样违反（`kernel/governance` 的 Validator 需要 Slice.Verify() / AllowedFiles() / AffectedJourneys()，runtime 期没人要 Slice.Init()）。
ADR §D4 选定的标准（"第三方实现"）是**实现者侧 ISP**，不是 ADR §D1 给 Cell 拆分用的**消费者侧 ISP**。两个标准混用让 D4 触发条件其实是"等不存在的需求出现"，几乎永不触发。
**证据**：
- ADR §D4 列出 (a)/(b)/(c) 三条 + 触发条件
- ADR §D1 表头 "消费者" 列——Cell 拆分理由就是"消费者群截然不同"
- `kernel/cell/interfaces.go:126` Slice 7 方法实际消费者：governance 调 Verify/AllowedFiles/AffectedJourneys；assembly 调 Init；BaseCell.OwnedSlices() 调 ID/BelongsToCell
**建议**：在 ADR §D4 显式声明"**Slice 不拆基于消费者侧 ISP 收益评估也成立**"——补一段：枚举当前 Slice 所有 in-tree 消费者，证明每个消费者基本都需要 ≥4 个方法，子接口拆分让每个调用点声明的 sub-interface 几乎与 Slice 全集等同，ISP 收益接近 0。**或者**接受拆分的对称性收益（参 io.Reader / io.Writer），把 Slice 拆 SliceIdentity / SliceLifecycle / SliceVerification 一并落地——避免半年后再讨论。当前的 "等触发" 实际是"永不触发"。
**AI-rebust 评级**：不涉及 enforcement 机制评级。
**Backlog 登记建议**：`PR441-FU-SLICE-ISP-DECISION-CLARIFY`（触发：本 PR 合并后下一个动 Slice 接口的 PR；裁决方案后再决定）。已登记 030 §G-10 KERNEL-CELL-PACKAGE-DECOMPOSE 中 "Cell 拆 CellLifecycle + CellDescriptor" 是同源问题——可考虑合并讨论。

---

## 概念模型一致性核查（task §4）

三份 ADR 的承接关系：

| ADR | 状态 | 与其他 ADR 的关系 |
|---|---|---|
| 202605101800 ISP split | Accepted | 原 §D6 提出 CELL-RAW-DEPS-01，被 ADR 202605101900 显式 amend |
| 202605101900 sealed marker | Accepted, Amends 202605101800 §D6 | §D4 删除前置 archtest 实体；§D2 双重防线建立 |
| 202605102000 lifecycle ctx | Accepted | 第二轮 review F3-C 集成测试，与前两份不在同一抽象层 |

**自洽性**：
- ADR 202605101900 在 Status 行明确 "Amends 202605101800 §D6"，表述精确
- ADR 202605101800 §D6 已加 "Amended by ADR 202605101900..." 顶层 Note，双向交叉引用完整
- 三份 ADR 的 AI-rebust 评级表（202605101800 §"AI-HARD 三档分级一览" / 202605101900 §"Consequences" / 202605102000 §Decision）口径一致，无冲突

**ADR 表述漂移核查**：
- ADR 202605101800 §"AI-HARD 三档分级一览" 末行说 "CELL-RAW-DEPS-01 自 R1 round-2 升至 Hard"——这条已被 ADR 202605101900 § §D4 驳回（实测 Medium，由 sealed marker 升 Hard）。202605101800 §D6 顶部 Note 说"以新 ADR 为准"，但 §"AI-HARD 三档分级一览" 末行没有同步 strike-through。**轻度漂移**——见 F3 同一类问题。建议补 strike-through 或加 "[Superseded by ADR 202605101900: 实际评级见新 ADR]"。

**整体评估**：概念自洽，演进 paper trail 完整，**无概念裂缝**。

---

## Soft carry-over 检查（task §5）

PR body 自评 Hard×2 / Medium×4 / Soft×1。逐项对照 `.claude/rules/gocell/ai-collab.md`：

| 评级 | 项 | 实测 |
|---|---|---|
| Hard | 4 段 compile-time check (`kernel/cell/base.go`) | ✓ 真 Hard，缺方法编译失败 |
| Hard | sealed marker (`kernel/persistence/cell_marker.go` + `kernel/outbox/cell_marker.go`) | ✓ 真 Hard，unexported sealed method |
| Hard（额外） | `TestCellIfaceISP00_MethodSetsHashGuard` SHA-256 hash guard | ✓ 真 Hard（见 F3） |
| Medium | `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` archtest | ✓ Medium，type-aware via SharedResolver + types.Unalias + types.Implements + 9 个负面 fixture（覆盖 raw / alias / inline-embed / pure-method / 命名 local-embed） |
| Medium | `CELL-RAW-INFRA-WRAPPER-LOCATION-01` archtest | ✓ Medium，type-aware caller-package check + dot-import 双形态识别 + 多 wrapper 函数覆盖断言 |
| Medium | `CELL-IFACE-ISP-COMPOSITE-01` / `METHODSETS-01` / `BASECELL-CHECK-01` | ✓ Medium，AST type-aware 识别 interface embedded type expression |
| Medium | `CELLMETA-SINGLE-SOURCE-03` 升 CellInventory | ✓ Medium，AST 扫子接口 |
| Soft | godoc `Consumers: <谁>` 引导段 | ✓ 真 Soft，但 ADR §D1 末段明确 "Soft 引导 + Medium archtest 联合，**主线由四段式 compile-time check Hard 锁定**"——没有把 Soft 当主防线，符合 ai-collab.md「Soft 严禁立项」（这里 Soft 不是 enforcement，是文档引导，不在新增 enforcement 机制范围内）|

**结论**：
- **没有新引入的 mandatory Soft archtest**——所有 mandatory 守护都 ≥ Medium
- 唯一的 Soft（godoc Consumers 段）是非 mandatory 引导，且明确标注 "主线由 Hard 锁定"
- 符合 `.claude/rules/gocell/ai-collab.md` 「立项硬门槛 ≥ Medium、Soft 严禁立项」
- **backlog `PR441-FU-RAW-INFRA-PARAM-SIBLING-EXPAND-01` 已登记**（docs/backlog.md cap-02）——这是 reviewer F3-2 提出的扫描范围扩展评估，不是 silent carryover，符合 ai-collab.md "允许暂留时必须同步登记 backlog"

---

## 优先级裁决

| Finding | 优先级 | 阻塞类型 |
|---------|--------|---------|
| F1 | P2 | 性能 / API 优雅性建议；不阻塞 |
| F2 | P1 | ADR 文字补充，避免后续 reviewer 反复挑战 Medium 评级 |
| F3 | P2 | ADR 表述补全（Hard guard 漏列）+ 顺带 strike-through 漂移 |
| F4 | P2 | Slice 拆分决议表述完善；与 030 §G-10 关联讨论 |

无 P0。本 PR 在架构师维度上**通过**，4 条 finding 全部为后续 follow-up 性质，可在下一个相关 PR 顺手处理。

---

## 与其他角色的分工（自检）

本审查**未涉及**以下属其他角色的内容：
- 分层隔离合规性（kernel/cells/runtime 边界扫描）→ Kernel Guardian
- 文档 ADR 内容质量 / 命名规范 → Doc Engineer
- archtest 落地是否实际跑通 / CI 覆盖 → DevOps
- code-level review（go-types 用法 / 边界 case 实现细节）→ Reviewer
- 任务范围 / sprint 影响 → PM

本审查只裁决：分层结构与职责切分合理性（F1, F4）/ 接口稳定性（无问题）/ 概念模型一致性（核查 §4）/ AI-rebust 评级与 Soft carryover 合规（核查 §5, F2, F3）。
