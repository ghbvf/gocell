# metadata-templates-v2.md 六角色第一性原理分析报告

**分析日期**: 2026-04-04
**分析对象**: `docs/architecture/metadata-templates-v2.md`（813 行）
**方法**: 六角色独立分析 + 交叉验证综合 + Round 3 对账
**文档版本**: Round 3 修复后（标题已改为 "GoCell Metadata Model V2"）

## 参与角色

| 角色 | 视角 | 发现数 | 高严重度 |
|------|------|--------|---------|
| 架构师 | 分层一致性、信息流向、单一职责 | 12 | 3 |
| 领域专家 | 语义准确性、概念边界、DDD建模 | 12 | 2 |
| 工具工程师 | 解析复杂度、验证可判定性、实现难度 | 11 | 3 |
| DX 体验 | 认知负担、上手成本、出错概率 | 20 | 7 |
| 魔鬼代言人 | 矛盾、边界case、隐含假设 | 17 | 3 |
| PM 产品 | 目标达成度、遗漏场景、演进路径 | 13 | 4 |

**合计**: 85 个发现，22 个高严重度

---

## Round 3 对账总览

Round 3 对两份报告（本报告 85 发现 + 独立分析 12 发现）合计 97 个发现进行了分诊：

| 分类 | 数量 | 结果 |
|------|------|------|
| 已修复 | 6 | 已落地到文档 |
| Scope 外待跟进 | 1 | CLAUDE.md 冲突 |
| 设计选择（不改） | 5 | 有意 trade-off |
| 不在此文档范围 | 7 | 独立文档/工具职责 |
| P2 增强建议 | 4 | 记录待后续迭代 |

### 已修复验证（6/6 已落地）

| Fix | 问题 | 对应发现 | 验证结果 |
|-----|------|---------|---------|
| Fix 1 | C14 external provider 矛盾 | 工具验证 | **已落地** — L795 C14 增加 external actor 豁免 |
| Fix 2 | C13 对 `["*"]` 不成立 | S-09 相关 | **已落地** — L794 C13 补充 `["*"]` 跳过 client 检查 |
| Fix 3 | select-targets 路由盲区 | 共识#3 (3/6) | **已落地** — L715 新增 `cells/{cell}/**` 兜底规则 |
| Fix 4 | contract.kind 无交叉验证 | 共识#7 (2/6) | **已落地** — L812 新增 D12 规则 |
| Fix 5 | status-board `lastUpdated` 漂移 | DX 验证 | **已落地** — L62/L806 统一为 `updatedAt` |
| Fix 6 | 文档标题不符 | 独立分析 | **已落地** — L1 改为 "GoCell Metadata Model V2" |

### 设计选择（不改，含评审意见）

| # | 决策 | Round 3 理由 | 六角色评审意见 |
|---|------|-------------|--------------|
| 1 | verify ref 保留全局状态解析 | 文档已显式声明依赖 | **有保留** — 4/6 角色共识被以"可行"搁置；"声明了依赖 != 依赖合理"；增量验证不可能这个问题未回应 |
| 2 | consistencyLevel 保留同名 | 通过位置和规则区分，改名增加概念 | **接受** — trade-off 合理，但建议补充三处语义说明段落 |
| 3 | SSOT inherit/override 机制 | Canonical Ownership Matrix 已指定唯一 owner | **接受** — 辩护逻辑严密 |
| 4 | journey.contracts curated subset | cells 是路由锚点，contracts 是人工关注点 | **接受** — 两层设计意图清晰，但"exhaustive"措辞仍有误导性 |
| 5 | ownerCell vs provider 分离 | schema 由 ownerCell 团队管理 | **有保留** — 回答了"谁管 schema"但未写入文档；C4 双重约束的设计理由未说明 |

### 不在此文档范围

| 问题 | Round 3 理由 | 六角色评审意见 |
|------|-------------|--------------|
| consistencyLevel 操作性定义 | 属于 consistency.md | 接受 |
| 合约版本升级/紧急回滚 | 运营流程需独立文档 | 接受，但需确保有人跟进 (PM P-02/P-04 标记高严重度) |
| L0 cell 悖论 | "L0 是 assembly 内编译链接的纯计算单元" | **需补充** — 这个定义正确但未写入 V2 文档本身 |
| generated artifact 一致性模型 | 工具实现细节 | 接受 |
| 四层模型拆分 | 当前结构满足需求 | 接受 |
| 值层次术语统一 | 规模不足以单独抽象 | 接受 |
| contractUsages 语义过载 | 当前只做引用映射 | 接受 |

---

## 第一部分：跨角色共识（按共识强度排序）

### 共识 #1: CLAUDE.md 与 V2 字段命名直接冲突 ── **Scope 外待跟进**

**发现者**: 架构师 [A-08]、DX [X-19]、PM [P-08]、魔鬼代言人 [S-13]（4/6 角色）

**问题**: CLAUDE.md 使用 `cellId`、`sliceId`、`ownedSlices`、`authoritativeData`、`contracts`（作为 cell.yaml 字段），而 V2 将这些全部列为 **Forbidden Legacy Names**。更根本的是，两者代表了不同的模型哲学：
- CLAUDE.md: cell 主动维护反向索引（聚合根视角）
- V2: cell 被动存在，由工具推导关系

**Round 3 处理**: 确认为 P0，但限定本轮 scope 只改 metadata-templates-v2.md，CLAUDE.md 更新需单独跟进。

**状态**: **开放 — 等待单独任务处理**。此问题已跨三轮审查被标记为 P0，是全团队最强共识。

---

### 共识 #2: verify naming convention 的 contract-id/role 解析歧义 ── **保留为设计选择**

**发现者**: 架构师 [A-11]、工具工程师 [T-01]、DX [X-04/X-08]、魔鬼代言人 [S-11]（4/6 角色）

**问题**: `contract.{contract-id}.{role}` 格式中，contract-id 本身含多个点分段，解析器无法从字符串本身确定边界。

**Round 3 决策**: 保留当前方案，理由是文档已显式声明依赖 contract 注册表做最长匹配。

**六角色评审**: 有保留。Round 3 回应了"文档是否说清楚了"，但未回应工具工程师的核心论点——"增量验证场景下，修改单个 slice.yaml 仍需全量加载 contract"。如果工具实现阶段证明这是瓶颈，建议重新评估 `::` 分隔符方案。

---

### 共识 #3: select-targets 路由盲区 ── **已修复 (Fix 3)**

**发现者**: 架构师 [A-05]、魔鬼代言人 [S-03/S-12]、PM [P-10]（3/6 角色）

**修复**: routing matrix 新增 `cells/{cell}/**` 兜底规则（L715），cell 级共享代码变更现在触发 cell full 路由。

**状态**: **已关闭**

---

### 共识 #4: journey.contracts "curated subset" 的完整性陷阱 ── **保留为设计选择**

**发现者**: 架构师 [A-10]、领域专家 [D-05]、魔鬼代言人 [S-04/S-15]（3/6 角色）

**Round 3 决策**: 保留两层设计（contracts = 人工关注点，cells = 路由锚点）。

**六角色评审**: 接受设计意图，但 C13 中"exhaustive"措辞相对于 curated subset 仍有误导性。建议将措辞改为 "exhaustive with respect to the listed contracts"。

**状态**: **已关闭（措辞改进为 P3 建议）**

---

### 共识 #5: ownerCell vs provider-side endpoint 语义 ── **保留为设计选择**

**发现者**: 领域专家 [D-02]、架构师 [A-02]（2/6 角色）

**Round 3 决策**: schema 兼容性由 ownerCell 团队管理（因为 schemaRefs 在 contract.yaml 中）。

**六角色评审**: 有保留。这个澄清解决了领域专家的核心问题，但：
- 这个回答只存在于 round3-analysis.md 中，未写入 V2 文档本身
- C4 为什么对 ownerCell 和 provider 都做一致性约束，设计理由未记录

**状态**: **已关闭（文档内补充说明为 P3 建议）**

---

### 共识 #6: consistencyLevel 同名异义 ── **保留为设计选择**

**发现者**: 领域专家 [D-03]、DX [X-05/X-14]（2/6 角色）

**Round 3 决策**: 通过位置和验证规则区分，改名会增加概念数量。

**六角色评审**: 接受 trade-off。但建议在各字段首次出现处补充一句语义说明（cell = 能力上限，slice = 实际级别，contract = 要求保证），减少认知负担。

**状态**: **已关闭（内联语义说明为 P2 建议）**

---

### 共识 #7: contract.kind 冗余 ── **已修复 (Fix 4)**

**发现者**: 魔鬼代言人 [S-01]、工具工程师 [T-02]（2/6 角色）

**修复**: 新增 D12 规则（L812）——`contract.kind` 必须等于 `contract.id` 的首段。kind 保留为独立字段（工具可读性），但交叉验证堵住了不一致的缝隙。

**状态**: **已关闭**

---

## 第二部分：独特视角发现状态追踪

### 工具工程师

| # | 发现 | 原严重度 | Round 3 状态 |
|---|------|---------|-------------|
| T-01 | verify ref 需全局状态解析 | 高 | 设计选择（保留，见共识#2） |
| T-06 | 精确模式索引无新鲜度检查 | 高 | **P2 增强 #4** |
| T-07 | 验证规则 A→B→C→D 执行顺序未声明 | 中 | **P2 增强 #1** |
| T-03 | C8 glob 重叠 "cross-match testing" 定义不明 | 中 | **P2 增强 #2** |
| T-04 | C9 "覆盖"定义模糊 | 中 | 未明确归类（归入 T-03 相关） |
| T-08 | contract domain-path 与目录路径一致性 | 中 | 未明确归类 |
| T-09 | 4/6 工具缺乏规格 | 高 | 不在此文档范围 |

### 领域专家

| # | 发现 | 原严重度 | Round 3 状态 |
|---|------|---------|-------------|
| D-02 | ownerCell vs provider 语义 | 高 | 设计选择（见共识#5） |
| D-03 | consistencyLevel 同名异义 | 高 | 设计选择（见共识#6） |
| D-07 | lifecycle 缺少 sunset/frozen | 中 | 不在此文档范围（运营流程） |
| D-05 | curated subset 缺判定标准 | 中 | 设计选择（见共识#4） |
| D-04 | contractUsages 缺复合角色 | 中 | 不在此文档范围 |
| D-08 | 多 Slice serve 同一 contract 无约束 | 低 | 未明确归类 |

### DX 体验

| # | 发现 | 原严重度 | Round 3 状态 |
|---|------|---------|-------------|
| X-19 | CLAUDE.md 字段冲突 | 高 | Scope 外 P0（见共识#1） |
| X-04/X-08 | verify naming 手写出错/解析歧义 | 高 | 设计选择（见共识#2） |
| X-15 | 缺少字段速查表 | 高 | **P2 增强 #3** |
| X-17 | validate-meta 错误输出格式未定义 | 高 | 未明确归类 |
| X-01 | 学习曲线过陡（15+概念） | 高 | 未明确归类 |
| X-02 | 文档结构偏规范参考 | 高 | 未明确归类 |
| X-11 | allowedFiles 替换语义 | 中 | 未明确归类 |
| X-13 | 继承导致幽灵覆盖 | 中 | 未明确归类 |

### 魔鬼代言人

| # | 发现 | 原严重度 | Round 3 状态 |
|---|------|---------|-------------|
| S-13 | CLAUDE.md 术语冲突 | 高 | Scope 外 P0（见共识#1） |
| S-03/S-12 | cell 级文件路由盲区 | 高 | **已修复 Fix 3** |
| S-07 | L0 cell 悖论 | 高 | 不在此文档范围（"L0 是编译链接单元"） |
| S-01 | contract.kind 冗余 | 中 | **已修复 Fix 4** |
| S-04/S-15 | curated subset 保证力度 | 中 | 设计选择（见共识#4） |
| S-05 | schemaRefs 裸文件名限制 | 中 | 未明确归类 |
| S-06 | 单 publisher 限制 | 中 | 未明确归类 |
| S-09 | `["*"]` 语义歧义 | 低 | **已修复 Fix 2**（C13 补充） |

### PM 产品

| # | 发现 | 原严重度 | Round 3 状态 |
|---|------|---------|-------------|
| P-08 | CLAUDE.md 冲突 | 高 | Scope 外 P0（见共识#1） |
| P-01 | run-journey/verify-cell 输入模糊 | 高 | 不在此文档范围 |
| P-02 | 版本升级并行期流程缺失 | 高 | 不在此文档范围（独立文档） |
| P-04 | 紧急回滚无合法路径 | 高 | 不在此文档范围（独立文档） |
| P-07 | 无元数据质量度量框架 | 中 | 未明确归类 |
| P-13 | assert 字段缺类型体系 | 中 | 未明确归类 |

---

## 第三部分：当前开放项汇总

### 仍需行动（按优先级）

| 优先级 | 项目 | 行动 | 负责范围 |
|--------|------|------|---------|
| **P0** | CLAUDE.md 字段名冲突 | 更新 CLAUDE.md Cell 开发规则为 V2 canonical names | CLAUDE.md |
| **P0** | glossary/overview 术语不同步 | 同步到 V2 canonical names | 其他文档 |
| **P2** | 验证规则执行顺序声明 | V2 文档内补充 A→B→C→D 分阶段说明 | V2 文档 |
| **P2** | C8 glob 重叠算法定义 | V2 文档内明确 "cross-match testing" 含义 | V2 文档 |
| **P2** | 字段速查表 | V2 文档内增加 Cheat Sheet 附录 | V2 文档 |
| **P2** | 精确模式索引新鲜度 | V2 文档内声明 stale fallback 行为 | V2 文档 |
| **P3** | L0 cell 定义写入文档 | 补充"L0 是 assembly 内编译链接的纯计算单元" | V2 文档 |
| **P3** | ownerCell schema 权威说明 | 补充 ownerCell 管理 schema 兼容性的说明 | V2 文档 |
| **P3** | consistencyLevel 三处语义内联 | 在各字段处补充一句语义说明 | V2 文档 |
| **P3** | C13 "exhaustive" 措辞精确化 | 改为 "exhaustive with respect to listed contracts" | V2 文档 |

### 已接受为 Scope 外（需独立跟进）

| 项目 | 来源 | 建议载体 |
|------|------|---------|
| 合约版本升级并行期流程 | PM [P-02] | 独立运营文档 |
| 紧急回滚路径 | PM [P-04] | 独立运营文档 |
| run-journey/verify-cell 完整规格 | PM [P-01]、工具 [T-09] | 工具设计文档 |
| validate-meta 错误输出格式 | DX [X-17] | 工具设计文档 |
| DX quickstart / 任务导向文档 | DX [X-01/X-02] | 独立 onboarding 文档 |
| 元数据质量度量框架 | PM [P-07] | gocell metrics 设计 |

---

## 第四部分：设计原则审计（Round 3 修复后）

| 原则 | 遵守度 | 说明 |
|------|--------|------|
| 1. Fact authored once | **良好** | version 已禁止；kind 冗余存在但 D12 交叉校验堵住缝隙 |
| 2. Canonical/Generated/Delivery-only | **良好** | Ownership Matrix 完整，assembly transition strategy 已文档化 |
| 3. Dynamic status not in canonical | **优秀** | 无违反，字段名已统一为 `updatedAt` |
| 4. Contract schema in versioned dirs | **优秀** | schemaRefs 相对路径 + 目录约定 + D9 校验 |
| 5. Sufficient input for 6 tools | **良好** | validate-meta 和 select-targets 充分；run-journey 有 checkRef + fixture；verify 有命名约定 |
| 6. Derivable facts generated | **良好** | allowedFiles 有约定默认；assembly 派生字段标注为 generated |

对比初始评估：原则 1 从"部分"升至"良好"（D12 修复），原则 5 从"部分"升至"良好"（routing matrix 补全）。

---

## 第五部分：Round 3 分诊覆盖率分析

97 个发现 → 23 个显式归类 → 74 个归为"重复/噪音"。

六角色 85 个发现中被显式归类的：

| 角色 | 总发现 | 显式归类 | 未归类 | 覆盖率 |
|------|--------|---------|--------|--------|
| 架构师 | 12 | 7 | 5 | 58% |
| 领域专家 | 12 | 6 | 6 | 50% |
| 工具工程师 | 11 | 7 | 4 | 64% |
| DX 体验 | 20 | 5 | 15 | 25% |
| 魔鬼代言人 | 17 | 9 | 8 | 53% |
| PM 产品 | 13 | 6 | 7 | 46% |

**DX 角色的覆盖率最低**（25%）。20 个 DX 发现中有 7 个高严重度，但只有速查表 [X-15] 被采纳为 P2。以下 DX 高严重度发现值得重新审视：

- **X-17** validate-meta 错误输出格式 — 直接影响开发者 debug 效率
- **X-01** 学习曲线（15+ 概念） — 影响 onboarding，但可通过独立 quickstart 文档解决
- **X-02** 文档结构偏规范参考 — 可通过速查表 (P2 #3) 部分缓解

---

## 总结

Round 3 修复有效解决了 6 个实质问题（C14 external provider、C13 通配符、路由盲区、D12 kind 校验、字段名漂移、标题不符）。5 个设计选择的判定整体合理，trade-off 有据可依。

**当前文档质量评估**: 从"规则参考手册"视角看，V2 已接近成熟——37 条验证规则体系化、routing matrix 完整、canonical ownership 清晰。

**主要残留风险**:
1. **文档生态不一致**（P0）: CLAUDE.md + glossary + overview 仍传达相反信息，这是 V2 落地的最大阻塞
2. **DX 缺口**: 文档为架构师和工具实现者写，对日常开发者不友好。P2 速查表是最小成本改善
3. **verify ref 全局状态**: 被接受为设计选择，但若工具开发期间成为瓶颈应重新评估
4. **操作流程空白**: 版本升级、紧急回滚等需独立文档覆盖，应确保有人跟进
