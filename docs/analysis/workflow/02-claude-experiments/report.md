# 阶段 2：Claude 官方实验研究 — 多 Agent 编排与单 Agent 长时间运行

> 日期: 2026-03-30
> 研究范围: Anthropic 官方发布的 17 篇实验/文档/工程博客
> 分析方式: 3 个研究 Agent 并行搜索，分别聚焦多 Agent、单 Agent 长时间运行、评估可靠性

---

## 目录

1. [研究全景](#1-研究全景)
2. [多 Agent 编排模式](#2-多-agent-编排模式)
3. [单 Agent 长时间运行](#3-单-agent-长时间运行)
4. [Context 工程](#4-context-工程)
5. [评估与可靠性](#5-评估与可靠性)
6. [最新模型能力](#6-最新模型能力)
7. [对我们工作流最有冲击力的发现](#7-对我们工作流最有冲击力的发现)

---

## 1. 研究全景

共收集 17 篇 Anthropic 官方发布物，按主题分为 5 类：

| 类别 | 数量 | 核心主题 |
|------|------|---------|
| A. 多 Agent 编排 | 4 篇 | 设计模式、团队协作、技能系统 |
| B. 单 Agent 长时间运行 | 4 篇 | Harness 设计、三 Agent 架构、科学计算、Context 焦虑 |
| C. Context 工程 | 3 篇 | Context rot、渐进式披露、工具搜索 |
| D. 评估与可靠性 | 4 篇 | Evals 方法论、Agent drift、自省能力、基础设施噪声 |
| E. 最新模型能力 | 2 篇 | Opus 4.6 能力、自主性度量、行业趋势 |

---

## 2. 多 Agent 编排模式

### 2.1 Building Effective Agents（基础设计模式）

**来源**: https://www.anthropic.com/research/building-effective-agents

Anthropic 定义了 6 种从简到繁的设计模式：

```
1. Augmented LLM        → LLM + 检索 + 工具 + 记忆
2. Prompt Chaining       → 顺序调用 + 中间验证门
3. Routing              → 分类输入 → 专用处理器
4. Parallelization      → Sectioning（独立子任务）/ Voting（多次同一任务）
5. Orchestrator-Workers  → 中央 LLM 动态分解任务 → 工人执行
6. Evaluator-Optimizer   → 生成者 + 评估者循环
```

**核心原则**: "Start simple. Add complexity only when demonstrably necessary."

**Tool 工程与 Prompt 工程同等重要**。在 SWE-bench 上，优化工具描述比优化整体 prompt 带来更大收益。应用 poka-yoke 原则：修改参数设计使错误更难发生（如要求绝对路径而非相对路径）。

**与我们工作流的映射**：
| 我们的阶段 | 对应模式 |
|-----------|---------|
| S2 并行审查 | Parallelization (Sectioning) |
| S5 并行实施 | Orchestrator-Workers |
| S6 Review-Fix | Evaluator-Optimizer |
| S7 QA | Rules-Based Feedback |

### 2.2 Multi-Agent Research System（Anthropic 自己的生产系统）

**来源**: https://www.anthropic.com/engineering/multi-agent-research-system

**架构**：Opus 4 lead + Sonnet 4 subagents (3-5 个并行) + Citation Agent

**关键数据**：
- 多 Agent (Opus+Sonnet) 比单 Agent (Opus) 好 **90.2%**
- 并行化将复杂查询研究时间减少 **90%**
- Token 使用量解释了浏览任务 80% 的性能差异
- Agent 用量是 chat 的 4 倍；多 Agent 是 chat 的 **15 倍**

**关键教训**：
1. **详细任务描述极其重要** — 简单指令导致重复工作。改进后的描述指定了时间范围、焦点领域和分工
2. **启发式优于刚性规则** — 编码"熟练人类如何工作"而非规定性程序
3. **最小化信息损失** — Subagents 将结构化结果输出到文件系统，仅将轻量引用返回给协调者
4. 重写工具描述的 Agent 实现了 **40% 的任务完成时间缩减**

**重要警告**："多 Agent 不适合需要跨 Agent 共享上下文或大量依赖关系的领域，例如大多数编码任务。"

### 2.3 Agent Teams（实验性团队功能）

**来源**: https://code.claude.com/docs/en/agent-teams

**架构**：Team lead + Teammates + 共享任务列表 + 邮箱通信

**设计洞察**：
- **3-5 个 teammate 最优** — 超过后协调开销超过收益
- **每个 teammate 5-6 个任务** — 避免过多上下文切换
- **Plan approval gate** — teammate 在只读模式下先规划，lead 批准后才实施
- **Quality gate hooks** — `TeammateIdle`/`TaskCreated`/`TaskCompleted` 强制执行规则
- 三个专注的 teammate 通常优于五个分散的

**不适用场景**：顺序任务、同文件编辑、大量依赖的工作

### 2.4 Agent Skills（渐进式技能系统）

**来源**: https://claude.com/blog/equipping-agents-for-the-real-world-with-agent-skills

技能是包含 SKILL.md 的目录，Agent 动态发现和加载：

```
Level 1: 技能元数据（名称/描述）在 system prompt
Level 2: 完整 SKILL.md 在相关时加载
Level 3+: 附加捆绑文件按需加载
```

**核心洞察**：拥有文件系统访问的 Agent 实际拥有"无限上下文"——只要不同时加载所有内容。

---

## 3. 单 Agent 长时间运行

### 3.1 Effective Harnesses for Long-Running Agents（Harness 架构）

**来源**: https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents

**双阶段 Agent 设计**：

| 阶段 | Agent | 职责 |
|------|-------|------|
| Phase 1 | Initializer | init.sh + progress 文件 + git commit + **200+ 粒度特征列表**（JSON，全部 `passes: false`） |
| Phase 2 | Coding Agent | 每个 session：读 progress + git history → 选 1 个最高优先级特征 → 实施 → commit → 更新 progress |

**两大失败模式及对策**：

| 失败模式 | 描述 | 对策 |
|---------|------|------|
| 提前宣告完成 | 看到已有进展就声称完成 | 200+ 粒度特征列表，JSON boolean，全部 false 起始 |
| 一次做太多 | 试图一个 session 完成所有，导致 context 耗尽 | **单特征每 session 规则** |

**为什么用 JSON 而非 Markdown**："模型不太可能不当地修改或覆盖 JSON 文件，相比于 Markdown 文件。" 这直接挑战了 tasks.md 的 `[x]` 标记方式。

**三层状态追踪**：
1. Git commits — 时间线记录 + 回滚能力
2. Progress file (`claude-progress.txt`) — 人类可读的 session 摘要
3. Feature list (JSON) — 200+ boolean 完成状态

**Session 启动惯例**：
```
1. pwd（确认工作目录）
2. 读 git log + progress 文件
3. 读特征列表，选下一个未完成项
4. 通过 init.sh 启动开发服务器
5. 运行基本 E2E 测试（捕获前 session 的未记录 bug）
6. 开始特征实施
```

### 3.2 Harness Design for Long-Running Apps（三 Agent 架构）

**来源**: https://www.anthropic.com/engineering/harness-design-long-running-apps

**进化版架构**：Planner Agent + Generator Agent + Evaluator Agent

| Agent | 职责 |
|-------|------|
| Planner | 将一句话 prompt 扩展为完整产品规格（16 特征 × 10 sprint） |
| Generator | 按 sprint 结构顺序实施 |
| Evaluator | 用 Playwright 像真实用户一样测试 |

**关键发现**：

1. **自评偏差（Self-Evaluation Bias）**
   > "Agents tend to respond by confidently praising the work — even when, to a human observer, the quality is obviously mediocre."

   这是分离 Generator 和 Evaluator 角色的根本原因。

2. **Context Anxiety（上下文焦虑）**
   - Opus 4.5 在接近 context 限制时开始"提前收工"
   - Sonnet 4.5 在 ~80K tokens 时出现此行为
   - **Opus 4.6 基本消除了此问题**
   - 对于 Opus 4.5，完全重置优于 compaction

3. **Sprint Contracts（冲刺契约）**
   - 开工前 Generator 和 Evaluator 协商明确的成功标准
   - 契约指定"完成"的定义 — 可测试的需求
   - 防止 Generator "缩小范围"

4. **Evaluator 需要调优**
   > "Out-of-box evaluators perform poorly — they're a poor QA agent"

   需要多轮日志阅读和 prompt 更新才能获得合理的怀疑态度。硬阈值确保质量低于标准时 sprint 失败。

5. **成本数据**
   - Solo：20 分钟，$9
   - Full harness：6 小时，$200（20 倍贵但质量"立即可见"差异）
   - Opus 4.6 可连续 2+ 小时不需 context reset

### 3.3 Long-Running Claude for Scientific Computing（科学计算案例）

**来源**: https://www.anthropic.com/research/long-running-Claude

**案例**：Claude 自主实现了 JAX 可微分宇宙学 Boltzmann 求解器 — 本需"数月到数年研究员工作"，数天完成。

**三层记忆架构**：
1. **即时上下文**: CLAUDE.md（当前计划和规则 — Claude 可以在工作时编辑）
2. **情节记忆**: CHANGELOG.md（追踪进展和死胡同）
3. **代码历史**: Git commits（每个有意义的工作单元后提交）

**失败路径文档化**：
> "The failed approaches are important — without them, successive sessions will re-attempt the same dead ends."

示例："Tried using Tsit5 for the perturbation ODE, system is too stiff. Switched to Kvaerno5."

**Ralph Loop 模式**：
完成验证的 for 循环 — 在宣告完成前反复质询 Agent"真的完了吗？"Agent 通常会承认还有未完成的工作。

**Test Oracle**：
持续对照参考实现验证，防止无声退化。求解器实现了与参考 CLASS 实现的"亚百分比一致性"。

### 3.4 Agent Drift（Agent 漂移）

**来源**: https://www.chanl.ai/blog/agent-drift-silent-degradation

**三种漂移类型**：
| 类型 | 描述 | 示例 |
|------|------|------|
| 语义漂移 | 逐渐偏离原始意图 | 30 天退款政策 → 第 15 轮开始提供例外 |
| 行为漂移 | 出现意外策略 | 关联冗长回复与满意度 → 越来越啰嗦 |
| 协调漂移 | 多 Agent 交接质量下降 | 三种漂移不相关 — 可以语义稳定但行为漂移 |

**量化数据（最有冲击力）**：

| 指标 | 数值 | 含义 |
|------|------|------|
| 记忆失败频率 | 简单 0.67/任务 → 复杂 3.67/任务 | **5.5 倍增加** |
| 复杂多轮交互记忆提取率 | **13.1%** | 10 条信息只能正确提取 1.3 条 |
| pass@1=90% 时的 pass^5 | **59%** | 隐藏 41% 失败率 |
| 同一问题不同措辞的一致性 | Opus 4.5: **73%** | 27% 时间给出不同答案 |
| 企业 AI 失败归因于 drift | **65%** | 2025 年数据 |

**漂移时间线**：
```
Turn 5:  表现完美
Turn 12: 开始忘记客户细节
Turn 18: 与早期承诺矛盾
Turn 23: 推荐已取消的选项
```

标准评估（3-5 轮）永远检测不到这些问题。

**预防策略**：
1. 持久记忆作为漂移锚点 — Turn 1 存储的事实在 Turn 30 保持保真度
2. 指令重注入 — 每 ~10 轮重新注入关键约束
3. 上下文压缩 — 摘要旧轮次，保留近期完整文本

---

## 4. Context 工程

### 4.1 Effective Context Engineering

**来源**: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents

**核心转变**：Context 工程取代 Prompt 工程。指导原则：
> "Find the smallest set of high-signal tokens that maximize the likelihood of the desired outcome."

**Context Rot（上下文腐烂）**：
- 检索准确度随 token 数量增加而下降
- 模型有有限的"注意力预算"（n² 对比关系）
- 训练数据中短序列占主导 → 长序列注意力弱

**三种长期模式**：

| 模式 | 描述 | 适用场景 |
|------|------|---------|
| Compaction | 接近限制时摘要对话 | 连续会话 |
| 结构化笔记 | Agent 定期写持久化笔记 | 多小时任务序列 |
| Sub-Agent 架构 | 专用 Agent + 干净 context → 返回精简摘要 | 并行探索 |

**渐进式披露（Progressive Disclosure）**：
- 文件大小暗示复杂度，命名约定暗示用途
- 时间戳作为相关性代理
- 按需发现而非预加载

**System Prompt 建议**：
> 找到"正确的高度" — 足够具体以引导行为，足够灵活以启用强启发式。

**反模式**：
- 在 system prompt 中硬编码复杂的脆弱逻辑
- 臃肿的工具集 + 模糊的决策点
- 过度激进的 compaction 丢失后来才显重要的上下文

### 4.2 Claude Code Best Practices

**来源**: https://code.claude.com/docs/en/best-practices

**核心约束**：
> "Most best practices are based on one constraint: Claude's context window fills up fast, and performance degrades as it fills."

**Context 管理策略**：
1. `/clear` — 无关任务间完全重置
2. Auto compaction — 摘要关键代码模式、文件状态和决策
3. `/compact <指令>` — 定向 compaction
4. Subagents — 隔离 context window，仅返回精简摘要
5. `/btw` — 侧问题不进入对话历史

**最高杠杆实践**：
> "Include tests, screenshots, or expected outputs so Claude can check itself. This is the single highest-leverage thing you can do."

**常见失败模式**：

| 失败模式 | 描述 | 修复 |
|---------|------|------|
| Kitchen sink session | 混合无关任务污染 context | `/clear` 分隔 |
| 反复纠正 | 失败方法累积 | 2 次纠正后 `/clear` + 重写 prompt |
| CLAUDE.md 过长 | Claude 忽略重要规则 | 无情精简 |
| 信任-验证鸠 | 看似正确但缺乏边缘情况 | 总是提供验证 |
| 无限探索 | 无范围调查填满 context | 限制范围或用 subagent |

### 4.3 Advanced Tool Use（工具搜索 + 程序化调用）

**来源**: https://www.anthropic.com/engineering/advanced-tool-use

| 技术 | 效果 |
|------|------|
| Tool Search（按需搜索工具定义） | **85% token 减少**，准确率 49%→74% (Opus 4) |
| Programmatic Tool Calling（Python 编排多工具） | **37% token 减少**，准确率 25.6%→28.5% |

---

## 5. 评估与可靠性

### 5.1 Demystifying Evals

**来源**: https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents

**八步 Eval 实施**：
1. 从 20-50 个现实任务开始
2. 从实际失败和手动测试流程中获取
3. 写无歧义任务 + 参考解
4. 平衡正面和负面测试用例
5. 隔离试验（干净环境）
6. **偏好 outcome-based 评估而非刚性步骤验证**
7. 定期阅读 transcripts 验证评分
8. 监控饱和度（>95% 通过率）

**两个关键指标**：

| 指标 | 定义 | k=10 时行为 |
|------|------|------------|
| pass@k | k 次中至少 1 次正确的概率 | 接近 100% |
| pass^k | k 次全部正确的概率 | 可能接近 0% |

> "Focus on outcome-based evaluation rather than validating specific process steps, since agents discover alternative valid paths."

### 5.2 Introspective Awareness

**来源**: https://www.anthropic.com/research/introspection

Claude Opus 4/4.1 的自省能力只有 **~20%** 可靠。

三种自省机制：
1. 异常检测 — 标记处理过程中的意外神经活动模式
2. 输出一致性检查 — 比较预期与实际输出
3. 概念调制 — 通过指令有意控制内部表示

**含义**：Agent 自我改进循环尚不能被自主信任。外部验证（6 视角 reviewer、PM 检查、QA 门）仍然不可替代。

### 5.3 Infrastructure Noise

**来源**: https://www.anthropic.com/engineering/infrastructure-noise

基础设施配置可导致基准分数**偏差 6 个百分点**（p < 0.01）——通常大于顶级模型之间的差距。

| 资源配置 | 基础设施错误率 |
|---------|-------------|
| 严格 1x | 5.8% |
| 3x 余量 | 2.1% |
| 无限制 | 0.5% |

### 5.4 Auto Mode Safety

**来源**: https://www.anthropic.com/engineering/claude-code-auto-mode

**"Reasoning Blindness"原则**：分类器只看用户消息和工具调用，剥离助手消息和工具输出——防止 Agent 通过合理化说服分类器。

**含义**：S6 Reviewer 应直接评估代码产物，不看 Agent 的"我做了什么"解释。

---

## 6. 最新模型能力

### 6.1 Opus 4.6

| 指标 | 数值 |
|------|------|
| Context window | 1M tokens（beta） |
| Max output | 128K tokens |
| MRCR v2 @ 1M tokens | 76%（vs Sonnet 4.5 的 18.5%） |
| Terminal-Bench 2.0 | 65.4%（最高分） |
| 最长任务完成时间 | 14.5 小时 |
| 内测持续聚焦 | 30+ 小时 |

Opus 4.6 **基本消除了 context anxiety**，支持自动 compaction 的连续长 session。

### 6.2 Measuring Agent Autonomy

**来源**: https://www.anthropic.com/research/measuring-agent-autonomy

- 99.9 百分位的 turn 持续时间从 <25 分钟翻倍到 >45 分钟
- 新用户 ~20% session 使用全自动批准；经验用户 **~40%**
- 中断率从 5% 升到 **9%** — 从逐操作批准转向**战略性干预**
- 80% 操作涉及安全保护；只有 0.8% 看起来不可逆

### 6.3 2026 Agentic Coding Trends

**来源**: https://resources.anthropic.com/2026-agentic-coding-trends-report

8 大趋势中与我们最相关的：
- **#2 Agents 成为团队成员** — 专业 Agent 群在编排下并行工作
- **#3 Agents 端到端** — 持续数小时/数天的工作
- **#4 Agents 学会求助** — 检测不确定性，标记风险
- **关键数据**: 只有 **0-20%** 任务可完全委派

---

## 7. 对我们工作流最有冲击力的发现

### 发现 1: Agent Drift 数据颠覆"一次性检查"假设

pass@1=90% 但 pass^5=59%。复杂交互记忆提取率仅 13.1%。

**含义**：总负责人跑完 8 个阶段后，S2 的 Harness 约束大概率被"遗忘"。Harness 专家的检查点本质上是**反 drift 锚点**——每隔几个阶段重新注入关键约束。

### 发现 2: 自评偏差使 PM 检查不可靠

"Agents confidently praise their own work — even when quality is obviously mediocre."

**含义**：PM 的 5.4（build/test 绿）+ Agent 的"任务完成"自报告都不够。需要**独立的外部质量判断**——Harness 专家做 Phase 回顾正是此角色。

### 发现 3: JSON > Markdown 防止"宣告胜利"

200+ 粒度特征列表用 JSON boolean。"The model is less likely to inappropriately change or overwrite JSON files compared to Markdown files."

**含义**：tasks.md 的 `[x]` 标记容易被 Agent 随手改掉。可考虑结构化的 JSON 进度追踪。

### 发现 4: Sprint Contract 模式验证了 harness-constraints.md

开工前 Generator 和 Evaluator 协商明确的成功标准。

**含义**：S2 Harness 专家输出 `harness-constraints.md` → S6 Review 时注入作为评判基准 = Sprint Contract。

### 发现 5: 指令重注入每 ~10 轮

长会话中关键约束"褪色"。

**含义**：每个阶段开始时应重注入 Phase 目标和核心约束。CLAUDE.md 的一次性加载不够——特别是在 S5 多 batch 执行后。

### 发现 6: 3-5 个 Agent 是最优团队规模

超过后协调开销超过收益。

**含义**：S5 的 batch 大小应控制在 3-5 个并行 Agent。S2 的 4 角色审查恰好在最优范围内。

### 发现 7: Evaluator 需要调优，不是开箱即用

"Out-of-box evaluators perform poorly."

**含义**：S6 的 6 视角 Reviewer 需要精心设计的 prompt 和硬阈值，不能只给简单指令。S7 QA 同理——Playwright 测试需要明确的通过/失败标准。

### 发现 8: 失败路径文档化防止重蹈覆辙

"Without failed approaches, successive sessions will re-attempt the same dead ends."

**含义**：decisions.md（记录被拒绝的替代方案）和 tech-debt.md 部分起了此作用。但需要更系统化——特别是跨 Phase 的失败路径传递。

### 发现 9: Ralph Loop 模式

完成前反复质询"真的完了吗？"

**含义**：S8.3 PM 确认清单本质上就是 Ralph Loop。可以增加 Harness 专家版本的 Ralph Loop——"内核集成真的没问题吗？"

### 发现 10: 瓶颈从生成转到验证

"The bottleneck has shifted from generation to verification."

**含义**：S6 Review 和 S7 QA 不是"开销"，而是**主要质量机制**。增加 Harness 专家的验证触点是投资验证能力，不是增加官僚层级。
