---
name: stage-0-init
description: "Phase 启动: 角色完整性+连续性+过载保护+产品上下文"
argument-hint: "[phase-goal-or-doc-path]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent]
---

# 阶段 0: 启动

**执行者**: 总负责人 + 产品经理

**绝对禁止跳步。** 本阶段的出口条件未满足不得进入阶段 1。

---

## 操作步骤

### 步骤 1: 确认 Phase 目标

1. 确认本 Phase 的目标（1-2 段描述）
2. 确认与 roadmap 的关系（是哪个 Phase，前置条件是否满足）

### 步骤 2: 执行角色完整性检查

按三层分组逐一核对。所有 Core Mandatory 角色必须 ON，不可跳过。

```
Core Mandatory（必须全部 ON，5 角色）:
[ ] 总负责人 — 调度+裁决+合并
[ ] 架构师 — S2 技术架构审查 + S6 裁决升级
[ ] 产品经理 — S0 产品上下文 + S2 验收标准审查 + S4 产品验收清单 + S8.2 产品评审 + S8.3-A 产品验收确认
[ ] 项目经理 — S4 依赖分析 + S5.1 batch 划分 + S5.4 进度跟踪 + S8.3-B 流程完成确认
[ ] Kernel Guardian — S2 kernel-constraints + S4 tasks 审查 + S8.2 Phase 回顾

Conditional Delivery（逐一标注 ON/OFF + 理由）:
[ ] 后端开发者 — S5 实施  → ON / OFF（理由:_______）
[ ] 前端开发者 — S5 实施  → ON / OFF（理由:_______）
[ ] 文档工程师 — S5 文档 + S8.2 收尾  → ON / OFF（理由:_______）
[ ] DevOps — S5 部署配置 + S7.0 测试环境  → ON / OFF（理由:_______）
[ ] QA 自动化 — S5 编写测试 + S7 执行测试  → ON / OFF（理由:_______）

Review Bench（6 个命名席位，S6 全部参与）:
[ ] 架构一致性 Reviewer
[ ] 安全/权限 Reviewer
[ ] 测试/回归 Reviewer
[ ] 运维/部署 Reviewer
[ ] DX/可维护性 Reviewer
[ ] 产品/用户体验 Reviewer

Governance（治理层）:
[ ] 使用者 — S7 验证
[ ] Roadmap 规划师 — S2 审查 + S8.2 roadmap 更新
```

### 步骤 3: 跳过记录检查

对每个角色核对"上一 Phase 是否被跳过"。如果某角色被跳过，必须记录枚举化原因：

| 跳过原因代码 | 含义 | 示例 |
|-------------|------|------|
| `SCOPE_IRRELEVANT` | 本 Phase 范围与该角色无关 | 纯后端 Phase 跳过前端开发者 |
| `RESOURCE_UNAVAILABLE` | 资源不可用（人/环境/工具） | CI 环境故障跳过 DevOps |
| `DEFERRED` | 延迟到后续 Phase 执行 | 测试环境准备延迟 |

**红色警告**: 连续 2 Phase 跳过同一角色 → 必须在 phase-charter.md 中写明处理方案。

### 步骤 4: 连续性检查

**仅 Phase 1+ 执行，首个 Phase 跳过此步骤。**

检查上一 Phase 的三份遗留文件：

```
[ ] 上一 Phase `kernel-review-report.md` 中的"必须修复"项已纳入本 Phase 范围
[ ] 上一 Phase `product-review-report.md` 中的"必须修复"项已纳入本 Phase 范围
[ ] 上一 Phase `tech-debt.md` 中标记"下一 Phase 修复"的项已纳入讨论范围
```

**过载保护**: 三份文件"必须修复"项合计 > 9 条时触发红色警告：
- 总负责人必须裁决优先级
- 超出承载的项目标记为 `DEFERRED` 并写入 phase-charter.md 的"延迟处理"章节
- 裁决结果必须记录理由

### 步骤 5: 派发产品经理产出 product-context.md

派发产品经理 Agent，prompt 必须注入以下三项：

1. **Phase 目标描述**（步骤 1 确认的内容）
2. **PRD**（项目级产品需求文档）
3. **上一 Phase `product-review-report.md`**（如存在）

产品经理产出 `specs/{branch}/product-context.md`，包含：
- 目标用户画像（persona）
- 成功标准（可量化）
- 范围边界（含非目标声明）

### 步骤 6: 产出 phase-charter.md 和 role-roster.md

**phase-charter.md 模板**:
```markdown
# Phase Charter — {Phase 编号}: {Phase 名称}

## Phase 目标
{1-2 段描述}

## 范围
### 目标（In Scope）
- ...

### 非目标（Out of Scope）
- ...

### N/A 声明
{列出本 Phase 不适用的标准文件及理由，phase-gate-check 据此跳过检查}

## 连续性处理
### 从上一 Phase 继承的必须修复项
| 来源文件 | 项目 | 处理方式 |
|---------|------|---------|
| kernel-review-report.md | ... | 纳入本 Phase / DEFERRED（理由） |
| product-review-report.md | ... | 纳入本 Phase / DEFERRED（理由） |
| tech-debt.md | ... | 纳入本 Phase / DEFERRED（理由） |

### 延迟处理（过载保护触发时填写）
| 项目 | 延迟理由 | 计划修复 Phase |
|------|---------|---------------|
```

**role-roster.md 模板**:
```markdown
# Role Roster — {Phase 编号}

## Core Mandatory
| 角色 | 状态 | 备注 |
|------|------|------|
| 总负责人 | ON | |
| 架构师 | ON | |
| 产品经理 | ON | |
| 项目经理 | ON | |
| Kernel Guardian | ON | |

## Conditional Delivery
| 角色 | 状态 | 理由 |
|------|------|------|
| 后端开发者 | ON/OFF | {理由} |
| 前端开发者 | ON/OFF | {理由} |
| 文档工程师 | ON/OFF | {理由} |
| DevOps | ON/OFF | {理由} |
| QA 自动化 | ON/OFF | {理由} |

## Review Bench
| 席位 | 状态 |
|------|------|
| 架构一致性 | ON |
| 安全/权限 | ON |
| 测试/回归 | ON |
| 运维/部署 | ON |
| DX/可维护性 | ON |
| 产品/用户体验 | ON |

## 其他
| 角色 | 状态 |
|------|------|
| 使用者 | ON |
| Roadmap 规划师 | ON |

## 跳过记录
| 角色 | 跳过原因 | 连续跳过次数 | 警告级别 |
|------|---------|-------------|---------|
```

### 步骤 7: 阶段门检查

执行出口检查：

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S0 --branch {branch} --check exit
```

---

## 硬性产出物

| 文件 | 路径 | 责任角色 |
|------|------|---------|
| phase-charter.md | `specs/{branch}/phase-charter.md` | 总负责人 |
| role-roster.md | `specs/{branch}/role-roster.md` | 总负责人 |
| product-context.md | `specs/{branch}/product-context.md` | 产品经理 |

## 出口条件

```
[ ] phase-charter.md 已写且非空
[ ] role-roster.md 已写且非空
[ ] product-context.md 已产出且非空
[ ] 角色完整性检查通过（Core Mandatory 全部 ON）
[ ] 连续性检查通过（如适用）
[ ] phase-gate-check.py --stage S0 --branch {branch} --check exit = PASS
```
