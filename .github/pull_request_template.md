## Summary

<!-- 一句话描述这个 PR 做了什么 -->

## Refs

<!-- 关联 issue / ADR / plan，如 Closes #NNN -->

## Test plan

- [ ] `go build ./...` 本地通过
- [ ] `go test ./修改的包/...` 本地通过（涉及逻辑变更时）
- [ ] skill / agent-class PR（触碰 `.claude/skills/**` 或 `.claude/agents/**`）：已附 smoke run 记录

> Note：最后一条 checkbox 的含义因触碰内容而异：
>
> - **触碰 `.claude/skills/daily-planner/**` 或 `.claude/agents/daily-planner.md`**：真正的 gate 是 CI `skill-daily-planner-smoke` job（pr-check.yml）；job 通过即可，勾 checkbox 仅作人工确认。
> - **触碰其他 skill 目录或 agent（无对应 CI job）**：必须手动在 PR body 粘贴本地运行 `bash .claude/skills/<skill>/test/smoke.sh` 的输出摘要（含 `# Summary: N pass, 0 fail` 行），checkbox 是唯一 gate。
