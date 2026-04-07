---
name: git-worktree
description: Git Worktree 项目约定（编号、基准分支、权限兼容、删除安全）。
---

# Git Worktree 项目约定

## 约束

- 目录：`worktrees/<NNN-short-name>`
- 基准：创建前 `git fetch origin`，基于 `origin/develop`
- 禁止 `cd worktrees/xxx && ...`，替代方案：
  - git: `git -C worktrees/xxx ...`
  - go: `go -C worktrees/xxx build/test ...`（Go 1.21+）
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

## Per-PR Worktree 约定

S5 per-PR 实施时，每个 PR 使用独立 worktree：

### 创建
```bash
git worktree add .claude/worktrees/{pr-name} -b {pr-branch}
```
- `{pr-name}`: 来自 pr-plan.md 的分支名最后一段（如 `pr-1-metadata`）
- `{pr-branch}`: 完整分支名（如 `phase-1/pr-1-metadata`）
- 不指定 start-point，默认基于当前 HEAD（即 develop 最新状态）

### 命名
```
.claude/worktrees/pr-1-metadata/
.claude/worktrees/pr-2-registry/
.claude/worktrees/pr-3-governance/
```

### 生命周期
1. S5.2a 创建 worktree
2. Developer Agent 在 worktree 中实施 + commit + push + create PR
3. Reviewer Agent 审查 PR diff
4. Fixer Agent 在同一 worktree 中修复（如需要）
5. PR merge 后立即删除: `git worktree remove .claude/worktrees/{pr-name}`

### 与编号 worktree 的区别
| | 编号 worktree | Per-PR worktree |
|---|---|---|
| 用途 | 长期特性分支 | 短期 PR 实施 |
| 命名 | `NNN-short-name` | `pr-{K}-{name}` |
| 生命周期 | 手动创建和删除 | PR merge 后自动删除 |
| 目录 | `worktrees/` | `.claude/worktrees/` |
