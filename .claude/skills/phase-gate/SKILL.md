---
name: phase-gate
description: "阶段门检查: 验证准入准出条件+FAIL处理"
argument-hint: "--stage SN --branch [branch] --check entry|exit"
allowed-tools: [Read, Glob, Grep, Bash]
---

# 阶段门检查（Phase Gate）

**何时调用**: 总负责人在每个阶段转换时调用。每个阶段的出口条件均包含 `phase-gate-check.sh` 检查。

---

## 调用方式

### 检查某阶段出口条件

```bash
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S{N} --branch {branch} --check exit
```

### 检查某阶段入口条件

```bash
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S{N} --branch {branch} --check entry
```

### 脚本行为

1. 读取 `.claude/skills/phase-gate/phase-gates.yaml` 中对应阶段的规则
2. 检查 `required_files` — 文件存在性 + 非空
3. 检查 `content_checks` — 文件内容匹配（正则 pattern）
4. 检查 `no_unchecked_tasks` — tasks.md 中无未勾选项（仅 S5 出口）
5. 输出 PASS/FAIL + 缺失清单
6. 写审计日志 `specs/{branch}/gate-audit.log`

---

## 每阶段 required_files 速查表

> **单一来源**: 此表从 `.claude/skills/phase-gate/phase-gates.yaml` 生成。如有差异以 YAML 为准。执行阶段门检查前务必确认 YAML 内容，不要仅依赖此速查表。
>
> **运行时依赖**: phase-gate-check.sh 需要 `python3` + `PyYAML`（`pip3 install pyyaml`）。

| 阶段 | 入口 required_files | 出口 required_files | 出口 content_checks |
|------|--------------------|---------------------|---------------------|
| **S0** | （无） | `phase-charter.md`, `role-roster.md`, `product-context.md` | phase-charter: `(目标\|范围\|非目标)`, role-roster: `(ON\|OFF)` |
| **S1** | `phase-charter.md`, `role-roster.md`, `product-context.md` | `spec.md`, `checklists/requirements.md` | spec.md: `(文档\|DevOps\|测试\|contract test\|journey test)` |
| **S2** | `spec.md` | `kernel-constraints.md`, `review-architect.md`, `review-roadmap.md`, `review-product-manager.md` | kernel-constraints: `(集成风险\|分层隔离\|核心约束\|元数据合规\|契约完整性)` |
| **S3** | `kernel-constraints.md`, `review-architect.md`, `review-roadmap.md`, `review-product-manager.md` | `decisions.md` | decisions: `(采纳\|拒绝\|延迟\|accept\|reject\|defer)` |
| **S4** | `decisions.md` | `plan.md`, `tasks.md`, `product-acceptance-criteria.md` | tasks: `(contract test\|journey test\|测试编写\|E2E)` + `(cell.yaml\|slice.yaml\|gocell validate\|元数据)` + `(文档\|godoc\|README)`, PAC: `(P1\|P2\|P3)` |
| **S5** | `tasks.md`, `product-acceptance-criteria.md` | `tasks.md` | tasks.md `no_unchecked_tasks` |
| **S6** | `tasks.md` | `review-findings.md`, `tech-debt.md` | review-findings: `(P0\|P1\|P2)`, tech-debt: `([TECH]\|[PRODUCT])` |
| **S7** | `review-findings.md`, `tech-debt.md` | `qa-report.md` | qa-report: `(go test\|contract test\|journey test\|coverage\|PASS\|FAIL)` |
| **S8** | `qa-report.md`, `tech-debt.md`, `user-signoff.md` | `kernel-review-report.md`, `product-review-report.md`, `phase-report.md` | kernel/product-review: `(绿\|黄\|红\|GREEN\|YELLOW\|RED)`, phase-report: `(变更摘要\|关键决策)` |

**注**: 所有 required_files 路径相对于 `specs/{branch}/`。user-signoff.md 为条件性文件，纯后端 Phase 通过 phase-charter.md N/A 声明跳过。

---

## phase-gates.yaml 数据源

脚本消费 `.claude/skills/phase-gate/phase-gates.yaml`，声明式定义所有准入准出规则。**此 YAML 文件是唯一真相来源**，修改规则时只改 YAML，不改此文档中的速查表（速查表仅做快速参考）。

---

## FAIL 时的处理流程

### 1. 查看缺失清单

脚本输出的 FAIL 结果包含具体缺失项列表，例如：

```
FAIL — S4 exit check
Missing files:
  - product-acceptance-criteria.md
Content check failed:
  - tasks.md: pattern "(Playwright|E2E|测试编写)" not found
```

### 2. 定位责任角色

根据缺失项查找文档职责矩阵，确定应由哪个角色补全：

| 缺失文件 | 责任角色 | 补全阶段 |
|---------|---------|---------|
| phase-charter.md | 总负责人 | S0 |
| role-roster.md | 总负责人 | S0 |
| product-context.md | 产品经理 | S0 |
| spec.md | 总负责人/Speckit | S1 |
| kernel-constraints.md | Kernel Guardian | S2 |
| review-product-manager.md | 产品经理 | S2 |
| decisions.md | 总负责人 | S3 |
| plan.md / tasks.md | Speckit | S4 |
| product-acceptance-criteria.md | 产品经理 | S4 |
| review-findings.md | 6 Reviewer | S6 |
| tech-debt.md | 总负责人 | S6 |
| qa-report.md | QA 自动化 | S7 |
| user-signoff.md | 使用者 | S7 |
| phase-report.md | 文档工程师 | S8 |
| kernel-review-report.md | Kernel Guardian | S8 |
| product-review-report.md | 产品经理 | S8 |

### 3. 回退到对应阶段

- 如果缺失的是当前阶段的产出物 → 在当前阶段补全
- 如果缺失的是前序阶段的产出物 → 回退到前序阶段补全后重新推进
- 禁止跳过检查强行进入下一阶段

### 4. N/A 声明处理

如果某文件确实不适用于本 Phase（如纯后端无 UI），需在 `phase-charter.md` 的"N/A 声明"章节中记录理由。phase-gate-check.sh 读取此声明后跳过对应文件检查。

### 5. 审计日志

每次执行结果写入 `specs/{branch}/gate-audit.log`，格式：

```
[{timestamp}] Stage: S{N} Check: {entry/exit} Result: {PASS/FAIL} Missing: [{文件列表}]
```

---

## 使用示例

```bash
# Phase 开始前检查 S0 出口
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S0 --branch {branch} --check exit

# 进入 S5 前检查入口
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S5 --branch {branch} --check entry

# S8 入口硬阻塞门
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S8 --branch {branch} --check entry

# S8 合并前最终检查
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S8 --branch {branch} --check exit
```
