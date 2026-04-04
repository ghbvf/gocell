# Workflow v4.2

`v4.2` 是本仓库当前最新的 workflow 设计基线。阅读时应区分“设计稿链路”和“当前实施基线”。

## 当前最该先看

1. [`docs/workflow-detailed.md`](docs/workflow-detailed.md)
   - `v4.2` 详细手册
2. [`202604040846-032-v4.2-cell-owned-slices-psql-s3-实施计划.md`](202604040846-032-v4.2-cell-owned-slices-psql-s3-实施计划.md)
   - 当前实施基线
3. [`plan.md`](plan.md)
   - `v4.1 -> v4.2` 的改造原则与设计动机
4. [`CLAUDE.md`](CLAUDE.md)
   - `v4.2` 的轻量协作规则入口

## 设计稿链路

- [`202604040242-029-v4.2-工作流实施计划.md`](202604040242-029-v4.2-工作流实施计划.md)
  - 第一版 `v4.2` 实施计划
- [`202604040251-030-v4.2-mcp-langgraph-具体实施方案.md`](202604040251-030-v4.2-mcp-langgraph-具体实施方案.md)
  - 中间方案，已被后续方案替代
- [`202604040257-031-v4.2-langgraph-first-从头开发实施方案.md`](202604040257-031-v4.2-langgraph-first-从头开发实施方案.md)
  - `LangGraph-first` 方案，后续被 `032` 收敛
- [`202604040846-032-v4.2-cell-owned-slices-psql-s3-实施计划.md`](202604040846-032-v4.2-cell-owned-slices-psql-s3-实施计划.md)
  - 当前推荐实施基线

## 配套材料

- 编排契约：[`../../contracts/202604030559-027-langgraph-claude-code-orchestration-contracts.md`](../../contracts/202604030559-027-langgraph-claude-code-orchestration-contracts.md)
- Worker 适配契约：[`../../contracts/202604031310-028-claude-code-codex-worker-adapter-contract.md`](../../contracts/202604031310-028-claude-code-codex-worker-adapter-contract.md)
- 运行手册：[`../../runbooks/202604020723-langgraph-claude-code-orchestration-runbook.md`](../../runbooks/202604020723-langgraph-claude-code-orchestration-runbook.md)

## 状态说明

- `029/030/031` 保留为 `v4.2` 形成过程中的历史链路。
- 当前需要落地时，默认以 `032 + docs/workflow-detailed.md + contracts + runbook` 为准。
