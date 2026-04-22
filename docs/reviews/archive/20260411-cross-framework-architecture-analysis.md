# 跨框架事件消费架构对比分析

**日期**: 2026-04-11
**范围**: 7 个主流框架对比 GoCell 事件消费架构，识别设计差距与可采纳模式
**基准**: PR#68 (Solution B) 后的 GoCell 事件消费栈

---

## 研究框架清单

| 框架 | 语言 | 定位 | 关注点 |
|------|------|------|--------|
| **Watermill** | Go | 事件驱动库 | Router/Subscriber/Middleware 全栈 |
| **NATS JetStream** | Go | 消息系统客户端 | Ack/Nak/Term 四态、Drain |
| **Sarama (Kafka)** | Go | Kafka 客户端 | ConsumerGroupHandler 三阶段生命周期 |
| **Temporal** | Go | 工作流引擎 | Worker Register/Start、NonRetryable、Heartbeat |
| **go-micro / Kratos / go-zero** | Go | 微服务框架 | 声明式注册、transport.Server、ServiceGroup |
| **MassTransit** | .NET | 消息总线 | 三级重试管线、Fault 事件、Outbox Inbox |
| **Axon** | Java | CQRS/ES 框架 | Processing Group、TokenStore、两级错误处理 |

---

## 维度一：Subscribe 生命周期（Setup vs Run 分离）

### 行业共识：所有框架都分离注册与执行

| 框架 | 注册阶段 | 执行阶段 | 分离方式 |
|------|---------|---------|---------|
| Watermill | `Router.AddHandler()` | `Router.Run(ctx)` | Router 统一管理 |
| Watermill-AMQP | `SubscribeInitialize(topic)` | `Subscribe()` 返回 channel | 显式预检方法 |
| NATS JetStream | `js.CreateConsumer(config)` | `consumer.Consume(handler)` | 两步 API |
| Sarama | `ConsumerGroupHandler.Setup()` | `ConsumeClaim()` + `Cleanup()` | 三阶段接口 |
| Temporal | `worker.RegisterActivity()` | `worker.Start()` / `worker.Run()` | 注册后启动，反序则 panic |
| go-micro | `server.NewSubscriber()` + `Subscribe()` | `server.Start()` | broker 订阅延迟到 Start |
| Kratos | `transport.Server` 注册 | `App.Run()` (errgroup) | Server 接口 |
| go-zero | `ServiceGroup.Add()` | `ServiceGroup.Start()` | 反序 Stop |
| MassTransit | `AddConsumer<T>()` + `ConfigureEndpoints()` | `bus.Start()` | DI 容器 + 生命周期 |
| Axon | `@EventHandler` + ProcessingGroup | `Configuration.start()` | 三层声明 |
| **GoCell** | **RegisterSubscriptions(sub)** | **sub.Subscribe() 阻塞** | **❌ 混合在一起** |

**GoCell 是唯一将 setup 和 run 混合在单一阻塞调用中的框架。**

这直接导致了 100ms 启发式竞态（P1）、Cell 内无监管 goroutine（P6）、InMemoryEventBus 行为偏离（P5）。

### 最佳实践提取

**Sarama 的三阶段模式**最为结构化：
```
Setup(session)        → 初始化资源（同步，可返回 error）
ConsumeClaim(session) → 处理消息（阻塞，per-partition goroutine）
Cleanup(session)      → 释放资源（同步，在 offset commit 前）
```

**Temporal 的强制约束**最安全：注册阶段结束后调用 `Start()`，反序操作 panic。

**Watermill 的 `Running() <-chan struct{}`** 最实用：一个 channel 信号表示"所有 handler 已就绪"。

---

## 维度二：错误分类（Permanent vs Transient）

### 行业共识：错误分类是核心概念，不是适配器细节

| 框架 | 错误分类机制 | 层级 |
|------|-------------|------|
| Watermill | `backoff.Permanent(err)` + PoisonQueue middleware | 中间件层 |
| NATS JetStream | `Ack/Nak/NakWithDelay/Term/InProgress` 五态 | 协议层 |
| Temporal | `ApplicationError{NonRetryable: true}` + `NonRetryableErrorTypes` 列表 | SDK 核心 |
| Kratos | `errors.Error{Code, Reason}` | 框架核心 |
| MassTransit | Retry → Redelivery → Error Queue 三级管线 | 框架核心 |
| Axon | `ListenerInvocationErrorHandler` vs `ErrorHandler` 两级 | 框架核心 |
| **GoCell** | **`PermanentError` 在 `adapters/rabbitmq/`** | **❌ 适配器层** |

**GoCell 的 `PermanentError` 定义在错误的层级。** Temporal、Watermill、NATS 都把错误分类放在 SDK/框架核心。GoCell 放在 adapter 导致 `kernel/outbox.WrapLegacyHandler` 和 `runtime/eventbus` 无法引用。

### NATS 的独特洞察：NakWithDelay + InProgress

```
Ack()              → 成功
Nak()              → 瞬态失败，立即重发
NakWithDelay(5s)   → 瞬态失败，延迟重发（调用方控制延迟）
Term()             → 永久失败，停止重发
InProgress()       → 心跳续租，重置 ack 超时
```

GoCell 的 `DispositionRequeue` 不支持 per-message 延迟，且缺少 `InProgress` 心跳。

### Temporal 的独特洞察：NextRetryDelay

handler 可以在错误中指定下次重试延迟：
```go
temporal.NewApplicationErrorWithOptions("rate limited", "RATE_LIMIT",
    temporal.ApplicationErrorOptions{NextRetryDelay: 30 * time.Second})
```

这比 GoCell 的全局 `RetryBaseDelay` 更灵活。

---

## 维度三：幂等模型

### GoCell 的 Claimer 是优势，不是问题

| 框架 | 幂等方式 | 复杂度 |
|------|---------|--------|
| Watermill | 内容 hash 去重 (Deduplicator middleware) | 低 |
| NATS | Publisher 端 Nats-Msg-Id 去重，consumer 端自理 | 低 |
| Sarama | Offset 位置追踪 | 中 |
| Temporal | Server 端 Workflow/Activity ID 去重 | 高（服务端） |
| MassTransit | Transactional Outbox Inbox (MessageId) | 高 |
| Axon | TokenStore 位置追踪 + 同事务提交 | 高 |
| **GoCell** | **两阶段 Claimer (Claim/Commit/Release + lease TTL)** | **高** |

GoCell 的 Claimer 比大多数 Go 框架更成熟：
- 比 Watermill 的 hash 去重更强（有 lease 防并发）
- 比 NATS 的 publisher 去重更全面（consumer 端保障）
- 与 MassTransit Inbox 和 Axon TokenStore 同级

**应保留并强化**，不需要重构。

---

## 维度四：Goroutine 监管

### 行业共识：中央 Supervisor 拥有 goroutine 生命周期

| 框架 | 监管模式 | 关闭策略 |
|------|---------|---------|
| Watermill | Router: 两级 WaitGroup + CloseTimeout | 信号 handler 停止 → 等待消息处理完 |
| NATS | ConsumeContext: `Stop()`/`Drain()` + `Closed() <-chan` | Drain 允许消费缓冲消息 |
| Temporal | Worker: `noRepoll` 停止拉取 → `WorkerStopTimeout` → cancel ctx | 两阶段：停止接收 + 等待完成 |
| go-zero | ServiceGroup: parallel start, reverse-stop, `sync.OnceFunc` | 反序关闭 |
| Kratos | `errgroup` 并行启动 + OS signal → `Stop()` | errgroup 管理 |
| MassTransit | Bus 生命周期管理所有 consumer | 配置化 |
| Axon | Configuration.start()/shutdown() | 处理组独立管理 |
| **GoCell** | **Cell 自行 `go func()` + `context.Background()`** | **❌ 无监管** |

GoCell 是唯一没有中央 goroutine 监管的框架。

### 最佳实践提取

**NATS 的 Drain 模式**：
```
Stop()  → 立即停止，丢弃缓冲
Drain() → 停止接收新消息，处理完缓冲消息后关闭
```

**Temporal 的两阶段关闭**：
```
1. noRepoll → 停止拉取新任务
2. WorkerStopChannel → 通知 handler 准备退出
3. WorkerStopTimeout → 超时后强制 cancel context
```

---

## 维度五：中间件组合

### 行业对比

| 框架 | 中间件模式 | 可组合性 |
|------|-----------|---------|
| Watermill | `func(HandlerFunc) HandlerFunc` 装饰器 | 高：全局/per-handler |
| Kratos | `func(Handler) Handler` 链式 | 高：transport 无关 |
| MassTransit | `IFilter<ConsumeContext>` 管线 | 极高：bus/endpoint/consumer 三级 |
| go-micro | `func(SubscriberFunc) SubscriberFunc` | 中 |
| **GoCell** | `TopicHandlerMiddleware` + `SubscriberWithMiddleware` | **中：但 ConsumerBase 内聚过多** |

**MassTransit 的洞察**：retry/redelivery/error-queue 是三个独立 filter，可拆分组合。GoCell 的 `ConsumerBase` 把 retry + idempotency + error classification 全部内聚，不够灵活。

---

## 维度六：独特模式（GoCell 可借鉴）

### 6.1 MassTransit `Fault<T>` 事件

消费失败时发布结构化错误事件，其他 consumer 可订阅做补偿/告警：
```csharp
public class OrderFaultConsumer : IConsumer<Fault<SubmitOrder>> {
    public Task Consume(ConsumeContext<Fault<SubmitOrder>> context) {
        // 错误上下文：原始消息 + 异常链 + 时间戳
    }
}
```

**GoCell 可采纳**：DLX 消息目前只有 broker 元数据，缺少结构化错误上下文。

### 6.2 Axon Processing Group

将多个 handler 分组，每组有独立的错误策略、排序策略、token store：
```java
@ProcessingGroup("order-projections")  // 独立错误隔离
@ProcessingGroup("notification-handlers")  // 可以独立暂停/重放
```

**GoCell 可采纳**：一个 Cell 内多个 subscription 共享同一错误策略。EventRouter 可支持 per-group 配置。

### 6.3 Temporal Heartbeat-as-Checkpoint

长时间运行的 handler 可以在心跳中保存进度，重试时从断点恢复：
```go
activity.RecordHeartbeat(ctx, progressDetails)
// 重试时：
if activity.HasHeartbeatDetails(ctx) {
    activity.GetHeartbeatDetails(ctx, &checkpoint)
}
```

**GoCell 可采纳**：当前 Claimer 只有 binary lease（持有/不持有），不支持进度保存。对 L4 DeviceLatent 场景有价值。

### 6.4 NATS NakWithDelay

handler 可以指定 per-message 重试延迟：
```go
msg.NakWithDelay(30 * time.Second)  // 限流场景：30s 后重试
```

**GoCell 可采纳**：HandleResult 增加可选 `RetryDelay` 字段。

---

## 综合诊断：GoCell 的位置

```
                    注册/执行分离  错误分类层级  幂等模型  goroutine监管  中间件灵活性
Watermill           ★★★★★       ★★★★☆      ★★☆☆☆   ★★★★★       ★★★★☆
NATS JetStream      ★★★★★       ★★★★★      ★★☆☆☆   ★★★★☆       ★★☆☆☆
Sarama              ★★★★★       ★★☆☆☆      ★★★☆☆   ★★★☆☆       ★★☆☆☆
Temporal            ★★★★★       ★★★★★      ★★★★★   ★★★★★       ★★★☆☆
MassTransit         ★★★★★       ★★★★☆      ★★★★☆   ★★★★☆       ★★★★★
Axon                ★★★★★       ★★★★☆      ★★★★★   ★★★★☆       ★★★☆☆

GoCell (现状)        ★☆☆☆☆       ★★☆☆☆      ★★★★☆   ★☆☆☆☆       ★★★☆☆
GoCell (修复后)      ★★★★☆       ★★★★☆      ★★★★☆   ★★★★☆       ★★★☆☆
```

### GoCell 的优势（应保留）

1. **Disposition enum** — 比 Watermill 的 error-only 和 go-micro 的 plain error 更显式
2. **两阶段 Claimer** — 比 Watermill hash 去重和 NATS publisher 去重更强
3. **broker-native DLX** — 比 Watermill 应用层 PoisonQueue 和 NATS advisory 更可靠
4. **SubscriberWithMiddleware** — 已有中间件链基础

### GoCell 的差距（必须修复）

1. **❌ Subscribe 生命周期未分离** — 8/8 对比框架都分离了
2. **❌ PermanentError 在 adapter 层** — 违反所有框架的错误分类层级
3. **❌ 无 goroutine 监管** — 8/8 对比框架都有中央 supervisor
4. **⚠ ConsumerBase 内聚过多** — MassTransit 启示：retry/idempotency/error 应可拆分

### GoCell 的机会（可选增强）

1. **NakWithDelay / NextRetryDelay** — per-message 重试延迟
2. **InProgress / Heartbeat** — 长运行 handler 续租
3. **Processing Group** — per-group 错误策略
4. **Fault Event** — 结构化错误事件发布

---

## 修订后的架构建议

基于 7 框架对比，原 3-PR 计划的方向正确，细化如下：

### Phase 1: PermanentError 提升到 kernel（必做，~1h）

与 Temporal `NonRetryableApplicationError`、NATS `Term()`、Watermill `backoff.Permanent()` 对齐。

### Phase 2: EventRouter 引入（必做，核心修复）

综合最佳实践：
- **Sarama 三阶段** → `AddHandler()` / `Run(ctx)` / `Close()`
- **Watermill `Running()`** → `Running() <-chan struct{}` 就绪信号
- **Temporal 两阶段关闭** → 停止接收 → 等待处理完 → 超时 cancel
- **NATS Drain** → `Drain()` vs `Stop()` 两种关闭语义
- **Kratos transport.Server** → EventRouter 实现 `Start(ctx)/Stop(ctx)`

### Phase 3: Checker 清理 + Receipt 加固（必做，清理）

与 Phase 1-2 一致，删除 legacy 路径，加 sync.Once。

### Phase 4: 可选增强（按需）

| 特性 | 参考框架 | 优先级 |
|------|---------|--------|
| HandleResult.RetryDelay | NATS NakWithDelay, Temporal NextRetryDelay | P2 |
| Receipt.InProgress() 心跳 | NATS InProgress, Temporal Heartbeat | P2 (L4 需要) |
| ConsumerBase 拆分 filter | MassTransit pipe-and-filter | P3 |
| Fault Event 发布 | MassTransit Fault<T> | P3 |
| Processing Group 分组 | Axon ProcessingGroup | P3 |
