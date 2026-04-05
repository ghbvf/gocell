# 多角色团队调度工作流 — 文件关联图 v4.2

## 概述

每个 Phase 严格按 9 个阶段（S0-S8）执行。本文档定义工作流涉及的**角色**、**阶段流转**和**所有文件关联关系**。各阶段的具体操作步骤见对应 Stage SKILL（`.claude/skills/stage-*/SKILL.md`）。

**绝对禁止跳步。** 每个阶段有明确的入口条件和出口条件，未满足不得进入下一阶段。阶段门由 `phase-gates.yaml` 定义，`phase-gate-check.py` 执行校验。

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

### 两层执行合同

**层 1: phase-gates.yaml** — 机器强制。文件存在性、内容模式、命令通过。gate 脚本自动执行，fail-closed（YAML 解析失败、阶段不存在、检查集为空均视为 FAIL）。

**层 2: SKILL.md 出口清单** — Agent 强制。仪式完成（双 PASS）、语义完整（AC 引用证据）、流程顺序（契约先行）。由执行 Agent 逐条确认，不可跳过。

各 SKILL 出口清单中每条标注 `[GATE]`（层 1 执行）或 `[AGENT]`（层 2 执行）。层 1 不覆盖的规则不意味着"可选"——它意味着该规则由 Agent 在 SKILL 出口清单中强制执行。

### 跳过警告枚举（phase-charter.md 中使用）

当某角色或产出物标记为跳过时，必须使用以下枚举值之一：

| 枚举值 | 含义 | 示例 |
|--------|------|------|
| `SCOPE_IRRELEVANT` | 本 Phase 范围不涉及 | 纯后端 Phase 跳过前端开发者 |
| `RESOURCE_UNAVAILABLE` | 资源不可用但应该参与 | 标记为 tech-debt 下一 Phase 补做 |
| `DEFERRED` | 延迟到后续 Phase | 文档工作推迟到下一 Phase |

禁止使用自由文本理由。N/A 声明格式为 `N/A:<枚举值> <文件名>`（如 `N/A:SCOPE_IRRELEVANT spec.md`）。phase-gate-check.py 结构化校验枚举值，不接受任意文本。

### Bash 编写规范

SKILL 和脚本中**禁止**使用链式 bash（`&&` / `||` / `;` 串联多个命令）。每个命令独立一个 code block，便于 Agent 逐条执行和错误定位。

**禁止**:
```bash
git checkout develop && git pull && go build ./... && go test ./...
```

**正确**:
```bash
git checkout develop
```

```bash
git pull
```

```bash
go build ./...
```

```bash
go test ./...
```

---

## 阶段流转（入口/出口文件关联）

各阶段的具体操作步骤见对应 Stage SKILL（`.claude/skills/stage-*/SKILL.md`）。

| 阶段 | SKILL | 入口文件 | 出口文件 | 执行者 |
|------|-------|---------|---------|--------|
| S0 启动 | stage-0-init | （无） | phase-charter.md, role-roster.md, product-context.md | 总负责人 + 产品经理 |
| S1 Specify | stage-1-specify | phase-charter.md, role-roster.md, product-context.md | spec.md, checklists/requirements.md | 总负责人 |
| S2 审查 | stage-2-review | spec.md | kernel-constraints.md, review-architect.md, review-roadmap.md, review-product-manager.md | 架构师 + Roadmap + KG + PM |
| S3 裁决 | stage-3-decide | kernel-constraints.md, review-architect.md, review-roadmap.md, review-product-manager.md | decisions.md, spec.md(更新) | 总负责人 |
| S4 Plan | stage-4-plan | decisions.md | plan.md, tasks.md, research.md, product-acceptance-criteria.md | 总负责人 + KG + PM + 项目经理 |
| S5 实施 | stage-5-implement | tasks.md, product-acceptance-criteria.md | tasks.md(全部完成) | 开发者 + 文档 + DevOps |
| S6 Review-Fix | stage-6-review-fix | tasks.md | review-findings.md, tech-debt.md | 6 席位 Reviewer |
| S7 QA | stage-7-qa | review-findings.md, tech-debt.md | qa-report.md, user-signoff.md, evidence/*/ | DevOps + PM |
| S8 收尾 | stage-8-close | qa-report.md, tech-debt.md, user-signoff.md, evidence/*/ | kernel-review-report.md, product-review-report.md, phase-report.md, CHANGELOG.md | 全角色 |

---

## 流程涉及文件清单

### 配置文件

| 文件 | 用途 |
|------|------|
| `.claude/skills/phase-gate/phase-gates.yaml` | 阶段门准入准出规则（唯一可执行合同） |

### 脚本文件

| 文件 | 用途 |
|------|------|
| `.claude/skills/phase-gate/scripts/phase-gate-check.py` | 阶段门检查脚本（S0-S8 每阶段 entry/exit 校验，fail-closed） |

### Agent 定义

| 文件 | 角色 | 参与阶段 |
|------|------|----------|
| `.claude/agents/architect.md` | 架构师 | S2 技术架构审查 + S6 裁决升级 |
| `.claude/agents/kernel-guardian.md` | Kernel Guardian | S2 内核约束 + S4 tasks 审查 + S8 Phase 回顾 |
| `.claude/agents/product-manager.md` | 产品经理 | S0 产品上下文 + S2 验收标准 + S4 AC 分级 + S7 使用者输入 + S8 产品评审/验收 |
| `.claude/agents/project-manager.md` | 项目经理 | S4 依赖分析 + S5 batch 划分/进度跟踪 + S8 流程确认 |
| `.claude/agents/roadmap.md` | Roadmap 规划师 | S2 范围审查 + S8 roadmap 更新 |
| `.claude/agents/doc-engineer.md` | 文档工程师 | S5 文档 + S8 收尾（phase-report/CHANGELOG/tech-debt-registry/architecture/onboarding） |
| `.claude/agents/devops.md` | DevOps | S5 部署配置 + S7 测试环境部署 |
| `.claude/agents/reviewer.md` | 6 席位 Reviewer（共用定义） | S6 Review-Fix 循环 |

无独立 agent 文件（由 SKILL.md 内联 prompt 派发）：后端/前端开发者（S5）、QA 自动化（S5/S7）、使用者（S7）。

### Stage SKILL 定义

| 文件 | 阶段 | 用途 |
|------|------|------|
| `.claude/skills/stage-0-init/SKILL.md` | S0 启动 | 角色完整性 + 连续性 + 过载保护 + 产品上下文 |
| `.claude/skills/stage-1-specify/SKILL.md` | S1 Specify | 创建分支 + 生成 spec + 验证需求完整性 |
| `.claude/skills/stage-2-review/SKILL.md` | S2 审查 | 4 角色并行审查 spec |
| `.claude/skills/stage-3-decide/SKILL.md` | S3 裁决 | 综合裁决 + 更新 spec + 记录决策 |
| `.claude/skills/stage-4-plan/SKILL.md` | S4 Plan | Plan + Tasks + Analyze + 内核审查 + AC 分级 + 依赖分析 |
| `.claude/skills/stage-5-implement/SKILL.md` | S5 实施 | 契约先行 + batch 划分 + 开发者派发 + 进度跟踪 |
| `.claude/skills/stage-6-review-fix/SKILL.md` | S6 Review-Fix | 6 席位审查 + 最多 3 轮修复 + tech-debt |
| `.claude/skills/stage-7-qa/SKILL.md` | S7 QA | 环境部署 + 测试执行 + 四视角验证 |
| `.claude/skills/stage-8-close/SKILL.md` | S8 收尾 | PR + 五路收尾 + 7 维评审 + 双 PASS + 合并 |
| `.claude/skills/phase-gate/SKILL.md` | 全阶段 | 阶段门检查派发（调用 phase-gate-check.py） |

### 产出物（`specs/{branch}/`）

| 产出物 | 产出阶段 | 消费阶段 |
|--------|----------|----------|
| `phase-charter.md` | S0 | S1 entry gate |
| `role-roster.md` | S0 | S5（QA 启用判断）、S8（角色完整性评分） |
| `product-context.md` | S0 | S1（specify 输入）、S2（产品经理审查）、S4（AC 产出）、S8（产品评审） |
| `spec.md` | S1（生成）→ S3（更新） | S2（审查）、S4（plan 输入）、S5（实施基准）、S6（review 对照）、S8（一致性检查） |
| `checklists/requirements.md` | S1 | S2（审查参考） |
| `review-architect.md` | S2 | S3（裁决输入） |
| `review-roadmap.md` | S2 | S3（裁决输入） |
| `kernel-constraints.md` | S2 | S3（裁决）、S4（tasks 审查）、S5（指令重注入）、S6（review 注入）、S8（回顾输入） |
| `review-product-manager.md` | S2 | S3（裁决输入） |
| `decisions.md` | S3 | S8（项目经理检查） |
| `plan.md` | S4 | S5（实施参考） |
| `tasks.md` | S4（生成） | S4（KG 审查）、S5（batch 划分 + 进度跟踪）、S8（完成度检查） |
| `research.md` | S4 | S5（实施参考） |
| `data-model.md` | S4 | S5（实施参考） |
| `product-acceptance-criteria.md` | S4 | S7（QA 引用 AC 编号）、S8（产品验收分级标准） |
| `task-dependency-analysis.md` | S4（或 S5.1 前） | S5.1（batch 划分输入） |
| `e2e/*.spec.ts` | S5（QA 编写） | S7（执行测试） |
| `review-findings.md` | S6 | S6（修复依据）、S8（项目经理检查） |
| `tech-debt.md` | S6 | S8（入口门 + KG 回顾 + 产品评审 + 项目经理检查 + Roadmap 输入）、下一 Phase S0（连续性检查） |
| `qa-report.md` | S7 | S8（入口门 + KG 回顾 + 产品评审 + 项目经理检查） |
| `user-signoff.md` | S7 | S8（入口门 + 产品评审 + 产品验收 + 项目经理检查） |
| `evidence/go-test/` | S7 | S7（qa-report 引用） |
| `evidence/validate/` | S7 | S7（qa-report 引用） |
| `evidence/journey/` | S7 | S7（qa-report 引用） |
| `evidence/playwright/` | S7 | S7（qa-report 引用 trace/screenshot） |
| `phase-report.md` | S8 | S8（Roadmap 输入 + 项目经理检查） |
| `kernel-review-report.md` | S8 | S8（Roadmap 输入）、下一 Phase S0（连续性检查） |
| `product-review-report.md` | S8 | S8（产品验收 + Roadmap 输入）、下一 Phase S0（连续性检查） |

### Repo 级持久文档

| 文件 | 更新阶段 | 维护责任 |
|------|----------|----------|
| `CHANGELOG.md` | S8 | 文档工程师 |
| `README.md` | S8 | 文档工程师 |
| `docs/architecture.md` | S8 | 文档工程师 |
| `docs/tech-debt-registry.md` | S8 | 文档工程师 |
| `docs/references/framework-comparison.md` | S5（对标对比规则引用） | -- |
| `contracts/`（OpenAPI spec） | S5 | 文档工程师 + 后端开发者 |

### 其他引用文件

| 文件 | 引用位置 | 用途 |
|------|----------|------|
| `docker-compose.test.yml` | S5 tasks / S7 环境部署 | 测试环境编排 |
| `playwright.config.ts` | S5 tasks / S7 环境确认 | E2E 测试配置（trace/video/screenshot） |
| `seed-test-data.sh`（或 Go test helper） | S5 tasks / S7 数据加载 | 种子数据脚本 |
| `examples/{project}/ui/e2e/*.spec.ts` | S5 编写 / S7 补充执行 | Playwright E2E 测试文件 |

---

## 待办（工作流相关）

| # | 来源 | 待办事项 | 优先级 |
|---|------|---------|--------|
| 1 | DevOps P0-1 | 创建 docker-compose.test.yml（S7 测试环境依赖） | Phase 3 前 |
| 2 | DevOps P1-4 | run-qa.sh 补充 Playwright 执行逻辑 | Phase 3 前 |
| 3 | DevOps P0-3 | run-qa.sh SCRIPT_DIR 路径修复（${BASH_SOURCE[0]}） | Phase 3 前 |
| 4 | DevOps P1-3 | phase-gate-check.py bash 白名单安全问题 | 低 |
| 5 | DevOps P2-7 | phase-gate-check.py unchecked task 正则不匹配缩进子任务 | 低 |
| 6 | 项目 P2-1 | S0 过载保护量化标准扩展（本 Phase 任务量上限） | 低 |
