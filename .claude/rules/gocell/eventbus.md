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
            Err:         outbox.NewPermanentError(err),
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
- `PermanentError` 是错误分类标签（用于 logging/metric 区分），**不触发 Disposition 升级**；handler 必须 explicit 返回 `DispositionReject` 才会路由到 DLX。返回 `Requeue + PermanentError` 会按 Requeue 走完 retry budget，最终经预算耗尽路径转 Reject（详见 ADR `docs/architecture/202605031900-adr-handler-vocabulary-collapse.md`）

### Service 构造模式（fail-fast on nil TxRunner）

Outbox-bound service 构造函数统一签名 `func NewXxx(...) (*XxxService, error)`，
body 顶层包含：

```go
if s.txRunner == nil {
    return nil, errcode.New(errcode.ErrValidationFailed,
        "xxx: TxRunner required; use WithTxManager")
}
```

12 个 service 全部遵循（accesscore: sessionlogin/sessionlogout/setup/rbacassign/identitymanage；
auditcore: auditappend/auditverify；configcore: flagwrite/configpublish/configwrite；
examples: ordercreate）。`OUTBOX-SERVICE-01` archtest 静态守卫该模式：禁止 method 内 nil
fallback；构造期 fail-fast 是唯一允许的 nil-branch。

`WithTxManager` 选项的入参 nil 静默忽略（保持 option 函数幂等），最终 nil 校验由
`NewService` 完成。

### Cell 订阅注册（Registry builder 模式）

Cell 在 `Init(ctx, reg)` 中通过 `reg.Subscribe(spec, handler, consumerGroup)` 声明订阅意图，bootstrap 把 RegistrySnapshot.Subscriptions drain 到 EventRouter，Router 管理所有 goroutine 生命周期。

```go
func (c *MyCell) Init(ctx context.Context, reg cell.Registry) error {
    if err := c.BaseCell.Init(ctx, reg); err != nil {
        return err
    }
    return reg.Subscribe(wrapper.ContractSpec{
        ID:        "event.my.topic.v1",
        Kind:      "event",
        Transport: "amqp",
        Topic:     "my.topic.v1",
    }, c.svc.HandleEvent, c.ID())
}
```

`consumerGroup` 与 owner cellID 解耦：drain loop 用 RegistrySnapshot 的 cell ID 作为
`Subscription.CellID`（observability owner），`consumerGroup` 仅作 broker 分区/竞争消费标识。
两者通常同值（`c.ID()`），需要 sub-group 的场景（例如 fan-out 消费）可显式分离。

**声明对齐约束**：`spec.ID` 必须同步声明在三处：
1. slice.yaml `contractUsages` 含 `{contract: event.my.topic.v1, role: subscribe}` 条目
2. contract.yaml `endpoints.subscribers` 含本 slice 所属 cell 的 ID
3. slice.yaml `verify.contract` 含 `contract.event.my.topic.v1.subscribe`

前两项任一漂移由 `gocell validate` ADV-06 规则拦截（error 级，双向校验
`endpoints.subscribers ↔ contractUsages[role=subscribe]`）。第三项
`verify.contract` ↔ `contractUsages` 闭环由 VERIFY-01 拦截，与 ADV-06 互补。

## 死信路由

- DLX 由 broker 原生处理（`DispositionReject` → `Nack(requeue=false)` → DLX exchange）
- L2 consumer 必须配置 DLX exchange（`SubscriberConfig.DLXExchange`）
- 死信消息必须可观测（计数指标或日志）

## 幂等模型

- PG outbox 的 `ClaimPending` 期生成 `lease_id`（UUID fencing token）写入 row；`MarkPublished/MarkRetry/MarkDead/ReclaimStale` 五个 SQL 全部以 `lease_id` 守 CAS（旧 worker 的 mark 必失败，参见 ADR `docs/architecture/202605051600-adr-pg-outbox-fencing.md`）。lease 是 store 层语义，handler / Settlement 不接触；relay writeBack 自动透传 `entry.LeaseID`，业务 handler 无感知。`OUTBOX-LEASE-ID-CAS-01` archtest 守卫。
- ConsumerBase 内部使用 `kernel/idempotency.Claimer`（两阶段 Claim/Commit/Release）实现幂等；handler 作者**不需要** import `kernel/idempotency`，也不需要读写任何 Settlement 字段。
- 业务 handler 实现 `EntryHandler = func(ctx, Entry) HandleResult`；`Settlement` 由 `SubscriberWithMiddleware` 在 `SubscribeEntry` 内部独立注入（业务 middleware chain → `ConsumerBase.Wrap` 转换为 `SubscriberHandler` → `Inner.Subscribe`），handler 不接触 Settlement（`OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01` archtest 守卫，Wave 1 upgrade from 旧 HANDLER-RECEIPT-WRITE-01）。
- 业务 middleware 签名为 `func(sub Subscription, next EntryHandler) EntryHandler`（不接触 Settlement）——对齐 Watermill router/Kratos transport/sarama session 业界共识：settle 由 transport 层独立决策（K#12 二轮深度修复，删 `AsMiddleware`）。
- Claim 获取处理租约 → handler 执行 → broker Ack 后 Settlement.Commit / 失败时 Settlement.Release（由 Subscriber delivery loop 完成）。
- 默认 fail-closed：Claimer 故障时 Requeue，不丢弃幂等保护。

## Stream 命名

- 新建 stream 前搜索已有常量，禁止重复定义
- stream 名 ≥ 3 次使用抽常量
- 按 stream 过滤事件时区分通道

## 事件负载

- 每个事件包含 `event_id`（UUID），用于幂等键构造
- 负载变更向后兼容（新字段 optional，或版本化如 `device.enrolled.v2`）
- 不兼容变更：先部署 consumer 再部署 producer
