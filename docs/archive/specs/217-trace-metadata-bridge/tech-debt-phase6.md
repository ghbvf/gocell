# Tech Debt — Phase 6

## 分类说明

- [TECH]: 技术债务（代码质量、架构退化、测试缺失）
- [PRODUCT]: 产品债务（降级体验、缺失功能、临时方案）

## 延迟项

| # | 标签 | 来源席位 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|---------|------|---------|-------------|
| 1 | [TECH] | 安全/权限 | `request_id` / `correlation_id` 直接从外部 `X-Request-Id` 提升为内部关联 ID，存在信任边界混淆风险。 | 这是既有 request-id middleware 设计，当前分支没有改动该链路；修复需要单独评估 header 契约、兼容性和错误回退策略。 | 下一个 security / observability hardening batch |
| 2 | [TECH] | 安全/权限 | observability IDs 与业务 metadata 共用自由 `map[string]string`，缺少系统保留命名空间或冲突防护。 | 需要跨 outbox writer、relay、subscriber 和公共契约统一调整，超出 `META-BRIDGE-01` 的最小闭环。 | 下一轮 contract / metadata hardening |
| 3 | [TECH] | 运维/部署 | consume 侧 bridge 没有 runtime kill switch，只能通过代码回退关闭。 | 当前变更不改变 payload 或 broker 协议，且本地默认测试矩阵与 PR CI 全绿；现阶段不引入 feature flag / env 开关的额外复杂度。 | 如生产验证需要，再在运维 hardening 批次处理 |

## 统计

- [TECH] 新增: 3 条
- [PRODUCT] 新增: 0 条
- 上一 Phase 遗留已解决: 0 条
