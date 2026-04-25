# GoCell PromQL Alerting Rules

> 推荐告警规则示例。可直接 copy 到 Prometheus rule files 或 PrometheusRule CRD。
>
> **命名约定**：所有指标 fqName 形如 `gocell_<bare>`，由 Prometheus provider 在
> `cmd/corebundle/metrics.go` 注入 `Namespace="gocell"`（单个指标 `Name` 字段保留裸语义，
> 不含前缀）。双前缀形式 `gocell_gocell_...` 是错误配置，由 PR-CFG-A round-3 修复。
>
> ref: prometheus-operator/kube-prometheus `manifests/prometheus-rules.yaml`;
> cortexproject/cortex `docs/operations/alerts.md`;
> HashiCorp Consul `docs/agent/telemetry.mdx`

---

## Outbox 可观测性

### OutboxEmitFailOpenDropped

fail-open 模式下持续丢事件。observability 类事件可以容忍短时丢失；安全/审计类事件应通过
entry-level `FailurePolicyFailClosed` 覆盖，不依赖此告警。

```yaml
- alert: GoCellOutboxEmitFailOpenDropped
  expr: sum(rate(gocell_outbox_emit_failopen_dropped_total[5m])) by (cell, topic) > 0.1
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Outbox fail-open dropping events ({{ $labels.cell }}/{{ $labels.topic }})"
    description: |
      Cell {{ $labels.cell }} is dropping outbox entries on topic {{ $labels.topic }}
      via DirectPublishFailOpen mode at >0.1 events/sec for 10m.
      Likely causes: broker unreachable, publisher misconfigured, or routing topic not bound.
      Check cell logs for "outbox: direct publish failed (fail-open)".
```

### OutboxEmitFailOpenSpike

短窗高峰（broker 短暂抖动），比持续告警更敏感，用于快速发现 broker 重启或网络抖动。

```yaml
- alert: GoCellOutboxEmitFailOpenSpike
  expr: sum(increase(gocell_outbox_emit_failopen_dropped_total[1m])) by (cell) > 100
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "Outbox fail-open spike ({{ $labels.cell }})"
    description: |
      Cell {{ $labels.cell }} dropped >100 outbox entries in 1m.
      This typically indicates broker connectivity loss or topic misconfiguration.
      Verify broker health and check cell startup logs for publisher initialization errors.
```

---

## Bootstrap Shutdown 可观测性

指标 `gocell_bootstrap_shutdown_total` 带 `outcome` 标签，取值见下表：

| outcome | 含义 |
|---|---|
| `clean` | 所有 ManagedResource teardown 成功，无超时 |
| `teardown_error` | 至少一个 teardown 返回非 nil 错误 |
| `timeout` | shutCtx 超时，强制结束 LIFO teardown 循环 |
| `signal_error` | shutdown 由组件失败触发（HTTP listener 崩溃 / worker 退出）而非用户 SIGTERM |

### BootstrapShutdownTeardownError

进程退出时至少一个 ManagedResource teardown 返回错误，数据或外部连接未必清理干净。

```yaml
- alert: GoCellBootstrapShutdownTeardownError
  expr: increase(gocell_bootstrap_shutdown_total{outcome="teardown_error"}[1h]) > 0
  for: 1m
  labels:
    severity: warning
  annotations:
    summary: "Cell shutdown teardown errored"
    description: |
      At least one ManagedResource teardown returned an error during shutdown.
      Resources (DB connections, brokers, open files) may not have been cleaned up.
      Check logs around process exit for "teardown error" entries.
```

### BootstrapShutdownTimeout

shutCtx 超时（强制关停），未必按 LIFO 顺序完成所有资源释放。通常意味着某资源的
`Teardown` 实现阻塞超过了配置的 shutdown timeout。

```yaml
- alert: GoCellBootstrapShutdownTimeout
  expr: increase(gocell_bootstrap_shutdown_total{outcome="timeout"}[1h]) > 0
  for: 1m
  labels:
    severity: warning
  annotations:
    summary: "Cell shutdown timed out"
    description: |
      Bootstrap shutdown exceeded configured timeout. LIFO teardown was aborted.
      Identify which ManagedResource blocked by searching logs for "shutdown timeout"
      and the last "teardown: starting" entry before the timeout.
```

### BootstrapShutdownSignalError

shutdown 由组件失败触发（HTTP listener 崩溃 / worker panic 退出），而非正常 SIGTERM。
这通常代表运行时异常，需立即排查根因。

```yaml
- alert: GoCellBootstrapShutdownSignalError
  expr: increase(gocell_bootstrap_shutdown_total{outcome="signal_error"}[1h]) > 0
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: "Cell shutdown triggered by component failure"
    description: |
      Bootstrap received a shutdown signal from a failing component (not user/SIGTERM).
      Typical causes: HTTP listener bind failure, worker goroutine panic, or
      health-check-triggered self-termination.
      Check logs for component-level errors immediately preceding shutdown.
```

---

## Phase 滞后告警

### BootstrapShutdownPhaseStuck

某 shutdown phase 进入但 LIFO teardown 长时间没有完成（典型卡死场景：外部连接等待超时，
依赖服务未就绪）。

```yaml
- alert: GoCellBootstrapShutdownPhaseStuck
  expr: >
    histogram_quantile(
      0.99,
      sum(rate(gocell_bootstrap_shutdown_phase_duration_seconds_bucket{phase="lifo_teardown"}[10m]))
        by (le)
    ) > 30
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Bootstrap shutdown lifo_teardown p99 > 30s"
    description: |
      The p99 duration of bootstrap lifo_teardown phase exceeds 30s over the last 10m.
      This suggests one or more ManagedResources are blocking teardown.
      Cross-reference with GoCellBootstrapShutdownTimeout for co-occurring timeouts.
```

---

## 调试 / 仪表板查询

以下 PromQL 片段可直接 paste 到 Grafana Explore 或 Dashboard panel。

### 每 cell 每 topic 的 fail-open drop rate

```promql
sum(rate(gocell_outbox_emit_failopen_dropped_total[5m])) by (cell, topic)
```

### shutdown phase p50/p95/p99（按 phase 分组）

```promql
histogram_quantile(
  0.95,
  sum(rate(gocell_bootstrap_shutdown_phase_duration_seconds_bucket[10m])) by (le, phase)
)
```

### 最近 1h shutdown outcome 分布（单实例）

```promql
sum(increase(gocell_bootstrap_shutdown_total[1h])) by (outcome)
```

### outbox relay 延迟 p99（relay collector）

```promql
histogram_quantile(
  0.99,
  sum(rate(gocell_outbox_relay_duration_seconds_bucket[5m])) by (le, cell)
)
```

---

## 注意事项

1. **fqName 单前缀**：所有规则中的指标名已包含 `gocell_` 前缀。若部署时 Prometheus
   provider 的 `Namespace` 配置不是 `"gocell"`，或 metric `Name` 字段已包含 `gocell_`
   前缀，将产生双前缀（`gocell_gocell_...`）——规则需同步修改。PR-CFG-A round-3 已修复
   `kernel/outbox/emitter.go`、`runtime/bootstrap/shutdown_metrics.go`、
   `kernel/assembly/hook_dispatcher.go` 三处裸名违规。

2. **PrometheusRule CRD**：在 Kubernetes 上使用 prometheus-operator 时，上述 YAML 块放在
   `PrometheusRule.spec.groups[].rules` 下，添加合适的 `namespace` 和 `labels.release`
   以匹配 Alertmanager 路由。

3. **告警路由建议**：`severity: warning` → PagerDuty low-urgency / Slack；
   `severity: critical` → PagerDuty high-urgency / on-call。
