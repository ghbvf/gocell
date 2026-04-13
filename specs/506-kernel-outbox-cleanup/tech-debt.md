# Tech Debt — 506 Kernel Outbox Cleanup

## 分类说明

- `[TECH]`: 技术债务（代码质量、架构边界、测试/运维护栏）
- `[PRODUCT]`: 产品债务（示例体验、可观测性、文档说明）

## 延迟项

| # | 标签 | 来源席位 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|---------|------|---------|-------------|
| 1 | [TECH] | 安全 / 运维 | 共享 `NoopOutboxWriter` / `DiscardPublisher` 仍然是显式可注入的公共 helper，尚无统一的“非测试/非示例禁止使用”护栏。 | 需要跨 cell/runtime 的装配策略或 lint/CI 规则，不适合并入这次局部 cleanup。 | 后续装配/治理批次 |
| 2 | [TECH] | DX / 测试 | `outbox.Receipt` 兼容 alias 仍保留，且仓库里还有剩余调用点尚未全面迁移到 `idempotency.Receipt`。 | 当前 PR 已把 canonical owner 和代表性路径迁移到位；全仓统一迁移属于下一波机械收口。 | 下一次 kernel/idempotency 收口批次 |
| 3 | [PRODUCT] | 运维 | discard path 目前依赖结构化日志暴露，没有专门的 metrics/health 指标。 | 当前需求仅覆盖 demo/test helper 清理；若要引入指标，需要先定义统一 observability 契约。 | 需要 demo/ops 观测增强时 |
| 4 | [PRODUCT] | 安全 / 产品 | `order-cell` 默认 nil publisher demo 语义仍然是“HTTP 成功但跳过 order.created 事件发布”。 | 这是现有 example 行为收紧后的产品决策问题，不适合在本次 outbox helper cleanup 中直接改掉默认 demo 体验。 | 下一次 order-cell runtime mode 收口批次 |

## 统计

- [TECH] 新增: 2 条
- [PRODUCT] 新增: 2 条
- 本轮已修复的 review finding: 3 条 P1 + 1 条 P2