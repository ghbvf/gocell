# GoCell 系统工程逐层审查 — runtime/ 层

| 项目 | 值 |
|------|----|
| 审查日期 | 2026-05-04 |
| 仓库 commit | `11600a4f` (develop) |
| 审查范围 | `runtime/`（15 子模块） |
| 审查维度 | ① 边界与接口 / ③ 依赖方向与耦合 / ④ 状态与生命周期 / ⑥ 可观测性 |

## 0. 摘要

runtime/ 层作为 kernel 抽象的具体编排层与外部世界的连接层，工程基础扎实：bootstrap 的 phase0–10 编排清晰、LIFO teardown 严格、与 fx/controller-runtime 对齐；HTTP 维度可观测性闭环完整（含 `cell` label + `_runtime` 哨兵）；shutdown 阶段自带 `bootstrap_shutdown_phase_*` 全套指标；分层方向干净（仅依赖 `kernel/` + `pkg/`，未发现对 cells/adapters 的反向引用）。

主要工程缺口集中在**事件驱动路径的可观测性**：runtime/eventrouter、runtime/eventbus、runtime/outbox/Relay 三处后台路径都**没有**等价于 HTTP 三件套的"register-at-startup + Provider 自动接入"机制——`outbox.RelayCollector` 是 caller 显式注入的字段（bootstrap 不从 `MetricsProvider` 自动 wire），eventrouter 完全没有 metrics 接入点，eventbus 的"buffer full → 丢消息"只走 slog Warn 而无计数。其次是若干次要的 DX/降级语义缺口。

## 1. 评级表

| 维度 | 评级 | 说明 |
|------|------|------|
| ① 边界与接口 | ✅ 已具备 | runtime 仅暴露接口给 cells（`outbox.Publisher/Subscriber`、`cell.RouteMux`、`distlock.Locker`、`auth.IntentTokenVerifier`），无内部类型泄漏；`runtime/crypto` / `runtime/command` / `runtime/outbox` 三个 type-alias 包刻意隔离 SDK 表面，archtest `AUTH-PLAN-04` 守卫 cells 不构造 AuthPlan |
| ③ 依赖方向与耦合 | ✅ 已具备 | `runtime/bootstrap/bootstrap.go:22-38` 仅 import `kernel/*` + `runtime/*` + `pkg/*`；distlock 实现接口、Redis 适配在 adapters；子模块间通过显式 wire（bootstrap 装配）而非交叉 import |
| ④ 状态与生命周期 | ✅ 已具备 | bootstrap 11 phase 编排完备（`bootstrap.go:344-365`）；phase10 四阶段（readiness flip → HTTP drain → LIFO teardown → finalize）显式可见且与 kube-apiserver 对齐；`runOnce`/`managedResourceTeardowns`/`workerErrCh`/`routerErrCh` 全部覆盖；Relay/eventrouter Stop 都带 ctx 预算与 idempotent 语义 |
| ⑥ 可观测性 | ⚠️ 部分具备 | HTTP 三件套 + shutdown 指标完整；但 outbox relay/eventrouter/eventbus 三条后台路径的指标接入点缺失或半接入，`runtime/observability` 缺少 outbox/event 命名空间，Relay collector 不被 bootstrap 自动注入 |

## 2. 问题清单

#### [P1] outbox Relay collector 未被 bootstrap 自动注入到 MetricsProvider
- **维度**：⑥ 可观测性
- **位置**：`runtime/outbox/relay.go:127-152`、`runtime/bootstrap/options_events.go:31-44`
- **复杂度**：Cx2
- **现象**：`NewRelay` 在 `cfg.Metrics == nil` 时回退为 `kout.NoopRelayCollector{}`，而 bootstrap 的 `WithPublisher`/`WithWorkers` 没有任何"自动从 `b.metricsProvider` 构造 RelayCollector 并注入"的对偶机制。HTTP 路径上 `httpCollector` 在 phase5 由 bootstrap 用 `MetricsProvider()` 自动 wire（`bootstrap.go:115-116`），但 Relay 由 caller 在 `cmd/corebundle` 显式构造；任何 bootstrap-only 用例都默默吞掉 `outbox_publish_total / outbox_relay_*` 指标。
- **建议方向**：参考 `shutdownMetrics` 的 register-at-startup 模式，在 bootstrap 增加 `WithRelay(...)` 或在 phase2 自动用 `b.metricsProvider` 构造 RelayCollector 并通过 RelayConfig.Metrics 注入；保留显式 override 通道。

#### [P1] eventrouter 完全没有 metrics 接入点
- **维度**：⑥ 可观测性
- **位置**：`runtime/eventrouter/router.go:78-115`、`runtime/bootstrap/phases_events.go:50-73`
- **复杂度**：Cx2
- **现象**：Router 字段（`router.go:78-94`）只有 `clock / running / healthErr` 等生命周期状态，没有 collector 字段；`buildEventRouter` 也只接 tracing middleware（`phases_events.go:55-58`）。结果是订阅启动 / Ready 等待 / setup 失败 / readyTimeout 超时等关键事件只有 slog 输出，没有 `event_router_subscriptions_active`、`event_router_ready_wait_seconds`、`event_router_setup_errors_total` 等结构化信号。这正是任务 prompt 标注的"事件层指标显式空缺"。
- **建议方向**：在 `eventrouter.Router` 引入一个 `Collector` 接口（与 `kout.RelayCollector` 同形），由 `New` 接收，bootstrap 在 phase6 用 `metricsProvider` 自动注入；至少覆盖 setup_errors / ready_timeouts / active_subscriptions 三个动词。

#### [P1] InMemoryEventBus "buffer full 丢消息" 仅 slog Warn 无计数
- **维度**：⑥ 可观测性
- **位置**：`runtime/eventbus/eventbus.go:179-203`
- **复杂度**：Cx1
- **现象**：`broadcast` / `roundRobin` 的 default 分支（订阅者 channel 满）只打 `slog.Warn(...)`。dev/test 是默认实现，dev 流量陡增或 handler hang 时事件静默丢失，没有 counter 让 dashboard / 告警在故障升级前发现。`DeadLetterLen()` 提供 in-process 检视，但没有 metric。
- **建议方向**：注入 `eventbus_dropped_total{reason=buffer_full|dead_letter}` counter（即使是 InMem 也走 `kernel/observability/metrics.NopProvider` 默认）。

#### [P1] runtime/observability 缺少 outbox / event 命名空间
- **维度**：⑥ 可观测性
- **位置**：`runtime/observability/metrics/collector.go:1-34`、`runtime/observability/metrics/provider_collector.go:42-72`
- **复杂度**：Cx2
- **现象**：`runtime/observability/metrics` 里只有 HTTP 维度的 `Collector` interface 和 `NewProviderCollector`（注册 `http_requests_total` / `http_request_duration_seconds`）。outbox 维度的 Collector 实现散落在 `kernel/outbox`（接口 + ProviderRelayCollector），没有等价的 `runtime/observability/metrics/outbox_collector.go` 把"register-at-startup + 与 HTTP 同 Namespace"模式收口；shutdown 维度自己藏在 `runtime/bootstrap/shutdown_metrics.go` 而非 observability 子包。结果是观察者要"推断"事件指标在哪个层注册。
- **建议方向**：把 shutdown / outbox / event 三套指标的工厂统一搬到 `runtime/observability/metrics/{shutdown,outbox,event}.go`，与 HTTP collector 同包同 Namespace，bootstrap 只调用工厂；让"register-at-startup"成为 runtime 单一惯例。

#### [P2] WithManagedCloser nil 静默忽略与 phase0 fail-fast 风格不一致
- **维度**：④ 状态与生命周期
- **位置**：`runtime/bootstrap/options_lifecycle.go:60-67`、`bootstrap.go:283-310`
- **复杂度**：Cx1
- **现象**：`managedResourceNil` 会在 phase0 fail-fast，但 `WithManagedCloser(nil)` 在 option 里直接 return（comment 说"consistent with addCloser semantics"）。两条相邻 API 一个 fail-fast 一个静默接受，对调用方一致性体验有损；CLAUDE.md 的"不静默降级"原则也偏向显式拒绝。
- **建议方向**：要么两者都 fail-fast、要么都 nil-tolerate；在 option 里记录 `managedResourceNil = true` 让 phase0 拒绝，对齐已有 ManagedResource 的处理。

#### [P2] runtime/eventbus dropped message warn 缺关键关联字段
- **维度**：⑥ 可观测性
- **位置**：`runtime/eventbus/eventbus.go:184-186`、`eventbus.go:198-202`
- **复杂度**：Cx1
- **现象**：`broadcast` 的 drop warn 不带 consumerGroup / entry_id / aggregate_id；`roundRobin` 也只带 group。`.claude/rules/gocell/observability.md` 要求"错误日志必须包含结构化关联字段"——丢消息属于"影响正确性"应该是 Error 级别且带 entry id 用于追溯。
- **建议方向**：升级为 `slog.Error`（或保留 Warn 但带 `entry_id` / `aggregate_id` / `event_type`），便于事故 timeline 重建。

## 3. 跨层观察

- **runtime ↔ kernel**：runtime 严格只消费 kernel 抽象，三个 type-alias 包（`runtime/crypto`、`runtime/command`、`runtime/outbox`）刻意把 kernel SDK 表面收窄到 runtime 单点暴露——这是层间契约的优雅做法，建议持续守住。
- **runtime ↔ adapters**：依赖方向干净（`runtime/distlock` 定义 `Locker` 接口，由 `adapters/redis` 实现）。但 bootstrap 对 `MetricsProvider` 的 default 是 `NopProvider`（`bootstrap.go:281`）——这意味着任何启用 Provider 的责任都压在 caller，runtime 自身的事件/outbox 路径却没有 register-at-startup 自动接入。HTTP 路径已经做了示范（`bootstrap.go:115-116` 的 `httpCollector` 自动 wire），事件路径需要补齐对偶。
- **runtime ↔ cells**：cells 通过 `cell.Registry`/`cell.RouteMux`/`outbox.Publisher` 等纯接口与 runtime 交互；archtest LAYER-07/AUTH-PLAN-04 守卫 cells 不直接 import `runtime/http/router` 与 AuthPlan 构造，约束闭环。
- **跨层失衡**：HTTP 维度的可观测性（含 `cell` label + `_runtime` 哨兵 + archtest 守卫）做到了"代码即契约"；事件维度只有 kernel 接口存在但 runtime 没有把它编排进 bootstrap 默认链路——这是 P1 集合的根因。

## 4. 一句话结论

runtime/ 层的生命周期编排、分层边界和 HTTP 可观测性已是范本级，但事件驱动路径（outbox relay / eventrouter / eventbus）的指标接入点系统性缺位，需要按 HTTP 路径的"register-at-startup + Provider 自动 wire"模式补齐对偶，方能让事件层故障可观测、可告警。
