---
name: pm-issue
description: "GitHub Issue / PR 的创建·更新·评论·label 管理单源规范。issue 贴 area/type/pri label；PR 双轴状态 label 流转；统一 PR 评论格式（ship/fix/pm-flow 共用）。当用户要建/改 backlog issue、贴 label、切 PR 状态、给 PR 留评论时使用。"
argument-hint: "<create-issue | edit-labels | pr-status | comment> [...]"
allowed-tools: [Read, Grep, Bash, AskUserQuestion]
---

# pm-issue — Issue / PR / Label / 评论 单源规范

> 真源 = GitHub Issues + Project v2 #3。label 体系 / 字段 / 评级 rubric 见 `.github/PROJECT.md`（唯一参考）。
> 本技能是 issue/PR 原子操作 + **统一 PR 评论格式** 的单源，`ship` / `fix` / `pm-flow` 引用本文件的评论格式。
> 所有 `gh` 命令用 `dangerouslyDisableSandbox: true`；写入前 `gh auth status`；create 前先 search 查重（幂等）。

仓库：`ghbvf/gocell`。Project v2：`--owner ghbvf --number 3`（title `gocell`）。

---

## 1. 新建 backlog issue

三维 label 齐全（area + type + pri）+ `backlog`。CLI 直接贴（不走 dropdown，必须显式贴 `pri-pX`，否则
`auto-label-priority.yml` 贴 `pri-missing` 哨兵）：

```bash
# 查重（幂等）
gh issue list --label backlog --search "<关键词>" --state open --json number,title

# 创建（body 用 here-doc 或 --body-file）
gh issue create \
  --label backlog --label pri-p2 --label area-eventing --label type-bug \
  --title "[<ID>] <简短标题>" \
  --body "$(cat <<'BODY'
## 现状
<当前代码状态 / 已尝试>

## 修复方向
<设计思路 / 范围>

## Files
- path/to/file.go

## Source
PR #NNN / docs/reviews/...
BODY
)"
```

- **area-XX**（1 个，8 选）：见 `.github/PROJECT.md` §2.1。
- **type-XX**（1 个，8 选）：见 §2.2。
- **pri-pX**：评级 rubric 见 §3。`/fix` 派生默认 `pri-p2`；`pri-p0` 仅 incident-driven，停下 AskUserQuestion 确认。
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
| **pr-status** | `pr-status/in-progress` | PR 创建后（ship/pm-flow 实施 + round-1） |
| | `pr-status/needs-codex` | round-1 内置 review+fix 完成，待外部 codex 二轮 |
| | `pr-status/ready` | round-2 fix 完成、无遗留，可合并 |
| **pr-review** | `pr-review/approved` | codex 二轮无需改 |
| | `pr-review/changes-requested` | codex 二轮提出需改项 |

```bash
gh pr edit <N> --add-label pr-status/needs-codex --remove-label pr-status/in-progress
gh pr edit <N> --add-label pr-status/ready      --remove-label pr-status/needs-codex
gh pr edit <N> --add-label pr-review/changes-requested
```

> PR 始终保持**恰好一个** `pr-status/*`。切换时同步 `--remove-label` 旧态。

## 4. 统一 PR 评论格式（单源 — ship/fix/pm-flow 引用）

每轮（ship round-1 / fix round-2）结束 `gh pr comment <N> --body "..."`。评论**必须含机器标记**
`<!-- pm:round-1 -->` 或 `<!-- pm:round-2 -->`（便于人工/工具辨识轮次）。模板：

```markdown
<!-- pm:round-1 -->
## 🛠 Round-1（ship 内置 review + fix）

**Reviewer**：<N> 维（按 diff 行数自动）
**Findings**：<总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）

| # | Finding | P/Cx | 处理 | 备注 |
|---|---------|------|------|------|
| 1 | ... | P1/Cx2 | ✅ 已修 | ... |
| 2 | ... | P2/Cx3 | ⏸ 遗留 | 需人工决策 |

**下一步**：切 `pr-status/needs-codex` → 待你跑 codex 二轮 review。
```

round-2 同构（标记 `<!-- pm:round-2 -->`，标题 `## 🔁 Round-2（codex 二轮 review + fix）`，
末尾「下一步」= 切 `pr-status/ready` 或列出 `pr-review/changes-requested` 遗留项）。

```bash
gh pr comment <N> --body "$(cat <<'C'
<!-- pm:round-1 -->
## 🛠 Round-1（ship 内置 review + fix）
...
C
)"
```

## 5. 沟通规则

- **任何 `gh issue create` / `gh issue close --reason "not planned"` 前**：先输出命令 + body 草稿，
  AskUserQuestion 确认再执行（对齐 `fix` 技能）。
- `pri-p0` 升级（incident/CVE）：停下 AskUserQuestion。
- label 编辑 / PR 状态切换 / 评论：按流程自动执行，不逐条问。
