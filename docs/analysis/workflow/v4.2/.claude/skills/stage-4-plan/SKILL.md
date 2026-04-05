---
name: stage-4-plan
description: "Speckit Plan+Tasks+Analyze, Harness审查先行, 产品经理AC分级, 项目经理依赖分析"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Skill, Agent]
---

# 阶段 4: Speckit Plan + Tasks + Analyze + Harness 审查 + 产品验收清单 + 任务依赖分析

**执行者**: 总负责人触发 Speckit + Kernel Guardian审查 + 产品经理 + 项目经理

**入口条件**: S3 出口通过（decisions.md 存在）

---

## 操作步骤

### 步骤 1: Speckit Plan

```
/speckit.plan specs/{branch}
```

产出: `plan.md` + `research.md` + `data-model.md` + `contracts/`

### 步骤 2: Speckit Tasks

```
/speckit.tasks specs/{branch}
```

产出: `tasks.md`（含 [P] 并行标记 + 依赖关系）

### 步骤 3: 验证 tasks.md 完整性

检查 tasks.md 是否包含以下非代码任务（确认已生成且可执行）：

```
[ ] 文档工程师的任务（OpenAPI/用户文档/部署文档）
[ ] DevOps 的任务（Docker/CI/部署配置更新）
[ ] QA 自动化的任务（Playwright E2E 测试编写 — 在 S5 执行）
[ ] DevOps: 安装 Playwright + playwright.config.ts（首次 Phase 做，后续复用）
[ ] DevOps: docker-compose.test.yml（PG + GoCell App 测试环境）
[ ] 后端开发者: 种子数据脚本（seed-test-data.sh 或 Go test helper）
[ ] QA 自动化: Playwright E2E 测试编写（3 用户视角，在 S5 执行）
```

如缺失，手动追加。**这是唯一允许手动修改 tasks.md 的场景。**

### 步骤 4: Speckit Analyze

```
/speckit.analyze specs/{branch}
```

检查 spec/plan/tasks 一致性。

### 步骤 5: Kernel Guardian审查 tasks.md（串行先行）

**此步骤必须在步骤 6 之前完成。**

派发 Kernel Guardian Agent：

```
角色: Kernel Guardian
任务: 审查 tasks.md，对照 kernel-constraints.md 中"必须验证"的约束:
1. 检查对应任务是否出现在 tasks.md
2. 检查 tasks.md 是否由 Speckit 生成（非手写）
3. 如缺失内核集成验证任务，追加到 tasks.md
输入: specs/{branch}/tasks.md + specs/{branch}/kernel-constraints.md
产出: tasks.md 追加（如有遗漏）+ 审查确认
```

### 步骤 6: 产品经理 + 项目经理并行（Harness 审查完成后）

**前置条件: 步骤 5 Harness 审查已完成。** 完成后才派发以下 2 个 agent：

**Agent 1 — 产品经理**:
```
角色: 产品经理
任务: 读取 spec.md + tasks.md + product-context.md，产出 specs/{branch}/product-acceptance-criteria.md。
产出必须包含:
- 每条 FR/User Story 的验收标准（AC）
- 每条 AC 标注优先级:
  P1（核心功能）— Phase 目标直接相关
  P2（增强功能）— 提升体验但非核心
  P3（基础设施）— 工具链/CI/文档等
- AC 到 tasks.md 任务的映射关系
- 每条 AC 的验证方式标注: [E2E 测试] [手动验证] [代码审查] [集成测试]
- 标注"手动验证"的 AC 翻译为步骤化操作指南（供 S7 使用者使用）
输入: specs/{branch}/spec.md + specs/{branch}/tasks.md + specs/{branch}/product-context.md
产出: specs/{branch}/product-acceptance-criteria.md
```

**Agent 2 — 项目经理**:
```
角色: 项目经理
任务: 读取 tasks.md + kernel-constraints.md，产出 specs/{branch}/task-dependency-analysis.md。
产出必须包含:
- 任务依赖图（哪些任务阻塞哪些）
- 关键路径识别
- 风险点标注（阻塞概率/影响范围）
- batch 预估（建议分几个 batch + 每 batch 包含哪些任务）
输入: specs/{branch}/tasks.md + specs/{branch}/kernel-constraints.md
产出: specs/{branch}/task-dependency-analysis.md
```

**注**: task-dependency-analysis.md 如未在 S4 完成，则为 S5.1 入口条件。Fallback: 如不存在，使用 tasks.md 内嵌依赖图进行 batch 划分。

### 步骤 7: 阶段门检查

```bash
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S4 --check exit
```

---

## AC 通过标准（分级）

| AC 优先级 | 定义 | 通过要求 |
|-----------|------|----------|
| P1（核心功能） | Phase 目标直接相关的功能 | 100% PASS，不允许 FAIL 或 SKIP |
| P2（增强功能） | 提升体验但非核心的功能 | 允许 SKIP 附理由，不允许 FAIL |
| P3（基础设施） | 工具链、CI、文档等支撑性工作 | 允许 SKIP |

产品经理在本阶段为每条 AC 标注 P1/P2/P3。S8.3-A 按此标准验收。

---

## 硬性产出物

| 文件 | 路径 | 责任角色 |
|------|------|---------|
| plan.md | `specs/{branch}/plan.md` | Speckit |
| tasks.md | `specs/{branch}/tasks.md` | Speckit + Harness 追加 |
| research.md | `specs/{branch}/research.md` | Speckit |
| product-acceptance-criteria.md | `specs/{branch}/product-acceptance-criteria.md` | 产品经理 |
| task-dependency-analysis.md | `specs/{branch}/task-dependency-analysis.md` | 项目经理 |

## 出口条件

```
[ ] plan.md 存在且非空
[ ] tasks.md 存在且含文档+DevOps+QA 测试编写+内核集成验证任务
[ ] tasks.md 内容检查通过（含 Playwright/E2E 关键词 + Docker/部署关键词 + OpenAPI/文档关键词）
[ ] 一致性检查通过（speckit.analyze 无严重问题）
[ ] Kernel Guardian确认无遗漏
[ ] product-acceptance-criteria.md 已产出且含 P1/P2/P3 分级
[ ] phase-gate-check.sh --stage S4 --check exit = PASS
```

**禁止**: 手写 tasks.md（Speckit 生成，仅允许追加缺失的非代码任务和内核集成验证任务）
