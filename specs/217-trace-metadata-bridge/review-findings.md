# Review Findings — Phase 6

## 审查基准版本

Commit: `22a6cdcbcab96b3bbd23ea6b83f8f9905422450b`
Branch: `fix/217-trace-metadata-bridge`
变更范围: `22 files changed`

## P0（阻塞）

无。

## P1（重要）

| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|
| 1 | 测试/回归、产品/框架消费者体验 | `src/tests/integration/outbox_fullchain_test.go` | Full-chain 验收只断言 metadata round-trip，没有证明 consumer handler context 里真的拿到了恢复后的 `request_id` / `correlation_id` / `trace_id`。 | 让全链路集成测试直接断言 handler context，而不是只看 `Entry.Metadata`。 |
| 2 | 架构一致性、产品/框架消费者体验 | `src/adapters/rabbitmq/subscriber.go` | consume 侧 observability bridge 落在 handler middleware 太晚，RabbitMQ subscriber 自身的 per-message 日志和 receipt/broker 处置日志仍用原始 ctx。 | 在 RabbitMQ delivery 边界先恢复 observability context，并让 subscriber 自身日志、handler 调用、receipt 结算共享同一个 delivery ctx。 |

## P2（建议）

| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|
| 1 | 测试/回归 | `src/runtime/bootstrap/bootstrap_test.go` | `TestBootstrap_EventRouter_HappyPath` 使用固定 `time.Sleep` 等待启动，存在 CI 慢机下的 flaky 风险。 | 使用 `WithListener` + `/healthz` readiness 判定后再 `cancel()`。 |
| 2 | DX/可维护性、测试/回归 | `src/kernel/outbox/observability_metadata_test.go` | helper 的边界语义没有被完整锁定，缺少 nil metadata、已有 context 优先、empty reserved key 填充等断言。 | 补齐 focused unit tests，覆盖边界分支。 |
| 3 | DX/可维护性、产品/框架消费者体验 | `src/runtime/observability/logging/doc.go`, `src/runtime/observability/logging/logging.go`, `src/runtime/http/middleware/access_log.go`, `specs/217-trace-metadata-bridge/product-acceptance-criteria.md` | 文档和代码注释没有同步 `correlation_id` 字段，也没有明确 reserved bridge key 的 `missing or empty` 语义。 | 同步注释、README/spec 文案到当前实现契约。 |

## 归属判定与处置

- `IN_SCOPE` 并已修复: P1-1, P1-2, P2-1, P2-2, P2-3
- `OUT_OF_SCOPE` 并转入 tech debt:
  - 安全席位提出的 `X-Request-Id` 信任边界问题，属于既有 request-id middleware 设计，不在本分支 diff 中。
  - 安全席位提出的 observability ID 与自由 metadata 共用 map 的命名空间/冲突防护问题，需要跨 writer/relay/subscriber/contract 的设计加固，不属于 `CID-01 + META-BRIDGE-01` 最小闭环。
  - 运维席位提出的 runtime feature flag / kill switch 建议，不属于当前分支的最小修复范围。

## 验证结论

- 本地默认测试矩阵已通过：`go test ./... -count=1`
- focused package tests 已通过：`go test ./kernel/outbox ./runtime/bootstrap ./adapters/rabbitmq -count=1`
- integration 测试代码已通过编译：`go test -tags=integration ./tests/integration -run '^$' -count=1`
- `TestIntegration_OutboxFullChain` 两次执行均因 Docker/testcontainers 基础设施波动失败，失败点分别为 Redis/PostgreSQL 容器启动，不是断言失败。