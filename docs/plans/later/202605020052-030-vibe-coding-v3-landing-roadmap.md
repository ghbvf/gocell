# 030 - Vibe Coding v3 工程化治理落地路线图（GoCell 实例化）

## 上下文

`~/Documents/methodology/v3.md` 提出了《Vibe Coding 工程化治理 v3》高阶方法论（七大思想融合：第一性原理 / 金字塔法则 / 系统工程 / 软件工程 / DevOps + 看板 / MDM 声明式 / Harness 工程 / 资本论）。

**关键认知**：v3 是 1 周前产出的探索性理论，未经任何工程实践验证。**本路线图不是"按 v3 走"，而是"用 v3 提问 + GoCell 实践验证 + 反向修订 v3"的双向过程**。GoCell 实践有权质疑、修改、抛弃 v3 中不工作的部分。详见 [三层关系 ADR](202605020052-adr-methodology-three-layer.md)。

本计划是 v3 与 GoCell 的"对话样本"，**同时作为跨语言跨平台的可复制路径骨架**——iOS / .NET / C++ / TypeScript 等生态可参照同骨架推导各自路径。GoCell 是首个完整推导样本，不是唯一正解。

## 关键纠偏（vs 早期评估）

- **GoCell 真实位置**：v3 阶段一**前 30%**（不是中后期 75-85%）；项目未发布
- **运行时自主能力**：**0/10**（runtime/agent/ 不存在；.claude/agents 是 IDE 协作辅助**不是** v3 运行时 Harness）
- **业务信号采集**：0/10（cell.yaml / slice.yaml 无 intent_id；无业务信号采集器）
- **整体路径长度**：6-12 个月跨 6 个 GD 批次

## 五条致命缺口（按生存威胁度）

| # | 缺口 | 为什么致命 |
|---|---|---|
| **A** | 运行时自主任务执行器 | v3 核心承诺"治理从编译时延伸到运行时"。无此则**永远只能编译时治理**，运行时漂移、健康衰减、契约违反全靠人工驱动 |
| **B** | 业务信号采集与反馈闭环 | GoCell 元数据驱动架构与业务现实**完全断开**。治理规则沦为自我循环 |
| **C** | 差异化制程（lifecycle-aware） | 实验/生产 cell 用同一套门禁。一旦 experimental cell 被 journey 误引用，实验代码污染生产路径 |
| **D** | 声明式状态协调（reconciliation） | 现有完全"拉模型"，运行时 drift 无法自动检测。journeys passCriteria 定义验收但无持续验证机制 |
| **E** | DORA 指标采集 | 即使 A-D 都补，无客观反馈则**无法证明治理 ROI** |

**关键洞察**：A 是其他 4 条的前提。**A 必须最早动**。

## 六台阶能力金字塔

通用骨架（去技术化），任何技术栈可复制：

| 台阶 | 通用能力 | 必须解决的根本问题 | 对应 GD |
|---|---|---|---|
| 0 | 领域骨架 + 契约骨架 + 静态校验 | "项目结构是否清晰" | ✅ 完成 |
| 1 | 声明式生命周期 | "实验代码与生产代码如何区分" | **GD1**（4-6 周）|
| 2 | 三维健康度 + 业务信号 | "模块还被需要吗、健康吗、在边界内吗" | **GD2**（6-8 周）|
| 3 | 差异化门禁 + DORA | "实验快跑、生产严守，治理 ROI 可证" | **GD3**（6-8 周）|
| 4 | AI 协作辅助 + 健康度代理 | "AI 怎么帮人做决定（人最终拍板）" | **GD4**（8-12 周）|
| 5 | 运行时代理 + reconciliation | "运行时漂移如何自动检测+修正" | **GD5**（12-16 周，最大缺口）|
| 6 | 真正 R1-R3 自治 | "AI 真正接管运维，人守边界" | **GD6**（远景，团队 ≥ 2 人后） |

## 八个骨架要素（跨栈通用）

| 要素 | 抽象定义 | GoCell 形态 |
|---|---|---|
| manifest | 模块的 desired state 声明文件 | cell.yaml / slice.yaml / contract.yaml |
| 静态守卫 | 编译时/构建时检查模块边界违反 | archtest 32 套 + governance 30+ 规则 |
| 信号采集器 | 机械化收集业务/架构/工程数据 | **GD2 新建** |
| 门禁分桶 | 按 lifecycle 应用不同强度的检查 | **GD3 新建** |
| 代理任务 | runtime 层可执行的自主任务抽象 | **GD5 新建（runtime/agent/）** |
| 可逆性梯度 | R1-R5 分级，决定哪些可自治 | **GD5 新建** |
| 协调循环 | 定期 desired vs current 检查 + 修正 | **GD5 新建（runtime/reconciler/）** |
| 决策审计存储 | 所有自动决策的可追溯记录 | **GD5 新建（generated/agent-decisions/）** |

## GD 批次详细规划

### GD0：建立对话框架（1 周）

**核心原则**：GD0 不是"对齐到 v3"，而是建立"v3 与 GoCell 实践对话的框架"。

**产出三件套**（本批次启动即开始）：
- 本路线图（即本文件）
- [v3 ↔ GoCell 对照索引](202605020052-001-v3-gocell-mapping.md)（含"v3 主张待验证清单"，11 条可被实践证伪的主张）
- [方法论三层关系 ADR](202605020052-adr-methodology-three-layer.md)（明确"实践证据 > v3 理论"的优先级，以及"质疑 v3 的机制"）

**GD0 之后每个 GD 末尾必做**：
- 自动开 `v3-feedback` issue（不等到 GD4）
- 更新对照索引中"v3 主张待验证清单"的 survive / modified / refuted 状态
- 若一个 GD 完成时全部主张 "survive"，要求复审是否真正质疑过 v3

### GD1：lifecycle 声明式（4-6 周）

**产出**：
- `kernel/metadata/types.go`：CellMeta / SliceMeta / AssemblyMeta + Lifecycle 枚举（experimental | candidate | asset | maintenance | retired）+ IntentID + HealthThresholds
- `kernel/governance/rules_lifecycle.go`：3 条 LIFECYCLE-* 规则
- lifecycle-aware archtest 拦截 experimental → production journey 引用
- `cmd/gocell/app/scaffold.go` 模板默认 lifecycle: experimental
- `hack/migrate-lifecycle-fields.sh`：批量迁移 3 cell + 20 slice + 8 journey + 1 assembly
- `.specify/memory/constitution.md`：新增 RL-18 "无 lifecycle 字段不允许构建"

**立即生效**：scaffold 默认值、status-board 色块、PR description 自动注入

**KPI**：100% lifecycle 字段覆盖；`gocell validate --strict` 0 error

### GD2：业务信号 + 三维健康度（6-8 周）

**产出**：
- cell.yaml / slice.yaml 加 `intent_id`（GoCell 当前 0/10 的最大补齐）
- `cmd/gocell/app/score.go`：`gocell score [--cell|--all]` CLI
- `tools/health/`：业务/架构/工程三档信号采集器
  - 业务：流量埋点 + 契约订阅数 + status-board 引用
  - 架构：聚合 archtest 32 套结果
  - 工程：复用 sonar + Codecov + git log
- `kernel/governance/healthscore_weights.yaml`（季度评审可校准）
- `kernel/governance/rules_health.go`：3 条 ADV 规则（任一维 < 30 warn / < 10 退役建议 / 三维全 < 50 持续两周 ERROR）
- markdown dashboard 模板

**KPI**：北极星指标可计算；3 cell + 20 slice 全部有非空三维分

### GD3：差异化门禁 + DORA（6-8 周）

**产出**：
- `.github/workflows/pr-check.yml` 按 lifecycle 分桶
  - experimental: fast track ~3min
  - candidate: + contract test + archtest + sonar
  - asset: + 4 层安全 + formal verify + e2e
  - retired: 拒任何修改
  - **govulncheck 全 lifecycle 必跑**（不参与差异化，安全旁路防护）
- `cmd/gocell/app/promote.go`：晋升仪式 CLI（强制 evidence）
- `kernel/governance/rules_promotion.go`：4 条规则（PROMOTE-EVIDENCE / PROMOTE-HEALTH / PROMOTE-RETIRE-WINDOW / PROMOTE-DOWNGRADE）
- `LIFECYCLE-DEPGRAPH-01`：experimental cell 不允许被 production journey 引用
- `tools/dora/`：五指标采集器（GitHub Actions API）
- `.github/workflows/health-snapshot.yml`：daily cron

**KPI**：experimental/candidate/asset PR CI 时长比 ≥ 1:2:5；DORA daily snapshot

### GD4：AI 协作辅助升级 + 健康度代理（8-12 周）

**产出**：
- `.claude/agents/health-auditor.md`（reviewer 派生）—— L0 只读，每周必看 ≤ 5 条
- `.specify/memory/constitution.md`：新增 RL-19 "AI 健康度 Agent 周建议接受率 < 90% 必查"
- `docs/architecture/promotion-policy.md`：每个状态的 CI 强度、签核人、回退路径
- `v3-feedback` issue 标签自动创建机制

**注意**：这里仍是"AI 协作辅助"层（agent 跑在 CI），是 GD5 真正"运行时自主"的过渡。

**KPI**：health-auditor 周报告 ≥ 4 周；AI 接受率有真实基线（**不预设 70-90%**）

### GD5：runtime/agent + reconciliation（12-16 周，**最大缺口**）

**5 子台阶（每个独立可发布）**：

- **GD5.A**：`kernel/agent/` AgentTask interface（纯模型，不实施）
- **GD5.B**：`runtime/agent/executor.go` R1-R5 可逆性执行器
- **GD5.C**：cell 生命周期钩子（`cell.OnHealthDrift` / `cell.OnLifecycleTransition`）
- **GD5.D**：`runtime/reconciler/` 30 min cron loop
- **GD5.E**：`generated/agent-decisions/*.jsonl` 决策审计存储

**KPI**：runtime/agent/ 框架成型；reconciler dry-run 30 天零误退役

**失败回退**：reconciler 误判 → 立即 disable cron，回到 GD4 "建议+人工执行" 模式

### GD6：真正自治 + 季度评审（远景，团队 ≥ 2 人后）

**前置**：GD1-5 全部稳定 ≥ 1 季度

**产出**：
- `.claude/agents/decision-auditor.md`（独立于 health-auditor）
- 季度评审 issue 自动开 + 强制填写 6 问题
- v3 → v3.1 修订机制
- `tools/methodology-self-audit/`：每季度按 v3 第 11.2 反模式 7 条评分
- `.specify/memory/constitution.md`：新增 RL-20 "独立审计员角色不允许合并到其他角色"

**单人项目模式约束**：decision-auditor 独立性 = 不同 .claude/agents 配置 + history.jsonl 时间戳比对替代真人独立角色

## 与 029 master roadmap 协调

| 029 Phase | GD 协调 |
|---|---|
| Phase 0 | ✅ 已过 |
| Phase 1（当前） | **GD0 立即启动**；GD1 schema 同 Phase 1 末并行 |
| Phase 2（K#02/K#03） | **GD 冻结**，仅跑数据采集 |
| Phase 3（K#04-K#06）| GD1 字段挂在 codegen 翻转后 schema 上 |
| Phase 4（K#07-K#11） | GD2 / GD3 启动 |
| 029 收尾后 | GD4 / GD5 / GD6 串行 |

**优先级**：029 > GD（v1.0 发布优先于 v3 落地）。**GD 不替代 029**。

## 真实风险与停止条件

### 项目级真实风险

1. **GoCell 自身能否走到 v1.0**：项目未发布。若发布失败 → GD 路线图同步暂停。**首个样本必须先成功**
2. **样本骨架过早抽象**：从单一样本（GoCell）抽出的骨架可能特定于 Go/cell-native。需第二个样本（推荐 iOS 或 .NET）验证
3. **跨栈推广耗散**：不同栈实施者可能误解骨架，回到"抄 GoCell"陷阱
4. **运行时自主在某些栈不可行**：嵌入式、前端因环境限制无法实现 G

### 停止条件

- 连续 2 个 GD 逾期 50% 或更多 → 暂停推进，重审能力定义
- GoCell v1.0 发布超期 6 个月 → GD 全部暂停
- GD5.A 立项后 8 周内拿不出可运行 AgentTask 模型 → 重新评估技术可行性

### 项目级反模式（禁止）

- ❌ "GoCell 是 v3 的最佳实例" → 这是循环论证
- ❌ "其他框架不如 GoCell 因为没用 v3" → v3 是参考，不是合规标准
- ❌ "GD6 必须达到才算落地完成" → GD5 已是巨大成就
- ❌ "把 029 路线图换成 GD 路线图" → 029 是修复轨道，GD 是发展轨道，并行不替代

## 度量起点

### 北极星：可信资产数

> **可信资产数** = lifecycle=asset 且健康度三维全 ≥ 60 且最近 30 天无致命安全 issue 的 cell 数

**当前值：0**（所有 cell 还没有 lifecycle 字段）。

### 次级指标（按台阶递进）

| 台阶 | 出口指标 | 当前值 |
|---|---|---|
| 0 完成 | 框架分层 + 契约 + 静态校验 | ✅ 完成 |
| 1 完成 | lifecycle 字段覆盖率 100% | 0% |
| 2 完成 | 三维健康度可算模块比例 | 0% |
| 3 完成 | DORA 五指标 daily snapshot | 0/5 |
| 4 完成 | AI 接受率有 ≥ 4 周真实数据 | 0 |
| 5 完成 | reconciler dry-run 30 天零误退役 | – |
| 6 完成 | R1-R3 自治稳定 1 季度 | – |

**禁止预设阶段二指标**（如 AI 接受率 70-90%）。这些指标只在 GD4 跑出真实数据后建立基线。

## 跨语言推广价值

本路线图同时作为 iOS / .NET / C++ / TypeScript 等生态的参考。完整的"跨技术栈对照表"参见 [完整 plan 文件](file:///Users/shengming/.claude/plans/1-2-3-rosy-nest.md)，含：

- 11 个骨架要素 × 5 个技术栈实例对照
- 4 个对照实例（iOS / .NET / C++ / TS）的关键差异 + 最大挑战
- AI 自主任务实施跨栈映射（7 子台阶 × 5 栈）

GoCell 跑通后，v3.md 应增加"v3 跨语言实例集"附录。

## 验证清单

- [ ] **GD0**：mapping 文档 + ADR + 本路线图登记完成
- [ ] **GD1**：100% lifecycle 字段覆盖；archtest 拦住越界
- [ ] **GD2**：北极星可计算；3 cell 有真实业务信号
- [ ] **GD3**：DORA daily snapshot；CI 时长按 lifecycle 1:2:5
- [ ] **GD4**：health-auditor 周报告 ≥ 4 周
- [ ] **GD5**：runtime/agent 成型；reconciler dry-run 30 天零误判
- [ ] **GD6**：决策审计可查询；季度评审例行；v3.1 发布

**跨栈推广验证**（远景）：
- 至少 1 个非 Go 栈（推荐 iOS 或 .NET）按本路线图推导出自己的 6 台阶路径并完成台阶 1-2
- v3.md 增加"跨语言实例集"附录

## 相关文档

- [v3 方法论原文](file:///Users/shengming/Documents/methodology/v3.md)（外部）
- [v3 ↔ GoCell 逐节映射](202605020052-001-v3-gocell-mapping.md)
- [方法论三层关系 ADR](202605020052-adr-methodology-three-layer.md)
- [完整 plan（含跨语言对照表）](file:///Users/shengming/.claude/plans/1-2-3-rosy-nest.md)
- [029 master roadmap](../202605011500-029-master-roadmap.md)（当前 v1.0 发布串行计划）
- [项目宪法](../../../.specify/memory/constitution.md)
- [CLAUDE.md](../../../CLAUDE.md)
