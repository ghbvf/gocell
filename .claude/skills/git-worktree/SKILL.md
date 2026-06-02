---
name: git-worktree
description: Git Worktree 项目约定（编号、基准分支、权限兼容、删除安全）。
---

# Git Worktree 项目约定

## 约束

- 目录：`worktrees/<Type>/<issue#-short-name>`（有关联 issue）/ `worktrees/<Type>/<short-name>`（无 issue）；分支名镜像该 path（如 `Feature/1234-short-name`）
- 基准：创建前 `git fetch origin`，基于 `origin/develop`
- 禁止 `cd worktrees/xxx && ...`，替代方案：
  - git: `git -C worktrees/xxx ...`
  - go: `go -C worktrees/xxx build/test ...`（Go 1.21+）
- 用完即删 `git worktree remove`

## 删除安全

**禁止在 worktree 目录内直接删除当前 worktree** — 会导致 Claude Code 工作目录丢失、会话异常。

正确顺序：
1. 在 worktree 内完成工作、提交、推送
2. **先退出 worktree 中的 Claude Code 会话**
3. **回到主仓库目录**，再执行 `git worktree remove worktrees/<Type>/<name>`

## 编号与类型

- **编号 = 关联 issue 编号**（以 issue 编号为准，不再按范围 +1）；**无关联 issue → 不编号** 。
- **Type**（path 首段 + 分支首段）按关键字判定：

| Type | 关键字 |
|------|--------|
| Feature | 默认 |
| Fix | fix, bug, hotfix, hardening |
| Refactor | refactor, cleanup, rename |
| Docs | docs, architecture, adr |
| Experiment | experiment, poc, spike |

例：issue #1234 的 feature → `worktrees/Feature/1234-short-name`，分支 `Feature/1234-short-name`；无 issue 的 refactor → `worktrees/Refactor/short-name`，分支 `Refactor/short-name`。
