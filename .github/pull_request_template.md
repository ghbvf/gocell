## Summary

<!-- 一句话描述这个 PR 做了什么 -->

## Refs

<!-- 关联 issue / ADR / plan，如 Closes #NNN -->

## Test plan

- [ ] `go build ./...` 本地通过
- [ ] `go test ./修改的包/...` 本地通过（涉及逻辑变更时）
- [ ] skill / agent-class PR（触碰 `.claude/skills/**` 或 `.claude/agents/**`）：已附 smoke run 记录

> Note：最后一条 checkbox 是人工提醒；真正的 gate 是 CI `skill-daily-planner-smoke` job（pr-check.yml）。
> 其他 skill 目录的 smoke 尚无自动 CI，需手动在 PR body 粘贴本地 `bash .claude/skills/<skill>/test/smoke.sh` 的输出摘要。
