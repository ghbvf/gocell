# EventBus 规范

## 事件传输选型（topology-gated）

composition root 的 `outbox.Publisher`/`outbox.Subscriber` 经
`cellmodules/eventtransport.Resolve(clk, topo, cfg)` 按 `Topology` 单源选型：

- demo / memory 拓扑 → 进程内 `runtime/eventbus`（Publisher == Subscriber 同实例）。
- postgres 拓扑 → 真实 broker（RabbitMQ，从 `GOCELL_AMQP_URL`）；缺 broker URL 启动期
  fail-closed，**不静默降级回 in-memory**（relay 必须把已持久化的 outbox entry 发到 broker，
  而非进程内 bus，否则跨进程/重启丢事件）。

in-memory bus **仅** demo 拓扑可达：`cmd/corebundle` 生产代码禁止直接 import
`runtime/eventbus`，由 depguard `corebundle-no-direct-eventbus` 守卫
（`COREBUNDLE-EVENTBUS-FUNNEL-01`，路径级 import ban）。扩展新 broker（mqtt）在
`eventtransport` 的 `brokerKind` switch 加分支 + 暴露选择 env，不在本约束外另开旁路。
权威语义见 `cellmodules/eventtransport/doc.go` 与 ADR `202606131500-1940`。

## ConsumerBase

所有 consumer 使用 `ConsumerBase`。它负责 Claim / Commit / Release、幂等、
退避重试和 DLX。业务 handler 只返回 `outbox.HandleResult`。

```go
func handle(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
    if permanent {
        return outbox.Reject(outbox.NewPermanentError(err))
    }
    if transient {
        return outbox.Requeue(err)
    }
    return outbox.Ack()
}
```

`HandleResult{}` 零值 invalid。业务代码使用 `Ack`、`Requeue`、`Reject` factory，
不手写 struct literal。subscriber 层扩展信息放 `DeliveryOutcome`，不污染业务结果。

## Disposition

| Disposition | 语义 | 行为 |
|-------------|------|------|
| Ack | 成功 | broker ack + receipt commit |
| Requeue | 瞬态失败 | 退避重试，预算耗尽后 reject |
| Reject | 永久失败 | broker nack/reject，进入 DLX |

`PermanentError` 只是错误分类，不自动把 Requeue 改成 Reject。

## Service required deps

service 必填依赖用 `gocell:"required"` tag，经 `gocell generate required-deps`
生成 `validateRequired()`。`NewService` 在 options loop 后调用 validate。可选依赖
在构造器内给默认值。

## 订阅注册

订阅单源是 `slice.yaml contractUsages`。cellgen 从 metadata 派生注册代码；业务
不手写平行 registry。订阅必须同时绑定 `ContractID`、`CellID`、consumer group。

webhook、grpc serve、event subscribe 遵循同一范式：声明在 metadata，派生到
`cell_gen.go`，运行时 registration 只消费派生结果。

## DLX 与幂等

- 永久错误进入 DLX。
- PG outbox claim 写入 lease/fencing token；所有状态回写以 lease 做 CAS。
- handler 不接触 lease；relay 或 subscriber write-back 负责透传。
- consumer group 命名必须稳定，避免重放时变成新消费者。

## Command dispatch

命令 dispatch 通过 generated typed API 和 Claimer 两阶段去重。producer 侧 key、
consumer 侧 claim、composition-root wiring 必须同源；不得新增裸字符串 dispatch。

## Projection

projection consumer 必须 wire `bootstrap.WithConsumerBase`。投影事件载体使用
`cellvocab.ProjectionEvent`；outbox entry 与 saga journal event 都实现该接口。
retained journal、checkpoint、tailer 的完整约束以对应 archtest godoc 和 ADR 为准。

outbox 派生投影的 durable journal（`projection_events`，EPIC #1504）由 emit 期同事务双写
装饰器写入：append 收口单一 sanctioned 路径（`PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01`），
双写 topic 集从 `slice.yaml` 投影声明派生、非手写（`PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01`）。
append-only 当前由 serving role 的 DB 引擎 `REVOKE UPDATE, DELETE`（migration 058）保护；code-level
no-delete archtest 是 ADR 规划中的纵深防御、尚未落地（ID 与排期以 ADR 为准）。盲区/评级/符号以对应
archtest godoc 与 ADR `202606071600-1504` 为准。

## 命名与 payload

- stream / topic / command key 使用稳定 dotted 名称。
- event payload 是 JSON object，字段 camelCase。
- event metadata 的 trace、request、principal 信息由 outbox envelope 注入，业务
  不伪造 reserved metadata key。
