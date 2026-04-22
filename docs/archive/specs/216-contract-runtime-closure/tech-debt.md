# Tech Debt — Contract Runtime Closure

## 分类说明

- `[TECH]`: 技术债务（代码质量、架构退化、测试缺失）
- `[PRODUCT]`: 产品债务（文档、体验、示例说明）

## 延迟项

| # | 标签 | 来源席位 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|---------|------|---------|-------------|
| 1 | [PRODUCT] | 产品 | `todo-order` README 与 journey 对 durable mode 的摘要说明已经改正大方向，但还没有把"显式 durable repository 注入"写进所有上层摘要段落。 | 不阻塞本批 contract/runtime closure；当前 checklist 已经覆盖该约束。 | 下一个 README 文案整理批次 |
| 2 | [TECH] | 架构 | durable repository 约束目前是运行时 fail-fast，而不是类型系统层面的仓储能力区分。 | 现有运行时保护已经足以阻塞错误装配；引入分层能力接口会扩大本批改动范围。 | 后续示例/装配抽象收敛批次 |
| 3 | [TECH] | 架构 | `discardPublisher` 定义在 order-cell/cell.go，属于通用 no-op 模式，未来 device-cell 等 Cell 也可能需要。建议提升到 `kernel/outbox.DiscardPublisher` 以避免重复。 | 只有 order-cell 有此需求，先保留在 Cell 内部。 | 第二个 Cell 引入时提取 |

## 已修复（Round 3）

| # | 标签 | 原始问题 | 修复方式 |
|---|------|---------|---------|
| R3-1 | [TECH] | `evt-` 前缀与 headers.schema.json "UUID" 描述矛盾 | 更新 10 个 headers.schema.json 描述为 "Prefixed event identifier (evt-{uuid})" |
| R3-2 | [TECH] | access-core contract test 路由路径不匹配生产路由 | 更新 contract YAML paths 和 contract_test.go 路由为 `/api/v1/access/users` |
| R3-3 | [TECH] | `outbox.Entry.Validate()` 不校验 ID | 新增 `ID != ""` 校验 |
| R3-4 | [TECH] | FMT-13 缺少反向约束（noContent=false 无 response schema） | 新增 SeverityWarning 级别检查 |
| R3-5 | [TECH] | order-create 测试 mock 重复定义 | 合并为共享 `recordingWriter`/`stubTxRunner` |
| R3-6 | [TECH] | 硬编码密码 `secret123` | 提取为 `testPassword` 常量 |
| R3-7 | [PRODUCT] | specs 目录包含绝对路径 | 替换为相对/通用路径 |

## 统计

- [TECH] 新增: 1 条 (discardPublisher 提取)
- [PRODUCT] 新增: 0 条
- Round 1-2 阻塞项已解决: 3 条
- Round 3 审查项已解决: 7 条