# Tech Debt — Contract Runtime Closure

## 分类说明

- `[TECH]`: 技术债务（代码质量、架构退化、测试缺失）
- `[PRODUCT]`: 产品债务（文档、体验、示例说明）

## 延迟项

| # | 标签 | 来源席位 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|---------|------|---------|-------------|
| 1 | [PRODUCT] | 产品 | `todo-order` README 与 journey 对 durable mode 的摘要说明已经改正大方向，但还没有把“显式 durable repository 注入”写进所有上层摘要段落。 | 不阻塞本批 contract/runtime closure；当前 checklist 已经覆盖该约束。 | 下一个 README 文案整理批次 |
| 2 | [TECH] | 架构 | durable repository 约束目前是运行时 fail-fast，而不是类型系统层面的仓储能力区分。 | 现有运行时保护已经足以阻塞错误装配；引入分层能力接口会扩大本批改动范围。 | 后续示例/装配抽象收敛批次 |

## 统计

- [TECH] 新增: 1 条
- [PRODUCT] 新增: 1 条
- 本 Phase 阻塞项已解决: 3 条