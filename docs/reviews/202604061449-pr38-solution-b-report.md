# PR 38 方案 B 重构报告

> 日期: 2026-04-06
> 目标 PR: `ghbvf/gocell#38`
> 范围: `src/adapters/rabbitmq/*`, `src/kernel/outbox/*`, `src/kernel/idempotency/*`, `src/adapters/redis/idempotency.go`
> 结论: 建议采用“处置结果 + 可提交幂等回执”的正构方案，不建议继续在当前 `error -> ACK/NACK` 语义上补丁。

---

## 1. 背景

当前实现有三个结构性问题：

1. `ConsumerBase.Wrap()` 在业务执行前就通过 `TryProcess()` 抢占并写入幂等状态，导致一旦后续需要 `requeue`，消息 redelivery 可能被误判为“已经处理过”，最终被提前 ACK。
2. `Subscriber` 的重连只稳定覆盖 `deliveries` channel 关闭场景，未完整覆盖 `Qos`、`ExchangeDeclare`、`QueueDeclare`、`QueueBind`、`Consume` 等 setup 阶段的瞬时断连。
3. `AcquireChannel()` 失败被统一归类为“可重连”，会把一部分永久性 `Channel()` 打开失败吞掉，形成热循环和日志噪声。

因此，问题不只是某个分支条件写错，而是“消息处置”和“幂等提交”两件事目前耦合得太早。

---

## 2. 设计目标

方案 B 的目标是：

1. 只有在 broker 明确接受最终处置后，才提交“已完成”的幂等状态。
2. 所有需要 redelivery 的路径都必须能释放处理租约，而不是留下永久“已处理”标记。
3. `Subscriber` 的重连覆盖完整 setup 生命周期，并且只对可恢复错误重试。
4. RabbitMQ adapter 尽量靠拢开源项目常见建模：`Ack / Reject / Requeue`，而不是让上层业务代码自己模拟 DLQ publish。

---

## 3. 方案总览

方案 B 的核心思想：

1. `ConsumerBase` 不再直接做“DLQ publish + 返回 nil/err”的混合控制。
2. 业务 handler 不再只返回 `error`，而是返回一个明确的“处置结果”。
3. 幂等模块不再只提供 `TryProcess()`，而是升级为 `Claim / Commit / Release` 两阶段模型。
4. `Subscriber` 负责在 `Ack` 或 `Nack` 成功后，决定是 `Commit` 幂等状态还是 `Release` 处理租约。

换句话说：

- `ConsumerBase` 负责“业务层意图”
- `Subscriber` 负责“broker 层处置”
- `Idempotency` 负责“处理状态生命周期”

---

## 4. 接口改造

### 4.1 outbox.Subscriber 与 handler 接口

建议改造 [src/kernel/outbox/outbox.go](/Users/shengming/Documents/code/gocell/src/kernel/outbox/outbox.go)：

```go
type Disposition uint8

const (
	DispositionAck Disposition = iota
	DispositionRequeue
	DispositionReject
)

type Receipt interface {
	Commit(ctx context.Context) error
	Release(ctx context.Context) error
}

type HandleResult struct {
	Disposition Disposition
	Err         error
	Receipt     Receipt
}

type EntryHandler func(context.Context, Entry) HandleResult

type Subscriber interface {
	Subscribe(ctx context.Context, topic string, handler EntryHandler) error
	Close() error
}

type TopicHandlerMiddleware func(topic string, next EntryHandler) EntryHandler
```

设计意图：

- `Ack`: 业务成功，或重复消息确认可安全跳过
- `Requeue`: 临时失败、shutdown、依赖不可用，要求 broker redelivery
- `Reject`: 业务终态失败，交给 broker-native DLX 处理

### 4.2 幂等接口

建议改造 [src/kernel/idempotency/idempotency.go](/Users/shengming/Documents/code/gocell/src/kernel/idempotency/idempotency.go)：

```go
type ClaimState uint8

const (
	ClaimAcquired ClaimState = iota
	ClaimDone
	ClaimBusy
)

type Claimer interface {
	Claim(ctx context.Context, key string, leaseTTL, doneTTL time.Duration) (ClaimState, Receipt, error)
}
```

说明：

- `ClaimAcquired`: 当前消费者拿到处理租约，可以执行业务
- `ClaimDone`: 已有消费者完成处理，可直接 `Ack`
- `ClaimBusy`: 已有消费者正在处理，当前投递应 `Requeue`

这里的 `Receipt` 是一次 claim 的回执对象，不是全局对象。

---

## 5. Redis 幂等实现

建议改造 [src/adapters/redis/idempotency.go](/Users/shengming/Documents/code/gocell/src/adapters/redis/idempotency.go) 为双状态键模型：

1. `lease:{key}`: 表示“处理中”，值为随机 token，带 `LeaseTTL`
2. `done:{key}`: 表示“已完成”，值固定，带 `DoneTTL`

### 5.1 Claim 语义

Lua 流程建议：

1. 如果 `done:{key}` 存在，返回 `ClaimDone`
2. 如果 `lease:{key}` 不存在，则 `SETNX lease:{key} token EX LeaseTTL`，返回 `ClaimAcquired`
3. 否则返回 `ClaimBusy`

### 5.2 Commit 语义

仅当 `lease:{key}` 的 token 匹配时：

1. `DEL lease:{key}`
2. `SET done:{key} 1 EX DoneTTL`

### 5.3 Release 语义

仅当 `lease:{key}` 的 token 匹配时：

1. `DEL lease:{key}`

### 5.4 TTL 建议

- `LeaseTTL`: 默认 5 分钟
- `DoneTTL`: 默认 24 小时

这样可以清晰区分：

- 正在处理
- 已经处理完成
- 需要重投但尚未完成

这正是修复当前 P0 的关键。

---

## 6. ConsumerBase 重构

建议改造 [src/adapters/rabbitmq/consumer_base.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/consumer_base.go)：

### 6.1 去掉应用侧 DLQ publish

`ConsumerBase` 不再依赖 `outbox.Publisher`，因此可删除：

- `publisher outbox.Publisher`
- `deadLetter(...)`
- `DLQTopic` 参与手动 publish 的逻辑

`ConsumerBase` 只负责把业务执行结果映射为 `HandleResult`。

### 6.2 Wrap 语义

新的 `Wrap()` 逻辑建议：

1. 调用 `Claim()`
2. 根据返回值分支：
   - `ClaimDone` -> 返回 `DispositionAck`
   - `ClaimBusy` -> 返回 `DispositionRequeue`
   - `ClaimAcquired` -> 进入业务执行
3. 业务执行结果：
   - 成功 -> `DispositionAck`
   - `PermanentError` -> `DispositionReject`
   - retry exhausted -> `DispositionReject`
   - `ctx.Done()` / shutdown / 临时依赖失败 -> `DispositionRequeue`

### 6.3 为什么 Reject 不再手动 publish DLQ

因为 RabbitMQ 本身支持：

- `Nack(requeue=false)` -> dead-letter 到已配置的 DLX

这样做的优势：

1. 适配层模型更简单
2. 不再需要在 `ConsumerBase` 内部再走一次 publisher confirm 链路
3. 处置模型更接近 `go-rabbitmq` 这样的主流设计

---

## 7. Subscriber 重构

建议改造 [src/adapters/rabbitmq/subscriber.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go)：

### 7.1 processDelivery 新语义

处理顺序改为：

```go
res := handler(ctx, entry)

var brokerErr error
switch res.Disposition {
case DispositionAck:
	brokerErr = ch.Ack(tag, false)
case DispositionReject:
	brokerErr = ch.Nack(tag, false, false)
case DispositionRequeue:
	brokerErr = ch.Nack(tag, false, true)
}

if brokerErr != nil {
	if res.Receipt != nil {
		_ = res.Receipt.Release(ctx)
	}
	return
}

if res.Receipt != nil {
	switch res.Disposition {
	case DispositionAck, DispositionReject:
		_ = res.Receipt.Commit(ctx)
	case DispositionRequeue:
		_ = res.Receipt.Release(ctx)
	}
}
```

### 7.2 这个时序为什么正确

- `Ack` 成功后再 `Commit`
- `Reject` 成功后再 `Commit`
- `Requeue` 成功后 `Release`
- broker 处置失败时统一 `Release`

这样可以彻底避免：

- 业务没完成却被标成 done
- redelivery 因为旧幂等状态被短路

---

## 8. 重连策略重构

建议改造 [src/adapters/rabbitmq/subscriber.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go) 和 [src/adapters/rabbitmq/connection.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go)。

### 8.1 错误分类

不要再把所有 `AcquireChannel()` 失败都包成 `errSubscriptionLost`。

应区分：

#### 可恢复错误

- `amqp.ErrClosed`
- delivery channel closed
- `*amqp.Error` 且 `Recover == true`
- 连接瞬时中断导致的 channel/consume/setup 失败

这些错误进入重试路径。

#### 不可恢复错误

- `ACCESS_REFUSED`
- `PRECONDITION_FAILED`
- topology 参数与现有 queue/exchange 不一致
- 权限错误
- 明确的 channel 限额耗尽

这些错误应直接返回给调用方。

### 8.2 setup 生命周期纳入重试

当前只对 delivery channel closed 做 reconnect 不够。

以下阶段都应纳入“可恢复则重试”的统一 setup 流程：

1. `AcquireChannel`
2. `Qos`
3. `ExchangeDeclare`
4. `QueueDeclare`
5. `QueueBind`
6. `Consume`

也就是说，`subscribeOnce()` 返回的不是“setup 某一步失败就结束”，而是“setup 返回一个可分类错误，由外层决定 retry or fail”。

### 8.3 防热循环

即使 `WaitConnected()` 立即返回，仍要在 retry loop 上增加最小 backoff，例如：

- 初始 200ms
- 指数增长
- 上限 5s 或 10s

这样可以避免永久错误时 CPU 自旋和日志洪泛。

---

## 9. 配置层变更

### 9.1 SubscriberConfig

[src/adapters/rabbitmq/subscriber.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go) 中的 `DLXExchange` 和 `DLXRoutingKey` 仍然保留，因为 broker-native DLX 仍然需要队列参数来声明。

### 9.2 ConsumerBaseConfig

建议从 [src/adapters/rabbitmq/consumer_base.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/consumer_base.go) 中移除：

- `DLQTopic`

新增：

- `LeaseTTL`
- `DoneTTL`

保留：

- `ConsumerGroup`
- `RetryCount`
- `RetryBaseDelay`

---

## 10. 测试计划

必须补以下测试：

### 10.1 幂等时序

在 [src/adapters/rabbitmq/rabbitmq_test.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/rabbitmq_test.go) 增加：

1. `Requeue` 后 `Release` 被调用，redelivery 能再次进入业务处理
2. `Ack` 成功后才 `Commit`
3. `Reject` 成功后才 `Commit`
4. `Ack/Nack` 失败后会 `Release`

### 10.2 setup 重连

增加表驱动测试，覆盖：

1. `Qos` transient failure -> retry
2. `ExchangeDeclare` transient failure -> retry
3. `QueueDeclare` transient failure -> retry
4. `QueueBind` transient failure -> retry
5. `Consume` transient failure -> retry
6. fatal setup error -> 直接返回

### 10.3 连接关闭场景

增加测试：

1. `Connection.Close()` 后 `Subscribe()` 不热循环
2. `AcquireChannel()` 永久失败时不会无限 retry

### 10.4 集成测试

在 [src/adapters/rabbitmq/integration_test.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/integration_test.go) 补：

1. `Nack(requeue=false)` 后消息进入 DLQ
2. `Requeue` 后消息可再次被消费
3. broker restart 后 setup 生命周期内可恢复

### 10.5 middleware / outbox 接口

在 [src/kernel/outbox/outbox_test.go](/Users/shengming/Documents/code/gocell/src/kernel/outbox/outbox_test.go) 补：

1. `EntryHandler` 和 `TopicHandlerMiddleware` 新签名测试
2. middleware 链下 `HandleResult` 透传测试

---

## 11. 实施顺序

建议按以下顺序落地：

### 第 1 刀

改 [src/kernel/outbox/outbox.go](/Users/shengming/Documents/code/gocell/src/kernel/outbox/outbox.go) 和 [src/kernel/idempotency/idempotency.go](/Users/shengming/Documents/code/gocell/src/kernel/idempotency/idempotency.go) 接口定义。

### 第 2 刀

改 [src/adapters/redis/idempotency.go](/Users/shengming/Documents/code/gocell/src/adapters/redis/idempotency.go)，实现 `Claim / Commit / Release`。

### 第 3 刀

改 [src/adapters/rabbitmq/consumer_base.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/consumer_base.go)，移除应用侧 DLQ publish，改为返回 `HandleResult`。

### 第 4 刀

改 [src/adapters/rabbitmq/subscriber.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go)，接入新的 disposition 与 receipt 生命周期。

### 第 5 刀

改 [src/adapters/rabbitmq/connection.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go) 与 `Subscriber` retry loop，补 setup 错误分类和 backoff。

### 第 6 刀

补单测和集成测试，确保至少覆盖以下回归：

- DLQ 不可用时不丢消息
- redelivery 不被错误幂等短路
- setup 阶段断连可恢复
- fatal channel-open 错误不会热循环

---

## 12. 风险与取舍

### 12.1 兼容性

方案 B 会改公共接口：

- `outbox.Subscriber`
- `TopicHandlerMiddleware`
- `idempotency.Checker`

这意味着需要同步调整所有调用点和测试桩。

### 12.2 broker-native DLX 的语义

RabbitMQ 官方文档表明：

- 经典队列的 DLX 历史上不是最强保证
- quorum queue 支持更安全的 at-least-once dead lettering

因此，如果业务对终态失败消息的可达性要求很高，应进一步评估使用 quorum queue，而不是仅依赖经典队列 DLX。

这里是基于 RabbitMQ 官方文档得出的结论，不是对本仓库代码的直接观察。

---

## 13. 最终建议

建议直接实施方案 B，而不是继续追加局部补丁。

原因：

1. 它能真正修掉当前 P0，而不是只让某一条路径“看起来在 requeue”
2. 它把 broker 处置时序和幂等提交时序对齐了
3. 它和现成开源实现的思路更一致，后续维护会更清晰
4. 它为后续引入更强死信保障或更清晰的 observability 留出了接口空间

---

## 14. 参考

1. RabbitMQ Dead Letter Exchanges
   [https://www.rabbitmq.com/docs/dlx](https://www.rabbitmq.com/docs/dlx)
2. RabbitMQ Quorum Queues / at-least-once dead lettering
   [https://www.rabbitmq.com/docs/3.13/quorum-queues](https://www.rabbitmq.com/docs/3.13/quorum-queues)
3. Watermill AMQP subscriber
   [https://github.com/ThreeDotsLabs/watermill-amqp/blob/master/pkg/amqp/subscriber.go](https://github.com/ThreeDotsLabs/watermill-amqp/blob/master/pkg/amqp/subscriber.go)
4. go-rabbitmq consume.go
   [https://github.com/wagslane/go-rabbitmq/blob/main/consume.go](https://github.com/wagslane/go-rabbitmq/blob/main/consume.go)
5. Redpanda Connect AMQP input
   [https://github.com/redpanda-data/connect/blob/main/internal/impl/amqp09/input.go](https://github.com/redpanda-data/connect/blob/main/internal/impl/amqp09/input.go)
