# Skill / Agent PR 测试纪律

> 触碰 `.claude/skills/*` 或 `.claude/agents/*` 的 PR，必须运行相关 smoke 并把 pass 摘要记录在 PR body 中。
> CI fail-closed job 是机器保证；PR body 记录是人工可见性补充。

## 触发条件

以下任一变更触发本规则：

- 修改 `.claude/skills/**`（含 `lib/`、`test/`、`SKILL.md`）
- 修改 `.claude/agents/**`

不触发：仅修改 `.claude/rules/`、`.claude/plans/`、`.claude/agent-memory/`。

## Enforcement 表

| 约束 | 载体 | 评级 | 说明 |
|------|------|------|------|
| daily-planner smoke Part A 通过 | CI `skill-daily-planner-smoke` job（`.github/workflows/pr-check.yml`） | CI fail-closed（ai-robust §适用范围外的 test，非 Soft 立项） | path-filter 仅触碰 `.claude/skills/daily-planner/**` 时跑；退出非 0 → job 失败，PR 阻塞 |
| PR body 含 smoke 通过摘要 | `.github/pull_request_template.md` checkbox | 人工提醒（Soft checklist） | 人工提醒，不 gate；其他 skill 目录无 CI job 时需手动粘贴本地 smoke 输出 |

> CI smoke 是测试行为（test discipline），不是 ai-robust §适用范围内的"约束 enforcement 机制"（archtest / governance rule / codegen funnel / type marker），故**不评 Hard / Medium / Soft archtest 分级**。
> PR body checkbox 是 Soft（字面约定），但这是**唯一** Soft——真正 gate 靠 CI job，不靠 checkbox。
> 本规则不引入任何新 archtest。

## 本地验证流程（用户 CI 纪律）

**新加或修改 CI 步骤前，本地先跑通 smoke，再写进 workflow**（同 `.claude/rules/gocell/skill-test-discipline.md` 本 PR 纪律）：

```bash
# daily-planner（离线 Part A，无需 gh auth）
bash .claude/skills/daily-planner/test/smoke.sh

# 输出示例
# Summary: 12 pass, 0 fail
```

`SMOKE_LIVE=1` 时跑 Part B（live read-only，需 `gh` auth + `project` scope），CI 不设该环境变量。

## 新增 skill 时的配套要求

| 条目 | 说明 |
|------|------|
| `test/smoke.sh` | 离线 Part A（确定性，无 live gh）+ 可选 Part B（`SMOKE_LIVE=1`）；镜像 `.github/scripts/auto-label-priority-test.sh` 的 `run_case` + PATH-stub + 计数 + `exit 1` 惯例 |
| `test/fixtures/` | Part A 所需的 JSON/YAML fixture 文件 |
| pr-check.yml path-filter job | 触碰新 skill 路径时触发；参考 `skill-daily-planner-smoke` job 结构 |

> 三件套缺一不补充时，CI 不会为新 skill 强制 gate——不应该静默。新 skill 未覆盖则在 PR body 手动记录 smoke 结果，并同步开 backlog 条目跟踪 CI job 配套。
