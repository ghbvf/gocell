# Review Findings — 506 Kernel Outbox Cleanup

## 审查基准版本

Commit: `e71d47c45933fddaff70adcf877219f05fc54ab0`
Branch: `refactor/506-kernel-outbox-cleanup`
变更范围: PR #113 (`refactor(kernel): clean up outbox helpers`)

## P0（阻塞）

| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|
| — | — | — | 无 | — |

## P1（重要）

| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|
| 1 | 测试 / GitHub review | `kernel/outbox/outbox.go` | `NoopOutboxWriter` 作为 `BatchWriter` 时未执行 entry 校验，和接口契约不一致，可能让测试误绿。已在 fix 轮修复。 | `Write`/`WriteBatch` 都先执行 `Entry.Validate()`，并补负向测试。 |
| 2 | 运维 / 产品 / GitHub review | `kernel/outbox/outbox.go` + `cells/order-cell/cell.go` + `cells/order-cell/slices/order-create/service.go` | `IsDiscardPublisher` 检测过宽，且默认 `order-cell` fallback 自动注入 discard sink 改变了原先 nil-sentinel 语义。已在 fix 轮修复。 | 用具体类型检测 discard sink，并恢复默认 nil fallback；仅对显式注入的 discard publisher 走专门日志路径。 |
| 3 | DX / 测试 | `kernel/idempotency/idempotency.go` + `adapters/redis/idempotency.go` + `adapters/rabbitmq/*` + `kernel/outbox/outboxtest/mock_receipt.go` | `Receipt` 迁移最初只停留在 alias 层，代表性 adapter/helper 仍旧宣称 `outbox.Receipt`。已在 fix 轮修复一批核心路径。 | 将代表性 adapter/helper/mocks 的签名与断言切到 `idempotency.Receipt`，保留 `outbox.Receipt` 兼容 alias。 |

## P2（建议）

| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|
| 1 | DX | `kernel/outbox/outbox.go` | `NoopOutboxWriter` 与 `DiscardPublisher` 的语义差异需要更直接的注释说明。已在 fix 轮补充文档化。 | 在类型注释中明确 durable discard 与 direct-publish discard 的差异。 |

## 处理结果

- 6 席位并行审查已完成，架构席位 PASS。
- PR 行级 review comment 2 条已纳入 fix 轮处理。
- fix 轮后的聚焦复核未发现新的 outbox/idempotency C1/C2 回归；`order-cell` 默认 nil publisher 的更大语义问题已登记到 `tech-debt.md`，不在本次 cleanup 内直接改产品行为。
- fix 轮后执行 `go test ./...` 与 `go build ./...`，结果全绿。