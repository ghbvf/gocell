---
name: pm-issue
description: "GitHub Issue / PR 的创建·更新·评论·label 管理单源规范。issue 贴 area/type/pri label；PR 双轴状态 label 流转；统一 PR 评论格式（ship/fix 共用）。当用户要建/改 backlog issue、贴 label、切 PR 状态、给 PR 留评论时使用。"
argument-hint: "<create-issue | edit-labels | pr-status | comment> [...]"
allowed-tools: [Read, Grep, Bash, AskUserQuestion]
---

# pm-issue — Issue / PR / Label / 评论 单源规范

> 真源 = GitHub Issues + Project v2 #3。label 体系 / 字段 / 评级 rubric 见 `.github/PROJECT.md`（唯一参考）。
> 本技能是 issue/PR 原子操作 + **统一 PR 评论格式** 的单源，`ship` / `fix` 引用本文件的评论格式。
> 所有 `gh` 命令用 `dangerouslyDisableSandbox: true`；写入前 `gh auth status`；create 前先 search 查重（幂等）。

仓库：`ghbvf/gocell`。Project v2：`--owner ghbvf --number 3`（title `gocell`）。

---

## 1. 新建 backlog issue

三维 label 齐全（area + type + pri）+ `backlog`。CLI 直接贴（不走 dropdown，必须显式贴 `pri-pX`，否则
`auto-label-priority.yml` 贴 `pri-missing` 哨兵）：

```bash
gh issue create \
  --label backlog --label pri-p2 --label area-eventing --label type-bug \
  --title "[<ID>] <简短标题>" --body-file <file>
# body 字段单源 = .github/ISSUE_TEMPLATE/backlog.yml 模版（现状 / 修复方向 / Files / Source；条件延后型加 Trigger）——本技能不复制其结构
```

- **area-XX**（1 个，8 选）：见 `.github/PROJECT.md` §2.1。
- **type-XX**（1 个，8 选）：见 §2.2。
- **pri-pX**：评级 rubric 见 `.github/PROJECT.md` §3。`/fix` 派生默认 `pri-p2`；`pri-p0` 仅 incident-driven，停下 AskUserQuestion 确认。
- **flag-cond**（可选）：条件延后型加此 label + body 写 `## Trigger`。

> P0 红线不得默认贴。area/type 漏贴时用 `gh issue edit <N> --add-label area-X --add-label type-X` 补。

## 2. 编辑 label / 关闭 issue

```bash
gh issue edit <N> --add-label area-data --remove-label area-eventing      # 改领域
gh issue edit <N> --add-label type-debt                                   # 加类型
gh issue close <N> --reason completed --comment "Fixed in PR #<NNN>"      # 修复闭合
gh issue close <N> --reason "not planned" --comment "<理由>"              # wontfix
```

epic 用 `epic` label + GitHub 原生 sub-issue（不手写 body task list）。子任务关联用 issue 页 "Create sub-issue"
或 `gh`（sub-issue API）。wave 排序交 `/pm-epic`。

## 3. PR 状态 label（两正交轴，约定流转）

| 轴 | label | 何时切 |
|----|-------|--------|
| **pr-status** | `pr-status/in-progress` | PR 创建后（ship 实施 + 内置 review/fix） |
| | `pr-status/needs-codex` | ship 内置 review/fix 完成，待外部 codex review |
| | `pr-status/ready` | fix 续修完成、无遗留，可合并 |
| **pr-review** | `pr-review/approved` | codex review 无需改 |
| | `pr-review/changes-requested` | codex review 提出需改项 |

```bash
gh pr edit <N> --add-label pr-status/needs-codex --remove-label pr-status/in-progress
gh pr edit <N> --add-label pr-status/ready      --remove-label pr-status/needs-codex
gh pr edit <N> --add-label pr-review/changes-requested
```

> PR 始终保持**恰好一个** `pr-status/*`。切换时同步 `--remove-label` 旧态。

## 4. 统一 PR 评论格式（单源 — ship/fix 引用）

**每次 ship/fix 结束都必须 `gh pr comment <N>` 留痕**（评论不省）。标记**按来源、不编 round 号**（轮次动态：1 次 ship + N 次 fix）：ship 用 `<!-- pm:ship -->`，每次 fix 用 `<!-- pm:fix -->`。同构模板：

```markdown
<!-- pm:ship -->                             ← fix 用 <!-- pm:fix -->
## 🛠 ship review + fix                       ← fix 用 ## 🔁 fix（findings triage + fix）

**Findings**：<总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）
- **F1** [P1·Cx2] <一句话摘要> → ✅ 已修
- **F2** [P2·Cx3] <一句话摘要> → ⏸ 遗留（需人工决策）

**下一步**：ship → 切 `pr-status/needs-codex`（待 codex）；fix → 切 `pr-status/ready`（全清）或列 `pr-review/changes-requested` 遗留。
```

## 5. 沟通规则

- **任何 `gh issue create` / `gh issue close --reason "not planned"` 前**：先输出命令 + body 草稿，
  AskUserQuestion 确认再执行（对齐 `fix` 技能）。
- `pri-p0` 升级（incident/CVE）：停下 AskUserQuestion。
- label 编辑 / PR 状态切换 / 评论：按流程自动执行，不逐条问。
