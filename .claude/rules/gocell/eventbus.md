# EventBus 规范

所有 consumer 使用 `ConsumerBase`，它已内置幂等检查、DLQ、自动重试。以下规则补充开发者职责。

## Consumer 声明要求

每个新 consumer 必须在代码注释中声明：

```go
// Consumer: cg-{service}-{event-type}
// Idempotency key: {prefix}:{group}:{event-id}, TTL 24h
// ACK timing: after business logic + idempotency key written
// Retry: transient errors → NACK+backoff / permanent errors → dead letter
```

## HandlerFunc 实现规则

- `return error` → ConsumerBase 触发 NACK + 退避重试（瞬态错误）
- `return nil` → ConsumerBase ACK（业务完成）
- unmarshal 失败 → 调用 `deadLetter(ctx, msg, err)` 路由到死信队列

```go
event, err := unmarshal(msg)
if err != nil {
    return deadLetter(ctx, msg, err) // 永久错误，不重试
}
```

## 死信路由

- L2 consumer 必须有死信队列
- 死信消息必须可观测（计数指标或日志）

## Stream 命名

- 新建 stream 前搜索已有常量，禁止重复定义
- stream 名 ≥ 3 次使用抽常量
- 按 stream 过滤事件时区分通道

## 事件负载

- 每个事件包含 `event_id`（UUID），用于幂等键构造
- 负载变更向后兼容（新字段 optional，或版本化如 `device.enrolled.v2`）
- 不兼容变更：先部署 consumer 再部署 producer
