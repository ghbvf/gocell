# Workflow 索引

从 harness-with-product-status 项目迁移的工作流文档，用于 GoCell 项目参考和适配。

## 目录结构

### 通用方法论参考（直接使用）

- [`01-team-analysis/report.md`](01-team-analysis/report.md) — 多角色并行分析方法论，量化 P0 bug 发现规律
- [`02-claude-experiments/report.md`](02-claude-experiments/report.md) — 17 篇 Anthropic 论文研究：Agent drift、多 Agent 编排、上下文工程
- [`03-feasibility-synthesis/report.md`](03-feasibility-synthesis/report.md) — 5 条结构性约束 + 3 层改进框架
- [`04-pm-split-team-review/report.md`](04-pm-split-team-review/report.md) — 并行确认、AC 优先级分级方法论

### 当前基线（待适配为 GoCell 版本）

- [`v4.2/docs/workflow-detailed.md`](v4.2/docs/workflow-detailed.md) — 9 阶段(S0-S8)详细工作流
- [`v4.2/plan.md`](v4.2/plan.md) — v4.1→v4.2 演进原理
- [`v4.2/.claude/`](v4.2/.claude/) — agents/skills/rules/phase-gate 完整配置
- [`v4.2/.specify/`](v4.2/.specify/) — 声明式阶段门禁(YAML) + 检查脚本

### 已删除（历史版本见 git）

v2/v3/v4/v4.1 版本文档及 v4.2 废弃实施方案（029-032）已清理。

## GoCell 适配状态

v4.2/.claude/ 文件适配进度：

| 分类 | 数量 | 文件 |
|------|------|------|
| 保留 | 5 | agents/devops, skills/stage-1-specify, skills/stage-3-decide, skills/phase-gate, phase-gate-check.sh |
| 待调整 | 17 | agents 其余 5 个, skills 其余 7 个, rules/go-standards, phase-gates.yaml |
| 删除 | 0 | 全部有参考价值 |

适配方向：harness-expert→kernel-guardian，Roadmap→兼容性审查，DDD 4 层→GoCell 分层，L1/L2/L3→L0-L4。
