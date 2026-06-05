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
        return outbox.Reject(outbox.NewPermanentError(err))
    }

    if err := processEvent(ctx, event); err != nil {
        // 瞬态错误 — ConsumerBase 退避重试
        return outbox.Requeue(err)
    }

    return outbox.Ack()
}
```

> **回落字面量**：`outbox.HandleResult.ProcessReason` / `SettlementObservers` 字段无法用 factory 表达，需要时直接构造 `outbox.HandleResult{...}` 字面量（典型场景：kernel internal retry plumbing、middleware-handler 协议）。`OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01` archtest 把字面量构造限定在 `kernel/outbox/result.go` / `consumer_base.go` / `outboxtest/conformance.go` 三处 allowlist；业务路径必须用 `outbox.Ack()` / `Requeue(err)` / `Reject(err)`。`HandleResult` 字段集本身由 `OUTBOX-HANDLERESULT-FIELDS-FROZEN-01` 冻结。

### Disposition 语义

| Disposition | 含义 | ConsumerBase 行为 |
|-------------|------|-------------------|
| `DispositionAck` | 成功处理 | broker Ack → Receipt.Commit |
| `DispositionRequeue` | 瞬态失败 | 退避重试，耗尽后 Reject |
| `DispositionReject` | 永久失败 | broker Nack(requeue=false) → DLX |

- **零值 HandleResult{} 的 Disposition 是 invalid**（不等于 Ack），会被安全降级为 Requeue
- `PermanentError` 是错误分类标签（用于 logging/metric 区分），**不触发 Disposition 升级**；handler 必须 explicit 返回 `DispositionReject` 才会路由到 DLX。返回 `Requeue + PermanentError` 会按 Requeue 走完 retry budget，最终经预算耗尽路径转 Reject（详见 ADR `docs/architecture/202605031900-adr-handler-vocabulary-collapse.md`）

### Service 构造模式（REQUIRED-DEP-NIL-GUARD-01 funnel）

所有 service struct 的 required 依赖字段通过 `gocell:"required"` tag 标注，
由 `gocell generate required-deps` 从 tag 生成 `service_required_gen.go::validateRequired()`。
`NewService` 在 options loop 之后调用一次：

```go
type Service struct {
    repo     domain.XxxRepository   `gocell:"required"`
    txRunner persistence.CellTxManager `gocell:"required" gocellKind:"KindInvalid" gocellCode:"ErrValidationFailed" gocellErr:"xxx: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
    emitter  outbox.CellEmitter        // optional — no tag (sealed marker; raw outbox.Emitter is forbidden in cell/slice public Options)
}

func NewService(..., opts ...Option) (*Service, error) {
    s := &Service{...}
    for _, o := range opts {
        o(s)
    }
    if err := s.validateRequired(); err != nil {
        return nil, err
    }
    return s, nil
}
```

`REQUIRED-DEP-NIL-GUARD-01` archtest 三件套（A1 golden lock / A2 callsite
uniqueness / A3 IsNilInterface ban）+ A4 tag whitelist + B1/B2/B3/B5 盲区反向自检
静态守卫该模式。22 个 service 全部遵循（accesscore 7 / auditcore 2 / configcore 5
/ examples 6）。

`WithTxManager` 选项的入参 nil 静默忽略（保持 option 函数幂等），最终 nil 校验由
`validateRequired()` 完成。

`OUTBOX-SERVICE-01` archtest 保留 SERVICE-02..05 子规则覆盖 outbox 侧约束
（publish / import / DirectEmitter / WithOutboxWriter）。
REQUIRED-DEP-NIL-GUARD-01.B2 superseded SERVICE-01-txRunner-method-branch
sub-condition；txRunner nil guard 现在由 `gocell:"required"` tag 统一生成。

### Cell 订阅注册（Registry builder 模式）

Cell 通过 `reg.Subscribe(spec, handler, consumerGroup, cellID, opts...)` 声明订阅意图，bootstrap 把 RegistrySnapshot.Subscriptions drain 到 EventRouter，Router 管理所有 goroutine 生命周期。该调用**由 cellgen 从 slice.yaml `contractUsages[role=subscribe]` 生成进 `cell_gen.go`**（下方代码是生成形态示意，不手写）。

```go
func (c *MyCell) Init(ctx context.Context, reg cell.Registrar) error {
    if err := c.BaseCell.Init(ctx, reg); err != nil {
        return err
    }
    return reg.Subscribe(contractspec.ContractSpec{
        ID:        "event.my.topic.v1",
        Kind:      "event",
        Transport: "amqp",
        Topic:     "my.topic.v1",
    }, c.svc.HandleEvent, "my-cg", c.ID())
}
```

`consumerGroup` 与 `cellID` 是 **两个完全独立的语义轴**，Registry.Subscribe 都要求显式提供：

- `cellID`（**第 4 个位置必填参数**，HARD 契约）= observability owner，作为 metric label / slog field / trace span attribute 的 cell 维度。codegen（contractgen NewSubscription + cellgen cell.tmpl）从 cell metadata 在编译期注入；business 手写 `reg.Subscribe` 时漏传是**编译失败**，不是 runtime fallback。
- `consumerGroup`（第 3 个位置必填参数）= broker 分区键 + 幂等命名空间 `"{ConsumerGroup}:{entry.ID}"`。同 group 竞争消费；不同 group 各自收到完整副本（fanout）。

两者**没有任何自动 fallback**：`Subscription.ObservabilityID()` 直接返回 `s.CellID`（不再 `if CellID == "" return ConsumerGroup`），`Subscription.Validate()` 拒空 CellID。Bootstrap drainCellSubscriptions 检查 `sub.CellID == snapshotKey`，不匹配 fail-fast（不再静默 `sub.OwnerCellID = id`）。

两者常同值（`c.ID()` 用作 consumerGroup 和 cellID），需要 sub-group 的场景（fanout 消费 / 角色分支 like `accesscore-rbac-session-sync`）可显式分离 consumerGroup，但 cellID 始终 = `c.ID()`。

**手写订阅用链式 builder（外部 cell，#1087）**：不跑 monorepo codegen 的外部 cell 用 `reg.Subscription(spec)` 链式 builder 而非裸写 positional `reg.Subscribe`——`.CellID()` 是**编译期红线**：终结方法 `.Register()` 只挂在 `*subscriptionBuilder` 上，而 `*subscriptionBuilder` 只能由 `*subscriptionDraft.CellID(id)` 产出，所以漏调 `.CellID()` 无路径到达 `.Register()`（编译失败，等价于 positional 第 4 参的 HARD 保证）。**两个 step 类型均 unexported（sealed construction，#1087 F1）**：外部包能链式调用但无法命名/零值构造任一类型，故无法绕过 `.CellID()` 伪造 builder。漏调 .CellID() 时的 Go 编译错误形如 "*cell.subscriptionDraft has no field or method Register"。`.ConsumerGroup()` 可选，缺省 = cellID；`.Register()` 路由进同一个 `RegistryRecorder.Subscribe` 校验漏斗——两种形态是「一个校验漏斗的两个生产者（机器 codegen / 人手写）」，不是向后兼容双路径。

```go
reg.Subscription(spec).
    CellID(c.ID()).
    ConsumerGroup("payment-charge"). // 可选；缺省 = CellID
    Handler(c.svc.HandleCharge).
    Register()
```

> **不要**新增 `WithSubscriptionCellID(string)` option，也**不要**把 builder 塌成单一类型让 CellID 变成可选链式方法 — 都会把 HARD 编译期红线降级为 Soft/Medium runtime fail-fast，由 `REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01` archtest（prong 1 positional 签名 + prong 2 两型 builder 方法集/返回类型冻结）拒绝。详见 ADR `docs/architecture/202605111000-adr-subscription-cellid-mandatory.md`。

**单源派生**：订阅的唯一权威源是 slice.yaml `contractUsages[role=subscribe]`，每条携带 `handler`（消费 handler 方法名，必填）+ `group`（消费组，可选，缺省 = 本 cell ID）：

```yaml
# slice.yaml
contractUsages:
  - contract: event.my.topic.v1
    role: subscribe
    handler: HandleEvent          # 必填
    # group: my-subgroup          # 可选，缺省 = cellID
verify:
  contract:
    - contract.event.my.topic.v1.subscribe   # 手写，见下
```

新增/扩展一个 consumer 只改 slice.yaml（上面）+ handler 本体；新建 slice 时另需在 cell.go 结构体声明 `*<sliceID>.Service`（或 `*<sliceID>.Consumer`）字段。codegen 从这些派生其余：

- **`reg.Subscribe` 调用**：cellgen 从 contractUsages[subscribe] 生成进 `cell_gen.go`；handler 表达式 `c.<field>.<handler>` 的 `<field>` 由 cellgen 按「字段指针类型包名 == sliceID」在 cell.go 结构体解析（0/>1 匹配 fail-fast）。**不再有 `// +slice:subscribe` marker**——残留 marker 是 markergen unknown-marker 编译期错误。
- **contract.yaml `endpoints.subscribers`**：派生字段（`EndpointsMeta.Subscribers` 为 `yaml:"-"`），parser 从所有订阅 slice 的 `belongsToCell` 计算；**禁止手写 `subscribers:`**（KnownFields 严格解码拒绝）。外部 actor 订阅者（无 slice、不可派生）写在 contract.yaml `actorSubscribers:`，与派生的 cell 集合并入 Subscribers。
- **slice.yaml `verify.contract` 的 `contract.<id>.subscribe`**：**仍手写**——它断言「该订阅有可执行的 consumer contract 测试」（`gocell verify <slice>` 实跑），非纯冗余；无测试时用 waiver 记录已知缺口。由 VERIFY-01 守闭包。

ADV-06（contract.subscribers ↔ slice CU 双向对齐）已退役——cell 订阅者单源派生后漂移结构上不可能；VERIFY-01 保留。守卫见 archtest `SUBSCRIBERS-DERIVED-FIELD-FROZEN-01`（reflect 锁 `yaml:"-"`）/ `CONTRACT-YAML-NO-SUBSCRIBERS-KEY-01` / `SUBSCRIBE-MARKER-RETIRED-01`。

> **同范式：webhook 双角色**。`contractUsages[role=webhook-receive|webhook-dispatch]` 走与 subscribe 完全一致的「slice.yaml 单源 → cellgen 派生进 cell_gen.go」范式，派生出 `reg.RegisterWebhookReceiver(webhook.ReceiverSpec{...}, handler)` / `reg.RegisterWebhookDispatch(...)`；contract.yaml 的 `endpoints.receivers` / `dispatchers` 是派生字段（`yaml:"-"`，禁手写）。守卫见 `CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01` / `WEBHOOK-MARKER-RETIRED-01`。字段集、派生形态、演化策略见 `contracts/webhook/README.md`。**运行时**（`reg.RegisterWebhookReceiver/Dispatch` 方法本体 + HTTP 接收 / dispatcher consumer）在 PR-3/PR-5 落地——PR-2 只覆盖契约识别 + cellgen 派生层。

### 常见错误排查

- **`gocell generate cell` 报 "no cell.go struct field for subscribing slice"**：cell.go
  结构体缺少对应 slice 的字段。在 cell struct 添加 `*<sliceID>.Service`（或
  `*<sliceID>.Consumer`）字段后重新运行。该字段必须在 `generate` 执行前已存在于
  cell.go。

- **`gocell generate cell` 报 "ambiguous field for slice <sliceID>"**：cell struct 中有
  多个字段的指针类型包名与 sliceID 相同。在 slice.yaml 的 subscribe CU 中添加
  `field: <fieldName>` 消歧（参见 sessionlogout 场景）。

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

## Projection ↔ ConsumerBase 装配（composition root）

Projection 经与 subscription **同一条 ConsumerBase 消费路径** 在 bootstrap phase6 被
projection coordinator 驱动；因此任何注册 projection 的 assembly，其 composition root
**必须** wire `bootstrap.WithConsumerBase`——漏掉则 phase6 启动即 fail-fast（PR #1483
`examples/todoorder/run.go` 即此 bug，CI 绿但 `go run` 崩）。该运行时不变式由下表 archtest
提前到 CI 静态层。

| Archtest ID | 摘要 | 评级 |
|---|---|---|
| `PROJECTION-CONSUMERBASE-WIRING-01` | composition-root 包（`examples/*` + `cmd/*`）凡 wire `bootstrap.WithProjection*` 必同包 wire `bootstrap.WithConsumerBase`，否则 CI 红 | 下游 Hard（`ResolvePackageRef` 类型解析 callee，alias / dot-import 不可绕）+ 上游 Medium（包级共址 presence check；唯一 Hard 路径 = projection 注册需 ConsumerBase typed token 的 kernel 重设计，won't-do 追踪 gh #1597，archtest 即定型） |

完整盲区清单 + 反向自检活在 `tools/archtest/projection_consumerbase_wiring_test.go` 的 package
godoc（单源）；运行时 defense-in-depth = `examples/todoorder/run_smoke_test.go` 启动 smoke
（真启动过 phase6，抓任意 boot 失败）。

## Projection 载体接口（`ProjectionEvent`）

投影 harness 的 `Apply` / `ReplaySource.Replay` 的 fn / `Cursor.Position` 收 **最小 typed 只读载体接口
`cellvocab.ProjectionEvent`**（`projection.ProjectionEvent` / `cell.ProjectionApply` 为同型 alias），而非具体
`outbox.Entry`——`outbox.Entry` 与（PR-03）saga journal 事件各自实现它，走同一 typed 漏斗（EPIC #1609 PR-01 /
ADR `docs/architecture/202606051200-1609-adr-saga-journal-projection-source.md` §D2）。接口 5 方法：`EventID()` /
`Payload()` / `OccurredAt()` / `Stream()` / `RestoreContext(ctx)`（`EventID`/`Stream` 是 outbox `ID`/`RoutingTopic`
的多态重命名）。接口家在 `kernel/cellvocab`（纯叶子）而非 ADR §4.1 写的 `kernel/projection`——cell↔projection 环
使后者编译不可表达（ADR §4.1/§6 amendment 记录）。

| Archtest ID | 摘要 | 评级 |
|---|---|---|
| `PROJECTION-EVENT-CARRIER-TYPED-01` | 投影公开载体 API（`projection.Apply` / `ReplaySource.Replay` fn / `Cursor.Position` / `cell.ProjectionApply`）只收 `cellvocab.ProjectionEvent`，禁裸 `outbox.Entry`；A2 broad scan 兜 kernel/projection+cellvocab 导出符号未来新增 | **type-system Hard（API shape，单轴非 funnel）**：签名即接口（编译期）+ archtest 下游禁裸收 `outbox.Entry`。**非** carrier-source-sealing funnel——`ProjectionEvent` 全导出可实现、载体来源不封闭（forge 防护在 wiring 层），故不声明 sealed-carrier 上游 Hard |

完整盲区清单 + 反向自检（RED/GREEN fixture）活在 `tools/archtest/projection_event_carrier_typed_test.go` 的
package godoc（单源）。

## Stream 命名

- 新建 stream 前搜索已有常量，禁止重复定义
- stream 名 ≥ 3 次使用抽常量
- 按 stream 过滤事件时区分通道

## 事件负载

- 每个事件包含 `eventId`（UUID）作为 envelope wire 字段，用于幂等键构造（DB 列名仍是 `event_id` per CLAUDE.md DB 字段 snake_case）
- 负载变更向后兼容（新字段 optional，或版本化如 `device.enrolled.v2`）
- 不兼容变更：先部署 consumer 再部署 producer
