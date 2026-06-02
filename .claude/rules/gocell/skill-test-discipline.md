# Skill / Agent PR 测试纪律

> 触碰 `.claude/skills/*` 或 `.claude/agents/*` 的 PR，必须记录相关验证并把摘要写进 PR body。
> 含可执行逻辑的 skill 用 CI smoke fail-closed 机器保证；prose skill（仅 SKILL.md）走 PR body 人工记录。

## 触发条件

以下任一变更触发本规则：

- 修改 `.claude/skills/**`（含 `lib/`、`test/`、`SKILL.md`）
- 修改 `.claude/agents/**`

不触发：仅修改 `.claude/rules/`、`.claude/plans/`、`.claude/agent-memory/`。

## Skill 两类

| 类 | 形态 | 验证载体 | 评级 |
|----|------|---------|------|
| **Prose skill** | 仅 SKILL.md + CLI 示例（无 `lib/*.sh`、无 `test/`）。当前：`ship` / `fix` / `pm-issue` / `pm-epic` / `pm-flow` | 无可执行逻辑可测 → PR body 记录验证方式（人工） | 人工提醒（Soft checklist） |
| **含逻辑 skill** | 带 `lib/*.sh` 纯函数 + `test/smoke.sh` 离线 Part A | CI path-filter smoke job fail-closed（退出非 0 阻塞 PR） | CI fail-closed（test discipline，非 archtest 分级） |

> 当前仓内**无含逻辑 skill**：daily-planner 已退役（PR-B 删除），其 CI `skill-daily-planner-smoke` job
> 同步移除。新增 pm-* 系列是 prose skill。未来新增含逻辑 skill 时按下方「配套要求」补三件套。

> CI smoke 是测试行为（test discipline），不是 ai-robust §适用范围内的"约束 enforcement 机制"（archtest /
> governance rule / codegen funnel / type marker），故**不评 Hard / Medium / Soft archtest 分级**。
> PR body checkbox 是 Soft（字面约定）；含逻辑 skill 真正 gate 靠 CI job。本规则不引入任何新 archtest。

## 含逻辑 skill 的配套要求（新增时）

**新加或修改 CI 步骤前，本地先跑通 smoke，再写进 workflow。**

| 条目 | 说明 |
|------|------|
| `test/smoke.sh` | 离线 Part A（确定性，无 live gh）+ 可选 Part B（`SMOKE_LIVE=1`）；镜像 `.github/scripts/auto-label-priority-test.sh` 的 `run_case` + PATH-stub + 计数 + `exit 1` 惯例 |
| `test/fixtures/` | Part A 所需的 JSON/YAML fixture 文件 |
| pr-check.yml path-filter job | 触碰新 skill 路径时触发（参考 `.github/scripts/auto-label-priority-test.sh` 的离线测试惯例） |

**通过判定标准**：退出码 0 = 通过；最后一行含 `# Summary: N pass, 0 fail`（`0 fail` 为必要条件）。示例：

```
# Summary: 12 pass, 0 fail
```

> 三件套缺一不补充时，CI 不会为新 skill 强制 gate——不应该静默。prose skill 在 PR body 记录验证方式即可。
