# EventBus 规范

所有 consumer 使用 `ConsumerBase`，它已内置幂等 Claim/Commit/Release、退避重试、DLX 路由。以下规则补充开发者职责。

## Consumer 声明要求

每个新 consumer 必须在代码注释中声明：

```go
// Consumer: cg-{service}-{event-type}
// Idempotency: Claimer (two-phase Claim/Commit/Release), TTL 24h
// Disposition: Ack on success / Requeue on transient / Reject on permanent
// DLX: broker-native via DispositionReject → Nack(requeue=false)
```

## Handler 实现规则（Solution B）

Handler 签名为 `outbox.EntryHandler`，返回 `outbox.HandleResult`：

```go
func handleEvent(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
    event, err := unmarshal(entry.Payload)
    if err != nil {
        // 永久错误 — Reject 路由到 DLX，不重试
        return outbox.HandleResult{
            Disposition: outbox.DispositionReject,
            Err:         &rabbitmq.PermanentError{Err: err},
        }
    }

    if err := processEvent(ctx, event); err != nil {
        // 瞬态错误 — ConsumerBase 退避重试
        return outbox.HandleResult{
            Disposition: outbox.DispositionRequeue,
            Err:         err,
        }
    }

    return outbox.HandleResult{Disposition: outbox.DispositionAck}
}
```

### Disposition 语义

| Disposition | 含义 | ConsumerBase 行为 |
|-------------|------|-------------------|
| `DispositionAck` | 成功处理 | broker Ack → Receipt.Commit |
| `DispositionRequeue` | 瞬态失败 | 退避重试，耗尽后 Reject |
| `DispositionReject` | 永久失败 | broker Nack(requeue=false) → DLX |

- **零值 HandleResult{} 的 Disposition 是 invalid**（不等于 Ack），会被安全降级为 Requeue
- `PermanentError` 包装的错误即使返回 Requeue 也会被 ConsumerBase 升级为 Reject

### 旧 handler 迁移

使用 `outbox.WrapLegacyHandler` 适配旧签名：

```go
legacy := func(ctx context.Context, entry outbox.Entry) error { ... }
handler := outbox.WrapLegacyHandler(legacy)
// nil error → Ack, non-nil → Requeue
```

注意：WrapLegacyHandler 不检测 PermanentError，需要通过 ConsumerBase 包装才能路由到 DLX。

## 死信路由

- DLX 由 broker 原生处理（`DispositionReject` → `Nack(requeue=false)` → DLX exchange）
- L2 consumer 必须配置 DLX exchange（`SubscriberConfig.DLXExchange`）
- 死信消息必须可观测（计数指标或日志）

## 幂等模型

- 使用 `Claimer`（两阶段 Claim/Commit/Release），不再使用旧 `Checker`
- Claim 获取处理租约 → handler 执行 → broker Ack 后 Commit / 失败时 Release
- 默认 fail-closed：Claimer 故障时 Requeue，不丢弃幂等保护

## Stream 命名

- 新建 stream 前搜索已有常量，禁止重复定义
- stream 名 ≥ 3 次使用抽常量
- 按 stream 过滤事件时区分通道

## 事件负载

- 每个事件包含 `event_id`（UUID），用于幂等键构造
- 负载变更向后兼容（新字段 optional，或版本化如 `device.enrolled.v2`）
- 不兼容变更：先部署 consumer 再部署 producer
