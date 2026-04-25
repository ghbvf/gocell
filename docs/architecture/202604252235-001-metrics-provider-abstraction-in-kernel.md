# ADR-001: Metrics Provider Abstraction Lives in kernel/observability/metrics

- **Status**: Accepted
- **Date**: 2026-04-25
- **Deciders**: PR-CFG-A 实施
- **Context PR**: PR#268 (refactor/527-pr-cfg-a-lifecycle-aggregate)

---

## Context

GoCell 分层依赖规则（CLAUDE.md "依赖规则"）规定：

- `kernel/` 不依赖 `runtime/`、`adapters/`、`cells/`（只依赖标准库 + `pkg/` + `gopkg.in/yaml.v3`）
- `runtime/` 可依赖 `kernel/`
- `cells/` 依赖 `kernel/` + `runtime/`
- `adapters/` 实现 `kernel/` 或 `runtime/` 定义的接口

Metrics 抽象（`Provider / CounterVec / HistogramVec / Counter / Histogram`）当前位于
`kernel/observability/metrics/`，被以下模块共用：

| 消费方 | 用途 |
|---|---|
| `kernel/outbox.NewProviderRelayCollector` | 注册 outbox relay 延迟直方图 + 计数器 |
| `kernel/outbox.NewDirectEmitter` | 注册 fail-open dropped counter（PR-CFG-A 引入） |
| `kernel/assembly.HookDispatcher` | 注册 cell hook observability dropped counter |
| `runtime/observability/metrics.NewProviderCollector` | 注册 HTTP 路由指标 |
| `runtime/bootstrap.shutdownMetrics` | 注册 shutdown phase duration 直方图 + outcome 计数器 |
| `adapters/prometheus.MetricProvider` | 实现 `metrics.Provider` 接口（生产后端） |
| `kernel/observability/metrics.NopProvider` | 实现 `metrics.Provider` 接口（测试用 nop） |

在 PR-CFG-A 之前，`kernel/outbox` 和 `kernel/assembly` 内部已经在使用 `metrics.Provider`。
如果将接口移出 kernel/，上述 kernel-internal 消费方的 import 路径必须反向指向更高层，
违背分层规则。

---

## Decision

`Provider` 接口（声明 + 默认 `NopProvider` 实现）放在 `kernel/observability/metrics/`。

`adapters/prometheus.MetricProvider` 在外侧实现该接口；`cmd/corebundle/metrics.go` 在
组装层注入 `Namespace="gocell"` 并将具体 Provider 传给所有 kernel/runtime 模块。

---

## Rationale

### 1. kernel/outbox 需要观测点

`outbox.NewProviderRelayCollector` 和 `outbox.NewDirectEmitter` 都是 `kernel/outbox`
包内函数，在 outbox 初始化时直接消费 `metrics.Provider` 注册自家计数器。若 metrics 抽象
放在 `runtime/`，则 `kernel → runtime` 产生反向依赖，直接破坏分层规则。

### 2. kernel/assembly 也需要观测点

`HookDispatcher`（`kernel/assembly/hook_dispatcher.go`）在 kernel 层注册 hook observer
dropped counter，同样是 kernel-internal 消费。

### 3. 接口最小，不耦合任何后端

`Provider` 仅声明 `CounterVec(opts) CounterVec`、`HistogramVec(opts) HistogramVec`、
`Unregister(c Collector) bool` 三个方法，不引用 `prometheus/*` 或 `go.opentelemetry.io/*`
任何类型。kernel 层声明纯抽象 + NopProvider 默认实现，符合"kernel 是底座灵魂"角色定位。

### 4. adapter 在外侧实现，依赖方向正确

`adapters/prometheus/metric_provider.go` 反向 import `kernel/observability/metrics` 并
实现接口（依赖方向：`adapters → kernel`）。生产注入路径：

```
cmd/corebundle/metrics.go
  → adapters/prometheus.NewMetricProvider(Namespace="gocell")
    → kernel/outbox, kernel/assembly, runtime/bootstrap (通过接口参数注入)
```

测试注入路径：

```
*_test.go
  → kernel/observability/metrics.NopProvider{}
    → 被测 kernel/runtime 模块（无 prometheus 依赖）
```

---

## Alternatives Considered

### Alternative A: 把 Provider 接口放 runtime/observability/metrics

- **问题**：`kernel/outbox` 和 `kernel/assembly` 必须 import `runtime/`，反向依赖，
  直接违反 CLAUDE.md 分层规则。
- **否决**。

### Alternative B: 把 Provider 接口放 pkg/

- **问题**：`pkg/` 定位是共享工具包（errcode / ctxkeys / httputil / query），承载
  observability 抽象接口与其定位不符，且 pkg/ 语义上不应感知 Cell 运行时的度量关切。
- **否决**。

### Alternative C: 每个 kernel 子包内联自己的 metrics 接口

- **问题**：`outbox`、`assembly`、`runtime/bootstrap` 各自重复声明形状相同的
  `CounterVec / HistogramVec`；`adapters/prometheus` 需针对 N 套接口重复适配；
  测试用 Nop 实现需重复维护。
- **否决**。

---

## Consequences

### Positive

- `kernel/runtime/cells` 任意层都能用同一个 `metrics.Provider` 接口注册指标，无 import 循环。
- `adapters/prometheus` 单点适配，通过 `MetricProviderConfig.Namespace` 统一注入
  `"gocell"` 前缀，确保所有指标 fqName 形如 `gocell_<bare>`。
- 测试用 `metrics.NopProvider{}` 一站式短路所有 metric 注册，无需启动 Prometheus registry。

### Negative

- `kernel/observability/metrics` 必须保持纯抽象——任何 `prometheus.*` / `otel.*` 类型泄漏
  立即破坏分层规则，需在 CI 中通过 `go build` 依赖图审查保护。
- 命名职责需要明确：`Name` 字段是裸语义（如 `outbox_relayed_total`），`Namespace` 在
  Provider 配置层注入。个别 collector 若将 `Namespace` 写进 `Name` 字段会导致双前缀
  `gocell_gocell_...`。PR-CFG-A round-3 已修复以下三处违规：
  - `kernel/outbox/emitter.go`（`gocell_outbox_emit_failopen_dropped_total` Name 字段）
  - `runtime/bootstrap/shutdown_metrics.go`（`gocell_bootstrap_shutdown_*` Name 字段）
  - `kernel/assembly/hook_dispatcher.go`（`gocell_assembly_hook_*` Name 字段）

---

## References

- `prometheus/client_golang` `prometheus/metric.go::BuildFQName` — Namespace/Subsystem/Name
  三段拼接语义；Name 字段应为裸语义，Namespace 由调用方注入
- Kubernetes `staging/src/k8s.io/component-base/metrics/opts.go` — Name 字段保留裸语义，
  Namespace 在 wrapper 层注入，与本 ADR 决策一致
- Watermill `components/metrics/builder.go` — Builder 统一注入 Namespace + Subsystem，
  单指标 Name 裸名；interface-driven 设计使后端可替换
- Uber fx — 接口驱动 lifecycle，adapter 反向实现；本 ADR 的 kernel/adapter 分层模式参照此思路
- `docs/ops/alerting-rules.md` — 基于本 ADR 命名约定编写的告警规则示例
