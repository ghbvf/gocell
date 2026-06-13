# ADR: 拓扑门控的 outbox 事件传输（topology-gated event transport）

- 状态: Accepted
- 日期: 2026-06-13
- Issue: #1940
- 关联: #1303（`PROD-MAIN-WIRING-NOOP-REJECT-01` option-A 决策）、EventBus 规范
  `.claude/rules/gocell/eventbus.md`

## 背景（Context）

postgres（durable）拓扑下事件走 outbox 模式：cell 在同一 PG 事务里写业务变更 + outbox
entry（持久、原子）→ relay worker 从 PG 认领 entry 并 publish 到一个消息 broker
（`outbox.Publisher`）→ 其他 cell 的 consumer 从 broker subscribe 处理
（`outbox.Subscriber`）。

在 #1940 之前，`cmd/corebundle/shared_deps.go` 无条件 `eventbus.New(clk)`，把**进程内
in-memory bus** 同时当作 `WithPublisher` + `WithSubscriber`——**无论 demo 还是 postgres**。
3 个 example composition root 同形。结果：postgres 下 WRITER 是 PG（持久），relay 却把
已持久化的 entry 发到进程内 bus —— **跨进程 / 重启事件丢失，持久化白做**。全仓库已存在
`adapters/rabbitmq` / `adapters/mqtt` 的完整 `Publisher`+`Subscriber`+`Connection`
实现，但 composition root 从未按拓扑选型接线（grep 确认：本 PR 前 `adapters/rabbitmq` 无任何
非自身、非测试导入者）。

这是 #1303 reviewer P0「memory fallback」记录的真实缺口：`PROD-MAIN-WIRING-NOOP-REJECT-01`
的 option-A **刻意排除** `runtime/eventbus.InMemoryEventBus`（它是当时全仓唯一接线的进程内
publisher 且非 Nooper），排除的代价就是本缺口。

## 决策（Decision）

1. **拓扑门控传输 funnel**：新增 `cellmodules/eventtransport.Resolve(clk, topo, cfg)`，
   按 `bootstrap.Topology` 单源选型，是 `kernel/outbox.ResolveEmitter` 的 publisher 侧对称物：
   - demo / memory 拓扑 → 进程内 `runtime/eventbus`（Publisher == Subscriber 同实例）。
   - postgres 拓扑 → 真实 broker（RabbitMQ，URL 来自 `GOCELL_AMQP_URL`），返回 broker
     `Connection` 作为 `lifecycle.ManagedResource`。
2. **完全切换（非 tee）**：postgres 下 broker 是**唯一** pub+sub 传输；in-memory bus 在
   postgres 拓扑**不可达**。relay→broker→consumer 是标准 durable outbox 链路。
3. **Fail-closed**：postgres 拓扑缺 `GOCELL_AMQP_URL` → 启动期报错，**不静默降级回 in-memory**。
   RabbitMQ `Connection` 构造器内立即 dial，故不可达 broker 也启动期 fail-fast。
4. **载体层归属**：resolver 落 `cellmodules/`（Composition Root 共享 helper 层，与 `cellsecrets`
   同位）而非 `runtime/composition`——后者按分层契约**禁止 import `adapters/`**。`SharedDeps`
   只持 `outbox.Publisher`/`outbox.Subscriber` **接口**（与 `MetricsProvider` 同理），具体实现
   由 composition root（cmd / examples）经 resolver 注入。
5. **broker 范围**：本 PR 只接 rabbitmq；扩展点留在**代码层**（`brokerKind` 内部 enum +
   `Resolve` switch 的 fail-closed default）。**不暴露 `GOCELL_EVENT_BROKER` env**——当前
   只有一个合法 broker，暴露一个单值 env + 软默认是预设未来需求（YAGNI）。

## 守卫与威胁模型（重评 #1303）

引入的约束「postgres 模式事件传输必须真实 broker，cmd 不得静默重接 in-memory」由三层闭环守卫：

- **静态（depguard，路径级 import ban）**：`corebundle-no-direct-eventbus`
  （`COREBUNDLE-EVENTBUS-FUNNEL-01`）禁止 `cmd/corebundle` **production** 代码 import
  `runtime/eventbus`——in-memory bus 仅经 `eventtransport.Resolve` 的 demo 分支可达。resolver
  落 cellmodules 使该禁止集**无 function-level carve-out**（cmd 包 fix 后 `eventbus.New` 调用数
  = 0）。`// INVARIANT:` 烙在 `cellmodules/eventtransport/doc.go`，depguard 做机器强制。
  评级 Medium（CI lint 强制；与 `saga-executor-no-journal-import` 同范式，ai-robust.md
  §「路径级 import ban → depguard」）。
- **运行时 fail-fast**：postgres + 缺 broker → 启动期 error（`resolveBrokerSpec` fail-closed）。
- **回归测试**：`eventtransport` 单测穷举拓扑门 + fail-closed；`cmd/corebundle` 集成 e2e
  （build-tag `integration`）证明 Resolve 对真实 RabbitMQ 产出可用传输（dial + 健康 probe +
  confirm-publish）。

**#1303 威胁矩阵重评**：#1303 记录的「postgres 下 relay 发 in-memory」缺口由本 ADR 关闭。
`PROD-MAIN-WIRING-NOOP-REJECT-01` 对 `InMemoryEventBus` 的刻意排除**依旧成立**（该规则的范围是
*emitter sink* 的 noop 拒绝，与本 funnel 正交）；本 ADR 不修改该规则，而是用一个**更小、已支持**
的 call-site/import 范式（depguard）覆盖生产根的 in-memory 重接面。把 `eventbus.New` 纳入
control-flow-aware 的全量禁止集是 #1303 refactor 种子里独立的大工程（规则基建尚不存在），不在本
PR 范围。

## 后果（Consequences）

- corebundle 在 postgres（real）拓扑下**硬依赖**一个可达的 RabbitMQ broker 才能启动——这是有意的
  durable 契约（缺 broker 不再静默退化成丢事件的 in-memory）。新增 env `GOCELL_AMQP_URL`。
- `SharedDeps.EventBus`（具体 `*InMemoryEventBus`）→ `Publisher outbox.Publisher` +
  `Subscriber outbox.Subscriber`（破坏式 retype，无兼容别名；`SHAREDDEPS-FIELDSET-FROZEN-01`
  golden 同步更新）。3 个 cell 模块 + corebundlestarter 改读新字段。
- examples `ssobff` / `iotdevice` 不在本 PR 范围（用户决策）：`ssobff` 是与 corebundle 同形的
  真 bug（postgres outbox + in-memory publisher），登记 follow-up backlog；`iotdevice` 的 MQTT
  tee 是有意的设备侧 demo 接线，不动。
- MQTT broker 接线延后：`Resolve` switch 留 fail-closed default 扩展点，补 = 加
  `GOCELL_EVENT_BROKER` env + 分支 + 测试（follow-up）。

## 参考

- 对称 funnel：`kernel/outbox/mode_resolver.go`（`ResolveEmitter`）
- 拓扑门先例：`cmd/corebundle/bundle_assembly.go`（`durabilityModeForTopology`）
- depguard import-ban 范式：`.golangci.yml`（`saga-executor-no-journal-import` /
  `corebundle-no-direct-eventbus`）、ai-robust.md §「路径级 import ban → depguard」
- 权威语义：`cellmodules/eventtransport/doc.go`
