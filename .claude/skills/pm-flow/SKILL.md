---
name: pm-flow
description: "双轮 PR 工作流编排：ship 实施 + round-1 内置 review/fix → 交接 codex 二轮 review → fix 读 PR 评论 round-2 续修。当用户要走完整 ship→codex→fix 流程、或在 codex 二轮评论后续修时使用。两段式：start（实施到待 codex）/ resume <#PR>（读 codex 评论续修）。"
argument-hint: "<#issue 或任务描述>  |  resume <#PR>"
allowed-tools: [Read, Grep, Bash, Agent, AskUserQuestion]
---

# pm-flow — ship → codex → fix 双轮编排

> 编排器：串起 `ship`（round-1 实施+内置 review+fix）→ **外部 codex 二轮 review**（你手动跑）→ `fix --from-pr`
> （round-2 续修）。label/评论原子操作的规范见 `pm-issue`（评论格式单源）；真源见 `.github/PROJECT.md`。
> codex 是外部独立审查工具，**本技能无法直接调用**——round-1 完成后停下交接，你跑完 codex 再 resume。
> `gh` 命令用 `dangerouslyDisableSandbox: true`。仓库 `ghbvf/gocell`。

---

## 入参分段

| 形态 | 段 | 动作 |
|------|----|------|
| `<#issue 或任务描述>` | **start** | 阶段 1–3（实施 → round-1 → 交接停下） |
| `resume <#PR>` | **resume** | 阶段 4–5（读 codex 评论 → round-2 → 收尾） |

---

## 阶段 1：实施 + round-1（运行 ship）

运行 `/ship <#issue 或任务描述>`（默认 L3：探索→计划→AskUserQuestion 确认→worktree→TDD→实施→PR→
内置 6 维 reviewer→/fix Cx1/Cx2）。ship 完成后产出：PR 号 + round-1 findings 表（已修 / 遗留）。

> pm-flow 不重复 ship 的步骤；ship 自带「多沟通」（方案/计划确认）。

## 阶段 2：round-1 收尾（pr-status + 评论）

PR 创建时 ship 已贴 `pr-status/in-progress`。round-1 fix 完成后：

```bash
# 按 pm-issue §4 格式贴 round-1 评论（含 <!-- pm:round-1 --> 标记）
gh pr comment <PR#> --body "$(cat <<'C'
<!-- pm:round-1 -->
## 🛠 Round-1（ship 内置 review + fix）
...findings 表...
**下一步**：待 codex 二轮 review。
C
)"
# 切状态
gh pr edit <PR#> --add-label pr-status/needs-codex --remove-label pr-status/in-progress
```

## 阶段 3：交接 codex（停下）

向用户输出交接提示并**结束本次执行**：

```
✅ Round-1 完成。PR #<N> 已切 pr-status/needs-codex。
👉 请在外部跑 codex 对 PR #<N> 做二轮 review（评论会写进 PR）。
   完成后运行：/pm-flow resume <N>   续 round-2。
```

> 不自动等待 / 不轮询；codex 是外部动作。

## 阶段 4：resume — 读 codex 评论 + round-2（运行 fix）

```bash
# 读 codex 二轮 review 评论（review 概要 + inline 行评论）
gh pr view <PR#> --json reviews,comments
gh api repos/ghbvf/gocell/pulls/<PR#>/comments --jq '.[] | {path,line,body}'
```

把 codex findings 交 `/fix --from-pr <PR#>`（fix 内部 triage：CONFIRMED IN/OUT/RELATED、RESOLVED、
CANNOT_VERIFY，按 Cx 自动决策 + 修 IN_SCOPE/RELATED）。

> codex 无新评论 / 全部 approved → 跳过 round-2 修复，直接阶段 5 收尾为 ready。

## 阶段 5：round-2 收尾（pr-status + 评论）

```bash
# 按 pm-issue §4 格式贴 round-2 评论（含 <!-- pm:round-2 --> 标记）
gh pr comment <PR#> --body "$(cat <<'C'
<!-- pm:round-2 -->
## 🔁 Round-2（codex 二轮 review + fix）
...codex findings triage + 修复结果...
C
)"

# 全清 → ready；仍有遗留 → changes-requested
gh pr edit <PR#> --add-label pr-status/ready --remove-label pr-status/needs-codex
# 或：gh pr edit <PR#> --add-label pr-review/changes-requested
```

输出最终摘要：PR 链接 / 两轮 findings 计数 / 未处理项（需人工决策）表。

## 沟通规则

- 阶段 3 交接是**硬停点**：不替用户跑 codex、不假装 codex 结果。
- round-2 仍有 Cx3/Cx4 或 OUT_OF_SCOPE 遗留 → 贴 `pr-review/changes-requested` + 摘要列出，不强行 ready。
- ship/fix 自身的 AskUserQuestion 沟通点（方案确认 / issue create）由各自技能负责，pm-flow 不重复。
