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
- examples `ssobff` / `iotdevice` 当时不在本 PR 范围（用户决策）：`ssobff` 是与 corebundle 同形的
  真 bug（postgres outbox + in-memory publisher），登记 follow-up backlog；`iotdevice` 的 MQTT
  tee 是有意的设备侧 demo 接线，不动。**`ssobff` gap 已由 #825/#2017 关闭**——见下 §Amendment
  2026-06-13。`iotdevice` 仍未核（待定其是否用 durable outbox）。
- MQTT broker 接线延后：`Resolve` switch 留 fail-closed default 扩展点，补 = 加
  `GOCELL_EVENT_BROKER` env + 分支 + 测试（follow-up）。

## Amendment 2026-06-13（#825/#2017）：funnel 扩到第二个 composition root + 两个新原语

本 amendment 关闭上文 §后果记录的 `ssobff` follow-up，并按 ai-robust.md §「ADR amendment 落地
必查」重评本 ADR 的威胁模型——**#1940 的 funnel 只 path-scope 了 `cmd/corebundle`，这正是 `ssobff`
（自带 composition root）能重现 #2017 同形 bug 的根因**。修正后威胁面扩展如下：

- **第二个 composition root**：`examples/ssobff` 现经 `eventtransport.Resolve` 接线传输，新增 depguard
  `ssobff-no-direct-eventbus`（与 `corebundle-no-direct-eventbus` 同 `COREBUNDLE-EVENTBUS-FUNNEL-01`
  族，路径级 import ban，Medium）。bus funnel 现覆盖两个生产 composition root。
- **两个新原语（claimer / nonce）**：`ssobff` 同形还有两处单 pod in-memory 原语——idempotency claimer
  （#825）与 service-token nonce store（同族）。二者现经新共享包 `cellmodules/replaydeps.Resolve`
  拓扑门控（demo→in-mem，real multi-pod→Redis，缺 Redis fail-closed），`cmd/corebundle` 的同形
  glue 一并上移到该包（两 root 复用代码+测试，对称本 ADR 抽 `eventtransport` 的范式）。
- **载体差异（depguard 不可表达 → archtest）**：claimer/nonce 的构造点禁令**不能**用 depguard——
  `kernel/idempotency`、`runtime/auth` 在两个 root 仍为类型合法 import（`idempotency.Claimer`、
  `kauth.NonceStore`），whole-package import ban 会误红。故改用调用级 AST 扫描 archtest
  `REPLAYDEPS-INMEM-FUNNEL-01`（路径限定两 root，含 RED fixture + dot-import 盲区自检，Medium）。
  bus（depguard import-ban）+ claimer + nonce（archtest call-ban）三 funnel 合起来确保**两个生产
  composition root 内每个 in-memory 单 pod 原语只经 sealed resolver 可达**。
- **评级**：三 funnel 均 Medium（CI 静态 + 运行时 fail-fast）。Hard 化（封死 in-mem 构造器）成本高
  （这些构造器在其它 example / runtime 包有合法 caller，越出本 PR 范围）→ 按 ai-robust.md §审查要求
  无低成本 Hard 路径，不登记 Hard 化 issue。`PROD-MAIN-WIRING-NOOP-REJECT-01` 对 `InMemoryEventBus`
  的排除仍正交、不变。
- **权威语义**：`cellmodules/replaydeps/doc.go`（§INVARIANT REPLAYDEPS-INMEM-FUNNEL-01）+
  `tools/archtest/replaydeps_inmem_funnel.go`。

## Amendment 2026-06-15（#2211）：`Transport.Kind` sealed broker-kind 事实

`Resolve` 现给每个 `Transport` 盖一个 sealed `bootstrap.EventTransportKind`（`Transport.Kind`）：
postgres → RabbitMQ 分支 mint `RealBrokerEventTransport()`，demo 分支 mint `InMemoryEventTransport()`。
这是 #1423 US3 phase0 broker-mandatory 闸的输入升级——闸由 `StorageBackend()=="postgres"` 代理改查
sealed `IsRealBroker()`（«non-nil ≠ real broker» 收口，见 ADR 1423 的 2026-06-15 amendment）。

- **本包是 real-broker 变体的唯一 sanctioned minter**：composition root 透传 `Transport.Kind` 到
  `bootstrap.WithEventTransportKind`，自身不调构造器。由新 archtest
  `EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01`（call-level scan，allowlist 本包）守。
- **评级**：sealed 构造 Hard；minter-单调用方结构性 Medium（跨模块 `framework`↔`cellmodules`，
  `internal/` 不可桥接 → 无低成本 Hard 路径，不立 issue，与 REPLAYDEPS-INMEM-FUNNEL-01 同族 Go 天花板）；
  生产端到端「split ⇏ in-mem」仍由本 ADR 的 `COREBUNDLE-EVENTBUS-FUNNEL-01` depguard 保 Hard。
- **权威语义**：`cellmodules/eventtransport/doc.go`（§INVARIANT EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01）+
  `framework/runtime/bootstrap/event_transport_kind.go`。

## Amendment 2026-06-18（#2152 PR-2）：per-cell broker URL dedup（egress-only）

`Config` 从单 `AMQPURL string` 改为 `Cells map[string]string`（per-cell broker URL）。composition root
按 cell 读 `GOCELL_<CELLID>_AMQP_URL`（缺省回退 assembly 级 `GOCELL_AMQP_URL`），传入 `Resolve`；新增纯函数
`dedupBrokerURL` 按 URL 去重，是 `cellmodules/percellpg.Resolve`（per-cell DSN 去重）的 broker 侧孪生。

- **去重语义**：同一 URL（colocated）→ 单 broker 连接（行为不变——旧的单 `GOCELL_AMQP_URL` 接线即「每个 cell
  回退到同值」这一情形）；distinct URL → **fail-closed**。三道 fail-closed（空 cell 集 / 任一空 URL / >1 distinct）
  逐字镜像 percellpg。
- **distinct fail-closed = egress-only 边界**：#2152 PR-1（`bootstrap.WithRelay` keyed-by-instance）只 fan-out
  了 relay（publisher）侧；subscriber 仍单例（phase6 单 event router）。单 subscriber 下 distinct broker 会孤儿化
  事件（cell A 发到 broker A，唯一 subscriber 只消费 agreed broker），故 resolver 拒绝 distinct 而非静默切断
  publish/subscribe 链。lifting（真 N-broker fan-out）需：① ingress fan-out（subscriber 单→N + phase6 N-router，
  #2366）；② relay/pool 源 #2341（per-cell PGProvider → N pools → N relays），与 percellpg 的同构 `>1` DSN
  fail-closed 同步 lift。
- **评级（AI-robust）**：现有 Hard 边界全保持——`COREBUNDLE-EVENTBUS-FUNNEL-01`（depguard）、
  `EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01`（mint 仍本包，循环 mint N 次不破包级 allowlist）、
  `SHAREDDEPS-FIELDSET-FROZEN-01`（`Publisher`/`Subscriber` 保持单字段——egress-only 不引入 N subscriber 字段）、
  `RELAY-SOLE-HOLDER-01`（无新 relay）。唯一新约束 = `dedupBrokerURL` 三道 fail-closed，**Medium**（纯函数
  startup fail-fast；env URL 组合 type 不可表达，runtime/纯函数 guard 是宪章认可载体；单一 sanctioned 守门点 +
  穷举单测 + anti-vacuity；blind spot = 无 archtest，与 percellpg 同先例，无低成本 Hard 路径，不立 issue）。
  **零新增 Soft**。unsafe 态（egress-only + distinct broker）经唯一构造路径 `Resolve` 直接不可得。
- **威胁矩阵重评**：#1940 原断言「broker 是 assembly 级单连接、per-cell broker seam 有意不建模」**被本 amendment
  取代**——per-cell broker URL seam 现已建模（egress-only 子集），distinct broker 由 fail-closed 守而非「不可表达」。
  「postgres 缺 broker URL → fail-closed，不静默降级 in-memory」的核心不变式保持（现按 per-cell 粒度，错误命名
  `GOCELL_<CELLID>_AMQP_URL`）。
- **权威语义**：`cellmodules/eventtransport/doc.go`（§"Per-cell broker URL dedup (egress-only, #2152 PR-2)"）。

## Amendment 2026-06-18（#2152 PR-3）：per-cell AMQP vhost/credential 隔离安全模型

### 凭据隔离 seam

per-cell AMQP URL（`GOCELL_<CELLID>_AMQP_URL`）本质上是凭据+vhost 隔离的入口点。AMQP DSN 格式
`amqp://user:pass@host/vhost` 携带 broker 用户名、密码与 vhost，operator 可为每个 cell 配置独立
的 user 和 vhost，使每个进程只持有访问自身 broker 资源所需凭据。

**为何不做 HKDF/派生层**：AMQP broker 用户是外部对象，由 broker operator 在 RabbitMQ 管理面
单独 provision，不存在 framework 可控的 master key（对比 #2153 HMAC keyring：token 签名 key 由
framework 持有并可派生）。per-cell 凭据是 operator 通过 `GOCELL_<CELLID>_AMQP_URL` 外部注入，
framework 不建新包、不做派生。

### 当前边界与运行期隔离

- distinct per-cell URL = egress-only fail-closed（同 PR-2，`dedupBrokerURL`）。
- 运行期 per-cell 隔离（每个 cell 连接独立 broker）blocked-by #2366（ingress 订阅者 N-router）
  + #2341（per-cell relay/pool fan-out），与 PR-2 fail-closed 一起 lift。

### AI-robust 评级显式分层

① **per-cell 运行期隔离**：Hard 今天不可得（运行期 blocked-by #2366/#2341，声称 Hard 即 overclaim）。

② **凭据 non-leak**：Soft（`connection.go` sanitize 函数注释约定）→ **Medium**（archtest
`AMQP-URL-REDACTION-FUNNEL-01`，typed AST field-selection scan，go/types 解析；blind spot =
local-var laundering + map-value 读，由 `TestDedupBrokerURL_CredentialNonLeak` 行为测试补）。

③ **Hard-via-sealed-URL-type**：封装 redacted-Stringer URL 类型并改 adapter 全调用方，成本高、
无低成本路径，不立 issue（章程「无低成本 Hard 路径不立 issue」）。

### 威胁矩阵补全

本 amendment 补全 ADR 1423 §"安全模型与已知缺口"中「共享 AMQP broker 凭据」行（详见 ADR 1423
的对应 amendment）。PR-2 的 fail-closed 是隔离的充分前提——distinct URL 被拒绝即意味着今天
colocated 模式下所有 cell 共享同一 AMQP 凭据（预期行为），而 split 运行期隔离须等 #2366/#2341。

**权威语义**：`cellmodules/eventtransport/doc.go`（§INVARIANT AMQP-URL-REDACTION-FUNNEL-01
+ §Per-cell credential/vhost isolation）。

## 参考

- 对称 funnel：`kernel/outbox/mode_resolver.go`（`ResolveEmitter`）、`cellmodules/replaydeps`（#825/#2017）
- 拓扑门先例：`cmd/corebundle/bundle_assembly.go`（`durabilityModeForTopology`）
- depguard import-ban 范式：`.golangci.yml`（`saga-executor-no-journal-import` /
  `corebundle-no-direct-eventbus`）、ai-robust.md §「路径级 import ban → depguard」
- 权威语义：`cellmodules/eventtransport/doc.go`
