---
name: stage-8-close
description: "PR+收尾+双确认+合并: 五路收尾+7维评审+双PASS"
argument-hint: "[branch-name]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent]
---

# 阶段 8: PR + 收尾 + 并行双确认 + 合并

**执行者**: 总负责人 + 文档工程师 + Kernel Guardian + 产品经理 + 项目经理 + Roadmap 规划师

---

## 8.0 入口检查（硬阻塞门）

```
[ ] specs/{branch}/qa-report.md 存在 → 否则拒绝进入，回到阶段 7
[ ] specs/{branch}/tech-debt.md 存在 → 否则回到阶段 6
[ ] specs/{branch}/user-signoff.md 存在（S7 始终产出；纯后端 Phase UI 视角标 N/A） → 否则回到阶段 7
```

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S8 --branch {branch} --check entry
```

---

## 8.1 总负责人: 创建 PR（不合并）

确认 pr-plan.md 中所有 PR 已合入 feature 分支，验证代码健康：

```bash
git push -u origin {branch}
```

```bash
go build ./...
```

```bash
go vet ./...
```

```bash
go test ./... -count=1
```

创建指向 develop 的 PR：

```bash
gh pr create --base develop --title "Phase {N}: {Phase 名称}" --body "..."
```

PR 描述包含:
- Phase 目标
- 关键变更摘要
- 已知 tech debt（引用 tech-debt.md）

---

## 8.2 五路收尾任务（A-D 并行 + E 串行）

总负责人派发以下任务，路径 A-D **并行执行**，路径 E 在 B/C/D 全部完成后触发。全部完成后进入 8.3。

### 路径 A: 总负责人自己

1. 更新 memory（tech debt、架构决策、已知风险）
2. 更新 roadmap plan（标记 Phase 完成 + 实际 vs 计划差异）

### 路径 B: 文档工程师

派发 Agent:
```
角色: 文档工程师
任务: 完成以下 5 项收尾文档:
1. specs/{branch}/phase-report.md — Phase 总结报告
2. CHANGELOG.md 追加 — 使用 git log 生成初稿:
   git log --oneline --no-merges --since="Phase 开始日期"
   然后按 Conventional Commits 分组整理
3. docs/tech-debt-registry.md 汇总更新 — 合并本 Phase tech-debt.md
4. docs/architecture.md 更新（如有结构变化）
5. 新人 onboarding 审查 — 从新人视角检查文档链路，补缺 CONTRIBUTING/onboarding/glossary

输入: specs/{branch}/tech-debt.md + git log + 项目文档
产出: 上述 5 项文档
```

**CHANGELOG 自动化**: 文档工程师必须先运行 `git log --oneline --no-merges --since="Phase 开始日期"` 获取 commit 列表作为初稿，不手写。

### 路径 C: Kernel Guardian Phase 回顾

派发 Agent:
```
角色: Kernel Guardian
任务: 执行 Phase 回顾，检查 7 个维度（绿/黄/红）。

输入:
- specs/{branch}/kernel-constraints.md
- specs/{branch}/tasks.md
- specs/{branch}/tech-debt.md
- specs/{branch}/qa-report.md
- specs/{branch}/role-roster.md
- git log --oneline --no-merges --since="Phase 开始日期"

7 个维度:
A. 工作流完整性 — 9 阶段(S0-S8)是否全执行
B. Speckit 合规 — 是否由 Speckit 生成而非手写
C. 角色完整性 — 适用角色是否全参与
   评分标准:
   - 绿: 所有适用角色（role-roster.md 中 ON 的角色）参与
   - 黄: 1-2 个缺席有合理理由
   - 红: 3+ 缺席或连续 2 Phase 缺席
D. 内核集成健康度 — 核心组件是否因本 Phase 退化
E. 标准文件齐全度 — 检查标准文件存在性（区分于功能完整度）
   标准文件清单: spec.md, decisions.md, plan.md, tasks.md, review-findings.md,
   tech-debt.md, qa-report.md, user-signoff.md, phase-report.md,
   kernel-review-report.md, product-review-report.md
F. 反馈闭环 — 上一 Phase 改进建议是否被执行
G. Tech Debt 趋势 — 本 Phase 新增 vs 解决（仅统计 [TECH] 标签）

产出: specs/{branch}/kernel-review-report.md
包含: "必须在下一 Phase 修复"项（不超过 3 条）
```

### 路径 D: 产品经理产品评审

派发 Agent:
```
角色: 产品经理
任务: 执行产品评审，检查 7 个维度（绿/黄/红）。

输入:
- specs/{branch}/product-context.md
- specs/{branch}/product-acceptance-criteria.md
- specs/{branch}/qa-report.md
- specs/{branch}/tech-debt.md
- specs/{branch}/user-signoff.md  ← 新增输入

7 个维度:
A. 验收标准覆盖率 — 分级通过标准:
   P1 AC（核心功能）= 100% PASS
   P2 AC（增强功能）= 允许 SKIP 附理由，不允许 FAIL
   P3 AC（基础设施）= 允许 SKIP
B. UI 合规检查 — 空状态处理？错误提示？Loading 状态？导航可达？
   引用 user-signoff.md 四视角评分中的具体发现
C. 错误路径覆盖率 — spec Edge Cases vs E2E 覆盖的比例
D. 文档链路完整性 — OpenAPI 含新 endpoints？README 引用新功能？部署文档含新配置？
E. 功能完整度 — spec 中定义的功能是否全部实现
F. 成功标准达成度 — product-context.md 中的成功标准是否满足
G. 产品 Tech Debt — 产品层面的妥协（[PRODUCT] 标签）
   本 Phase 新增: N 条，上一 Phase 遗留已解决: M 条

产出: specs/{branch}/product-review-report.md
包含: 不超过 3 条必须修复项
```

### 路径 E: Roadmap 规划师（B/C/D 完成后触发）

**前置条件**: 路径 B/C/D 全部完成。派发前必须验证文件存在：
```
[ ] specs/{branch}/phase-report.md 存在（路径 B 产出）
[ ] specs/{branch}/kernel-review-report.md 存在（路径 C 产出）
[ ] specs/{branch}/product-review-report.md 存在（路径 D 产出）
```
任一缺失则等待对应路径完成，不可跳过。

派发 Agent:
```
角色: Roadmap 规划师
任务: 将本 Phase 结果回灌到下一 Phase 计划。
输入: phase-report.md + tech-debt.md + kernel-review-report.md + product-review-report.md
产出: roadmap 更新记录
```

---

## 8.3 并行双确认

8.2 全部完成后，两方**同时执行**，互不阻塞。

### 8.3-A 产品经理产品验收确认

```
[ ] product-context.md 存在
[ ] product-acceptance-criteria.md AC 通过分级标准:
    P1 AC（核心功能）= 100% PASS
    P2 AC（增强功能）= 无 FAIL（SKIP 必须附理由）
    P3 AC（基础设施）= 允许 SKIP
[ ] product-review-report.md 无红色维度
[ ] user-signoff.md 判定非 REJECT（如适用；纯后端 Phase UI 视角标 N/A）
→ 产品 PASS / 产品 FAIL（列出未达标项）
```

### 8.3-B 项目经理流程完成确认

```
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
[ ] Playwright E2E 测试存在（有 UI 时；否则标注 N/A）
[ ] tech-debt-registry.md 已汇总更新
[ ] kernel-review-report.md 存在且 7 维度已评分
[ ] product-context.md 存在
[ ] product-acceptance-criteria.md 存在
[ ] product-review-report.md 存在
[ ] user-signoff.md 存在且四视角完整（纯后端 Phase UI 视角标 N/A）
[ ] user-signoff.md 判定非 REJECT
[ ] review-findings.md 存在

记忆:
[ ] memory 已更新
[ ] roadmap 标记 Phase 完成

→ 项目 PASS / 项目 FAIL（列出未完成项）
```

### 回退路径（分离）

- **产品 FAIL** → 回到 8.2 补做产品相关修复 → 仅重走 8.3-A
- **项目 FAIL** → 回到 8.2 补做流程相关修复 → 仅重走 8.3-B
- 单方 FAIL 不影响另一方已获得的 PASS

### 回退限制

- 产品/项目 FAIL 最多回退 1 次
- 回退只能补文档/配置，不能改代码（改代码必须回 S6）
- 2 次 FAIL → Phase 挂起，总负责人裁决

---

## 8.4 双 PASS 后收尾

**仅在产品 PASS AND 项目 PASS 后执行。**

1. 将 8.2 收尾文档 commit + push 到 feature 分支：

```bash
git add specs/ docs/ CHANGELOG.md
```

```bash
git commit -m "docs: Phase {N} closing — {Phase 名称}"
```

```bash
git push origin {branch}
```

2. 阶段门最终检查:

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S8 --branch {branch} --check exit
```

3. 合并 PR（保留 per-PR commit 粒度，便于 bisect/revert）：

```bash
gh pr merge {pr-url} --merge
```

4. 确认 Phase 完成

---

## 硬性产出物

| 文件 | 路径 | 责任角色 |
|------|------|---------|
| phase-report.md | `specs/{branch}/phase-report.md` | 文档工程师 |
| CHANGELOG.md | 项目根目录 | 文档工程师 |
| tech-debt-registry.md | `docs/tech-debt-registry.md` | 文档工程师 |
| kernel-review-report.md | `specs/{branch}/kernel-review-report.md` | Kernel Guardian |
| product-review-report.md | `specs/{branch}/product-review-report.md` | 产品经理 |
| 产品 PASS/FAIL | 8.3-A 确认结果 | 产品经理 |
| 项目 PASS/FAIL | 8.3-B 确认结果 | 项目经理 |

## 出口条件

```
[ ] kernel-review-report.md 存在且 7 维度已评分 [GATE]
[ ] product-review-report.md 存在且 7 维度已评分 [GATE]
[ ] phase-report.md 存在 [GATE]
[ ] CHANGELOG.md 已更新 [GATE]
[ ] 产品 PASS [AGENT]
[ ] 项目 PASS [AGENT]
[ ] phase-gate-check.py --stage S8 --branch {branch} --check exit = PASS [GATE]
```
