# Research: 运行时契约注册中心对标（控制面 + 数据面）

> 来源：两个 `explorer` agent 并行开源探索（2026-06-16）。控制面 agent 对标 Confluent Schema Registry / Kubernetes / Pact / Envoy xDS；数据面 agent 对标 watermill / go-control-plane / controller-runtime / Istio。所有签名/路径均来自实拉源码与官方 API doc。

## 1. GoCell 现状（已读源码，分解前提）

- `framework/kernel/registry/contract.go`：`ContractRegistry` **完全只读**——`NewContractRegistry(project)` 一次性构建 `contracts/byKind/byOwner` map，读时 `deepCopyContract`，**无 mutation API**。doc 注明「populated once during assembly bootstrap and remain immutable afterward」。
- `framework/runtime/eventrouter/router.go`（770 行）：「声明 then `Run`-once」——`runGuard sync.Once` 保证 `Run` 只一次；`handlers []handlerConfig` 受 `mu sync.Mutex` 保护，**Run 后冻结，无运行时增删**；`Close` 三阶段 StopIntake→cancel→drain；全局单 `runCtx`/`r.cancel`（cancel 即全停）。
- `framework/kernel/governance/validate.go`：`Validator` 纯 Go 校验器，今天只被 `gocell validate` CLI 调用 → 可直接 runtime 化（「把 `gocell validate` 从 CI 搬到注册端点」）。

## 2. 控制面对标（→ P0/P1/P2/P3/P6/P7）

### 2.1 Confluent Schema Registry — 注册 API + 同步校验门（→ US3/US6）

- **双端点设计**：`POST /subjects/{s}/versions`（写入）与 `POST /compatibility/subjects/{s}/versions/{v}`（dry-run 只判不落库）**共用一套校验逻辑**（`isCompatibleWithPrevious`）。
- **写前同步校验**：`KafkaSchemaRegistry.register` 严格有序全同步——先 `waitUntilKafkaReaderReachesLastOffset()`（读己之写屏障）→ compatibility check（不兼容直接抛，无中间态）→ 分配 id → append-only log 落库。
- **Mode=READONLY** 全局闸冻结写入，与 per-subject compatibility 正交。
- **采纳**：复用 `governance.Validator` 暴露 `:check`(dry-run) + `submit` 双入口；写前同步拒绝（不过不进 submitted）；append-only 事件溯源（载体换 PG outbox，非 Kafka）；leader 接写复用 reconcile leader-elect。
- **偏离**：GoCell 是契约**元数据**非 schema 字节；多阶段状态机（submitted→probing…）而非 SR 校验完即终态——静态门同步、活体 conformance 异步。

### 2.2 Kubernetes — Admission + CRD condition + Aggregation（→ US1/US2/US7/US15）

- **AdmissionResponse** `{Allowed bool, Result *Status, Warnings []string, AuditAnnotations}` = governance gate 响应模型最佳对标；`ValidatingWebhook.FailurePolicy=Fail`（默认 fail-closed）+ `SideEffects: NoneOnDryRun`（dry-run 抑制副作用）。
- **CRD 状态机**：`NamesAccepted`（group 内 Group+Plural+Kind 冲突检测）→ `Established`（首次无冲突即激活，后续保持）→ `Terminating`，与 GoCell `submitted/probing/conformant/active/retired` 高度同构。
- **校验分层**：admission = 同步 inline（写前阻塞）；CRD Established = 异步 controller condition（写后推进）→ 直接对应 GoCell saga(人审)/reconcile(活体收敛) 正交边界，**无需新引擎**。
- **采纳**：`{allowed,result,warnings}` 响应；FailurePolicy=Fail；dryRun 抑制副作用；NamesAccepted 换成 `(kind,domain-path,version,owner)` 四元组唯一性；同步门 + 异步 condition 分层作状态机骨架。
- **偏离**：API aggregation（APIService 转发外部 apiserver）仅作 P0 进程外论证引用，不复制其 TLS/discovery 全栈。

### 2.3 Pact / Provider Verification — 活体 conformance（→ US17/US18）

- **对活体黑盒验契约**（`providerBaseUrl`，验兑现非测代码）；`stateHandlers` 的 setup/teardown 配对（测试数据进出对称）；`requestFilter` 注入瞬态凭据（token 不落契约）。
- **can-i-deploy 矩阵**：无 successful verification 不准 deploy → GoCell `probing→conformant` 晋级 = conformance result 通过；result 是带 provider 版本的不可变记录。
- **采纳**：活体握手范式；setup/teardown sealed 配对（缺 teardown 校验/编译错，强于 Pact 运行时约定，AI-robust Hard）；凭据注入不入契约；result append-only。
- **偏离**：bi-directional pact（消费者期望↔提供者验证）不适用——GoCell 是「中心声明→活体验证兑现」单向。

### 2.4 Envoy xDS — 控制面/数据面分离（→ US1/US11）

- **DiscoveryRequest/Response + version_info + nonce + ACK/NACK**：NACK 时 client 填 error_detail、**保留上一个 good version**（坏配置永不生效，last-good 在线）；ADS make-before-break 排序（依赖资源先于引用方）。
- **采纳**：控制面声明→数据面无重启加载（P0 论证主骨架）；NACK 保留 last-good（坏契约不应用）；version_info ≈ fencing epoch，收敛复用 reconcile。
- **偏离**：单 gRPC 长流多路复用 → GoCell 走 L2 outbox 事件（`contract-activated`），不建 xDS 长流。

## 3. 数据面对标（→ P4：US9/US10/US11/US12）

### 3.1 watermill `message/router.go` — 动态 handler（最高优先）

- `Router.handlers map[string]*handler` + `handlersLock *sync.RWMutex`；`AddHandler` 后可 `RunHandlers()`（docstring 显式支持运行时增）；`handlerAdded chan struct{}` 信号；`h.started` 标志保证 RunHandlers 幂等；每 handler 独立 `h.stopFn = cancel`（per-handler ctx）；handler 退出 defer 内 `delete(r.handlers, name)` 自删 + `close(h.stopped)`；在途 message 由 `runningHandlersWg.Wait()` drain。
- **GoCell 缺失**：Run 后冻结 slice、全局单 cancel、无单 handler stop、slice 无删除、只有全局 Running()。
- **采纳**：slice→`map[topic+group]`；per-handler 子 ctx + stopFn；退出自删 map；started 幂等。**偏离**：watermill `AddHandler` 带 publish-side（GoCell 经 outbox.Writer 发布，Router 只订阅）；重名 panic→返回 error（治理禁生产 panic）。

### 3.2 go-control-plane `SnapshotCache` — 原子版本化推送

- `SetSnapshot` 在 `mu` 内整体替换 `snapshots[node]` + 触发 watch；每 typeURL opaque version，version 未变不重推；ADS `orderResponseWatches()` 强制排序。
- **采纳**：`RegistrySnapshot` 单调 version + diff（仅对变化订阅 add/remove，避免全 Router 重建）；版本回退 fail-closed。**偏离**：GoCell 是 push 式 reconcile 单点增删，不引 per-node SnapshotCache 全套（订阅是有状态 goroutine，整份替换=全重建代价高）。

### 3.3 controller-runtime `source.go` — ctx-per-source 生命周期

- `TypedSource.Start(ctx, queue)` 绑定 ctx，cancel→watch goroutine 退出；`TypedSyncingSource.WaitForSync(ctx)` 就绪屏障（对标 GoCell `Subscriber.Ready`）。
- **采纳**：每个动态订阅 = ctx-scoped runnable，与 reconcile/leader-elect「丢 lease 取消 lease-scoped ctx」同构；WaitForSync ≈ 复刻现有 Phase3 单 handler 版。**偏离**：不引 informer/workqueue；leader gating 由 composition root 决定（与 saga-projection locker 一致），Router 不自建 leader gate。

### 3.4 Istio Pilot `discovery.go` — debounce 批量

- `DebounceOptions{DebounceAfter, debounceMax}` 双触发（静默 + 上限保底）+ `PushRequest.Merge` 合并窗口内多次变更。
- **采纳**：`contract-activated` 消费侧 debounce 合并多激活为一次 diff reconcile；累积 toAdd/toRemove 集窗口末一次 apply。**偏离**：GoCell 数据面天然增量（add/remove 单 handler），debounce 只合并触发频率，不做全量 vs 增量传输决策。

## 4. P4 改造清单（数据面 agent 给出，→ US9-US12 拆分依据）

| PR | 范围 | 对标 | US |
|----|------|------|----|
| A | `handlers []` → `map[handlerKey]*runningHandler` + 重名返 error（行为等价地基） | watermill map | US9 |
| B | `runRootCtx` 提升 + per-handler 子 ctx + `AddRunningHandler`/`RemoveHandler`（fail-closed 半启动清理）；与 `runGuard sync.Once` 兼容（Run 仍一次，增删仅 started 后） | watermill stopFn + controller-runtime ctx-per-source | US10 |
| C | `RegistrySnapshot` 版本化 + diff `Reconcile` + `contract-activated` debounce + archtest 守卫（子 ctx 挂树 / fail 必 hcancel） | go-control-plane + istio | US11 |
| D | HTTP Router 一次性 drain → 运行时 add/remove route（不绕 auth） | net/http 包装 mux | US12 |

> `Close` 三阶段 drain **零改动复用**：Phase2 全局 cancel `runRootCtx` 整棵树 → 动态 handler 子 ctx 随之取消；Phase3 `wg.Wait()` 自然等动态 handler 退出。这是 GoCell 优于直接照搬 watermill 之处（watermill auto-close 逻辑 GoCell 不需要）。

## 5. 综合裁决（GoCell 取舍）

| 维度 | 取舍 |
|------|------|
| 注册 API | REST `submit` + `:check`(dry-run) 共用 `governance.Validator`；契约元数据非 schema 字节 |
| 校验门 | 静态 governance gate 同步（不过不进 submitted）+ 活体 conformance 异步（probing→conformant，reconcile 收敛）——K8s 分层 |
| 审批 | 机器门(gate+conformance) → admin 人审(saga 有限步) → active；四系统均无人审，人审是 zero-trust 增量需自研 |
| 命名空间 | `(kind,domain-path,version,owner)` 四元组 + `_framework` sentinel 单列（对标 NamesAccepted） |
| conformance 副作用 | Pact setup/teardown 配对（sealed 强制，Hard）+ K8s dryRun 抑制 + 凭据注入不入契约；彻底档合成租户 hard-depend #1337 |
| 控制面/数据面 | xDS NACK 保 last-good + CRD 不重启加载 + ADS 依赖序；收敛复用 reconcile + fencing epoch；append-only 状态（载体 PG outbox） |
| 数据面增删 | watermill map + per-handler ctx + 退出自删；go-control-plane 版本化 diff；istio debounce 合并；controller-runtime WaitForSync 屏障 |

## 6. 引用（供 PR/commit）

```
ref: confluentinc/schema-registry core/.../storage/KafkaSchemaRegistry.java + rest/resources/SubjectVersionsResource.java@master
ref: kubernetes/api admission/v1/types.go + admissionregistration/v1/types.go@master；apiextensions-apiserver pkg/apis/apiextensions/v1/types.go@master
ref: pact-foundation/pact-js docs/provider.md@master；docs.pact.io/pact_broker/can_i_deploy
ref: envoyproxy/envoy xds_protocol（官方 docs）
ref: ThreeDotsLabs/watermill message/router.go@master — AddHandler/RunHandlers/handlerAdded/stopFn/delete-on-exit
ref: envoyproxy/go-control-plane pkg/cache/v3/simple.go@main — SnapshotCache.SetSnapshot atomic + per-typeURL version + watch diff
ref: kubernetes-sigs/controller-runtime pkg/source/source.go@main — TypedSource.Start(ctx,queue) + WaitForSync
ref: istio/istio pilot/pkg/xds/discovery.go@master — debounce DebounceAfter/debounceMax + PushRequest.Merge
GoCell 现状: framework/kernel/registry/contract.go、framework/runtime/eventrouter/router.go、framework/kernel/governance/validate.go
```
