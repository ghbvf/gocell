## Summary

<!-- 一句话描述这个 PR 做了什么 -->

## Refs

<!-- 关联 issue / ADR / plan，如 Closes #NNN -->

## Test plan

- [ ] `go build ./...` 本地通过
- [ ] `go test ./修改的包/...` 本地通过（涉及逻辑变更时）
- [ ] skill / agent-class PR（触碰 `.claude/skills/**` 或 `.claude/agents/**`）：prose 技能在 PR body 记录验证方式；含可执行逻辑的技能附本地 smoke 输出（`# Summary: N pass, 0 fail`）

## PR 状态 label（约定，见 `.github/PROJECT.md` §5）

PR 走双轮流程，用 label 表达状态——两正交轴：

- **pr-status**（流转）：`pr-status/in-progress` → `pr-status/needs-codex`（待 codex 二轮）→ `pr-status/ready`
- **pr-review**（codex 结论）：`pr-review/approved` / `pr-review/changes-requested`

> 流转由 `/ship` `/fix` `/pm-flow` 自律切换 + 每轮 `gh pr comment` 留痕（`<!-- pm:round-1 -->` / `<!-- pm:round-2 -->`）。
