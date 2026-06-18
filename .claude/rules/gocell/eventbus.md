# EventBus 规范

## 事件传输选型（topology-gated）

composition root 的 `outbox.Publisher`/`outbox.Subscriber` 经
`cellmodules/eventtransport.Resolve(clk, topo, cfg)` 按 `Topology` 单源选型：

- demo / memory 拓扑 → 进程内 `runtime/eventbus`（Publisher == Subscriber 同实例）。
- postgres 拓扑 → 真实 broker（RabbitMQ，从 `GOCELL_<CELLID>_AMQP_URL`，缺省回退
  `GOCELL_AMQP_URL`）；缺 broker URL 启动期 fail-closed，**不静默降级回 in-memory**（relay
  必须把已持久化的 outbox entry 发到 broker，而非进程内 bus，否则跨进程/重启丢事件）。

in-memory bus **仅** demo 拓扑可达：**composition root**（`cmd/corebundle` + `examples/ssobff`）
生产代码禁止直接 import `runtime/eventbus`，由 depguard `corebundle-no-direct-eventbus` /
`ssobff-no-direct-eventbus` 守卫（`COREBUNDLE-EVENTBUS-FUNNEL-01`，路径级 import ban）。扩展新
broker（mqtt）在 `eventtransport` 的 `brokerKind` switch 加分支 + 暴露选择 env，不在本约束外另开旁路。
权威语义见 `cellmodules/eventtransport/doc.go` 与 ADR `202606131500-1940`。

## per-cell AMQP vhost/credential 隔离（#2152 PR-3）

per-cell URL 携带 per-cell 凭据（user:pass）和 vhost。split 拓扑下 operator 为每个 cell
provision 独立的 vhost 和 AMQP user，使每个进程只持有访问自身 broker 资源所需凭据——
cell 进程无法跨 cell 发布或消费事件。凭据由 broker operator 外部配置（非 framework 派生，
无 HKDF/派生层，原因：AMQP broker 用户是外部对象，不存在 framework 可控的 master key）。

distinct per-cell URL 今 egress-only fail-closed（运行期隔离须 #2366/#2341 完成）；凭据
non-leak 由 `AMQP-URL-REDACTION-FUNNEL-01`（Medium，typed AST funnel）守，权威语义见
`cellmodules/eventtransport/doc.go` 与 ADR `202606131500-1940`。

## 复用层选型（claimer / nonce，topology-gated）

composition root 的 outbox 消费幂等 claimer + 内部 listener service-token nonce store 同样经
`cellmodules/replaydeps.Resolve(ctx, clk, topo)` 按 `Topology` 单源选型：demo/single-pod → in-memory；
real multi-pod → Redis-backed（client 作 ManagedResource），缺 Redis 配置启动期 fail-closed。两个
composition root（corebundle + ssobff）复用同一包，不各自接线 in-memory 原语。`idempotency.NewInMemClaimer`
/ `auth.NewInMemoryNonceStore` 在这两个 root 内**仅** `replaydeps.Resolve` 的 demo 分支可达——因两包仍
为类型合法 import，depguard 无法表达，故由 archtest `REPLAYDEPS-INMEM-FUNNEL-01`（调用级 AST 扫描，
路径限定两 root）守卫。bus / claimer / nonce 三 funnel 合起来确保 composition root 内每个 in-memory
单 pod 原语只经 sealed resolver 可达。权威语义见 `cellmodules/replaydeps/doc.go`。

## saga 投影资源选型（journal / checkpoint / locker，topology-gated）

saga-journal CQRS 投影消费者的运行依赖经 `cellmodules/sagaprojectiondeps.Resolve(ctx, clk, topo, cfg)`
按 `Topology` 单源选型（eventtransport / replaydeps 的第 3 个 sibling resolver）：saga journal（与
Coordinator 共用，再以 `journal.GlobalReader` 喂投影）+ `projection.OwnerCheckpointStore` + 投影
`TxRunner` + 每投影 leader `distlock.Locker`：

- demo/memory → `MemJournal` + `MemOwnerCheckpointStore` + in-process locker + `DemoTxRunner`。
- postgres → PG `PGJournal` + PG `ProjectionCheckpointStore` + PG `TxManager`；单 pod in-process
  locker，real multi-pod → Redis-backed locker。PG pool / Redis client 由 composition root 注入
  （root 已持 pool 跑 migration，避免开第二个 pool）。
- fail-closed：postgres 缺 pool / multi-pod 缺 Redis → 启动期报错，**不静默降级**回 in-memory
  journal（丢重启事件）或 in-process locker（多副本各自当 leader 双投影）。

in-process 单 pod 锁原语 `distlock.NewInProcessDriver` 在 wiring 层（`cmd/*` / `cellmodules/*` /
`examples/*`）**仅** `sagaprojectiondeps.Resolve` 的 demo 分支可达——因 `runtime/distlock` 为类型合法
import，depguard 无法表达，故由 archtest `SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01`（调用级 AST 扫描，
allowlist 该 resolver 目录）守卫。这是 bus / claimer / nonce 之外**第 4 个** sealed 单 pod 原语
funnel。权威语义见 `cellmodules/sagaprojectiondeps/doc.go`。

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

producer 与 consumer **双侧**都收口到 codegen typed API：cell 经生成式
`<cmd>.EmitAsync` / `<cmd>.EmitAsyncFromIdempotencyKey`（per-command wrapper，bake
DispatchID + 锁 payload 为 `*Request`）发命令，**不**直调 runtime `command.EmitAsync`
（裸 DispatchID）。三层嵌套 funnel：业务 → 生成 wrapper（`COMMAND-ASYNC-EMIT-CALLER-01`
锁两个 runtime emit 出口的调用方为 generated/runtime） → runtime `command.EmitAsync`
（`COMMAND-ASYNC-EMIT-FUNNEL-01` 锁裸 `outbox.Emit`/`NewEntry` 命令 topic 构造）→
`outbox.NewEntry`。consumer 侧对称由 `COMMAND-ASYNC-DISPATCH-CALLER-01` +
`COMMAND-DISPATCH-REGISTER-CALLER-01` 锁。wrapper 存在性 codegen+golden Hard、caller
funnel type-aware scan Medium（Go 天花板，#2059）。符号/盲区见对应 archtest godoc 与 ADR
`202606040550-1044`。

## Projection

projection consumer 必须 wire `bootstrap.WithConsumerBase`。投影事件载体使用
`cellvocab.ProjectionEvent`；outbox entry 与 saga journal event 都实现该接口。
retained journal、checkpoint、tailer 的完整约束以对应 archtest godoc 和 ADR 为准。

outbox 派生投影的 durable journal（`projection_events`，EPIC #1504）由 emit 期同事务双写
装饰器写入：append 收口单一 sanctioned 路径（`PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01`），
双写 topic 集从 `slice.yaml` 投影声明派生、非手写（`PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01`）。
append-only 主守卫是 serving role 的 DB 引擎 `REVOKE UPDATE, DELETE`（migration 058，Hard、不可绕）；
code-level `DELETE`/`TRUNCATE projection_events` 字面量另由 archtest
`PROJECTION-EVENT-JOURNAL-NO-DELETE-01`（Medium 纵深，含 anti-vacuity + RED/GREEN fixture）守。盲区/评级/
符号以对应 archtest godoc 与 ADR `202606071600-1504` 为准。

## 命名与 payload

- stream / topic / command key 使用稳定 dotted 名称。
- event payload 是 JSON object，字段 camelCase。
- event metadata 的 trace、request、principal 信息由 outbox envelope 注入，业务
  不伪造 reserved metadata key。
