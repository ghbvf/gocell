# 多角色团队调度工作流 — 详细操作手册 v4.2

## 概述

每个 Phase 严格按 9 个阶段（S0-S8）执行。本文档定义每个阶段的**具体操作步骤**、**使用的工具/命令**、**产出物**和**进入下一阶段的检查项**。

**绝对禁止跳步。** 每个阶段有明确的入口条件和出口条件，未满足不得进入下一阶段。每个阶段出口必须通过 `phase-gate-check.sh --stage SN --check exit = PASS` 才可进入下一阶段。

---

## 角色体系（三层分组）

### Core Mandatory（每 Phase 必须在场）

| 角色 | 职责 | 参与阶段 |
|------|------|----------|
| 总负责人 | 调度+裁决+合并 | S0/S1/S2/S3/S5/S6/S8 |
| 架构师 | 技术架构审查+S6 裁决升级 | S2/S6 |
| 产品经理 | 产品上下文+验收标准+产品评审+产品验收 | S0/S2/S4/S7/S8 |
| 项目经理 | 依赖分析+batch 划分+进度跟踪+流程确认 | S4/S5/S8 |
| Kernel Guardian | kernel-constraints+tasks 审查+Phase 回顾 | S2/S4/S8 |

### Conditional Delivery（按需，S0 时确认本 Phase 是否启用）

| 角色 | 职责 | 启用条件 |
|------|------|----------|
| 后端开发者 | S5 实施 | 几乎每 Phase 启用 |
| 前端开发者 | S5 实施 | 有 UI 变化时启用 |
| 文档工程师 | S5 文档 + S8.2 收尾 | 几乎每 Phase 启用 |
| DevOps | S5 部署配置 + S7.0 测试环境 | 几乎每 Phase 启用 |
| QA 自动化 | S5 编写测试 + S7 执行测试 | 几乎每 Phase 启用 |

### Review Bench（6 个命名席位，S6 审查）

| 席位 | 审查焦点 |
|------|----------|
| 架构一致性 Reviewer | DDD 分层、聚合边界、模块耦合 |
| 安全/权限 Reviewer | 认证鉴权、数据暴露、攻击面、供应链 |
| 测试/回归 Reviewer | 测试覆盖、回归风险、边界用例 |
| 运维/部署 Reviewer | Docker/CI 配置、migration 安全、环境一致性 |
| DX/可维护性 Reviewer | 代码可读性、命名、复杂度、文档内链 |
| 产品/用户体验 Reviewer | 交互流程、错误提示、空状态、用户路径 |

### Governance（治理层）

| 角色 | 职责 | 参与阶段 |
|------|------|----------|
| 使用者 | S7 四视角验证 | S7 |
| Roadmap 规划师 | S2 范围审查 + S8.2 roadmap 更新 | S2/S8 |

### Agent 定义 vs SKILL 的职责分离

**Agent 定义文件**（`.claude/agents/*.md`）= 角色身份 + 领域知识 + 评判框架 + 行为约束。
**SKILL 文件**（`.claude/skills/stage-*/SKILL.md`）= 工作流编排 + 派发指令 + 输入输出路径 + 文件模板。

Agent 不包含阶段标签（S2/S4/S8）、文件路径（`specs/{branch}/...`）或报告模板。这些由 SKILL 在派发 prompt 中指定。Agent 被 SKILL 派发时自动加载其定义文件作为角色上下文，SKILL 的派发 prompt 补充"现在做什么"。

以下角色有独立 agent 定义文件：架构师、Kernel Guardian、产品经理、项目经理、文档工程师、DevOps、Roadmap 规划师、6 席位 Reviewer。

以下角色通过 SKILL.md 内联 prompt 派发，无独立 agent 文件：
- **后端/前端开发者** — S5 实施任务，prompt 由总负责人在 stage-5 SKILL.md 中按 batch 动态构造
- **QA 自动化** — S5 编写测试 + S7 执行测试，prompt 嵌入 stage-5/stage-7 SKILL.md
- **使用者** — S7 四视角验证，验证清单嵌入 stage-7 SKILL.md

设计理由：开发者/QA/使用者的 prompt 高度依赖具体 Phase 的任务内容（tasks.md、AC 编号等），固化为 agent 定义文件反而降低灵活性。

### SKILL 自动调用策略

所有 stage SKILL 和 phase-gate SKILL **不设置** `disable-model-invocation`。

原因：工作流由总负责人自动驱动 S0→S8，每个 stage SKILL 应能被 AI 自动调用，不需要人工手动输入 `/stage-N` 触发。加了 `disable-model-invocation: true` 反而打断自动化流程。

phase-gate 同理 — 它是每个阶段转换时 AI 自动调用的验证工具，限制自动调用会导致阶段门形同虚设。

### 跳过警告枚举（phase-charter.md 中使用）

当某角色或产出物标记为跳过时，必须使用以下枚举值之一：

| 枚举值 | 含义 | 示例 |
|--------|------|------|
| `SCOPE_IRRELEVANT` | 本 Phase 范围不涉及 | 纯后端 Phase 跳过前端开发者 |
| `RESOURCE_UNAVAILABLE` | 资源不可用但应该参与 | 标记为 tech-debt 下一 Phase 补做 |
| `DEFERRED` | 延迟到后续 Phase | 文档工作推迟到下一 Phase |

禁止使用自由文本理由。phase-gate-check.sh 读取 phase-charter.md 中的 N/A 声明时验证枚举值。

---

## 阶段 0: 启动

**执行者**: 总负责人 + 产品经理

**操作步骤**:
1. 确认本 Phase 的目标（1-2 段描述）
2. 确认与 roadmap 的关系（是哪个 Phase，前置条件是否满足）
3. 执行角色完整性检查（三层分组核对）
4. 确认 Conditional Delivery 角色本 Phase 启用状态（逐一标注 ON/OFF + 枚举理由）
5. 派发产品经理产出 `specs/{branch}/product-context.md`

**产品经理 Agent prompt 必须注入**:
- Phase 目标描述
- PRD
- 上一 Phase `product-review-report.md`（如存在）

产品经理产出 `product-context.md`（persona + 成功标准 + 范围边界）。

**角色完整性检查清单**:
```
Core Mandatory（必须全部 ON）:
[ ] 总负责人 — 调度+裁决+合并
[ ] 架构师 — S2 技术架构审查 + S6 裁决升级
[ ] 产品经理 — S0 产品上下文 + S2 验收标准审查 + S4 产品验收清单 + S7 使用者验证输入 + S8.2 产品评审 + S8.3-A 产品验收确认
[ ] 项目经理 — S4 依赖分析 + S5.1 batch 划分 + S5.4 进度跟踪 + S8.3-B 流程完成确认
[ ] Kernel Guardian — S2 kernel-constraints + S4 tasks 审查 + S8.2 Phase 回顾

Conditional Delivery（逐一标注 ON/OFF + 枚举理由）:
[ ] 后端开发者 — S5 实施  → ON / OFF（理由: SCOPE_IRRELEVANT|RESOURCE_UNAVAILABLE|DEFERRED）
[ ] 前端开发者 — S5 实施  → ON / OFF（理由: SCOPE_IRRELEVANT|RESOURCE_UNAVAILABLE|DEFERRED）
[ ] 文档工程师 — S5 文档 + S8.2 收尾  → ON / OFF（理由: SCOPE_IRRELEVANT|RESOURCE_UNAVAILABLE|DEFERRED）
[ ] DevOps — S5 部署配置 + S7.0 测试环境  → ON / OFF（理由: SCOPE_IRRELEVANT|RESOURCE_UNAVAILABLE|DEFERRED）
[ ] QA 自动化 — S5 编写测试 + S7 执行测试  → ON / OFF（理由: SCOPE_IRRELEVANT|RESOURCE_UNAVAILABLE|DEFERRED）

Review Bench（6 个命名席位，S6 全部参与）:
[ ] 架构一致性 Reviewer
[ ] 安全/权限 Reviewer
[ ] 测试/回归 Reviewer
[ ] 运维/部署 Reviewer
[ ] DX/可维护性 Reviewer
[ ] 产品/用户体验 Reviewer

Governance:
[ ] 使用者 — S7 四视角验证
[ ] Roadmap 规划师 — S2 范围审查 + S8.2 roadmap 更新
```

**跳过记录检查**:
对每个角色核对"上一 Phase 是否被跳过"。连续 2 Phase 跳过同一角色标记红色警告。

**连续性检查**（Phase 1+ 执行，首个 Phase 跳过）:
```
[ ] 上一 Phase `kernel-review-report.md` 中的"必须修复"项已纳入本 Phase 范围
[ ] 上一 Phase `product-review-report.md` 中的"必须修复"项已纳入本 Phase 范围
[ ] 上一 Phase `tech-debt.md` 中标记"下一 Phase 修复"的项已纳入讨论范围
```

**过载保护**: 三份文件"必须修复"项合计超过 9 条时触发红色警告，总负责人必须裁决优先级。

**产出物**: `specs/{branch}/phase-charter.md`（Phase 目标 + 范围 + 非目标 + 连续性处理结论 + Scope N/A 声明） + `specs/{branch}/role-roster.md`（角色启用清单） + `specs/{branch}/product-context.md`

**出口条件**: `phase-gate-check.sh --stage S0 --branch {branch} --check exit = PASS`

---

## 阶段 1: Speckit Specify

**执行者**: 总负责人触发

**入口条件**: `phase-gate-check.sh --stage S1 --branch {branch} --check entry = PASS`

**操作步骤**:
1. 创建并切换到 feature branch: `git checkout -b feat/{number}-{short-name} main`
3. 执行: `/speckit.specify 描述`（将 product-context.md 作为上下文输入）
4. 检查生成的 spec.md，确认:
   - FR 中包含文档需求（"系统必须提供 API 文档"）
   - FR 中包含 DevOps 需求（"系统必须更新 Docker/CI 配置"）
   - FR 中包含测试需求（"系统必须提供 E2E 测试"）

**产出物**: `specs/{branch}/spec.md` + `specs/{branch}/checklists/requirements.md`

**出口条件**: `phase-gate-check.sh --stage S1 --branch {branch} --check exit = PASS`

**注**: product-context.md 已在 S0 出口保证存在，S1 不再重复检查。

---

## 阶段 2: 4 角色并行审查 spec

**执行者**: 总负责人派发 4 个 Agent（使用各 agent 定义文件中声明的工具权限）

**入口条件**: `phase-gate-check.sh --stage S2 --branch {branch} --check entry = PASS`

**操作步骤**:
1. 并行派发 4 个 agent:
   ```
   Agent(name=architect):       "审查 spec.md，从技术架构角度给出 5-10 条修改建议"
                                 产出: review-architect.md
   Agent(name=roadmap):         "审查 spec.md，从范围/PRD 对齐角度给出 5-10 条修改建议"
                                 产出: review-roadmap.md
   Agent(name=kernel-guardian):  "审查 spec.md，产出结构化报告 specs/{branch}/kernel-constraints.md:
     (a) 从 GoCell 内核集成角度的 5-10 条修改建议
     (b) 集成风险评估（高/中/低）
     (c) 本 Phase 必须验证的内核约束清单
     (d) 工作流可执行性评估（spec 能否走完 8 阶段？哪里可能卡住？）"
                                 产出: kernel-constraints.md
   Agent(name=product-manager): "读取 spec.md + product-context.md，从验收标准/用户故事角度给出 5-10 条修改建议。
     每条标注类别: [验收标准缺失] [用户体验] [范围偏移] [优先级质疑]"
                                 产出: review-product-manager.md
   ```
2. **等待全部 4 个 agent 返回**（不可提前进入下一步）
3. 汇总 4 份修改建议

**产出物**: `specs/{branch}/review-architect.md` + `specs/{branch}/review-roadmap.md` + `specs/{branch}/kernel-constraints.md` + `specs/{branch}/review-product-manager.md`

**出口条件**: `phase-gate-check.sh --stage S2 --branch {branch} --check exit = PASS`

---

## 阶段 3: 综合裁决 + 更新 Spec + 记录决策

**执行者**: 总负责人

**入口条件**: `phase-gate-check.sh --stage S3 --branch {branch} --check entry = PASS`

**操作步骤**:
1. **指令重注入**: 重述 Phase 目标 + kernel-constraints.md 中的关键约束
2. 逐条审查 4 方建议，对每条做裁决（采纳/拒绝/延迟）
3. 使用 `/speckit.clarify` 将采纳的建议编码回 spec.md（不手动修改 spec）
4. 写 `specs/{branch}/decisions.md`（ADR 格式）:
   - 每个重要决策: 决策内容 + 理由 + 被否决的替代方案
   - 延迟到后续 Phase 的项目列表
   - 对 kernel-constraints.md 中每条建议的 accept/reject/defer 标记

**产出物**: 更新后的 spec.md + decisions.md

**出口条件**: `phase-gate-check.sh --stage S3 --branch {branch} --check exit = PASS`

---

## 阶段 4: Speckit Plan → Tasks → Analyze + Kernel Guardian 审查 + 产品验收清单 + 任务依赖分析

**执行者**: 总负责人触发 Speckit 技能 + Kernel Guardian审查 + 产品经理 + 项目经理

**入口条件**: `phase-gate-check.sh --stage S4 --branch {branch} --check entry = PASS`

**操作步骤**:
1. 执行: `/speckit.plan specs/{branch}`
   - 生成 plan.md + research.md + data-model.md + contracts/
2. 执行: `/speckit.tasks specs/{branch}`
   - 生成 tasks.md（含 [P] 并行标记 + 依赖关系）
3. 检查 tasks.md 是否包含（确认任务已生成且可执行，不只是需求声明）:
   - [ ] 文档工程师的任务（OpenAPI/用户文档/部署文档）
   - [ ] DevOps 的任务（Docker/CI/部署配置更新）
   - [ ] QA 自动化的任务（Playwright E2E 测试编写 — **在 S5 执行**）
   - 如缺失，手动追加（这是唯一允许手动修改 tasks.md 的场景）
4. 执行: `/speckit.analyze specs/{branch}`
   - 检查 spec/plan/tasks 一致性
5. **Kernel Guardian审查 tasks.md**（与 analyze 并行或之后）:
   - 对照 `kernel-constraints.md` 中"必须验证"的约束，检查对应任务是否出现在 tasks.md
   - 检查 tasks.md 是否由 Speckit 生成（非手写）
   - 如缺失内核集成验证任务，追加到 tasks.md
6. **前置条件: Kernel Guardian 审查（步骤 5）已完成。** 完成后才派发以下 2 个 agent:
   ```
   Agent(name=product-manager): "读取 spec.md + tasks.md + product-context.md，产出 specs/{branch}/product-acceptance-criteria.md:
     - 每条 FR/User Story 的验收标准（AC）
     - 每条 AC 标注优先级: P1（核心功能）/ P2（增强功能）/ P3（基础设施）
     - AC 到 tasks.md 任务的映射关系
     - 每条 AC 的验证方式标注: [E2E 测试] [手动验证] [代码审查] [集成测试]
     - 标注"手动验证"的 AC 翻译为步骤化操作指南（供 S7 使用者使用）"
   Agent(name=project-manager): "读取 tasks.md + kernel-constraints.md，产出 specs/{branch}/task-dependency-analysis.md:
     - 任务依赖图（哪些任务阻塞哪些）
     - 关键路径识别
     - 风险点标注（阻塞概率/影响范围）
     - batch 预估（建议分几个 batch + 每 batch 包含哪些任务）"
   ```

**产出物**: plan.md + tasks.md + research.md + 一致性报告 + product-acceptance-criteria.md + task-dependency-analysis.md

**出口条件**: `phase-gate-check.sh --stage S4 --branch {branch} --check exit = PASS`

**注**: task-dependency-analysis.md 从 S4 硬出口移到 S5.1 入口条件。如 S4 中项目经理已完成则直接使用；如未完成则在 S5.1 之前完成。Fallback: 如果 task-dependency-analysis.md 不存在，直接使用 tasks.md 内嵌依赖图进行 batch 划分。

**tasks.md 必须包含的非代码任务（QA 环境准备 + 测试编写）**:
```
[ ] DevOps: 安装 Playwright + playwright.config.ts（首次 Phase 做，后续复用）
[ ] DevOps: docker-compose.test.yml（PG + GoCell App 测试环境）
[ ] 后端开发者: 种子数据脚本（seed-test-data.sh 或 Go test helper）
[ ] 文档工程师: OpenAPI spec + 用户文档更新
[ ] DevOps: Docker/CI 配置更新
[ ] QA 自动化: Playwright E2E 测试编写（3 用户视角，在 S5 执行）
```

这些任务在阶段 5 执行，确保阶段 7 QA agent 有可执行的环境和测试脚本。

**禁止**: 手写 tasks.md（Speckit 生成，仅允许追加缺失的上述任务和内核集成验证任务）

---

## 阶段 5: 并行实施（项目经理规划 + 总负责人派发 + 项目经理跟踪）

**执行者**: 项目经理规划 batch + 总负责人派发 + 项目经理跟踪

**入口条件**: `phase-gate-check.sh --stage S5 --branch {branch} --check entry = PASS`

**契约先行 QA 规则**: 对含 API 变更的 batch，强制执行以下顺序:
```
1. 后端开发者先产出 OpenAPI 片段（contracts/ 目录更新）
2. QA 自动化基于 contract 编写/更新测试
3. 后端/前端实现业务逻辑
4. batch 末尾集成运行测试
```
S5 出口条件增加: 本 Phase 新增的 API endpoint 在 contracts/ 或 OpenAPI spec 中有对应定义。

**Playwright 配置要求**（仅 role-roster.md 中 QA自动化=ON 且存在 UI 组件时适用）: QA 自动化编写测试时，playwright.config.ts 必须包含:
```typescript
use: {
  trace: 'on',
  video: 'on-first-retry',
  screenshot: 'only-on-failure',
}
```

**操作步骤（实施循环）**:

```
5.1 项目经理:
    入口条件: task-dependency-analysis.md 存在（如不存在，使用 tasks.md 内嵌依赖图）
    基于 task-dependency-analysis.md 分析 tasks.md 依赖关系，划分当前可并行的 batch，提交 batch 方案给总负责人
    **契约先行**: 含 API 变更的 batch 必须标注 contract-first 顺序（contract → test → implement）
    **指令重注入**: 重述 Phase 目标 + kernel-constraints.md 中的关键约束（防止 Agent Drift）
5.2 总负责人: 审批 batch 方案，派发 Agent(run_in_background=true) x N
    - 后端开发者: Go 代码任务
    - 前端开发者: Vue 任务
    - 文档工程师: 文档任务
    - DevOps: 部署配置任务
    - QA 自动化: Playwright E2E 测试编写/更新
    **开发者 Agent 要求**:
    - 完成任务后自行运行 go build + go test 并在返回结果中报告 PASS/FAIL
    - **对标对比规则**: 新建或重构 kernel/、cells/、runtime/、adapters/ 下模块时，必须先查 `docs/references/framework-comparison.md` 找到对标文件路径，用 WebFetch 从 GitHub 拉取对标源码，提取关键设计决策，并在 handoff note 中注明 `ref: {framework} {file}` + 采纳/偏离理由
    **每个 Agent 完成后产出 handoff note**: 含 build/test 结果 + 变更摘要 + 已知风险 + 对标引用（如适用）
5.3 等待: batch 中所有 agent 完成
5.4 项目经理:
    - 读取每个开发者 Agent 返回结果中的 build/test PASS/FAIL 状态（不亲自运行）
    - 标记 tasks.md 中已完成任务 [x]
    - 报告: 完成数/总数 + 阻塞项 + 下一 batch 就绪状态
5.5 判断:
    - 还有未完成任务 → 回到 5.1
    - 全部完成 → 进入阶段 6
```

**产出物**: 代码 + 文档 + 部署配置 + `e2e/*.spec.ts`（QA 编写） + tasks.md 全部标记 [x] + 每 batch handoff note

**出口条件**: `phase-gate-check.sh --stage S5 --branch {branch} --check exit = PASS`（tasks.md 无未勾选项 + 新增 API 在 contracts/ 有定义）

---

## 阶段 6: Review-Fix 循环（最多 3 轮）

**执行者**: 总负责人派发 Reviewer + 开发者修复

**入口条件**: `phase-gate-check.sh --stage S6 --branch {branch} --check entry = PASS`

**操作步骤**:

```
Round 1 (全量):
6.1 派发 6 个命名席位 review Agent:
    - 架构一致性 Reviewer: DDD 分层、聚合边界、模块耦合
    - 安全/权限 Reviewer: 认证鉴权、数据暴露、攻击面、供应链
    - 测试/回归 Reviewer: 测试覆盖、回归风险、边界用例
    - 运维/部署 Reviewer: Docker/CI 配置、migration 安全、环境一致性
    - DX/可维护性 Reviewer: 代码可读性、命名、复杂度、文档内链
    - 产品/用户体验 Reviewer: 交互流程、错误提示、空状态、用户路径

    **Reviewer prompt 强制注入上下文**:
    - `specs/{branch}/kernel-constraints.md`（Kernel Guardian 约束基准）
    - `specs/{branch}/spec.md` 最新版（需求对照基准）
    - 增加检查维度: "实现是否违反 Kernel Guardian 定义的内核约束？"
    - Reviewer 自行执行 `git diff main...HEAD --stat` 获取变更范围（不由总负责人注入）
    - Reviewer 指令: "直接审查代码变更和测试覆盖，不参考 Agent 对自身工作的描述"

6.2 收集 findings → 产出 specs/{branch}/review-findings.md
    结构化格式: P0/P1/P2 分类 + 发现席位 + 受影响文件
    **新增字段**: "审查基准版本" = commit hash（git rev-parse HEAD）
6.3 总负责人裁决: 哪些修/哪些延迟
6.4 派发开发者修复 P0 + 选定的 P1
6.5 验证: build + test

Round 2 (聚焦):
6.6 派发聚焦 review（只检查修复 + 回归）
6.7 VERIFIED → 进入阶段 7; ISSUE → 修复 → Round 3

Round 3 (最终):
6.8 只检查 P0
6.9 总负责人: 将 P1+ 记录到 specs/{branch}/tech-debt.md
     tech-debt.md 格式要求:
     - 每条标注分类标签: [TECH] 或 [PRODUCT]
     - [TECH]: 技术债务（代码质量、架构退化、测试缺失）
     - [PRODUCT]: 产品债务（降级体验、缺失功能、临时方案）
     - 含: Reviewer 发现 + 总负责人裁决延迟的理由 + 建议修复时机
6.10 仍有 P0 → 升级到架构师裁决
```

**产出物**: 修复后的代码 + `specs/{branch}/review-findings.md` + `specs/{branch}/tech-debt.md`（P1+ 延迟项，含 [TECH]/[PRODUCT] 标签）

**出口条件**: `phase-gate-check.sh --stage S6 --branch {branch} --check exit = PASS`

---

## 阶段 7: QA + 使用者验证

**硬阻塞门**: 阶段 6 完成后**自动进入**阶段 7。不是总负责人决定是否执行。`qa-report.md` 不存在则阶段 8 拒绝进入。

**执行者**: DevOps（环境部署）+ QA Agent（执行测试）+ 使用者（四视角验证）

**入口条件**: `phase-gate-check.sh --stage S7 --branch {branch} --check entry = PASS`

**操作步骤**:

```
7.0 DevOps: 部署测试环境
    - 使用 docker-compose.test.yml 启动 PG + GoCell App 测试环境
    - 确认 Playwright 已安装且配置可用（playwright.config.ts）
    - 确认 playwright.config.ts 包含 trace: 'on', video: 'on-first-retry', screenshot: 'only-on-failure'
    - 确认种子数据已加载（seed-test-data.sh 或 Go test helper）
    - 产出: 测试环境就绪确认

7.1 QA Agent: 执行测试（测试脚本已在 S5 编写完成）
    - 运行: go test ./... -v -count=1
    - 运行: gocell validate（元数据验证）
    - 运行: gocell verify --journeys（Journey 验收测试）
    - 运行: npx playwright test（仅 role-roster.md 中 QA自动化=ON 且存在 UI 组件时执行；否则标注 N/A）
    - 收集结果
    - 补充回归脚本（如发现 S5 遗漏的场景）
    - **证据采集**: 确认 specs/{branch}/evidence/playwright/ 目录包含 trace + screenshots
    - 证据路径: specs/{branch}/evidence/playwright/（trace zip + screenshot png）

7.2 使用者验证（按四个 Persona 视角顺序执行）:

    视角 A — PM（浏览器 UI 全流程）:
    A1. 项目列表页加载 → 3 秒内渲染 + 表头含义清晰
    A2. 创建项目 → 成功反馈 + 列表自动刷新
    A3. 点击项目 → 导航到 Runs 页 + URL 含 project_id
    A4. 审批中心 → 过滤器可用 + 空状态有意义
    A5. 导航栏切换 → 三入口可用 + 当前位置高亮
    A6. 整体: PM 能否通过 UI 回答"项目状态如何"

    视角 B — 开发者（UI + API 混合）:
    B1. 提交 Run 表单 → 字段有标记 + 必填项提示
    B2. API POST /api/v1/runs → 201 + run_id + < 1s
    B3. Run 列表 → 新 Run 出现 + SSE 状态实时更新
    B4. 任务详情 → 元数据卡片 + 时间线
    B5. SSE 连接状态指示器 → 颜色/文案一致
    B6. 访问不存在的 task → 404 清晰 + 返回入口

    视角 C — Vibe Coder（纯 API）:
    C1. GET /health → 200
    C2. POST /projects → 信封格式 {"data":{...}}
    C3. GET /projects?page=1&pageSize=5 → data/total/page/pageSize
    C4. GET /events/stream → SSE + text/event-stream
    C5. POST 不存在的审批 → 标准错误格式
    C6. 跨端点分页格式一致性
    C7. 整体: API 能否支撑脚本化自动化

    视角 D — 框架集成者（Go 开发者首次接入）:
    D1. go get 安装 → 无 replace/vendor 异常，依赖干净
    D2. godoc 可读性 → 导出类型/函数有清晰注释，package doc.go 存在
    D3. examples/ 可运行 → go run 或 docker-compose up 一键启动，README 步骤完整
    D4. Cell/Slice 脚手架 → gocell scaffold cell/slice 产出可编译骨架
    D5. 错误信息可定位 → errcode 错误码帮助开发者定位问题
    D6. 整体: 新开发者能否在 30 分钟内跑通一个 example 并理解 Cell 模型

    每视角评分 1-5:
    1=不可用 2=有明显摩擦 3=可接受 4=流畅 5=优秀

    产出: specs/{branch}/user-signoff.md
    判定: APPROVE(均>=4无P0) / CONDITIONAL(均>=3) / REJECT(任一<3或P0)

7.3 QA 写 qa-report.md:
    - Playwright 测试范围和结果
    - 覆盖的用户场景
    - 未覆盖的场景（记录原因）
    - 手动验证结论
    - 引用 product-acceptance-criteria.md 中的 AC 编号
    - **每条结论必须引用证据路径**（specs/{branch}/evidence/playwright/ 下的具体文件）
```

**产出物**: `examples/{project}/ui/e2e/*.spec.ts`（S5 编写，S7 补充） + `specs/{branch}/qa-report.md` + `specs/{branch}/user-signoff.md` + `specs/{branch}/evidence/playwright/`（trace + screenshots）

**出口条件**: `phase-gate-check.sh --stage S7 --branch {branch} --check exit = PASS`

**绝对禁止跳过此阶段。** 即使没有 UI 变化，也需要运行已有的 E2E 测试确认无回归。

---

## 阶段 8: PR + 收尾 + 并行双确认 + 合并

**执行者**: 总负责人 + 文档工程师 + Kernel Guardian + 产品经理 + 项目经理

**入口条件**: `phase-gate-check.sh --stage S8 --branch {branch} --check entry = PASS`

**操作步骤**:

```
8.0 入口检查（硬阻塞门）:
    [ ] specs/{branch}/qa-report.md 存在 → 否则拒绝进入，回到阶段 7
    [ ] specs/{branch}/tech-debt.md 存在 → 否则回到阶段 6
    [ ] specs/{branch}/user-signoff.md 存在（S7 始终产出；纯后端 Phase UI 视角标 N/A） → 否则回到阶段 7

8.1 总负责人: 创建 PR（不合并）
    - git add + commit + push
    - gh pr create
    - PR 描述包含 Phase 目标、关键变更摘要、已知 tech debt

8.2 总负责人派发收尾任务（并行，全部完成后进入 8.3）:
    - 总负责人自己:
      a) 更新 memory（tech debt、架构决策、已知风险）
      b) 更新 roadmap plan（标记 Phase 完成 + 实际 vs 计划差异）
    - 派发文档工程师:
      a) specs/{branch}/phase-report.md
      b) CHANGELOG.md 追加（**先运行 `git log --oneline main..{branch}` 生成初稿**，再人工润色分类）
      c) docs/tech-debt-registry.md 汇总更新
      d) docs/architecture.md 更新（如有结构变化）
      e) 新人 onboarding 审查（从新人视角检查文档链路，补缺 CONTRIBUTING/onboarding/glossary）
    - 派发 Kernel Guardian Phase 回顾（与文档工程师并行）:
      输入: kernel-constraints.md + tasks.md + tech-debt.md + qa-report.md + role-roster.md + git log
      检查 7 个维度（绿/黄/红）:
        A. 工作流完整性 — 8 阶段是否全执行
        B. Speckit 合规 — 是否由 Speckit 生成而非手写
        C. 角色完整性 — 适用角色是否全参与（引入"适用角色"概念: 在 role-roster.md 中标记 ON 的角色。纯后端 Phase 前端开发者标记 N/A。评分: 绿=所有适用角色参与，黄=1-2 个缺席有理由，红=3+ 缺席或连续 2 Phase 缺席）
        D. 内核集成健康度 — 核心组件是否因本 Phase 退化
        E. 标准文件齐全度 — 标准文件是否齐全（区分于产品功能完整度，仅检查文件存在性）
        F. 反馈闭环 — 上一 Phase 改进建议是否被执行
        G. Tech Debt 趋势 — 本 Phase 新增 vs 解决（仅统计 [TECH] 标签）
      产出: specs/{branch}/kernel-review-report.md
      包含: "必须在下一 Phase 修复"项（不超过 3 条）
    - 派发产品经理产品评审（与文档工程师、Kernel Guardian并行）:
      输入: product-context.md + product-acceptance-criteria.md + qa-report.md + tech-debt.md + **user-signoff.md（四视角评分）**
      检查 7 个维度（绿/黄/红）:
        A. 验收标准覆盖率 — 分级通过标准:
           P1 AC（核心功能）= 100% PASS
           P2 AC（增强功能）= 允许 SKIP 附理由，不允许 FAIL
           P3 AC（基础设施）= 允许 SKIP
        B. UI 合规检查 — 空状态处理？错误提示？Loading 状态？导航可达？（引用 user-signoff.md 视角 A 评分）
        C. 错误路径覆盖率 — spec Edge Cases vs E2E 覆盖的比例
        D. 文档链路完整性 — OpenAPI 含新 endpoints？README 引用新功能？部署文档含新配置？
        E. 功能完整度 — spec 中定义的功能是否全部实现
        F. 成功标准达成度 — product-context.md 中的成功标准是否满足
        G. 产品 Tech Debt — 产品层面的妥协（仅统计 [PRODUCT] 标签）
      产出: specs/{branch}/product-review-report.md
      包含: 不超过 3 条必须修复项
    - 派发 Roadmap 规划师（**在文档工程师+Kernel Guardian+产品经理全部完成后**触发）:
      输入: phase-report.md + tech-debt.md + kernel-review-report.md + product-review-report.md
      将本 Phase 结果回灌到下一 Phase 计划
      产出: roadmap 更新记录

8.3 并行双确认（8.2 全部完成后，两方同时执行）:

    8.3-A 产品经理产品验收确认:
        [ ] product-context.md 存在
        [ ] product-acceptance-criteria.md AC 通过分级标准:
            P1 AC（核心功能）= 100% PASS
            P2 AC（增强功能）= 无 FAIL（SKIP 必须附理由）
            P3 AC（基础设施）= 允许 SKIP
        [ ] product-review-report.md 无红色维度
        [ ] user-signoff.md 判定非 REJECT(如适用)
        → 产品 PASS / 产品 FAIL（列出未达标项）

    8.3-B 项目经理流程完成确认:
        代码:
        [ ] tasks.md 所有任务标记 [x]
        [ ] 开发者 Agent 报告 build + test 全绿
        [ ] 无未 merge 的 review fix

        文档:
        [ ] spec.md 最终版与实现一致
        [ ] decisions.md 记录了所有裁决
        [ ] tech-debt.md 记录了所有延迟项（含 [TECH]/[PRODUCT] 标签）
        [ ] phase-report.md 已写
        [ ] OpenAPI spec 含本 Phase 新增 endpoints
        [ ] 部署文档已更新
        [ ] README.md 反映最新功能
        [ ] architecture.md 反映结构变化（如有）
        [ ] CHANGELOG.md 已追加

        质量:
        [ ] qa-report.md 记录测试范围和结果
        [ ] Playwright E2E 测试存在
        [ ] tech-debt-registry.md 已汇总更新
        [ ] kernel-review-report.md 存在且 7 维度已评分
        [ ] product-context.md 存在
        [ ] product-acceptance-criteria.md 存在
        [ ] product-review-report.md 存在
        [ ] user-signoff.md 存在(如适用)
        [ ] review-findings.md 存在
        [ ] evidence/playwright/ 目录非空

        记忆:
        [ ] memory 已更新
        [ ] roadmap 标记 Phase 完成

        → 项目 PASS / 项目 FAIL（列出未完成项）

    回退路径（分离）:
    - 产品 FAIL → 回到 8.2 补做产品相关修复 → 仅重走 8.3-A
    - 项目 FAIL → 回到 8.2 补做流程相关修复 → 仅重走 8.3-B
    - 单方 FAIL 不影响另一方已获得的 PASS

8.4 总负责人: 合并 PR（仅在产品 PASS AND 项目 PASS 后执行）
    - `phase-gate-check.sh --stage S8 --branch {branch} --check exit = PASS`
    - 将 8.2 收尾文档 commit + push 到 PR 分支
    - gh pr merge
    - 确认合并成功
```

**产出物**: specs/{branch}/phase-report.md + specs/{branch}/kernel-review-report.md + specs/{branch}/product-review-report.md + 产品经理 signoff + 项目经理 signoff

**出口条件**: `phase-gate-check.sh --stage S8 --branch {branch} --check exit = PASS`

---

## 文档职责矩阵

| 阶段 | 角色 | 产出文档 |
|------|------|----------|
| S0 启动 | 总负责人 | `phase-charter.md` + `role-roster.md` |
| S0 启动 | 产品经理 | `product-context.md` |
| S1 Specify | 总负责人/Speckit | `spec.md` + `requirements.md` |
| S2 审查 | 架构师 | `review-architect.md` |
| S2 审查 | Roadmap 规划师 | `review-roadmap.md` |
| S2 审查 | Kernel Guardian | `kernel-constraints.md` |
| S2 审查 | 产品经理 | `review-product-manager.md` |
| S3 裁决 | 总负责人 | `spec.md` 更新 + `decisions.md` |
| S4 Plan | Speckit | `plan.md` + `tasks.md` + `research.md` + `data-model.md` |
| S4 审查 | Kernel Guardian | tasks 审查 + 追加遗漏任务 |
| S4 验收清单 | 产品经理 | `product-acceptance-criteria.md`（含 AC 分级: P1/P2/P3） |
| S4 依赖分析 | 项目经理 | `task-dependency-analysis.md` |
| S5 实施 | 后端开发者 | 代码 + 测试 + handoff note（含 build/test 结果） |
| S5 实施 | 前端开发者 | 代码 + handoff note |
| S5 实施 | 文档工程师 | OpenAPI spec + 用户文档 + 部署文档 |
| S5 实施 | DevOps | Dockerfile + compose + CI + seed 数据 |
| S5 实施 | QA 自动化 | `e2e/*.spec.ts`（编写测试脚本，含 trace/video 配置） |
| S5.4 检查 | 项目经理 | `tasks.md` 标记 [x] + 每 batch 进度报告 |
| S6 Review | 6 个命名 Reviewer | `review-findings.md`（结构化，P0/P1/P2 + 审查基准 commit hash） |
| S6 Fix | 总负责人 | `tech-debt.md`（含 [TECH]/[PRODUCT] 标签） |
| S7 QA | DevOps | 测试环境就绪确认 |
| S7 QA | QA 自动化 | `qa-report.md` + Playwright 报告 + 补充回归脚本 + `evidence/playwright/` |
| S7 验证 | 使用者 | `user-signoff.md`（四视角评分 + APPROVE/CONDITIONAL/REJECT） |
| S8 PR | 总负责人 | PR 创建 |
| S8 收尾 | 总负责人 | memory 更新 + roadmap 标记 |
| S8 收尾 | 文档工程师 | `phase-report.md` + `CHANGELOG.md`（git log 初稿） + `tech-debt-registry.md` + `architecture.md` + onboarding 审查 |
| S8 回顾 | Kernel Guardian | `kernel-review-report.md` |
| S8 评审 | 产品经理 | `product-review-report.md`（含 user-signoff.md 输入） |
| S8 更新 | Roadmap 规划师 | roadmap 更新记录 |
| S8.3-A | 产品经理 | 产品验收 PASS/FAIL |
| S8.3-B | 项目经理 | 流程确认 PASS/FAIL |

---

## AC 通过标准（分级）

| AC 优先级 | 定义 | 通过要求 |
|-----------|------|----------|
| P1（核心功能） | Phase 目标直接相关的功能 | 100% PASS，不允许 FAIL 或 SKIP |
| P2（增强功能） | 提升体验但非核心的功能 | 允许 SKIP 附理由，不允许 FAIL |
| P3（基础设施） | 工具链、CI、文档等支撑性工作 | 允许 SKIP |

产品经理在 S4 产出 `product-acceptance-criteria.md` 时为每条 AC 标注 P1/P2/P3。S8.3-A 按此标准验收。

---

## 文档生命周期分类

每 Phase 产出约 20+ 文档。按生命周期分两类，便于管理信息密度：

### 持久文档（Phase 结束后长期维护）

| 文档 | 位置 | 维护责任 |
|------|------|---------|
| CHANGELOG.md | 项目根目录 | 文档工程师 |
| docs/architecture.md | 项目根目录 | 文档工程师 |
| docs/tech-debt-registry.md | 项目根目录 | 文档工程师 |
| OpenAPI spec | contracts/ 或 docs/ | 文档工程师 |
| README.md | 项目根目录 | 文档工程师 |

### 过程文档（Phase 结束后归档，不再主动维护）

| 文档 | 归档条件 |
|------|---------|
| review-architect.md, review-roadmap.md, review-product-manager.md | decisions.md 已吸收所有采纳建议 |
| review-findings.md | tech-debt.md 已记录所有延迟项 |
| task-dependency-analysis.md | tasks.md 全部完成 |
| role-roster.md | Phase 关闭 |
| phase-charter.md | Phase 关闭 |

持久文档由 S8.3-B 项目经理检查。过程文档在 Phase 关闭后保留在 `specs/{branch}/` 下作为历史记录，不需要跨 Phase 更新。

---

### 已知待定项

| 编号 | 问题 | 影响 |
|------|------|------|
| v4.2 待定 | Speckit 技能 vs Stage 技能职责边界模糊（stage-N 包装 speckit-X），后续评估是否合并 | 低 |
| v4.2 待定 | 阶段门 timeout + fallback：S2/S6 并行 agent 增加 min_required + timeout_minutes + on_timeout:degrade；S5 batch 级单 agent 失败重试一次后标记 blocked。按需实现 | 低 |
| FP-01 | 同模型自审局限性：所有角色由同一 AI 模型扮演，交叉检验存在盲区。已知限制 | 已知 |
| FP-04 | 工作流复杂度与宪法 YAGNI 原则的张力。后续按实际运行反馈简化 | 已知 |
| A-01 | phase-gate-check.sh 依赖 Python+PyYAML，与 Go 项目技术栈不一致。后续改为 Go 实现或 yq | 中 |
| R-02 | 宪法 17 条红线中 3 条（noop publishers / L2 fire-and-forget / 非可重建 read model）在工作流中无检查点 | 低 |
