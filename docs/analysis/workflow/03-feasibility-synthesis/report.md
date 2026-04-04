# 阶段 3：可行性综合分析 — 基于团队分析 + Claude 实验数据

> 日期: 2026-03-30
> 输入: 阶段 1 四角色团队分析 + 阶段 2 Claude 官方 17 篇实验
> 输出: 可行性评估 + 分层改进方案 + 硬阻塞门设计

---

## 目录

1. [综合方法论](#1-综合方法论)
2. [项目自身实验数据（Phase 0-2）](#2-项目自身实验数据phase-0-2)
3. [Claude 实验数据对四方建议的验证/挑战](#3-claude-实验数据对四方建议的验证挑战)
4. [结构性约束分析](#4-结构性约束分析)
5. [分层改进方案](#5-分层改进方案)
6. [硬阻塞门设计](#6-硬阻塞门设计)
7. [workflow-detailed.md 修改方案](#7-workflow-detailedmd-修改方案)
8. [风险与缓解](#8-风险与缓解)
9. [实施路线图](#9-实施路线图)

---

## 1. 综合方法论

本报告不是简单汇总四方建议，而是将团队分析与 Claude 官方实验数据交叉验证：

```
四方建议 × Claude 实验数据 = 验证/挑战矩阵
    ↓
项目自身实验数据（Phase 0-2）= 可行性约束
    ↓
分层改进方案（零成本 / 低成本 / 中期）
```

---

## 2. 项目自身实验数据（Phase 0-2）

三个 Phase 的执行数据暴露了几个**结构性约束**，这些约束决定了哪些改进方案可行、哪些不可行。

### 约束 1: 总负责人 = 执行者 = 跳步者

所有跳步都是总负责人（Claude）做的。这意味着**硬阻塞门是唯一有效的防护机制**。

| 防护类型 | 示例 | Phase 0-2 效果 |
|---------|------|--------------|
| 硬阻塞门 | qa-report.md 是 S8 入口条件 | **有效** — 引入后 QA 未再被跳过 |
| 规范写入 CLAUDE.md | PM 职责定义 | **无效** — Phase 2 tasks.md 仍未标记 |
| 角色完整性清单 | 12 角色核对 | **部分有效** — Phase 0/1 跳过 Harness 审查 |

**Claude 实验佐证**：
- Agent Drift 研究：指令在长 session 中"褪色"——CLAUDE.md 规范在 S5 多 batch 执行后可能已被遗忘
- Auto Mode Safety：机械检查（文件存在性）比语义判断更可靠
- Harness 设计：JSON boolean 比 Markdown checkbox 更抗篡改

**结论**：任何改进如果不是硬阻塞门，在 3 个 Phase 内大概率会被绕过。

### 约束 2: Agent 调用的真实成本

每个 Agent 调用 = 一次完整的 context window + 推理 + token 消耗。

| Phase 2 数据 | 效果 |
|-------------|------|
| S2 四角色并行审查（4 Agent） | 好 — 5 个关键设计决策输入 |
| S5 并行实施（多 background Agent） | 好 — 但 PM 跟踪 Agent 被遗漏 |
| S6 六视角 Review（6+ Agent） | 好 — 找到问题 |

**Claude 实验佐证**：
- Multi-Agent Research System: 多 Agent 比单 Agent 好 90.2%，但 token 用量是 15 倍
- Agent Teams: 3-5 个 teammate 最优，超过后协调开销超过收益
- Tool Search: 按需加载工具定义减少 85% token

**结论**：Agent 调用有效，但每多一个就有被"省略"的风险。应精选 2-3 个高价值触点，而非 8 阶段全覆盖。

### 约束 3: 文件是唯一可靠的跨 Phase 状态传递

| 机制 | Phase 0-2 效果 |
|------|--------------|
| memory 更新 | Phase 2.5 发现未更新 |
| workflow-detailed.md | 有效但被动 |
| decisions.md | Phase 0-2 缺失 |
| tech-debt.md | 存在但无跨 Phase 追踪 |

**Claude 实验佐证**：
- Long-Running Claude: 三层记忆（CLAUDE.md + CHANGELOG.md + Git）
- Memory Tool: "ALWAYS VIEW YOUR MEMORY DIRECTORY BEFORE DOING ANYTHING ELSE"
- Structured Note-Taking: 写入持久化笔记优于依赖 context 记忆

**结论**：Phase 回顾的首要价值不是"分析"而是"把结论写成文件"。文件才能跨 Phase 存活。

### 约束 4: Context Anxiety 与 Phase 天然重置点

**Claude 实验佐证**：
- Opus 4.5 在 ~80K tokens 时"提前收工"
- Opus 4.6 基本消除此问题
- 科学计算案例: 连续 2+ 小时无需 reset

**结论**：我们的 Phase 是天然的 context 重置点——每个 Phase 可视为独立 session。但 Phase 内的 8 个阶段可能在单个 session 中执行，此时 drift 和 context anxiety 都是风险。

---

## 3. Claude 实验数据对四方建议的验证/挑战

### 3.1 被验证的建议

| 四方建议 | Claude 实验验证 | 验证强度 |
|---------|---------------|---------|
| 增加 S8 Phase 回顾 | Long-Running Harness: 失败路径文档化防止重蹈覆辙 | **强** |
| harness-constraints.md 作为 Sprint Contract | Harness Design: Generator+Evaluator 开工前协商成功标准 | **强** |
| S6 注入 harness-constraints.md | Multi-Agent Research: 详细任务描述避免重复工作 | **强** |
| 硬阻塞门而非规范约束 | Auto Mode Safety: 机械检查 > 语义判断 | **强** |
| PM 管"有没有"，Harness 管"对不对" | Harness Design: 分离 Generator 和 Evaluator 角色 | **强** |
| S0 检查上一 Phase 遗留项 | Long-Running Claude: session 启动先读 progress + git log | **强** |
| 7 维度结构化评分 | Demystifying Evals: outcome-based > process-based | **中** |

### 3.2 被挑战的建议

| 四方建议 | Claude 实验挑战 | 调整方向 |
|---------|---------------|---------|
| Harness 8 阶段全覆盖 | Agent Teams: 3-5 agent 最优，超过后递减 | 精选 2-3 个高价值触点 |
| 3 个硬阻塞门 | Best Practices: "Start simple, add complexity only when necessary" | 先 2 个门，观察效果后再加 |
| tasks.md `[x]` 标记 | Harness: JSON boolean 更抗篡改 | 中期考虑 JSON 进度追踪 |
| 每 Phase 都做深度回顾 | Context Engineering: compaction + note-taking 优于重复全量分析 | 每 Phase 轻量 + 每 2 Phase 深度 |
| CLAUDE.md 加更多规则 | Best Practices: CLAUDE.md 过长导致忽略重要规则 | 精简核心 + 角色指令按需加载 |

### 3.3 被增强的建议

| 四方建议 | Claude 实验增强 | 增强方向 |
|---------|---------------|---------|
| S6 增加 Harness 视角 | Reasoning Blindness: reviewer 应看产物不看解释 | Reviewer prompt 明确指示"不看 Agent 的说明，只看 diff" |
| PM 5.4 检查 | Ralph Loop: 完成前反复质询 | 增加"真的完了吗"验证步骤 |
| 指令重注入 | Agent Drift: 每 ~10 轮重注入 | 每个阶段开始时重注入 Phase 目标 |
| S7 QA | pass@k vs pass^k | 考虑多次运行测试，不是跑一次绿就算 |

---

## 4. 结构性约束分析

综合项目数据和 Claude 实验，提炼出 5 个不可违反的结构性约束：

### 约束 A: 硬门 > 软规范

任何关键检查点必须有文件存在性的机械检查作为入口条件。纯 CLAUDE.md 规范在 3 个 Phase 内会被绕过。

### 约束 B: 并行 > 串行

并行执行的 Agent 存活率远高于串行追踪的 Agent（Phase 2 数据：S2 并行审查成功 vs PM 串行追踪被遗漏）。新触点应设计为与现有阶段并行。

### 约束 C: 文件 > 记忆

跨 Phase 的知识传递必须通过文件。Memory 更新被遗漏，但文件（如 qa-report.md）被硬门保护后 100% 存在。

### 约束 D: 精选 > 全覆盖

Agent Teams 的 3-5 最优规模 + "Start simple" 原则 = 精选 2-3 个高价值触点，而非 8 阶段全覆盖。

### 约束 E: 改 Prompt > 加 Agent

零成本改进（改现有 Agent 的 prompt）优先于增加新 Agent 调用。85% 的 token 节省来自按需加载而非预加载全部。

---

## 5. 分层改进方案

### 第一层：零成本改进（立即落地，不加 Agent）

| # | 改进 | 怎么做 | 依据 |
|---|------|--------|------|
| Z1 | S2 输出结构化 | Harness Agent prompt 改为产出 `harness-constraints.md`（建议 + 集成风险评估 + 可执行性评估） | Sprint Contract 模式 |
| Z2 | S6 注入 Harness 上下文 | 6 视角 Reviewer prompt 注入 `harness-constraints.md`，增加"内核集成"检查维度 | Reasoning Blindness + 详细任务描述 |
| Z3 | S6 Reviewer 只看 diff 不看解释 | Reviewer prompt 增加："直接审查代码变更，不参考 Agent 的自我描述" | Auto Mode Safety |
| Z4 | S0 跳过记录 | 角色完整性清单增加"上一 Phase 是否跳过该角色"字段 | Agent Drift 预防 |
| Z5 | 每阶段开始时重注入 Phase 目标 | 在 S3/S5/S6/S8 开始时明确重述 Phase 目标和 harness-constraints.md 关键约束 | 指令重注入每 ~10 轮 |

**总成本**：0 个新 Agent 调用，0 分钟额外等待。

### 第二层：低成本改进（加 1-2 个 Agent 调用）

| # | 改进 | 怎么做 | 硬门 | 额外耗时 |
|---|------|--------|------|---------|
| L1 | S4 tasks 审查 | Speckit 生成后派发 1 个 Harness Agent 检查内核集成任务 + Speckit 合规 | harness-constraints.md 中必须任务是否出现在 tasks.md | ~5 min（与 analyze 并行） |
| L2 | S8 Phase 回顾 | S8.2 与文档工程师并行派发 1 个 Harness Agent 做 7 维度回顾 | `harness-review-report.md` 为 S8 完成条件 | ~15 min（与 8.2 并行） |

**总成本**：2 个新 Agent 调用。由于都与现有阶段并行，关键路径增加 ~5 分钟（仅 L1）。

### 第三层：中期改进（Phase 4+ 再考虑）

| # | 改进 | 前提条件 | 依据 |
|---|------|---------|------|
| M1 | S5 内核组件 batch 抽检 | 需先定义"内核组件文件列表" | Agent Teams: 按需触发 |
| M2 | tasks.md → JSON 进度追踪 | 需改 Speckit 输出格式 | Harness: JSON > Markdown 防篡改 |
| M3 | 入口/出口条件脚本化 | 需 DevOps 开发检查脚本 | Auto Mode: 机械检查最可靠 |
| M4 | 跨 Phase 流程性能度量 | 需 3+ Phase 基线数据 | Demystifying Evals: 量化指标 |
| M5 | 每 2 Phase 深度 Harness 审计 | 需积累足够历史数据 | Long-Running: 定期全面评估 |
| M6 | CLAUDE.md 精简 + 角色指令按需加载 | 需改造为 Skill 文件 | Agent Skills: 渐进式披露 |

---

## 6. 硬阻塞门设计

基于"最小有效防护"原则，只增加 2 个硬门：

### 门 1: 阶段 2 出口（已有但细化）

```
阶段 2 出口条件:
[ ] 架构师 agent 返回 ✓
[ ] Roadmap 规划师 agent 返回 ✓
[ ] Harness 专家 agent 返回 ✓ → 产出 specs/{branch}/harness-constraints.md
[ ] PM agent 返回 ✓
→ 4/4 才进入阶段 3。3/4 或以下拒绝进入。
```

**依据**：
- Phase 0/1 都因跳过 Harness 审查导致后续 P0
- 机械检查（4 个 Agent 是否全部返回）不需要语义判断
- harness-constraints.md 作为 Sprint Contract 贯穿后续阶段

### 门 2: 阶段 8 入口（新增 1 项）

```
阶段 8 入口检查:
[ ] specs/{branch}/qa-report.md 存在 ← 已有
[ ] specs/{branch}/tech-debt.md 存在 ← 已有
[ ] specs/{branch}/harness-review-report.md 存在 ← 新增
→ 全部存在才进入阶段 8 合并流程
```

**依据**：
- harness-review-report.md 在 S8.2 与文档工程师并行产出，不增加关键路径
- 文件存在性检查是最可靠的机械检查
- 确保 Phase 回顾不被跳过

### 为什么不在 S4 出口加硬门

Phase 0-2 中 S4 从未被跳过（Speckit 被绕过是在 S1，不是 S4）。S4 的 Harness 审查是增量价值，追加任务到 tasks.md 即可，不需要硬门。如果 Phase 3 证明 S4 也会被跳过，再升级为硬门。

---

## 7. workflow-detailed.md 修改方案

### 阶段 0 修改

```diff
 **角色完整性检查清单**:
+注意: 对每个角色检查"上一 Phase 是否被跳过"。连续 2 Phase 跳过标记红色。
+
+**连续性检查**（如果不是首个 Phase）:
+[ ] 上一 Phase harness-review-report.md 中的"必须修复"项已纳入本 Phase 范围
+[ ] 上一 Phase tech-debt.md 中标记"下一 Phase 修复"的项已纳入 tasks 讨论范围
```

### 阶段 2 修改

```diff
 **操作步骤**:
 1. 并行派发 4 个 agent:
    Agent(name=architect):      "审查 spec.md，从技术架构角度给出 5-10 条修改建议"
    Agent(name=roadmap):        "审查 spec.md，从范围/PRD V4 对齐角度给出 5-10 条修改建议"
-   Agent(name=harness-expert): "审查 spec.md，从 SLE 内核集成角度给出 5-10 条修改建议"
+   Agent(name=harness-expert): "审查 spec.md，产出结构化报告:
+     (a) 从 SLE 内核集成角度的 5-10 条修改建议
+     (b) 集成风险评估（高/中/低）
+     (c) 本 Phase 必须验证的内核约束清单
+     (d) 工作流可执行性评估（这个 spec 能否走完 8 阶段？哪里可能卡住？）
+     产出文件: specs/{branch}/harness-constraints.md"
    Agent(name=pm):             "审查 spec.md，从用户故事/验收标准角度给出 5-10 条修改建议"

-**出口条件**: 4 个 agent 全部返回
+**出口条件**: 4 个 agent 全部返回（逐一确认）+ harness-constraints.md 已产出
```

### 阶段 4 新增步骤

```diff
 4. 执行: `/speckit.analyze specs/{branch}`
    - 检查 spec/plan/tasks 一致性
+5. Harness 专家审查 tasks.md（1 个 Agent 调用，与 analyze 并行或之后）:
+   - 检查 harness-constraints.md 中的"必须验证"任务是否出现在 tasks.md
+   - 检查 tasks.md 是否由 Speckit 生成（非手写）
+   - 如缺失内核集成任务，追加到 tasks.md
+   - 此步骤不需要硬阻塞门，缺失直接追加即可
```

### 阶段 5 修改（指令重注入）

```diff
 5.1 总负责人: 分析 tasks.md 依赖关系，找出当前可并行的 batch
+    重注入: 重述 Phase 目标 + harness-constraints.md 中的关键约束
 5.2 总负责人: 派发 Agent(run_in_background=true) × N
```

### 阶段 6 修改

```diff
 Round 1 (全量):
-6.1 派发 6 视角 review Agent(subagent_type=Explore)
+6.1 派发 6 视角 review Agent(subagent_type=Explore)
+    所有 Reviewer 的 prompt 注入 specs/{branch}/harness-constraints.md 作为审查基准
+    增加检查维度: "实现是否违反 Harness 专家定义的内核约束？"
+    Reviewer 指令: "直接审查代码变更和测试覆盖，不参考 Agent 对自身工作的描述"
```

### 阶段 8 修改

```diff
 8.0 入口检查（硬阻塞门）:
     [ ] specs/{branch}/qa-report.md 存在 → 否则拒绝进入，回到阶段 7
     [ ] specs/{branch}/tech-debt.md 存在 → 否则回到阶段 6
+    [ ] specs/{branch}/harness-review-report.md 存在 → 否则执行 Harness Phase 回顾

 8.2 总负责人派发收尾任务（并行）:
     - 总负责人自己:
       a) 更新 memory
       b) 更新 roadmap plan
     - 派发文档工程师:
       a-e) [现有 5 项]
+    - 派发 Harness 专家 Phase 回顾:
+      输入: harness-constraints.md + tasks.md + tech-debt.md + qa-report.md + git log
+      检查 7 个维度（绿/黄/红）:
+        A. 工作流完整性（8 阶段是否全执行）
+        B. Speckit 合规（是否由 Speckit 生成）
+        C. 角色完整性（12 角色是否全参与）
+        D. 内核集成健康度（核心组件是否退化）
+        E. 文档完整度（标准文件是否齐全）
+        F. 反馈闭环（上一 Phase 改进建议是否被执行）
+        G. Tech Debt 趋势（新增 vs 解决）
+      产出: specs/{branch}/harness-review-report.md
+      包含: "必须在下一 Phase 修复"项（不超过 3 条）
```

### CLAUDE.md 对应修改

角色完整性检查中 Harness 专家职责扩展：
```diff
-[ ] Harness 专家 — 阶段 2 审查 spec
+[ ] Harness 专家 — 阶段 2 审查 spec（→ harness-constraints.md）+ 阶段 4 tasks 审查 + 阶段 8.2 Phase 回顾（→ harness-review-report.md）
```

文档职责矩阵增加行：
```diff
+| 阶段 2 审查 | **Harness 专家** | `harness-constraints.md`（结构化审查 + 内核约束清单） |
+| 阶段 4 Tasks | **Harness 专家** | tasks.md 内核集成任务审查签字 |
+| 阶段 8 合并后 | **Harness 专家** | `harness-review-report.md`（7 维度评分 + 必须修复项） |
```

---

## 8. 风险与缓解

### 风险 1: harness-review-report.md 变成走过场

**概率**: 中（历史上 decisions.md 和 phase-report.md 都曾流于形式）

**缓解**:
- 7 维度评分强制绿/黄/红三选一——不允许"N/A"或空白
- "必须修复"项不超过 3 条——聚焦防止敷衍
- 下一 Phase S0 的连续性检查验证这些项是否被纳入

### 风险 2: Harness 专家 Agent 的 prompt 质量不够

**概率**: 高（Claude 实验明确指出"out-of-box evaluators perform poorly"）

**缓解**:
- Phase 3 作为试点，观察 harness-constraints.md 和 harness-review-report.md 的质量
- 如果质量不够，迭代 prompt（"several rounds of development loop reading logs and updating prompts"）
- 参考 Sprint Contract 模式设计明确的成功标准

### 风险 3: 新增 2 个 Agent 调用被省略

**概率**: 低（L1 与 S4 并行；L2 与 S8.2 并行且有硬门保护）

**缓解**:
- L2（Phase 回顾）有硬门保护——harness-review-report.md 是 S8 入口
- L1（S4 tasks 审查）无硬门，但直接追加到 tasks.md，即使被跳过也只影响任务完整性，不影响流程

### 风险 4: CLAUDE.md 继续膨胀

**概率**: 高（每次改进都往 CLAUDE.md 加规则）

**缓解**:
- 中期（M6）将角色指令拆为 Skill 文件，按需加载
- 当前修改控制在最小范围：角色检查清单 1 处 + 文档职责矩阵 3 行

---

## 9. 实施路线图

### Phase 3（立即）

```
优先级 P0（必须）:
  Z1: S2 Harness prompt 结构化 → harness-constraints.md
  Z2: S6 Reviewer 注入 harness-constraints.md
  L2: S8.2 Phase 回顾 → harness-review-report.md（硬门）
  Z4: S0 角色跳过记录

优先级 P1（应该）:
  L1: S4 tasks 审查
  Z3: S6 Reviewer "只看 diff"
  Z5: 每阶段重注入 Phase 目标
```

### Phase 4-5（观察 Phase 3 效果后）

```
如果 Phase 3 的 harness-constraints.md 质量好:
  → M1: S5 内核组件 batch 抽检
  → M5: 每 2 Phase 深度审计

如果 Phase 3 的 tasks.md 标记仍不可靠:
  → M2: JSON 进度追踪

如果 Phase 3 的 CLAUDE.md 已过长:
  → M6: 角色指令拆为 Skill 文件
```

### Phase 6+（远期）

```
M3: 入口/出口条件脚本化
M4: 跨 Phase 流程性能度量
工作流参数可配置化（简单 Phase 简化，复杂 Phase 加固）
```

---

## 附录: 关键 Claude 实验引用

| 实验 | 关键数据点 | 引用位置 |
|------|-----------|---------|
| Multi-Agent Research System | 多 Agent 比单 Agent 好 90.2% | §3.1 验证 |
| Agent Drift | pass@1=90% 但 pass^5=59%；记忆提取率 13.1% | §4 约束分析 |
| Harness Design | "agents confidently praise own work"；JSON > Markdown | §3.2 挑战 |
| Long-Running Claude | 三层记忆；失败路径文档化；Ralph Loop | §3.1 验证 |
| Demystifying Evals | pass@k vs pass^k；outcome-based > process-based | §3.3 增强 |
| Auto Mode Safety | Reasoning Blindness 原则 | §5 零成本改进 Z3 |
| Agent Teams | 3-5 最优；Plan approval gate | §4 约束 D |
| Context Engineering | 指令重注入每 ~10 轮；CLAUDE.md 过长导致忽略 | §5 Z5, §8 风险 4 |
| Best Practices | "Start simple"；验证是最高杠杆 | §4 约束 D |
| Infrastructure Noise | 6% 基准偏差 | §3.3 S7 QA 增强 |
