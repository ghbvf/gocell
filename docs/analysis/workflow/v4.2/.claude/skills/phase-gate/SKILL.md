---
name: phase-gate
description: "阶段门检查: 执行phase-gate-check.sh验证准入准出条件+每阶段required_files速查+FAIL处理"
allowed-tools: [Read, Glob, Grep, Bash]
---

# 阶段门检查（Phase Gate）

**何时调用**: 总负责人在每个阶段转换时调用。每个阶段的出口条件均包含 `phase-gate-check.sh` 检查。

---

## 调用方式

### 检查某阶段出口条件

```bash
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S{N} --check exit
```

### 检查某阶段入口条件

```bash
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S{N} --check entry
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

| 阶段 | 入口 required_files | 出口 required_files | content_checks |
|------|--------------------|---------------------|----------------|
| **S0** | （无） | `phase-charter.md`, `role-roster.md`, `product-context.md` | （无） |
| **S1** | `phase-charter.md`, `role-roster.md`, `product-context.md` | `spec.md`, `checklists/requirements.md` | spec.md 含 `(文档\|DevOps\|E2E\|Playwright\|测试)` |
| **S2** | `spec.md` | `kernel-constraints.md` | （无） |
| **S3** | `kernel-constraints.md` | `decisions.md` | （无） |
| **S4** | `decisions.md` | `plan.md`, `tasks.md`, `product-acceptance-criteria.md` | tasks.md 含 `(Playwright\|E2E\|测试编写)` + `(Docker\|docker-compose\|部署)` + `(OpenAPI\|文档)` |
| **S5** | `tasks.md`, `product-acceptance-criteria.md` | （动态检查） | tasks.md `no_unchecked_tasks` |
| **S6** | （无） | `review-findings.md`, `tech-debt.md` | （无） |
| **S7** | （无） | `qa-report.md`, `user-signoff.md` | （无） |
| **S8** | `qa-report.md`, `tech-debt.md` | `kernel-review-report.md`, `product-review-report.md`, `phase-report.md` | （无） |

**注**: 所有 required_files 路径相对于 `specs/{branch}/`。

---

## phase-gates.yaml 数据源

脚本消费 `.claude/skills/phase-gate/phase-gates.yaml`，声明式定义所有准入准出规则：

```yaml
stages:
  S0:
    exit:
      required_files: [phase-charter.md, role-roster.md, product-context.md]
  S1:
    entry:
      required_files: [phase-charter.md, role-roster.md, product-context.md]
    exit:
      required_files: [spec.md, checklists/requirements.md]
      content_checks:
        - file: spec.md
          pattern: "(文档|DevOps|E2E|Playwright|测试)"
  S2:
    entry:
      required_files: [spec.md]
    exit:
      required_files: [kernel-constraints.md]
  S3:
    entry:
      required_files: [kernel-constraints.md]
    exit:
      required_files: [decisions.md]
  S4:
    entry:
      required_files: [decisions.md]
    exit:
      required_files: [plan.md, tasks.md, product-acceptance-criteria.md]
      content_checks:
        - file: tasks.md
          pattern: "(Playwright|E2E|测试编写)"
        - file: tasks.md
          pattern: "(Docker|docker-compose|部署)"
        - file: tasks.md
          pattern: "(OpenAPI|文档)"
  S5:
    entry:
      required_files: [tasks.md, product-acceptance-criteria.md]
    exit:
      content_checks:
        - file: tasks.md
          check: no_unchecked_tasks
  S6:
    exit:
      required_files: [review-findings.md, tech-debt.md]
  S7:
    exit:
      required_files: [qa-report.md, user-signoff.md]
  S8:
    entry:
      required_files: [qa-report.md, tech-debt.md]
    exit:
      required_files: [kernel-review-report.md, product-review-report.md, phase-report.md]
```

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
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S0 --check exit

# 进入 S5 前检查入口
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S5 --check entry

# S8 入口硬阻塞门
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S8 --check entry

# S8 合并前最终检查
bash .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh --stage S8 --check exit
```
