# Feature Specification: kernel/reconcile L4 Desired-State 收敛控制环

**Feature ID**: `661-kernel-reconcile`
**Created**: 2026-05-26
**Status**: **PARKED-ON-TRIGGER**（trigger 未满足；本文档冻结设计，等触发后激活）
**Issue**: [#661](https://github.com/ghbvf/gocell/issues/661) KERNEL-RECONCILE-01
**Priority**: P3
**Labels**: `cap-08`, `flag-cond`, `type-feat`, `backlog`

---

## ⚠️ Trigger Gate（必须先满足才能进入实施）

本规格是 **trigger-gated 设计**，本文档冻结技术决策与 PR 切分，但 **不立项实施**，直到下列触发条件至少满足两条：

| # | 触发条件 | 来源 | 预计满足时间 |
|---|---------|------|------------|
| T1 | `pkicell.rotation` slice 落地（证书续期 L4 控制环） | `docs/plans/product-roadmap/202604301030-winmdm-prd-on-gocell.md` §2.1 | winmdm Stage 1，2027 Q1 |
| T2 | `mdmcell.command` 重发逻辑落地（命令超时主动驱动） | 同上 §2.2 + §4.2 | winmdm Stage 2，2027 Q2-Q3 |
| T3 | `devicelifecycle.cronsweep` slice 落地（设备墓碑状态机） | 同上 §4.4 | winmdm Stage 4，2027 Q4 |
| T4 | `zerotrust.trustscore` 周期重评落地 | `docs/plans/product-roadmap/202604301100-zt-extensibility-and-mdm-decoupling.md` §2.1 | zt Phase 5，2029 Q1 |

**满足判定规则**：T1/T2/T3 中至少 **两个**生产 cell 落地（不包括 example），或 T1/T2/T3 任一 + T4 同时落地。`examples/iotdevice` 不计入消费方计数。

**激活流程**：触发满足时，开新 PR 系列引用本规格，把 plan.md / tasks.md 的 PR 序列付诸实施。本文档自身不再修改。

**违反此 gate 的后果**：参见 issue body「plan-D §10 禁止预建 mdm/」；在 trigger 满足前合并任何 `kernel/reconcile/*.go` 代码均视为违章。

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 证书续期周期收敛（Priority: P1，对应 T1）

**Actor**: `pkicell.rotation` slice 维护者（winmdm Stage 1, 2027 Q1）

**Plain Language**:
作为 `pkicell` 的维护者，我希望声明一个 reconciler 实现，框架会按指定间隔扫描所有 `cert_issued` 行；对每个 `expires_at < now + 30d` 的非终态证书，框架调我的 `Reconcile(ctx, Request{EntityID: certID})`；我返回 `Result{RequeueAfter: nextWindow}` 或 `error` 让框架决定重试。我**不**需要自己写定时器、不需要自己管退避、不需要自己跑 leader election；但我**必须**保证 `Reconcile` 幂等——leader election 不是 fencing 保证，跨副本残余并发由框架 epoch fencing CAS + 我的幂等兜底（见 ADR §4.3/§4.4）。

**Why this priority**: 证书过期等同生产中断。L4 收敛是 winmdm 上线的必要前提，pkicell 是最先落地的消费方。

**Independent Test**: 在 `examples/iotdevice` 用 fake `Reconciler` 验证：注入 100 条非终态实体 + ticker=1s + reconciler 返回 `RequeueAfter=5s`；observable：第 1 秒扫描 100 次，后续每秒只扫描到点的（按 RequeueAfter 排程），永不重入运行中的 entity。

**Acceptance Scenarios**:
1. **Given** 100 条非终态实体，**When** Loop 启动 + Reconcile 返回 `Result{RequeueAfter: 5s}`，**Then** 第一轮全部扫描；后 4 秒不扫描；第 5 秒起按到期顺序再扫
2. **Given** Reconcile 返回 `error`（非 PermanentError），**When** 下一次 tick，**Then** 走指数退避（baseDelay=5ms, maxDelay=1000s），不立即重入
3. **Given** Reconcile 返回 `PermanentError`，**When** 下一次 tick，**Then** 该 entity 进入死信通道（counter +1，不再调度）

---

### User Story 2 - 设备命令超时重发（Priority: P1，对应 T2）

**Actor**: `mdmcell.command` slice 维护者（winmdm Stage 2, 2027 Q2-Q3）

**Plain Language**:
作为 `mdmcell.command` 的维护者，我希望声明一个 reconciler，扫描所有 Status ∈ {Pending, Sent} 且 `now > sentAt + SendToCompleteTimeout` 的命令；reconciler 触发重发逻辑。多副本部署时，框架用 leader election（best-effort 收窄）+ epoch fencing CAS（结构兜底）+ reconciler 幂等，把重复命令收敛到 at-most-once-effective；**leader election 本身不是 fencing 保证**（client-go 明示「does not guarantee that only one client is acting as a leader」，见 ADR §4）。

**Why this priority**: 命令丢失会直接影响 MDM 的 SLO（device 收到管理命令的成功率）。leader-elect 是 hard 依赖。

**Independent Test**: 在 fake adapter 实现 `LeaderElector`：模拟两个 reconciler 实例并起，验证只有 leader 实例触发 Reconcile，follower 静默；leader 失效时 follower 在 LeaseDuration 内接管。

**Acceptance Scenarios**:
1. **Given** 两个 reconciler 实例 + 同一 reconciler ID，**When** 同时 Start，**Then** 只有获得 lease 的实例调用 Reconcile，另一个 `awaitProbe` 等待
2. **Given** leader 实例进程崩溃，**When** LeaseDuration（默认 15s）后，**Then** follower 接管，扫描周期对齐 LeaseDuration（接管后单 leader 稳态；流转瞬间的残余并发由 epoch fencing + 幂等兜底，**不**声称零并发）
3. **Given** leader 释放 lease（graceful shutdown），**When** stop 完成，**Then** follower 即时接管（< 1s），无长期空窗
4. **Given** 旧 leader L1 在 `Reconcile(X)` 中途 STW 暂停 + lease 过期、L2 以 `Epoch+1` 接管并写 X，**When** L1 苏醒后以旧 `Epoch` 重放写 X，**Then** 写路径 CAS 拒绝 stale-epoch 写、设备**不**收到重复命令（fencing real-failure-injection conformance）

---

### User Story 3 - 设备墓碑状态机（Priority: P2，对应 T3）

**Actor**: `devicelifecycle.cronsweep` slice 维护者（winmdm Stage 4, 2027 Q4）

**Plain Language**:
作为 `devicelifecycle` 的维护者，我希望以 reconciler 模式实现 `online → offline → dormant(30d) → recycled(90d) → deleted(180d)` 状态机；每个 tick 扫描 `last_seen < threshold` 的设备，按分档推进状态。

**Why this priority**: P2 因为状态机本身可被 cell 内部 ticker 实现，但 reconcile 框架能提供 leader-elect + observability metric 复用。

**Independent Test**: 注入 4 种 last_seen 偏移的 device fixture，验证一轮扫描后所有 device 状态推进到正确档位；连跑 3 轮验证幂等。

**Acceptance Scenarios**:
1. **Given** device A: `last_seen = now - 31d`，**When** 一轮 reconcile，**Then** 状态推进到 `dormant`
2. **Given** device B: `last_seen = now - 91d`，**When** 一轮 reconcile，**Then** 状态推进到 `recycled`
3. **Given** device C: `last_seen = now - 181d`，**When** 一轮 reconcile，**Then** 状态推进到 `deleted`（软删除标记）

---

### User Story 4 - 信任分周期重评（Priority: P3，对应 T4）

**Actor**: `zerotrust.trustscore` 维护者（zt Phase 5, 2029 Q1）

**Plain Language**:
作为 `zerotrust` 的维护者，我希望声明 reconciler 扫描所有 `last_evaluated_at + ttl < now` 的 session/device 信任分实体；每个调 trustscore 重评算法 → 写入新分 → 触发 `iapgateway` 决策更新（通过 outbox emit event）。

**Why this priority**: zt 在 2029 Q1 才启动，到时 kernel/reconcile 已稳定 ≥1 年；这只是消费方扩展，本身不驱动 v1 设计。

**Independent Test**: 注入 50 个 trust score 实体（不同 ttl），验证按 ttl 到期次序触发 Reconcile，且每个 Reconcile 后 outbox 收到对应 event。

**Acceptance Scenarios**:
1. **Given** 实体 ttl=10s, ttl=20s, ttl=30s，**When** Loop 启动，**Then** 触发顺序与到期次序一致
2. **Given** Reconcile 失败 + 返回 `error`，**When** 退避后重试，**Then** ttl 不重置（基于上次成功评估的 anchor）

---

### Edge Cases

- **Reconcile panic**：框架 recover → 转 transient error → 走退避路径；不让单个 entity 的 panic 影响其他 entity
- **Loop 关闭中 Reconcile 在跑**：StopTimeout 内等待，超时强制 cancel ctx；reconciler 必须响应 ctx.Done()
- **Reconcile 阻塞超长（> 单 tick interval）**：单 entity 串行（不并发同 ID）；多 entity 按 MaxConcurrentReconciles 并发；超长被 ctx deadline 切断
- **leader 流转期的双扫描/双写**：lease lock（Redis SETNX / PG advisory lock）只 best-effort 收窄并发窗口——leader election **非 fencing**（client-go 明示）。正确性由 monotonic-epoch 写路径 CAS（拒 `incoming_epoch < 已见最高` 的 stale 写）+ reconciler 幂等兜底（见 ADR §4.3/§4.4）；Loop 须在 lease 丢失瞬间 cancel lease-scoped ctx 收窄窗口
- **空非终态集合**：scan 返回 0 行 → 跳过本轮，不触发 RequeueAfter（避免无意义自循环）
- **RequeueAfter = 0**：等价于"按 default tick interval"重入，不立即重试
- **死信 entity 复活**：reconciler 内部可重置状态把 PermanentError 实体重新激活，由消费方负责（框架不提供 unmark API）

---

## Requirements *(mandatory)*

### Functional Requirements

**FR-001 (Reconciler 接口)**: 框架 MUST 提供 `kernel/reconcile.Reconciler` 接口，签名为 `Reconcile(ctx context.Context, req Request) (Result, error)`；其中 `Request = struct{ EntityID string }`，`Result = struct{ RequeueAfter time.Duration }`。

**FR-002 (RequeueAfter 语义)**: 框架 MUST 根据 `Result.RequeueAfter` 决定下次入队时机；`> 0` 表示 N 后重入；`= 0` 表示按 default tick interval；error 非 nil 表示走退避（不读 RequeueAfter）。

**FR-003 (PermanentError 语义)**: 框架 MUST 区分 `kernel/reconcile.PermanentError`（不重试，进死信 counter）与 transient error（指数退避重试）。复用现有 `errcode.IsPermanent` 或新增等价 marker。

**FR-004 (Loop 调度骨架)**: 框架 MUST 提供 `Loop` 类型（`NewLoop(opts)` + `Start(ctx) error` + Lifecycle 集成），复用 `runtime/command.SweeperLifecycle` 451 LoC 的 control-plane ticker / start / stop / awaitProbe 实现，仅改名 + 解耦命令实体。

**FR-005 (Trigger 抽象)**: 框架 MUST 提供 `Trigger` 接口（`Start(ctx, chan<- Request) error`，替代 controller-runtime `Source`），最小实现 `TickerTrigger(clk clock.Clock, interval time.Duration)`（发零值 `Request{}` resync 脉冲，节拍走注入 clock——clock 为强制位置参 per `CLOCK-POSITIONAL-INJECTION-01`，原草图 `TickerTrigger(interval)` 与 TDD「注入时钟、不依赖 wall-clock」冲突，A4 落地裁决为注入 clock，详见 ADR §3.2 F4 amendment）；选配 `ChannelTrigger(<-chan Request)` 用于 outbox 事件唤醒。

**FR-006 (LeaderElector 接口)**: 框架 MUST 提供 `LeaderElector` 接口（`AcquireLease(ctx, reconcilerID) (LeaseToken, error)` + `ReleaseLease(ctx, LeaseToken) error` + `RenewLease(ctx, LeaseToken) error`）；adapters/ 层提供 Redis 与 PG advisory lock 两个实现。leader election **非 fencing 保证**（client-go 明示），故：`LeaseToken` MUST 携带**单调 fencing token** `Epoch uint64`（每次换持有者 +1，RenewLease 保持不变）；`Loop` MUST 从 lease 派生 lease-scoped ctx、在 lease 丢失瞬间 cancel 中断 in-flight Reconcile。

**FR-006b (FencedRepository 写路径 CAS)**: 框架 MUST 提供 `FencedRepository`/`FencedWriter` seam——`Loop` 给每次 `Reconcile` 注入 epoch-bound 写句柄，reconciler 唯一写面经此 handle，写路径 CAS 拒绝 `incoming_epoch < 资源已见最高 epoch` 的 stale 写（Kleppmann monotonic fencing，**非** `kernel/outbox` 的 UUID identity-fencing）。绕过在 type system 不可表达（上游 Hard = 唯一写面 + sealed 构造；下游 Hard = `RECONCILE-FENCED-WRITE-FUNNEL-01` callsite）。受 §6 trigger gate 封存（A6 设计，不今天建）。

**FR-007 (并发度控制)**: 框架 MUST 支持 `MaxConcurrentReconciles int` 选项；同一 EntityID 串行（防止重入），不同 EntityID 按上限并发。default = 1。

**FR-008 (Backoff 默认值)**: 框架 MUST 用指数退避：`baseDelay = 5ms`, `maxDelay = 1000s`（对齐 controller-runtime `client-go workqueue.DefaultControllerRateLimiter()`）。

**FR-009 (panic recovery)**: 框架 MUST recover Reconcile 内 panic + 转 transient error + record metric；与现有 `kernel/wrapper.WrapConsumer` panic recovery 形态对齐。

**FR-010 (可观测性)**: 框架 MUST 暴露以下 metric（与 `runtime/command.preflightSweepErrorCounter` 同源 wiring）：
- `reconcile_total{reconciler, result}` counter（result ∈ success/transient/permanent/skipped）
- `reconcile_duration_seconds{reconciler}` histogram
- `reconcile_in_flight{reconciler}` gauge
- `reconcile_leader{reconciler}` gauge（0/1）

**FR-011 (kernel/command 迁移)**: 框架的现有消费方 `kernel/command.Sweeper` MUST 改为实现 `kernel/reconcile.Reconciler`；`runtime/command/lifecycle.go` 表面变薄 adapter；`examples/iotdevice` 同步迁移并跑通端到端。

**FR-012 (archtest 守卫)**: 框架的接口冻结 MUST 走 archtest（接口签名 + Result/Request 字段集 frozen + Loop carve-out 等同 `PROD-CLOCK-INJECTION-01`）；`COMMAND-PROJECTION-EXPLICIT-01` MUST 同步扩 kind 枚举。

**FR-013 (conformance harness)**: 框架 MUST 提供 `reconciletest.ConformanceFactory` 复用 `kernel/command/commandtest.QueueFactory` 形态，让消费方一次跑过所有契约（leader 流转 / RequeueAfter / PermanentError / panic recovery / **fencing：stale-epoch 写被 CAS 拒、无重复命令**，real-failure-injection）。

### Key Entities

- **Reconciler**: 消费方实现的接口；输入 EntityID + ctx；输出 Result + error；状态收敛逻辑由消费方写
- **Request**: 框架传入 reconciler 的最小定位单元；仅包含 EntityID string（不带 NamespacedName）
- **Result**: reconciler 返回给框架的调度提示；仅包含 RequeueAfter time.Duration（不带 Requeue bool 或 Priority）
- **Loop**: 框架的调度环；持有 reconciler + trigger + leader + backoff 配置；生命周期挂在 cell registrar
- **Trigger**: 触发源；最小实现 TickerTrigger；选配 ChannelTrigger
- **LeaderElector**: best-effort 单 leader 选举接口（**非 fencing**，含单调 `Epoch` token）；adapter 层有 Redis / PG advisory lock 实现
- **FencedWriter**: epoch-bound 写句柄；reconciler 唯一写面，写路径 CAS 拒 stale-epoch（跨副本正确性闭环，见 ADR §4.3）
- **PermanentError**: 错误 marker，告诉框架"不要重试，记录到死信 metric"

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

**SC-001 (接口最小性)**: `kernel/reconcile` public API 行数 ≤ 200（接口 + 类型，不含实现）；对比 controller-runtime pkg/reconcile + pkg/builder 总和 ≥ 800 行的简化倍率 ≥ 4x。

**SC-002 (代码复用率)**: ≥ 80% 的调度骨架代码来自 `runtime/command.SweeperLifecycle` 平移（451 LoC 中迁移 ≥ 360 LoC）；新写代码集中在 Reconciler 接口 / Result/Request 类型 / Trigger / LeaderElector。

**SC-003 (消费方迁移成本)**: `kernel/command.Sweeper` 改为实现 `kernel/reconcile.Reconciler` 的 diff ≤ 100 LoC；`examples/iotdevice` 改为新 wiring 的 diff ≤ 150 LoC。

**SC-004 (leader 流转 RTO)**: leader 进程崩溃 + follower 接管 P99 RTO ≤ LeaseDuration + 1s；graceful shutdown 接管 P99 RTO ≤ 1s。

**SC-005 (退避收敛)**: transient error 连续 20 次后退避达到 maxDelay = 1000s 上限；不发生退避抖动或溢出。

**SC-006 (并发隔离)**: 单 EntityID 串行验证（无重入）；MaxConcurrentReconciles=N 下 N+1 个不同 EntityID 触发时，第 N+1 个排队（不丢失）。

**SC-007 (panic 隔离)**: 单 Reconcile panic 不影响其他 entity 的 reconciler 执行；framework continue tick；panic 转 transient error 在 metric 可见。

**SC-008 (archtest 覆盖)**: ≥ 5 条新 archtest 守卫接口与 carve-out（Reconciler 字段集 frozen / Result 字段集 frozen / Loop 控制面 ticker carve-out / LeaderElector 接口 frozen / 消费方 wiring 必经 Builder funnel）。

**SC-009 (文档完整性)**: `.claude/rules/gocell/reconcile.md` 单源文档覆盖：接口契约、Reconciler 实现模式、leader 选举 disposition 语义、死信判定流程、与 saga/projection 边界。

**SC-010 (零向后兼容包袱)**: 迁移后 `runtime/command/lifecycle.go`（连同 `SweepTicker` / `SweeperLifecycle` 命名）MUST 完全删除（不留 alias / 不留 deprecation）。name-frozen **不**靠 standing archtest 守——`RECONCILE-NAMING-FROZEN-01` 为 won't-do（grep-of-deleted-name = Soft，ai-robust「Soft 严禁立项」）；真护栏是类型删除 Hard（任何残留引用即编译错误）+ 既有 `RECONCILE-BUILDER-FUNNEL-01` + 编译期 `var _ reconcile.Reconciler = (*command.Sweeper)(nil)` 断言 + 一次性 merge-gate grep（生产源为空）。

---

## Assumptions

- **A1**：trigger 满足时 GoCell v1.0 已 GA（按 #1051 gate "v1.0 GA + P0 5 项就绪"），kernel 公开 API 进入「v2 升级期」（按 `api-versioning.md`），新 kernel package 引入不需 deprecation 期
- **A2**：`runtime/command.SweeperLifecycle` 在 trigger 满足前不会有不兼容重构（确保迁移 baseline 稳定）；如发生大重构（如 PROD-CLOCK-INJECTION-01 上游 Hard 化），本规格需先 amend 后再激活
- **A3**：`adapters/redis` 与 `adapters/postgres` 已具备 advisory lock / SETNX 原语，可承载 LeaderElector 实现；无需额外引入 etcd / zookeeper
- **A4**：消费方 cell（pkicell / mdmcell / devicelifecycle / zerotrust）按 PRD 时间表落地；如出现取消或 cell 边界调整，trigger 条件需重新评估
- **A5**：`saga` (#969) 与 `projection harness` (#1079) 在 trigger 满足前已 ship，边界明确：saga 解 L3 step orchestration，projection 解 CQRS read side；reconcile 仅承担 L4 desired-state 收敛，三者不重叠
- **A6**：`kernel/reconcile` 不依赖 `runtime/` / `adapters/` / `cells/`（满足 CLAUDE.md 分层约束）；LeaderElector 接口在 kernel 层声明，实现在 adapters 层

---

## Out of Scope

显式排除以下事项，避免泛化过度：

- **业务编排（saga）**：多 step + 补偿逻辑；归 #969
- **CQRS 投影（projection）**：event-driven 读模型构建；归 #1079
- **HTTP webhook**：外部系统主动通知；归 #1066 `kernel/webhook`
- **任务调度（cron）**：定时单次任务；走 outbox + ChannelTrigger 在消费方实现
- **设备配置下发**：归 mdmcell.command（消费方）
- **K8s informer / CRD watch**：GoCell 不依赖 K8s 控制平面
- **Predicate / Source.Informer / Builder.For/Owns/Watches**：controller-runtime 中 K8s 专属抽象
- **优先队列（Result.Priority）**：controller-runtime 新增的 K8s 调度器特性，GoCell 无需求
