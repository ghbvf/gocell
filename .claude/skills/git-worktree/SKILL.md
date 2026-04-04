---
name: git-worktree
description: Git Worktree 项目约定（编号、基准分支、权限兼容、删除安全）。
---

# Git Worktree 项目约定

## 约束

- 目录：`worktrees/<NNN-short-name>`
- 基准：创建前 `git fetch origin`，基于 `origin/main`
- 禁止 `cd worktrees/xxx && ...`，替代方案：
  - git: `git -C worktrees/xxx ...`
  - go: `go -C worktrees/xxx build/test ...`（Go 1.21+）
  - grep/ls/find: 直接用绝对路径，或用 Grep/Glob 工具
- 用完即删 `git worktree remove`

## 删除安全

**禁止在 worktree 目录内直接删除当前 worktree** — 会导致 Claude Code 工作目录丢失、会话异常。

正确顺序：
1. 在 worktree 内完成工作、提交、推送
2. 导出工作总结到 `bak/worktree/<NNN-short-name>/memory.md`（主仓库路径）
3. **先退出 worktree 中的 Claude Code 会话**
4. **回到主仓库目录**，再执行 `git worktree remove worktrees/<NNN-short-name>`

## 编号

| 类型 | 范围 | 关键词 |
|------|------|--------|
| Feature | 001–199 | 默认 |
| Fix | 200–299 | fix, bug, hotfix, hardening |
| Refactor | 500–599 | refactor, cleanup, rename |
| Docs | 800–899 | docs, architecture, adr |
| Experiment | 900–999 | experiment, poc, spike |

编号取范围内已有最大值 +1，`printf "%03d"` 格式化。扫描 `specs/` + `git branch -a` 确定。
