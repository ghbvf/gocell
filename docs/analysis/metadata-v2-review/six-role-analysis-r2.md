# metadata-model-v2.md 六角色第一性原理分析（第二轮）

**分析日期**: 2026-04-04
**分析对象**: `docs/architecture/metadata-templates-v2.md`（843 行，Round 3 修复后版本）
**方法**: 六角色独立分析 + 交叉验证，**严格限定只看本文件**
**与第一轮的关系**: 第一轮 85 个发现 → Round 3 修复 6 项 → 本轮对修复后版本增量分析

---

## 总览

| 角色 | 文件内发现数 | 高 | 中 | 低 |
|------|-------------|---|---|---|
| 架构师 | 15 | 0 | 8 | 7 |
| 领域专家 | 14 | 0 | 6 | 8 |
| 工具工程师 | 12 | 0 | 4 | 8 |
| DX 体验 | 12 | 2 | 4 | 6 |
| 魔鬼代言人 | 9 | 3 | 3 | 3 |
| PM 产品 | 7 | 2 | 3 | 2 |

**合计**: 73 个文件内发现（剔除跨文件引用后），高 11 / 中 28 / 低 34

---

## 第一部分：高严重度发现

### H1: cell.id / slice.id 与目录名一致性——无验证规则 [S-20/S-26]

**发现者**: 魔鬼代言人（独立发现）

文档两处声明了目录约定:
- L269: "The cell directory name must equal `cell.id`"
- L318: "The slice directory name must equal `slice.id`"

但 43 条验证规则中**没有任何一条**检查这个约束。B10 只验证 `belongsToCell` vs 目录中的 `{cell-id}`，B7 只验证 contract version vs 目录路径。最基础的 `id == 目录名` 映射反而是验证盲区。

**反例**: `mv cells/access-core/slices/session-login/ cells/access-core/slices/session-auth/` 后忘改 yaml 中 `id: session-login` → validate-meta 全部通过 → select-targets、verify 的路径推导全部指向不存在的路径。

**建议**: 新增 B12: `cell.id` must equal cell directory name; B13: `slice.id` must equal slice directory name。

---

### H2: derived-anchor desync 窗口——validate-meta 与 select-targets 无执行顺序约束 [S-19]

**发现者**: 魔鬼代言人

derived-anchor 的 Resolution Rule 是 "validate-meta checks declared == computed"。但文档**没有规定工具间的执行顺序**。如果 CI 中 select-targets 在 validate-meta 之前运行（或并行运行），derived-anchor 的 desync（如目录改了但 yaml 没改）会导致路由误判而不报错。

L779 只定义了 validate-meta **内部**的分组顺序 A→B→C→D，不是工具间的顺序。

**建议**: 文档应声明 `validate-meta` 必须在 `select-targets` 之前通过，或 `select-targets` 应内置最小 derived-anchor 一致性检查。

---

### H3: 字段速查表仍然缺失 [DX-07]

**发现者**: DX 体验

第一轮 P2 增强 #3 被采纳但未实现。843 行文档中没有任何形式的速查表。确认 slice.yaml 必填字段需从 L348 开始读，继承字段要跳到 L356，derived-anchor 含义要回到 L95。

---

### H4: 任务导向文档仍然缺失 [DX-08]

**发现者**: DX 体验

文档仍是纯参考手册。没有"创建新 Cell 步骤"或"定义新 Contract 步骤"等面向开发者工作流的内容。

---

### H5: run-journey 仍是六工具中规格最薄弱的 [P-06]

**发现者**: PM 产品

verify-cell 有了独立章节（L721-728），但 run-journey 和 generate-assembly **仍无独立工具规格章节**。run-journey 的行为散布在 L179、L225、L838 三处。缺失：输入合约、执行模型（串行/并行）、输出格式、fixture 失败处理。

---

### H6: 合约版本升级流程和紧急回滚路径仍为空白 [P-05]

**发现者**: PM 产品

L462 只说 "create a new version instead"。v1/v2 共存期间 contractUsages 如何写、journey.contracts 列哪个版本、deprecated 后紧急回滚怎么办——全部无指导。

---

### H7: slice.verify.contract identifier 格式未被任何验证规则覆盖 [T-06]

**发现者**: 工具工程师

D8 只检查 `passCriteria.checkRef` 的格式（verify naming convention），不检查 `slice.verify.contract` 列表中的 identifier 格式。D7 只检查"非空"。如果 slice 声明了 `verify.contract: ["contract.http.auth.login.v1"]`（缺少 role 后缀），validate-meta 不会报错，错误延迟到 verify-slice 运行时才发现。

**建议**: 扩展 D8 或新增规则：所有 verify identifier（不仅是 passCriteria.checkRef）都应遵循 verify naming convention 格式。

### H8: generatedAt 不够——缺少 sourceFingerprint [review-feedback]

**发现者**: 外部审查反馈

D15 要求 generated artifact 包含 `generatedAt` 时间戳，但时间戳只能证明"什么时候生成的"，不能证明"从正确的输入生成的"。场景：改了 slice.yaml 但忘了重新生成索引，索引的 generatedAt 是今天但内容已过时。至少还需要 `sourceFingerprint`（所有输入文件的 hash），让 validate-meta 判断"世界变了但索引没跟上"。这是 lockfile 的标准模式（go.sum、package-lock.json）。

**建议**: D15 增加 sourceFingerprint 要求；validate-meta 对比当前源文件 hash 与 artifact 中记录的 hash，不一致则报错。

---

### H9: L0 cell 与 cell 定义存在概念冲突 [review-feedback]

**发现者**: 外部审查反馈

文档定义 cell 为 "runtime boundary + data sovereignty"（Layer 2, L267）。但 L0 cell 没有 runtime boundary（同进程直接 import）、没有 data sovereignty（不持状态）、不参与 contract。它更像 library partition 而非 cell。

当前 L0 Cell Interaction Model（~L297）将其定义为"纯计算库"，这与 cell 的核心定义相矛盾。要么修改 cell 定义以包含"计算分区"，要么在文档中显式承认 L0 是 cell 定义的有意放宽（设计 trade-off）。

**建议**: 在 L0 Cell Interaction Model 中增加一段 trade-off 声明："L0 is an intentional relaxation of the cell boundary concept — it retains cell-level governance (ownership, smoke testing, routing) without runtime isolation. This is a pragmatic extension, not a pure architectural partition."

---

### H10: journey 多重职责未被标注为设计 trade-off [review-feedback]

**发现者**: 外部审查反馈

Journey 同时承担验收规格（goal + passCriteria）、测试计划（checkRef + fixtures）、影响分析锚点（cells 被 select-targets 使用）。团队会议决策"不拆"，但文档没有显式标注这是有意识接受的语义耦合。缺少这个声明，后续读者很容易把 journey 当成"精确依赖图"的权威来源，而 contract-inferred mode 已经承认它不是。

**建议**: 在 Layer 1 section 开头补充 trade-off 声明："Journey spec serves three roles — acceptance specification, test plan, and routing anchor — by design. This coupling simplifies governance (one file, one owner) at the cost of semantic purity. In particular, `journey.cells` is an exhaustive routing anchor (C13), but `journey.contracts` is a curated subset that must not be treated as a complete dependency graph."

---

### H11: journey.contracts curated 标准中"构成主路径"不可判定 [review-feedback]

**发现者**: 外部审查反馈

当前 L168 的 curated 入选标准为 "(a) directly asserted by a passCriteria entry, or (b) on the journey's critical path"。标准 (a) 可验证（检查 checkRef 解析链是否涉及该 contract），但标准 (b) "on the journey's critical path" 是主观判断，不同团队理解不同，不可自动验证。

**建议**: 删除 "(b) on the journey's critical path"，只保留可判定标准 "(a) directly asserted by a passCriteria entry"。如果团队仍需要列出更多 contract 以提高可读性，可以改为 "directly or indirectly asserted by passCriteria entries"。

---

## 第二部分：跨角色共识

### 共识 A: derived-anchor with override (allowedFiles) 破坏 5 类分类互斥性（3/6 角色）

**发现者**: 领域专家 [D-01]、魔鬼代言人 [S-21]、架构师 [A-04]

Design Goal 2 (L8) 声称 "belongs to exactly one value category"。但 allowedFiles (L363) 在未声明时是 derived-anchor（从目录约定计算），声明时是**完全替换**——不是 "declared == computed" 的校验关系。这实际上是第六种变体。

**建议**: 在 Value Resolution Model 中为 derived-anchor 增加 strict / defaulted 两个子类型，或将 allowedFiles 重新归类为 canonical with convention default。

---

### 共识 B: verify-cell / run-journey 规格不足以指导实现（4/6 角色）

**发现者**: 工具工程师 [T-05]、DX [DX-05]、PM [P-02/P-06]、架构师 [A-14]

verify-cell (L721-728) 从"完全无描述"升级到 4 行规格——方向正确但仍缺：错误处理策略、输出格式、退出码、并行语义。run-journey 和 generate-assembly 仍无独立章节。

---

### 共识 C: delivery-only 的"不受验证"措辞与 D3-D5 实际行为矛盾（2/6 角色）

**发现者**: 领域专家 [D-03]、架构师 [A-03]

L101 说 delivery-only "Not subject to architectural validation or routing"。但 D3/D4/D5 对 status-board 的 state/risk/blocker 做了枚举校验。措辞应改为 "Not subject to architectural **topology** validation or routing. Format rules in Group D still apply."

---

### 共识 D: D 组"部分依赖 C"无规则支撑（2/6 角色）

**发现者**: 架构师 [A-10]、工具工程师 [T-02]

L779 说 "Group D partially depends on C"。但审查 D1-D12 所有规则，没有一条显式引用 C 组输出。D7 的核心依赖是 B 组（解析 belongsToCell 获取 effective consistencyLevel），不是 C 组。

**建议**: 改为 "Group D depends on B (for field resolution)" 或标注 D 组中具体哪条依赖 C。

---

### 共识 E: domain-path 应禁止 v\d+ 段（2/6 角色）

**发现者**: 架构师 [A-07]、魔鬼代言人 [S-18]

verify ref 的 v\d+ 解析方案在当前 A3 格式下是正确的，但如果 domain-path 含 `v\d+` 段（如 `api.v2.auth`），目录路径 `contracts/http/api/v2/auth/v1/` 中的 `v2` 容易被误认为版本号。文档未显式禁止。

**建议**: A3 补充约束 "Domain-path segments must not match `v\d+`"。

---

## 第三部分：改善确认

本轮确认以下第一轮问题已有效修复:

| 第一轮问题 | 修复方式 | 验证结果 |
|-----------|---------|---------|
| verify ref 全局状态依赖 (T-01, 4/6共识) | v\d+ 确定性解析 (L399-404) | **有效** — A3 约束下逻辑正确 |
| select-targets 路由盲区 (3/6共识) | `cells/{cell}/**` 兜底规则 (L740) + fixture 路由 (L747) | **有效** |
| contract.kind 无交叉验证 (2/6共识) | D12 规则 (L842) | **有效** |
| 验证规则执行顺序 (2/6共识) | A→B→C→D 显式声明 (L779) | **有效**（D 组描述需微调） |
| verify-cell 无描述 (PM P-01) | 独立章节 (L721-728) | **部分有效**（方向正确但不足） |
| Contract-Inferred Mode 过度承诺 | 重命名 + limitation 声明 (L757-775) | **有效** |
| allowedFiles 强制 required | 降级为 convention-default optional (L363) | **有效** |

---

## 第四部分：优先行动建议

### P0: 验证规则缺失（影响路径推导正确性）

| 行动 | 对应发现 |
|------|---------|
| 新增 B12: `cell.id` == cell directory name | H1 [S-20/S-26] |
| 新增 B13: `slice.id` == slice directory name | H1 [S-20/S-26] |
| 扩展 D8 或新增规则: 所有 verify identifier 需格式校验 | H7 [T-06] |
| D15 增加 sourceFingerprint 要求（不只 generatedAt） | H8 [review-feedback] |
| journey.contracts curated 标准删除"主路径"，只保留可判定标准 | H11 [review-feedback] |

### P1: 规格补全（影响工具实现一致性）

| 行动 | 对应发现 |
|------|---------|
| 声明工具执行顺序: validate-meta 先于 select-targets | H2 [S-19] |
| run-journey 补充独立工具规格章节 | H5 [P-06], 共识 B |
| generate-assembly 补充独立工具规格章节 | 共识 B |
| delivery-only 措辞精确化 | 共识 C |
| D 组依赖描述修正 | 共识 D |
| L0 cell 增加 trade-off 声明（承认是 cell 定义的有意放宽） | H9 [review-feedback] |
| Journey section 增加多重职责 trade-off 声明 | H10 [review-feedback] |

### P2: 模型精化

| 行动 | 对应发现 |
|------|---------|
| derived-anchor 增加 strict/defaulted 子类型 | 共识 A |
| A3 补充 domain-path 禁止 v\d+ 约束 | 共识 E |
| 合约版本升级/紧急回滚操作指引 | H6 [P-05] |
| C4 AND 语义补充示例 | [A-16] |

### P3: DX 改善

| 行动 | 对应发现 |
|------|---------|
| 字段速查表附录 | H3 [DX-07] |
| 任务导向速查章节 | H4 [DX-08] |
| validate-meta 错误输出示例 | [DX-09] |
| verify ref 手写 checklist | [DX-03] |

---

## 第五部分：团队复核补充发现

团队复核后确认当前版本已不属于"必须全盘重做"的状态，但识别出 3 个需要继续重开的主题和 6 个具体问题。

### R-1 (严重): journey.cells 的完整性保证仍然证不出来

文档一方面明确说 `journey.contracts` 是 curated subset，不是全量（L168）；另一方面又说 contract-inferred mode 以 `journey.cells` 为锚，由 C13 保证完整性（L770）。但 C13 实际只检查 `journey.contracts` 里列出的 contract（L839）。既然输入不是全量，就不能证明 `journey.cells` 对真实参与方完整。

这是一个结构性限制：从 curated subset 无法推导 exhaustive set。文档需要承认这一点，而非声称 C13 提供完整性保证。

### R-2 (严重): L0 cell 与 cell 基础定义仍然冲突

文档把 cell 定义为 "runtime boundary + data sovereignty"（L114），强调所有 cross-cell interaction 默认需要 contract（L124）。但 L0 被定义为同 assembly 内可直接 import 的纯计算库（L298），规则 C7 也承认这个例外（L833）。

要么 L0 不是 cell（不放在 cell 层级），要么 cell 不是纯 runtime boundary（重写定义）。当前的 trade-off 声明方案还没落地。

### R-3 (高): 路径约定还没校验闭环

B10 只校验 `belongsToCell` 与路径中 `{cell-id}` 一致（L820）。还缺：
- `cell.id` == `cells/{cell-id}/cell.yaml` 目录名
- `slice.id` == `cells/{cell-id}/slices/{slice-id}/` 目录名

这两个是 verify 路径推导和 select-targets 路由的基础，但当前只有文档约定没有硬校验。

### R-4 (高): allowedFiles 跨常规目录声明的路由盲区

文档允许 slice 拥有非常规路径（共享 proto、跨目录生成代码，L363），但 routing matrix 只覆盖 `cells/**`、`contracts/**`、`journeys/**`、`assemblies/**`、`actors.yaml`、`fixtures/**`（L748）。如果 slice 的 `allowedFiles` 指向 repo 其他位置（如 `proto/shared/**`），该路径的变更不会命中任何路由规则。

### R-5 (中高): generated artifact 一致性仍靠流程不靠数据

有 `generatedAt`、重算 diff、CI 顺序要求（L778/L786/L860），但缺 `sourceFingerprint` 证明"这个生成物对应哪组输入"。时间戳只证明何时生成，不证明从正确输入生成。

### R-6 (中): journey.contracts curated 标准仍有主观性

passCriteria 直接断言可判定，但 "critical path" 仍是解释性概念（L168）。同时 "出现在任何 journey.contracts 里" 被用来判断 full contract（L625），治理强度受作者主观选择影响。

---

### 团队复核总判断

当前版本**不需要全盘重做**，但进入稳定期前需要闭合 3 个主题：

| 主题 | 核心问题 | 可选方案 |
|------|---------|---------|
| **journey.cells 完整性** | C13 从 curated subset 推不出 exhaustive set | A: 承认 cells 也是 best-effort 并删除 "exhaustive" 声称<br>B: 增加 warning 级推导扫描（扫 cells 的所有 slice contractUsages 找漏列的 cell） |
| **L0 cell 定位** | 与 cell 基础定义矛盾 | A: 重写 cell 定义为 "governance partition"（含 runtime boundary 和 computation partition 两种）<br>B: 将 L0 从 cell 层级移除，归入 pkg/ |
| **路径约定与 generated artifact 硬约束** | 约定无规则 + 一致性无签名 | A: 新增 B12/B13 路径校验 + D15 增加 sourceFingerprint<br>B: 保持约定但声明为 convention（不升级为 error） |

---

## 第六部分：最终一致性审查补充发现

一致性审查确认文档接近稳定期，但识别出 4 个收窄主题和 3 个措辞修复。

### F-1 (严重): cell / L0 / contract requirement 总规则未系统改写

Layer 2 总表已将 cell 扩为 "governance partition"（L114），L0 章节承认有意放宽（L300），但以下位置仍使用旧表述：
- L269: cell.yaml 仍说 "runtime-boundary facts"
- L428: contract 章节仍用 "Every cross-cell interaction requires a contract" 普遍性表述
- L625: lightweight contract 章节同上

不是硬冲突，但总定义还没系统性传播到所有引用位置。

### F-2 (严重): journey.cells 已诚实标注为 best-effort，但意味着 contract-inferred mode 只能是优化

文档现在正确承认了 C13 是 best-effort（L849）、contract-inferred mode 不是精确依赖图（L170/L775）。这是正确的，但需要在工具设计上确认：contract-inferred mode 永远不能替代 coarse mode 作为唯一路由策略。

### F-3 (高): "single source of truth" 表述过强

文件开头说 "This document is the single source of truth for GoCell's metadata model"（L5），但 consistencyLevel 操作性语义已外置到 `consistency-levels.md`（L437/L802）。应改为 "This document defines the structural and topological rules for GoCell's metadata model" 或类似表述。

### F-4 (高): full-contract 判定仍依赖 curated 字段

"When to Model Lightweight Contracts" 的 full contract 判定条件中仍有 "The interaction appears in any journey's contracts list"（L637）。journey.contracts 是 curated subset，让治理强度受作者主观选择影响。应改为不依赖 journey.contracts 的客观标准。

### F-5 (中高): ownerCell warning 仍将治理关系投影为运行时信号

C4 拆分后（L840），ownerCell warning 语义为"治理团队可能缺乏该级别运维经验"。这合理但模糊——warning 的处理方式和升级条件未定义。

### F-6 (中): assembly generated artifact 承载结构不清

D15 要求 assembly optional-generated fields 带 `generatedAt` 和 `sourceFingerprint`（L871），但 assembly.yaml 的 YAML 结构中没有展示这两个字段应放在哪里（是 exportedContracts 的 sibling？还是嵌在 generated.* 下？）。

### F-7 (中): 3 个措辞修复

1. `allowedFiles` "derived-anchor with override" 破坏五分类互斥 — Value Resolution Model 需补充子类说明
2. Group D "partially depends on C" 无实际依据 — 应删除
3. delivery-only "Not subject to validation" 过宽 — 改为 "Not subject to architectural topology validation or impact routing"

---

### 最终一致性审查总判断

当前版本**可进入稳定期**，前提是完成以下 4 个收窄主题：

| 主题 | 改动范围 | 估计行数 |
|------|---------|---------|
| 1. 统一改写 cell/L0/contract requirement 总规则 | L269, L428, L625 及其他引用位置 | ~10 行 |
| 2. "single source of truth" 改为准确表述 | L5 | 1 行 |
| 3. full-contract 判定从 journey.contracts 解耦 | L637 | ~3 行 |
| 4. assembly generated artifact 承载结构写清 | assembly YAML 示例 + D15 说明 | ~8 行 |

加上 3 个措辞修复（~5 行），总改动量约 27 行。

---

## 第七部分：精度审查补充发现

本轮审查确认文档规则密度已经足够，剩余问题为精度问题而非结构性缺失。

### G-1 (高): contract domain-path 与目录路径无验证规则

B7 只验证 version 段 vs 目录路径，D9 只验证 kind 段 vs 目录路径。domain-path 中间段（如 `auth.login` → `auth/login/`）无任何规则校验。`id: http.auth.login.v1` 放在 `contracts/http/foo/bar/v1/` 里不会报错。

### G-2 (高): C15 会产生系统性 false positive（4/6 角色共识）

C15 扫描 journey.cells 中所有 cell 的 slice contractUsages，发现未列入 journey.contracts 的 active contract 时 warning。但 journey.contracts 的 curated criterion 是"被 passCriteria 直接或间接断言"——一个 cell 可能有大量 contractUsages 与该 journey 的验收标准无关。C15 会对这些无关 contract 全部 warning，产生噪音。

### G-3 (高): "indirectly asserted" 不可机器验证

L168 的 curated criterion 包含 "directly or indirectly asserted by a passCriteria entry"。"直接断言"可判定（checkRef 解析链涉及该 contract），但"间接断言"没有终止条件——间接到什么程度算间接？机器无法判定。

### G-4 (高): sourceFingerprint 双工具生成一致性未定义

D15 要求 generated artifact 含 sourceFingerprint。但 validate-meta 和 generate-assembly 都可能生成 generated fields。两个工具对"输入文件集合"的定义可能不同（validate-meta 看全部 metadata，generate-assembly 只看 assembly 相关），导致同一 artifact 被两个工具算出不同 hash。

### G-5 (高): C13 + C15 级联盲区

C13 检查 journey.contracts 中 contract 的参与方是否在 journey.cells 中。C15 检查 journey.cells 中 cell 的 contractUsages 是否在 journey.contracts 中。但如果一个 contract **和**它的参与 cell 同时被遗漏——两条规则都不会触发，因为 C13 不知道有这个 contract，C15 不知道有这个 cell。

---

### 精度审查总判断

**规则密度已足够。** 以上 5 个发现都是精度问题（C15 范围、curated 定义、fingerprint 规范），不是结构性缺失。

**核心结论：下一步应该是实现 validate-meta + scaffold，不是继续加规则。** 精度问题在工具实现过程中会自然暴露并修正，而文档继续迭代的边际收益已经递减。

---

## 设计原则遵守度（仅看本文件）

| 原则 | 遵守度 | 说明 |
|------|--------|------|
| 1. Fact authored once | **良好** | kind 冗余有 D12 交叉校验; ownerCell 冗余有设计说明 (L452) |
| 2. Exactly one value category | **部分** | allowedFiles 破坏互斥性 [共识A]; delivery-only 措辞不精确 [共识C] |
| 3. Dynamic status not in canonical | **优秀** | 无违反 |
| 4. Contract schema in versioned dirs | **优秀** | B11 新增文件存在性校验；domain-path 段缺校验 [G-1] |
| 5. Sufficient input for 6 tools | **良好** | validate-meta 完整; select-targets 完整; verify-cell 有了; run-journey/generate-assembly 仍薄弱 |
| 6. Derivable facts generated | **良好** | Value Resolution Model 明确标注各字段类别 |

---

## 总结

文档从第一轮的 85 个发现、22 个高严重度，经过多轮修复后：
- **核心结构问题已有效修复**：verify ref 解析、路由盲区、验证规则顺序、值分类模型、路径硬校验、sourceFingerprint
- **已闭合的主题**：journey.cells best-effort、L0 governance partition、路径 B12/B13、路由兜底、curated 标准
- **规则密度已足够**，剩余为精度问题

**进入稳定期前的最终改动**（约 27 行）：
1. **cell/L0/contract 总规则统一改写** (F-1): L269/L428/L625
2. **"single source of truth" 改为准确表述** (F-3): L5
3. **full-contract 判定从 journey.contracts 解耦** (F-4): L637
4. **assembly generated artifact 承载结构** (F-6): 示例 + D15
5. **3 个措辞修复** (F-7): allowedFiles 子类/D depends C/delivery-only

**实现阶段处理**（不再在文档中迭代）：
- G-1: domain-path 目录校验 → validate-meta 实现时补规则
- G-2: C15 false positive → validate-meta 实现时调整扫描范围
- G-3: "indirectly" 不可判定 → 实现时改为 "directly asserted" only
- G-4: fingerprint 双工具一致性 → 实现时统一 hash 算法
- G-5: C13+C15 级联盲区 → 已知限制，coarse mode 兜底

**保留但不阻塞**：
- ownerCell warning 升级条件
- run-journey/generate-assembly 独立章节
- DX 速查表和任务导向文档
7. **DX 缺口** (P3): 速查表和任务导向文档
