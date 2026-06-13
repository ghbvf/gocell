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

## HTTP Metrics `cell` Label 迁移 / Runbook

HTTP 指标 `gocell_http_requests_total` 与 `gocell_http_request_duration_seconds` 的
`cell` label 现在表示请求归属的粗粒度 cell owner，由 router root attribution 从
`RouteGroup.CellID` / HTTP namespace 写入。collector 不再从 assembly/config/default
推导 cell，也不做旧 label / 新 label 的 double-write。

`cell="_runtime"` 是运行时哨兵，不代表业务 cell 或错误状态。它覆盖没有落入任何
cell-owned RouteGroup 的请求，例如 `/healthz`、`/readyz`、`/metrics`、listener 外 404、
framework isolation 404，以及未归属 listener 的流量。业务 RouteGroup namespace 内的
auth / rate-limit / circuit-breaker / body-limit 前置拒绝和 chi 405 应继续归属对应业务
cell。

推荐过滤：

- 业务 SLO / 业务错误率 / cell dashboard：`{cell!="_runtime"}`
- 运行时探针、scrape 自身、未匹配流量排查：`{cell="_runtime"}`
- listener 总流量 / 容量评估：不过滤 `cell`

Prometheus 侧过渡建议：

```yaml
groups:
- name: gocell-http-metrics-cell-label
  rules:
  - record: gocell:http_requests:rate5m_by_cell_route_status
    expr: |
      sum by (cell, method, route, status) (
        rate(gocell_http_requests_total{cell!="_runtime"}[5m])
      )
  - record: gocell:http_request_duration:p95_by_cell_route
    expr: |
      histogram_quantile(
        0.95,
        sum by (le, cell, route) (
          rate(gocell_http_request_duration_seconds_bucket{cell!="_runtime"}[5m])
        )
      )
```

remote-write 或业务专用 Prometheus 若不需要 runtime series，可在 scrape 侧丢弃
`_runtime`，避免在代码里恢复兼容写入：

```yaml
metric_relabel_configs:
- source_labels: [__name__, cell]
  regex: 'gocell_http_(requests_total|request_duration_seconds(_bucket|_sum|_count)?|request_body_limit_rejections_total);_runtime'
  # Note: request_body_limit_rejections_total is a counter — it has no _bucket/_sum/_count
  # suffixes (those belong to histograms/summaries only). The regex matches the bare metric name.
  action: drop
```

## gRPC Metrics `cell` Label

gRPC 指标 `gocell_grpc_server_requests_total` 与
`gocell_grpc_server_request_duration_seconds` 的 `cell` label 与 HTTP 共享同一
`_runtime` 哨兵语义与单源（`runtime/observability/metrics.RuntimeCellSentinel`）。

gRPC cell attribution 已接线（#1383 / #1152）：`UnaryCellAttribution` 拦截器按
`FullMethod → cellID`（生成式 registrar `CellIDForMethod` 派生）写入 `ctxkeys.CellID`，
`UnaryMetrics` 经 sealed `metrics.ResolveCellLabel(ctx, validCellIDs)` 读取并对
assembly closed set 校验。行为与 HTTP 侧 `CellAttribution` 中间件对称。

运维影响：

- 业务 SLO / 告警对 gRPC 指标**可以**使用 `{cell!="_runtime"}` 过滤（与 HTTP 一致）。
  `_runtime` 仅出现在未归属流量：未注册方法（如原生 grpc-health 服务）、attributed
  cellID 不在 assembly closed set、或 ctx 无 cellID。
- reader 侧契约（cell label 经 `ResolveCellLabel` + sealed `CellLabel` 解析，越界/缺失
  降级 `_runtime`）由 archtest `GRPC-METRICS-LABEL-CELLID-CTXSOURCE-01` 守卫。
- gRPC access log 行 `"grpc request"` 的状态字段名为 `code`（string gRPC status，如
  `"OK"`/`"NOT_FOUND"`），区别于 HTTP access log `"http request"` 的 `status`（int，如
  `200`/`404`）——协议语义差异，跨协议日志查询时注意区分。

## HTTP Body-Limit 拒绝计数器

`gocell_http_request_body_limit_rejections_total{cell, route}` 记录 BodyLimit 中间件
因 Content-Length 超限（fast-path）返回 413 的次数。流式超限（MaxBytesReader 在读
body 时触发）依赖 handler 将 `*http.MaxBytesError` 通过 `pkg/httputil.WriteError`
映射为 413：框架生成的 handler 走此路径，其产生的 413 会计入
`gocell_http_requests_total{status="413"}`；自定义 handler 若不调用 `httputil.WriteError`
则不会产生 413 计数。两路 413 计数来源不同，不重复。

Label 语义：

| label | 含义 |
|---|---|
| `cell` | 同 `http_requests_total.cell`：路由归属 cell ID，或 `_runtime`（框架路径） |
| `route` | 低基数路由模板（同 RouteFor 返回值）。当请求路径与已注册路由匹配时，RouteResolver 返回路由模板（如 `/api/v1/upload/blob`）；仅当路径无法匹配任何已注册路由时才回退到 `"unmatched"`。fast-path 期间 ServeMux 尚未写入 pattern recorder，故由 RouteResolver（ctx 中由 CellAttribution 注入）负责模板解析。 |

推荐告警（短时突增，提示 client 配置错误或资源滥用）：

```yaml
- alert: GoCellHTTPBodyLimitRejectionSpike
  expr: |
    sum(rate(gocell_http_request_body_limit_rejections_total{cell!="_runtime"}[5m])) by (cell, route) > 1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "HTTP body-limit rejections spiking ({{ $labels.cell }}/{{ $labels.route }})"
    description: |
      Cell {{ $labels.cell }} route {{ $labels.route }} is rejecting requests
      with Content-Length > body limit at >1/sec for 5m.
      Possible causes: client sending oversized payloads, misconfigured body limit,
      or DDoS amplification attempt.
      排查：通过结构化日志字段定位拒绝请求（注意 4xx 日志默认每 100 条采样一条）：
        code=ERR_BODY_TOO_LARGE status=413 cell_id=<cell> route=<route>
      其中 cell_id 字段对应 kernel/ctxkeys.CellIDFrom（access log 记为 cell_id，
      非 cell），request_id 字段可关联同一请求的跨日志记录。
      同步检查 BodyLimit 配置（WithBodyLimit 参数）与客户端 Content-Length 分布。
```

调试 PromQL：

```promql
# 按 cell / route 分组的 body-limit 拒绝速率
sum(rate(gocell_http_request_body_limit_rejections_total[5m])) by (cell, route)
```

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

## MQTT Dead-Letter Sink 可观测性

`mqtt_dlx_failed_total{cell, reason}` 是 MQTT adapter 死信路径**装配真实 collector 后**的唯一运维恢复钩子（未装配时不发射,见下方 ⚠ 前提）。
当一条 reject/poison 消息路由到 `$dead/<topic>` 失败（topic unmintable / broker publish
error / broker PUBACK reason ≥ 0x80）时，adapter 按 fail-closed 取舍 ack-as-poison 丢弃该
消息（见 ADR-048 §3 line-193 + §Amendment 2026-06-02）。MQTT 传输层结构上无法保证
no-loss（leave-unacked 会复活 Option C 的 HoL stall，ADR-050 §1），因此 `$dead` 失败时
消息**确实丢失**——`mqtt_dlx_failed_total > 0` 是运维必须介入的信号，**不是**可容忍的
降级。RTO 内未恢复死信管道（broker / ACL / topic 配置）即意味着永久消息丢失。

> 该信号"每个 drop 路径必记"的 no-silent-exit 轴由 **sealed-construction archtest funnel**
> `MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01` 守（H1 签名 floor + H2 禁 mqtt 伪造[字面量/零值/new] +
> H4 dlxoutcome 唯一生产者 + H3a 构造器必记），配类型系统 **floor**——`routeDeadLetter` 必返回封印
> `dlxoutcome.Outcome`，但 Outcome 是无 state 的纯 token，零值可伪造（`Outcome{}`/`*new(...)`），
> **漏记 metric 的退出仍能编译**，由 H2/H4 archtest 拦截而非编译器。**评级 Medium（archtest），非类型系统 Hard**
> ——stateless token 的字面 Hard 在 Go 不可达（见 ADR-048 §Amendment 2026-06-11 / #1440 / #1873 F1）。
> drop→failure 语义匹配（review F1）由 H3b 聚合计数 + 行为测试守。funnel + floor 均不会被代码改动静默移除。
> 真正的 no-loss 保证应由消费 cell 在本地事务捕获 poison 消息
> 实现（重定位，deferred — 见 ADR-048 §Amendment 2026-06-02）。
>
> ⚠ **前提:信号仅在 wire 了 provider-backed `SubscriberCollector` 后才发射。**
> `mqtt.NewSubscriber` 默认 `NoopSubscriberCollector{}`（不发射任何指标），且当前 develop
> **无生产 MQTT subscriber 装配**（adapters/mqtt 仅由测试构造）。因此这两条告警在 develop
> 上不会触发,直到第一个 MQTT-consuming cell 经 `WithSubscriberCollector(...)` 注入真实
> collector 并部署——届时随该 cell 落 wiring guard,见 backlog #1435。在那之前,death-letter
> 失败可观测性 = 0,本节是装配后的规则模板,不是 develop 现状的活跃保护。

### MQTTDeadLetterSinkUnhealthy

持续 > 0：死信管道不健康，消息正在丢失。

```yaml
- alert: GoCellMQTTDeadLetterSinkUnhealthy
  expr: sum(rate(gocell_mqtt_dlx_failed_total[5m])) by (cell, reason) > 0
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "MQTT dead-letter sink unhealthy ({{ $labels.cell }}/{{ $labels.reason }})"
    description: |
      Cell {{ $labels.cell }} failed to route reject/poison messages to $dead/<topic>
      (reason {{ $labels.reason }}) for 2m — these messages are DROPPED (fail-closed,
      ack-as-poison). MQTT transport cannot guarantee no-loss for the $dead path.
      Likely causes: broker unreachable, $dead topic ACL denial (PUBACK 0x87), or
      topic unmintable. Restore the dead-letter sink within RTO to stop message loss.
      Check cell logs for "mqtt: dead-letter publish failed" (broker publish error)
      and "mqtt: cannot mint $dead topic" (topic unmintable) — both increment this metric.
```

### MQTTDeadLetterSinkSpike

短窗高峰（broker 抖动 / ACL 误配），比持续告警更敏感。

```yaml
- alert: GoCellMQTTDeadLetterSinkSpike
  expr: sum(increase(gocell_mqtt_dlx_failed_total[1m])) by (cell) > 50
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "MQTT dead-letter sink failure spike ({{ $labels.cell }})"
    description: |
      Cell {{ $labels.cell }} dropped >50 reject/poison messages from $dead capture in 1m.
      Indicates broker connectivity loss or $dead topic misconfiguration; messages lost.
      Verify broker health and $dead/# publish authorization.
```

---

## Config Event Consumer 可观测性

config event consumer 拆成两条生命周期边界不同的指标：

- `gocell_config_event_process_total{cell,slice,reason}` 记录 handler 内已知的处理原因。
- `gocell_config_event_settlement_total{cell,slice,disposition,result}` 记录 broker / subscriber 最终处置。

当前仅覆盖 `accesscore/configreceive` 与 `configcore/configsubscribe`。

| process reason | 含义 |
|---|---|
| `ack` | handler 接受事件并完成业务处理 |
| `stale` | 事件已过期或 replay，安全跳过 |
| `permanent_error` | payload / schema / 语义错误，不应重试 |
| `no_tenant` | （#1577）`accesscore/configreceive` refetch 时事件 envelope 无 tenant —— tenant-correct 不变式违反（envelope/restore 管线错误），fail-closed Reject 进 DLX；正常生产流量应为 0 |
| `transient` | refetch 瞬态失败（configcore internal GET 不可达 / 超时 / 5xx），ConsumerBase 退避重试；持续增长表示下游故障 |

| settlement disposition/result | 含义 |
|---|---|
| `ack/success` | delivery 最终 ack 成功 |
| `reject/success` | handler 永久错误或显式 reject 后进入 DLX |
| `reject/retry_exhausted` | 重试耗尽后 reject，进入 DLX |
| `requeue/success` | delivery 最终 requeue，将被重投 |
| `requeue/commit_failed` | handler 原本 ack，但 receipt commit 失败，已降级 requeue |
| `ack/ack_failed` 或 `*/nack_failed` | broker settlement 调用失败，需要检查 broker/channel 状态 |

### ConfigEventConsumerReject

handler 永久错误（payload / schema / 语义问题）或显式 DispositionReject 后进入 DLX。
`result="success"` 桶仅命中 PermanentError 或 handler 主动 Reject 路径，与重试耗尽桶互斥。

```yaml
- alert: GoCellConfigEventConsumerReject
  expr: sum(increase(gocell_config_event_settlement_total{disposition="reject",result="success"}[10m])) by (cell, slice) > 0
  for: 0m
  labels:
    severity: warning
  annotations:
    summary: "Config event consumer permanent reject ({{ $labels.cell }}/{{ $labels.slice }})"
    description: |
      Config event consumer {{ $labels.cell }}/{{ $labels.slice }} is routing
      handler-explicit errors to DLX (result=success means the handler returned
      DispositionReject, not retry exhaustion — log level Error).
      Check consumer logs for "handler rejected entry, routing to DLX" and verify
      producer/contract drift.
```

### ConfigEventConsumerRetryExhausted

ConsumerBase 重试预算耗尽后进入 DLX。通常意味着下游依赖持续故障，重试已经无法在本次投递内恢复。

```yaml
- alert: GoCellConfigEventConsumerRetryExhausted
  expr: sum(increase(gocell_config_event_settlement_total{disposition="reject",result="retry_exhausted"}[10m])) by (cell, slice) > 0
  for: 0m
  labels:
    severity: warning
  annotations:
    summary: "Config event consumer retry budget exhausted ({{ $labels.cell }}/{{ $labels.slice }})"
    description: |
      Config event consumer {{ $labels.cell }}/{{ $labels.slice }} exhausted
      ConsumerBase retry budget; downstream dependency likely failing.
      Check consumer logs for "retry budget exhausted" and verify configcore
      internal GET readiness plus the downstream dependency named by the handler error.
```

### ConfigEventConsumerPermanentError

payload/schema 永久错误不应在正常生产流量中增长；任意持续增长通常表示 producer/contract
漂移，或旧版本事件在重放。

```yaml
- alert: GoCellConfigEventConsumerPermanentError
  expr: sum(increase(gocell_config_event_process_total{reason="permanent_error"}[10m])) by (cell, slice) > 0
  for: 0m
  labels:
    severity: warning
  annotations:
    summary: "Config event permanent errors ({{ $labels.cell }}/{{ $labels.slice }})"
    description: |
      Config event consumer {{ $labels.cell }}/{{ $labels.slice }} is routing
      permanent payload/schema errors to DLX. Compare event payloads with
      contracts/event/config/* schemas and recent producer deploys.
```

### ConfigEventConsumerNoTenant（#1577）

`accesscore/configreceive` 在 refetch 时从事件 envelope 取不到 tenant —— 违反 #1577
建立的「真实 tenant 必达内部 config 读」不变式（envelope/restore 管线错误），fail-closed
Reject 进 DLX。正常生产流量恒为 0；任意增长即 producer 未带 tenant principal / consumer
ctx restore 回归 / 旧版本（pre-#1577）事件重放，须立即排查。

```yaml
- alert: GoCellConfigEventConsumerNoTenant
  expr: sum(increase(gocell_config_event_process_total{reason="no_tenant"}[10m])) by (cell, slice) > 0
  for: 0m
  labels:
    severity: critical
  annotations:
    summary: "Config event refetch missing tenant ({{ $labels.cell }}/{{ $labels.slice }})"
    description: |
      Config event consumer {{ $labels.cell }}/{{ $labels.slice }} received an
      event without a tenant in context and Rejected it to DLX (#1577 tenant-correct
      invariant violation — should be 0 in production). Check the producer's
      principal envelope (outbox tenant) and the consumer ctx RestoreContext path;
      DLX-replayed pre-#1577 events also surface here.
```

### ConfigEventConsumerTransient

refetch 瞬态失败（configcore internal GET 不可达 / 超时 / 5xx），由 ConsumerBase 退避重试。
单次抖动可接受；持续增长指向 configcore internal listener / service-token / configreceive
refetch 链路的下游故障。

```yaml
- alert: GoCellConfigEventConsumerTransient
  expr: sum(rate(gocell_config_event_process_total{reason="transient"}[5m])) by (cell, slice) > 0.1
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Config event refetch transient failures ({{ $labels.cell }}/{{ $labels.slice }})"
    description: |
      Config event consumer {{ $labels.cell }}/{{ $labels.slice }} is retrying
      refetch on transient errors. Check configcore internal listener / readiness,
      service-token verification, and the configreceive → http.config.internal.get.v1 path.
```

---

## Bootstrap Shutdown 可观测性

指标 `gocell_bootstrap_shutdown_total` 带 `outcome` 标签，取值见下表：

| outcome | 含义 |
|---|---|
| `success` | 所有 ManagedResource teardown 成功，无超时 |
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

## Auth 服务令牌防重放

`/internal/v1/*` 的 service token 中间件以 `gocell_auth_service_token_verify_total{result, reason}`
暴露每个请求的鉴权结果。以下规则按 `reason` 标签拆出三类独立告警，便于 SOC 与 SRE 分别接管。

| reason | 语义 | HTTP | 对应响应 errcode |
|---|---|---|---|
| `replay` | nonce 已被消费（防重放命中） | 401 | `ERR_AUTH_REPLAY_DETECTED` |
| `nonce_store_full` | InMemoryNonceStore 容量满，无可回收 entry | 503 | `ERR_SERVICE_UNAVAILABLE` |
| `nonce_store_error` | nonce store 后端故障（Redis 不可达等） | 500 | `ERR_INTERNAL` |
| `missing` / `invalid_mac` / `expired` / `legacy_format` / `invalid_format` | 鉴权语法/凭据错误 | 401 | `ERR_AUTH_UNAUTHORIZED` |
| `missing_caller_cell` | 4-part token 中 callerCell 段为空 | 401 | `ERR_AUTH_UNAUTHORIZED` |
| `invalid_caller_cell` | callerCell 不符合 `^[a-z][a-z0-9-]*$` | 401 | `ERR_AUTH_UNAUTHORIZED` |
| `internal` | service token 中间件内部错误 | 500 | `ERR_INTERNAL` |

5xx 响应只暴露状态级公共 errcode；原始内部 errcode 保留在服务端日志中，指标保留低基数 `reason` 标签用于告警分流。

### AuthServiceTokenReplayDetected

replay 是安全信号——任意命中都意味着 token 在 TTL 窗口内被复用。可能源自 token 泄漏 /
中间人 / 客户端重试逻辑 bug。critical 级别立即升级。

```yaml
- alert: GoCellAuthServiceTokenReplayDetected
  expr: sum(rate(gocell_auth_service_token_verify_total{result="failure",reason="replay"}[5m])) > 0
  for: 0m
  labels:
    severity: critical
  annotations:
    summary: "Service token replay detected"
    description: |
      /internal/v1/* received a service token whose nonce was already consumed within the
      TTL window. Possible token leak / MITM / client retry bug.
      Triage: grep slog for "code=ERR_AUTH_REPLAY_DETECTED"; identify caller IP and
      check recent token issuance logs for the same caller.
```

### AuthNonceStoreFull

InMemoryNonceStore 达到容量上限且无过期 entry 可回收。短窗内是容量问题（请求被拒绝
返回 503，client 重试可恢复）；长期持续意味着 TTL 配置过长或调用速率超出单 pod 容量，
应迁移到分布式 store。

```yaml
- alert: GoCellAuthNonceStoreFull
  expr: sum(rate(gocell_auth_service_token_verify_total{result="failure",reason="nonce_store_full"}[5m])) > 0
  for: 2m
  labels:
    severity: warning
  annotations:
    summary: "In-memory nonce store at capacity"
    description: |
      InMemoryNonceStore reached its max-entries cap with no expired entries to reclaim.
      Mitigation: shorten TTL, raise WithMaxEntries cap, or inject a distributed
      NonceStore via WithServiceTokenNonceStore.
```

### AuthNonceStoreError

nonce store 后端故障（例如 Redis 不可达 / 连接断开）。区别于容量满（503 transient），
infra 错误返回 500，需要立即排查依赖健康度。

```yaml
- alert: GoCellAuthNonceStoreError
  expr: sum(rate(gocell_auth_service_token_verify_total{result="failure",reason="nonce_store_error"}[5m])) > 0
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "Nonce store backend errors"
    description: |
      Service token middleware received non-replay, non-capacity errors from the nonce
      store. Likely backend (Redis) connectivity / configuration issue.
      Triage: check NonceStore implementation health; cross-reference with adapter
      readiness probes.
```

### AuthServiceTokenAuthFailureSpike

通用鉴权失败（reason 为 missing / invalid_mac / expired / legacy_format / invalid_format /
missing_caller_cell / invalid_caller_cell）短时高峰。零星失败正常（典型为部署窗口的滚动重启）；
持续 spike 意味着 key 漂移、客户端配置错误或扫描攻击。

```yaml
- alert: GoCellAuthServiceTokenAuthFailureSpike
  expr: |
    sum(rate(gocell_auth_service_token_verify_total{
      result="failure",
      reason!~"replay|nonce_store_full|nonce_store_error|internal"
    }[5m])) > 1
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Service token auth failures spiking"
    description: |
      Non-replay auth failures (missing / invalid_mac / expired / legacy_format /
      invalid_format / missing_caller_cell / invalid_caller_cell) exceed 1/sec for 10m.
      Inspect reason label distribution via `sum(rate(...)) by (reason)` and verify
      ring rotation / client config / caller_cell token format.
```

---

## Event Router / Outbox Consumer 可观测性（D3a-1 新增）

以下规则覆盖 D3a-1 PR #589 引入的 6 个新 metric family。

### EventRouterSetupErrorRate

事件路由器订阅建立持续失败，通常表示 broker/topic 配置问题。

```yaml
- alert: GoCellEventRouterSetupErrorRate
  expr: sum(rate(gocell_event_router_setup_errors_total[5m])) by (cell, reason) > 0
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Event router setup errors ({{ $labels.cell }})"
    description: |
      Cell {{ $labels.cell }} has persistent event router subscription setup failures
      (reason={{ $labels.reason }}) for 5m.
      Likely causes: broker unreachable, topic not bound, or auth misconfiguration.
      Check cell logs for "event router: subscription setup failed".
```

### EventRouterRuntimeErrorRate

事件路由器运行时持续出现错误，通常表示订阅交付故障或 broker 连接不稳定。

```yaml
- alert: GoCellEventRouterRuntimeErrorRate
  expr: sum(rate(gocell_event_router_runtime_errors_total[5m])) by (cell, reason) > 0
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Event router runtime errors ({{ $labels.cell }})"
    description: |
      Cell {{ $labels.cell }} has persistent event router runtime errors
      (reason={{ $labels.reason }}) for 5m.
      Likely causes: SubscribeEntry delivery failure, broker reconnect loop,
      or ready-wait timeout exceeded after Phase 3.
      Check cell logs for "event router: runtime error".
```

### OutboxConsumerRejectedSpike

outbox consumer reject 速率高于阈值，表示消息被路由到 DLX。持续 reject 意味着
handler 逻辑错误或上游 payload 格式问题。

```yaml
- alert: GoCellOutboxConsumerRejectedSpike
  expr: sum(rate(gocell_outbox_consumer_rejected_total[5m])) by (cell, reason) > 0.1
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Outbox consumer reject spike ({{ $labels.cell }})"
    description: |
      Cell {{ $labels.cell }} outbox consumer reject rate > 0.1/sec for 10m
      (reason={{ $labels.reason }}).
      DLX-routed messages indicate permanent handler errors or upstream payload drift.
      Check handler logs and compare event payloads against current contract schema.
```

### OutboxPendingDepthHigh

outbox **eligible** pending depth（不含 backoff 中的重试 entry）增长表示
consumer 消费速率落后，或 broker 连接断开。

注意：`outbox_pending_depth` Gauge 仅统计 `status=pending` 且
`next_retry_at IS NULL OR <= now()` 的可领取行——重试 backoff 中的 entry 不计入。
持续的重试堆积需通过 relay 侧 outcome 指标
`outbox_relayed_total{outcome="dead"|"lost"}` 或 reclaim-budget readyz 探针
诊断（`outbox_consumer_rejected_total` 是 subscriber 侧 DLX 计数，与 relay
retry-backoff 不同失败域），本 Gauge 不会随之增长。每次 Relay reclaim tick
更新一次（默认间隔为 `runtime/outbox.DefaultRelayReclaimInterval = 30s`，
而非 Prometheus scrape 间隔），应使用较长的 `for:` 窗口避免 scrape 窗口内的
假阳性。

注：`outbox_relayed_total` 自 #1674 起带 `kind` label（`event` = broker publish /
`command` = 进程内 async command dispatch），与 `outcome`（`published|retried|dead|
skipped|lost`）正交。上面不带 `kind` filter 的查询按两类求和，语义不变；按类对账用
`outbox_relayed_total{kind="command",outcome="published"}`（命令分发吞吐）/
`{kind="command",outcome="dead"}`（命令死信）等。两个 label 的值集都由
`kernel/outbox` 的 typed enum 单源派生、冻结进 `metrics-schema.yaml` golden
（OUTBOX-RELAY-LABEL-VALUES-FROZEN-01）。

注: 此告警仅在 storage_backend=postgres 部署生效；memory 模式无 Relay，指标不产生 sample。

```yaml
- alert: GoCellOutboxPendingDepthHigh
  expr: max(gocell_outbox_pending_depth) by (cell) > 1000
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Outbox eligible pending depth high ({{ $labels.cell }})"
    description: |
      Cell {{ $labels.cell }} outbox eligible pending depth > 1000 for 5m
      (excludes rows still in retry backoff).
      Consumer may be falling behind or broker connection dropped.
      Note: this Gauge is updated once per Relay ReclaimInterval
      (defaults to runtime/outbox.DefaultRelayReclaimInterval = 30s),
      not per scrape — treat the value as "eligible depth at last reclaim tick".
      For tighter sampling, decrease ReclaimInterval. Retry backlog is not
      reflected here; diagnose via outbox_relayed_total{outcome="dead"|"lost"}
      (relay-side outcome counter) — outbox_consumer_rejected_total is the
      subscriber-side DLX counter, a different failure domain.
```

### 调试指标（无告警，仅 dashboard）

以下两个新 metric 不设告警阈值，供运维排查使用：

- **`gocell_event_router_subscriptions_active{cell}`**（Gauge）— 当前活跃订阅数。
  用于 operational dashboard 展示 event router 健康度；cell 维度聚合可快速
  发现某 cell 订阅掉零。

- **`gocell_event_router_ready_wait_seconds{cell}`**（Histogram）— event router
  等待 Ready 信号的耗时分布。关注 p95 spike（通常发生在 broker 重连期间）；
  p99 持续高于 bootstrap 超时（30s）提示 broker 不可达或 topic 配置错误。
  可通过以下 PromQL 监控：
  `histogram_quantile(0.95, sum(rate(gocell_event_router_ready_wait_seconds_bucket[5m])) by (le, cell))`

---

## Projection 可观测性（CQRS rebuild harness）

`kernel/projection.Coordinator`（L3 read-model 投影）注册三个 metric，全部带
`{cell, projection}` 双 label（Prometheus provider 加 `gocell_` namespace 前缀）。
一个 cell 可托管多个 projection，故 `projection` label 与 `cell` 同为聚合维度。

| Metric (registered name) | 类型 | 含义 |
|---|---|---|
| `projection_event_replay_lag_seconds` | Gauge | read-model 落后多久 = `now − 最近一次 apply 的事件 OccurredAt`（域时间，非 store 时间） |
| `projection_pending_events` | Gauge | 未应用事件数 = replay 源 head − 已提交 checkpoint（积压深度，按需在 readyz/快照读取时计算） |
| `projection_rebuild_duration_seconds` | Histogram | 一次完整 rebuild（Stop→Reset→Replay→Catchup）的墙钟耗时；仅 dashboard，无告警阈值 |

`<cell>_projection_<name>_lag` readyz probe 在 `pending_events > 0` 且
`replay_lag_seconds > 300`（`projectionLagThresholdSeconds`）时返回 unhealthy；
冷启动（无 pending 或尚无 apply）healthy。probe 与下方 lag 告警同源，告警先于
probe 翻红可用作早期信号。另一个 probe `<cell>_projection_<name>_store_ready`
在 replay.Head 或 store.LoadOffset 报错时返回 unhealthy（存储可达性，独立失败域）。

### ProjectionReplayLagHigh

read-model 持续落后表示投影 consumer 消费速率跟不上事件产生速率，或某次
rebuild 卡在 Replay 相。下游查询读到陈旧 read-model。

```yaml
- alert: GoCellProjectionReplayLagHigh
  expr: max(gocell_projection_event_replay_lag_seconds) by (cell, projection) > 300
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Projection replay lag high ({{ $labels.cell }}/{{ $labels.projection }})"
    description: |
      Projection {{ $labels.cell }}/{{ $labels.projection }} replay lag > 300s
      for 5m — the read-model is stale. Check consumer throughput, or whether a
      rebuild is stuck in Replay. Cross-check gocell_projection_pending_events:
      lag high WITH pending>0 = falling behind; lag high WITH pending==0 = idle
      stream (benign, the lag is just "no new events"). The readyz probe
      <cell>_projection_<name>_lag flips unhealthy only when BOTH hold
      (pending>0 AND lag>threshold). The companion store-ready probe
      <cell>_projection_<name>_store_ready covers storage unavailability.
      Note: the > 300 threshold mirrors kernel/projection.projectionLagThresholdSeconds
      (probe.go); if that constant changes, update this expression to match.
      The replay_lag gauge is updated on readyz/snapshot reads (no background
      ticker), so it samples at probe cadence — use a 5m `for:` window to avoid
      false positives between probe scrapes.
```

### ProjectionPendingEventsHigh

积压深度增长（head 远超 checkpoint）表示 apply 速率落后或 consumer 停摆。

```yaml
- alert: GoCellProjectionPendingEventsHigh
  expr: max(gocell_projection_pending_events) by (cell, projection) > 1000
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Projection pending events high ({{ $labels.cell }}/{{ $labels.projection }})"
    description: |
      Projection {{ $labels.cell }}/{{ $labels.projection }} has > 1000 un-applied
      events for 5m. Gauge is computed on readyz/snapshot reads (no background
      ticker), so it samples at probe cadence. A sustained climb with flat
      checkpoint advance indicates the apply path is wedged; inspect the
      projection.apply trace span and gocell_projection_event_replay_lag_seconds.
```

### Rebuild duration（仅 dashboard）

`gocell_projection_rebuild_duration_seconds` 无告警阈值（rebuild 是手动/计划触发的
运维动作）。用于容量规划——若 Stage-1 全量 rebuild p95 ≥ 30min，触发 ADR
§Q4 记录的 v1.1 snapshot-store epic。Histogram buckets 覆盖至 1800s (30min)，
确保 p95 ≥ 30min 的触发条件可测量而不塌缩入 +Inf。p95 PromQL：
`histogram_quantile(0.95, sum(rate(gocell_projection_rebuild_duration_seconds_bucket[1h])) by (le, cell, projection))`

### `projection_events` journal 增长监控（#1504 PR-02）

durable 投影 journal `projection_events` 是 **append-only、归档能力尚 out-of-scope（ADR §D8）**，
故从 PR-02 装饰器部署起持续增长。topic-filter（D4）已把写入面限到真会被 replay 的 projection-source
事件，但仍须监控**总**磁盘占用（heap + 索引 + TOAST，JSONB payload 大可触发 TOAST offload，总占用可达
heap 的数倍）并接入容量告警。表大小无内置应用 metric，由 PG 侧采集。

> postgres_exporter 默认不暴露按表的 `pg_total_relation_size`——需配 custom query collector
> （`queries.yaml`：`SELECT relname, pg_total_relation_size(relid) AS bytes FROM pg_stat_user_tables`），
> 或用默认已暴露的行数 `pg_stat_user_tables_n_live_tup{relname="projection_events"}` 做近似告警（行数阈值
> 按平均行宽换算）。下例假设已暴露 `pg_total_relation_size_bytes{relname=...}`。

```yaml
- alert: GoCellProjectionEventsJournalGrowthHigh
  # pg_total_relation_size（含索引 idx_projection_events_id + PK btree + TOAST），非裸 heap。
  expr: pg_total_relation_size_bytes{relname="projection_events"} > 5e10  # 50 GiB，按部署容量调
  for: 30m
  labels:
    severity: warning
  annotations:
    summary: "projection_events journal large (append-only, no archival yet)"
    description: |
      The durable projection_events journal exceeds the capacity threshold (total
      relation size incl. indexes + TOAST). It is append-only (ADR 202606071600-1504
      §D7) with archival still out-of-scope (§D8): truncation MUST stay ≥
      MIN(projection_checkpoints.offset_seq) or rebuildable history is lost. Treat as a
      capacity-planning signal — provision storage, or prioritise the archival epic
      (D8). Do NOT manually DELETE/TRUNCATE the table (serving role is REVOKEd
      UPDATE/DELETE; only a table-owner migration could, and must honour the checkpoint
      floor).
```

运维注意（ADR §8）：首次 full rebuild 后 `projection_event_replay_lag_seconds` 快速降至 ~0 仅表示
读完 journal，**不**代表历史完整（bootstrap gap：journal 仅从 PR-02 部署起 append）；数据完整性须
经 `projection_checkpoints.offset_seq` + 业务校验确认，不能仅看 lag。

---

## Saga-Journal Tailer 可观测性（#1609 PR-04）

`runtime/saga/tailer.Tailer`（saga 终态 model-A catch-up 投影驱动）是新长驻组件，
**不经 ConsumerBase / saga Coordinator 路径**，运维信号不自动继承——故自带运行面。
所有 metric 带 `{cell, projection}` 双 label（Prometheus 加 `gocell_` 前缀），与
Projection 投影 metric 同维度约定。readiness probe `<cell>_saga_tailer_<proj>_ready`
在 Tailer 未运行或 journal/checkpoint 存储不可达时返回 unhealthy（lag 是 metric，不进 probe）。

| Metric (registered name) | 类型 | 含义 |
|---|---|---|
| `saga_journal_tailer_lock_acquire_failed_total{reason}` | Counter | per-projection distlock 抢锁失败（leader gate 跳过该 tick），`reason ∈ {contended, ctx_canceled, backend_error}` |
| `saga_journal_tailer_drain_total{result}` | Counter | 有进展或失败的 drain，`result ∈ {ok, store_error, apply_error}`（空闲 caught-up tick 不计） |
| `saga_journal_tailer_checkpoint_advance_total{result}` | Counter | 每次 AdvanceIfOwner，`result ∈ {ok, stale_owner, error}` |
| `saga_journal_tailer_pending_events` | Gauge | 残余积压 = HeadSeq − checkpoint（上次干净 tick 后） |
| `saga_journal_tailer_last_success_timestamp_seconds` | Gauge | 上次完整 tick 的 unix 时间（驱动停摆告警） |

> **`stale_owner` 是良性交接，不是故障**：`checkpoint_advance_total{result="stale_owner"}`
> 表示一个被废黜的旧 leader 在 lock 交接窗口被 CAS fence（设计预期，见 ADR D5(b)），
> 告警必须排除该 result，只对 `result="error"` 报警。同理 `lock_acquire_failed_total{reason="contended"}`
> 是正常多进程竞争，只对 `reason="backend_error"`（distlock I/O 故障）报警。

### SagaTailerStalled

checkpoint 长期不动（`last_success` 时间戳不前进）= tailer 停摆：无 leader 在跑、
或 drain 反复失败、或 distlock 后端不可达。下游读投影读到陈旧 saga 终态。

```yaml
- alert: GoCellSagaTailerStalled
  expr: |
    time() - max(gocell_saga_journal_tailer_last_success_timestamp_seconds) by (cell, projection) > 600
    or absent(gocell_saga_journal_tailer_last_success_timestamp_seconds) == 1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Saga journal tailer stalled ({{ $labels.cell }}/{{ $labels.projection }})"
    description: |
      No saga-journal tailer tick has succeeded for {{ $labels.cell }}/{{ $labels.projection }}
      in > 10m. The checkpoint is silently frozen.
      First arm — per-{cell,projection} staleness, including cold/never-succeeded
      projections: the tailer seeds last_success = start time when it starts (see
      Tailer.Start), so the {cell,projection} series exists from startup. A projection
      that has never had a successful tick therefore fires once its age exceeds the
      threshold — it does not vanish from the series set, so a single cold projection is
      caught even while other projections keep reporting (the seed is what makes per-label
      cold detection work; without it a cold projection would have no series). max() by
      (cell,projection) means one healthy replica keeps the projection green.
      Second arm (absent()) — coarse backstop only: fires when the gauge disappears
      ENTIRELY (every replica down / metrics pipeline broken), which the first arm cannot
      see because a comparison on an empty vector yields no samples.
      Triage: is a tailer pod running and winning the per-projection distlock? See
      saga-runbook.md §"场景 5：投影 tailer 停滞" (HeadSeq vs checkpoint SQL + distlock
      key holder). Cross-check gocell_saga_journal_tailer_drain_total{result} and
      gocell_saga_journal_tailer_lock_acquire_failed_total{reason}.
```

### SagaTailerLagHigh

积压增长（HeadSeq 远超 checkpoint）= apply 速率落后或 tailer 停摆。

```yaml
- alert: GoCellSagaTailerLagHigh
  expr: max(gocell_saga_journal_tailer_pending_events) by (cell, projection) > 1000
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Saga journal tailer backlog high ({{ $labels.cell }}/{{ $labels.projection }})"
    description: |
      > 1000 un-applied saga events for {{ $labels.cell }}/{{ $labels.projection }} for 5m.
      Gauge is written after each clean tick (probe/tick cadence, no background ticker).
      A sustained climb with flat checkpoint advance indicates the drain is wedged or the
      tailer is not the leader. Inspect gocell_saga_journal_tailer_drain_total{result}.
```

### SagaTailerLockAcquireFailures

distlock 后端 I/O 故障率——`contended` 是正常竞争、必须排除，只对 `backend_error` 报警。

```yaml
- alert: GoCellSagaTailerLockAcquireFailures
  expr: sum(rate(gocell_saga_journal_tailer_lock_acquire_failed_total{reason="backend_error"}[5m])) by (cell, projection) > 0
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Saga journal tailer distlock backend faulting ({{ $labels.cell }}/{{ $labels.projection }})"
    description: |
      The saga-journal tailer cannot reach the distlock backend (reason=backend_error).
      reason=contended is benign multi-process contention and is excluded. Sustained
      backend_error means no tailer can confirm leadership → drains stop → pair with
      GoCellSagaTailerStalled. Check the distlock (Redis) backend health.
```

### SagaTailerCheckpointAdvanceFailures

checkpoint 推进故障——`stale_owner` 是良性交接、必须排除，只对 `error` 报警。

```yaml
- alert: GoCellSagaTailerCheckpointAdvanceFailures
  expr: sum(rate(gocell_saga_journal_tailer_checkpoint_advance_total{result="error"}[5m])) by (cell, projection) > 0
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Saga journal tailer checkpoint advance failing ({{ $labels.cell }}/{{ $labels.projection }})"
    description: |
      AdvanceIfOwner is failing with result=error (tx/storage fault) for
      {{ $labels.cell }}/{{ $labels.projection }}. result=stale_owner is a benign leader
      handoff (CAS fence) and is excluded. Sustained error means apply+advance cannot
      commit → checkpoint frozen. Inspect the projection.apply tx path and DB health.
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
  sum(rate(gocell_outbox_poll_duration_seconds_bucket[5m])) by (le, cell, phase)
)
```

### service token verify 全失败原因比例

```promql
sum(rate(gocell_auth_service_token_verify_total[5m])) by (result, reason)
```

把 result/reason 联合分组的趋势贴到 Grafana stack panel，可一眼看出 replay 突袭、
nonce store 后端故障、token 失效（key 漂移）三类不同诱因的占比变化。

### config event consumer process reason 分布

```promql
sum(rate(gocell_config_event_process_total[5m])) by (cell, slice, reason)
```

### config event consumer settlement 分布

```promql
sum(rate(gocell_config_event_settlement_total[5m])) by (cell, slice, disposition, result)
```

---

## HTTP 幂等 (#1460)

`idempotency_requests_total{cell,state}` 由 HTTP idempotency 中间件（`runtime/http/idempotency`）经 `obmetrics.IdempotencyCollector` 发射，bootstrap 在配置了真实 metrics `Provider` 时自动接线。`cell` 是归属 RouteGroup 的 cell（框架路径为 `_runtime`）。`state` 取值冻结为 6 个终态（archtest `IDEMPOTENCY-REQUESTS-STATE-LABEL-VALUES-FROZEN-01` 守）：

- `acquired`：新 claim（已处理请求分母，claim 时计，不是其余 state 之和）
- `replayed`：缓存响应回放
- `busy`：在途 lease 冲突 → 409
- `store_error`：Claim 路径失败 → 500
- `oversize`：响应超限不录（`acquired` 的子事件）
- `key_reused`：同 key 不同 body → 422（附 per-field diff 字段名；安全相关）

> 仅当配置真实 `Provider` 且某 listener 接线了 idempotency store 时才会有时间序列；无 store 时该 metric 仅出现在 `/metrics` HELP，无序列。

### GoCellIdempotencyReplayStorm

`busy` 速率持续偏高表示客户端重试风暴或在途请求卡死（同 key 并发争用 lease）。

```yaml
# rate() 返回每秒事件数；`> 1` 在 busy 平滑速率超过 1/sec（≈ 60/min）时触发。
# 按 (target_busy_per_min)/60 调参（如 6/min → > 0.1）。默认过滤业务 cell。
- alert: GoCellIdempotencyReplayStorm
  expr: sum(rate(gocell_idempotency_requests_total{state="busy",cell!="_runtime"}[5m])) > 1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "HTTP idempotency busy (in-flight lease) spike"
    description: "ClaimBusy 409 速率 >1/sec（≈ 60/min）持续 5min。客户端重试风暴或在途请求卡死。排查在途 handler 时延与客户端重试退避。"
```

### GoCellIdempotencyKeyReused

`key_reused`（同 Idempotency-Key 不同 body）非零通常是客户端 bug，持续偏高可能是重放尝试——安全相关。下面的告警在速率 > 0.05/sec（≈ 3/min）持续 15min 时触发（info）；低于该阈值的零星 key_reused **不**触发告警，需结合 slog `idempotency_key_hash` 与下方调试查询人工确认。

```yaml
# key_reused 速率 > 0.05/sec（≈ 3/min）持续 15min 告警（info）。安全相关——更低的
# 零星速率不在此告警，用下方调试查询 + slog idempotency_key_hash 人工确认。
- alert: GoCellIdempotencyKeyReused
  expr: sum(rate(gocell_idempotency_requests_total{state="key_reused"}[15m])) > 0.05
  for: 15m
  labels:
    severity: info
  annotations:
    summary: "Idempotency key reused with different body"
    description: "同 key 不同 body 指纹不匹配 422 速率 >0.05/sec（≈ 3/min）持续 15min（响应附 per-field diff 差异字段名）。客户端 bug 或重放尝试，结合 slog idempotency_key_hash 关联请求来源。"
```

### 调试查询

```promql
# 重放命中率（缓存有效性）：replayed / (acquired + replayed)
sum(rate(gocell_idempotency_requests_total{state="replayed"}[5m]))
  / sum(rate(gocell_idempotency_requests_total{state=~"acquired|replayed"}[5m]))

# 不可缓存率（响应过大未录）：oversize / acquired
sum(rate(gocell_idempotency_requests_total{state="oversize"}[5m]))
  / sum(rate(gocell_idempotency_requests_total{state="acquired"}[5m]))

# store error 率（Claim 路径 500；Record/Release 失败仅 slog，不在此 metric）
sum by (cell) (rate(gocell_idempotency_requests_total{state="store_error"}[5m]))

# key_reused 率（任意非零，含低于告警阈值的零星事件）：按 cell 拆分定位来源，
# 再用 slog idempotency_key_hash 关联具体请求。
sum by (cell) (rate(gocell_idempotency_requests_total{state="key_reused"}[5m]))
```

---

## Auth 账户自动锁定

`auth_account_lockout_total{reason}` 由 `runtime/auth.AccountLockoutMetrics` 在 accesscore sessionlogin 路径发射，`reason` 取值：
- `threshold_locked`：连续失败达阈值，账户被自动锁定
- `lazy_unlocked`：TTL 到期，sessionlogin 透明解锁

### GoCellAuthAccountAutoLockSpike

短时内多个账户被自动锁定，可能意味着暴力破解攻击或系统性认证问题。

```yaml
# rate() returns events-per-second. The expr `rate(...[5m]) > 1` therefore
# fires when the smoothed lockout rate exceeds 1 lock/second (≈ 60 locks/min)
# over the trailing 5-minute window. Tune by `(target_locks_per_min)/60`
# (e.g. 1 lock/min → > 0.017). Description text matches this semantics.
- alert: GoCellAuthAccountAutoLockSpike
  expr: sum(rate(gocell_auth_account_lockout_total{reason="threshold_locked"}[5m])) > 1
  for: 5m
  labels:
    severity: warning
    cell: accesscore
  annotations:
    summary: "Account auto-lockout spike detected"
    description: "Accounts are being auto-locked at >1/sec (≈ 60/min) over a 5-minute window. Possible brute-force attack or systemic auth issue. Runbook: docs/ops/login-failure-triage.md#auto-lockout-相关-slog-事件"
```

### GoCellAuthAccountLazyUnlockUnusualFrequency

lazy-unlock 频率持续偏高，可能意味着攻击者在利用 TTL 边界周期性尝试。

```yaml
# rate() returns events-per-second; `> 0.5` fires when the smoothed
# lazy-unlock rate exceeds 0.5/sec (≈ 30/min) over the trailing 15-minute
# window. Same unit-conversion guidance as GoCellAuthAccountAutoLockSpike:
# `(target_unlocks_per_min)/60` (e.g. 0.5 unlocks/min → > 0.0083).
- alert: GoCellAuthAccountLazyUnlockUnusualFrequency
  expr: sum(rate(gocell_auth_account_lockout_total{reason="lazy_unlocked"}[15m])) > 0.5
  for: 15m
  labels:
    severity: info
    cell: accesscore
  annotations:
    summary: "High lazy-unlock frequency"
    description: "lazy-unlock rate exceeds 0.5/sec (≈ 30/min) sustained over 15min. Indicates attackers may be exploiting TTL boundary."
```

---

## Saga (PR-1210 / #1109)

Saga metrics are registered per-cell when a `SagaCollector` is wired via
`obmetrics.NewSagaCollector(provider, cellID)`. The six counters share the
`gocell_` namespace prefix. Three are step-level (Executor-emitted); three are
coordinator-level (Coordinator-emitted, added in #1109). They are only emitted
when a real metrics `Provider` is wired — saga is not yet in a production cell
(examples/orderfulfillment uses `NopProvider`), so production wiring lands with
the saga-as-cell migration.

Step-level:

- `gocell_saga_step_outcome_total{cell,definition_id,outcome}`: total Execute calls
  terminated, labeled by outcome variant (`succeeded` / `failed` / `expired` /
  `canceled` / `lease_lost`). Filter `outcome="failed"` to baseline forward-step
  failure rate per definition.

- `gocell_saga_step_retry_total{cell,definition_id,step_name}`: total retry attempts
  fired between Execute invocations (attempt N > 1). Per-step label allows per-step
  retry-budget tuning.

- `gocell_saga_heartbeat_failed_total{cell,reason}`: total heartbeat tick failures,
  labeled by reason (`infra_error` = transient backend error; `stale_lease` = another
  coordinator owns the lease). Sustained `stale_lease` rate indicates leader-elect
  instability.

Coordinator-level:

- `gocell_saga_tick_total{cell,result}`: total Coordinator ClaimPending cycles,
  labeled by `result` (`claimed` = ≥1 instance claimed; `empty` = idle tick;
  `error` = ClaimPending failed). Loop liveness — a flat tick rate means the
  coordinator goroutine stalled.

- `gocell_saga_drive_total{cell,definition_id,result}`: total driveOne completions,
  labeled by `result` (`ok` / `error`). Per-instance forward-progress throughput.

- `gocell_saga_leader_elect_skip_total{cell,definition_id,reason}`: total
  leader-elect skips, labeled by `reason` (`contended` = another coordinator holds
  the per-instance distlock — normal in multi-process; `ctx_canceled` = shutdown;
  `backend_error` = distlock backend I/O fault). `reason="backend_error"` is the
  **lock-acquire failure rate**. Replaces log-scraping the Debug-level skip path.

### GoCellSagaInstanceStuckSkipping

An instance is being claimed and leader-elect-skipped every tick (high `contended`)
but never advances (`drive{result="ok"}` ≈ 0) — a coordinator that holds neither
the distlock nor makes progress, e.g. a wedged peer holding a stale distlock.

```yaml
# Fires when leader-elect contended skips are sustained while successful drives
# are absent for the same (cell, definition_id) pair — the "stuck skipping, not
# advancing" signal #1109 was created to surface without log scraping.
#
# Grouped by (cell, definition_id): if cell X has definition A stuck-skipping
# (contended) but definition B advancing (ok-drives), collapsing to `by (cell)`
# would mask A's stuck-skip via the `unless` set-difference. Per-definition
# grouping surfaces the correct signal without cardinality explosion (definition_id
# is bounded to the registered set in the producer).
#
# `unless on(cell, definition_id)` (set difference) — NOT `and ... == 0`: the
# worst case this alert targets is a coordinator that NEVER drives, in which case
# the gocell_saga_drive_total{result="ok"} series does not exist at all. With
# `and ... == 0` the right side is an empty vector and the alert silently never
# fires. `unless <right> > 0` keeps every contended-heavy (cell, definition_id)
# that has no matching pair with a positive ok-drive rate — including absent series.
- alert: GoCellSagaInstanceStuckSkipping
  expr: |
    sum(rate(gocell_saga_leader_elect_skip_total{reason="contended"}[5m])) by (cell, definition_id) > 0.1
    unless on(cell, definition_id)
    sum(rate(gocell_saga_drive_total{result="ok"}[5m])) by (cell, definition_id) > 0
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Saga instances skipping (contended) but not advancing"
    description: "Cell {{ $labels.cell }} definition {{ $labels.definition_id }} has sustained leader-elect contended skips with zero successful drives over 10min. A peer may hold a stale distlock. Runbook: docs/ops/saga-runbook.md"
```

### GoCellSagaLockAcquireFailures

distlock backend I/O faults are preventing leader election — the coordinator
cannot confirm leadership and skips every instance fail-closed.

```yaml
# rate() = events/sec; `> 0.05` fires at >0.05 backend-error skips/sec (≈ 3/min)
# over the 5m window. backend_error is distlock backend I/O (Redis) faults only
# (contended / ctx_canceled are classified separately), so any sustained rate is
# a real fault — tune by your distlock backend's acceptable transient-error floor.
- alert: GoCellSagaLockAcquireFailures
  expr: sum(rate(gocell_saga_leader_elect_skip_total{reason="backend_error"}[5m])) by (cell) > 0.05
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Saga distlock lock-acquire failures"
    description: "Cell {{ $labels.cell }} leader-elect is failing on distlock backend I/O (>0.05/sec over 5min). Check the distlock backend (Redis) health. Runbook: docs/ops/saga-runbook.md"
```

**StatusCompensationFailed terminal state**: when a saga instance reaches
`status=compensation_failed` (status=8 in PG), the compensation phase itself failed.
This is distinct from `status=failed` (forward failure, no rollback attempted).
Dashboard queries should branch on this distinction:

```promql
# Forward step failures — saga will attempt compensation if steps were committed.
increase(gocell_saga_step_outcome_total{outcome="failed"}[5m])

# Heartbeat / lease instability.
increase(gocell_saga_heartbeat_failed_total{reason="stale_lease"}[5m])
```

### Saga event kind 速查

<!-- gocell:generated:saga-event-kind-legend — DO NOT EDIT (regen: gocell generate saga-coverage) -->
`kind` 速查：1=step_started，2=step_completed，3=step_failed，4=step_compensated，5=compensation_started，6=saga_succeeded，7=saga_failed，8=saga_compensated，9=saga_expired，10=step_compensation_failed，11=saga_compensation_failed。
<!-- /gocell:generated:saga-event-kind-legend -->

**诊断与处置 runbook**：补偿失败（CompensationFailed）/ lease 卡死 / journal 增长的完整诊断 SQL、决策树（幂等外部副作用 / 不可逆操作 / 基础设施故障三分支）与审计要求见 **`docs/ops/saga-runbook.md`**。本节只保留指标告警职责；上面的 `kind` 速查生成区供 runbook 诊断 SQL 交叉引用。

---

## Reconcile Leader 可观测性

`gocell_reconcile_leader{reconciler}` 是一个 Gauge，值为 1 当该实例持有对应 reconcilerID
的 lease，否则为 0（由 kernel/reconcile/metrics.go 的 `metricReconcileLeader` 注册）。
在 leader-elect 模式下，任意时刻健康集群中**每个 reconcilerID 恰好应有 1 个实例持有 lease**；
0 代表 leader 空缺，>1 代表脑裂异常（应由 fencing 机制阻止写放大，但 gauge 层面不应出现）。

### ReconcileLeaderVacancy

leader 空缺持续超过 LeaseDuration + 1s：无实例持有 lease，reconcile 工作停摆。

`sum by (reconciler)` keeps the per-reconciler dimension (so each reconcilerID
alerts independently and `{{ $labels.reconciler }}` is populated — a bare `sum`
would collapse all reconcilers and drop the label). The vacancy rule has TWO arms:
the `== 0` arm catches "exporting but no holder"; the `absent(...)` arm catches
"the whole series vanished" (every replica down / scrape lost), which `== 0` alone
would miss (a comparison on an empty vector yields no samples → no alert). Replace
`<id>` in the `absent()` arm with each reconcilerID you run (absent() needs a fully
specified series).

```yaml
- alert: GoCellReconcileLeaderVacancy
  expr: |
    sum by (reconciler) (gocell_reconcile_leader) == 0
    or absent(gocell_reconcile_leader{reconciler="<id>"})
  for: 16s
  labels:
    severity: critical
  annotations:
    summary: "Reconcile leader vacant ({{ $labels.reconciler }})"
    description: |
      No instance holds the reconcile lease for reconciler={{ $labels.reconciler }}
      (or the metric series is entirely absent — total scrape loss / all replicas down).
      All reconcile work is paused until a follower acquires the lease.
      Typical causes: all replicas crashed, Redis/PG backend unreachable, or
      lease TTL misconfiguration (RenewInterval >= TTL causes spurious lease loss).
      Triage: check logs for "lease lost; relinquishing leadership" /
      "renew I/O error; abandoning lease term" and verify elector backend health.
      Note: for=16s assumes a default lease TTL of ~15s (LeaseDuration+1s headroom);
      adjust to match your configured leaseDuration + 1s.
```

### ReconcileLeaderSplitBrain

多个实例同时持有 gauge=1：脑裂异常信号。正常 handoff 期间可能出现短暂双重 gauge=1，
但持续 > 2s 表示 fencing 层以上的 gauge 未及时更新（或 lease backend 状态异常）。

```yaml
- alert: GoCellReconcileLeaderSplitBrain
  expr: |
    sum by (reconciler) (gocell_reconcile_leader) > 1
  for: 2s
  labels:
    severity: critical
  annotations:
    summary: "Reconcile leader split-brain anomaly ({{ $labels.reconciler }})"
    description: |
      More than one instance reports reconcile_leader=1 for
      reconciler={{ $labels.reconciler }}. Cross-replica correctness is guarded
      by the epoch-fencing CAS (ErrFencedWriteStale will dead-letter stale writes),
      but the gauge anomaly indicates the lease backend or gauge update path is
      inconsistent. Investigate elector backend state and lease TTL configuration.
      Short transient doubles during handoff are expected; sustained > 2s is not.
```

---

## Webhook（KERNEL-WEBHOOK-01 PR-6）

四个指标：`gocell_webhook_deliveries_total{result,source}`（发端投递结果）/
`gocell_webhook_delivery_duration_seconds{source}`（发端时延）/
`gocell_webhook_signature_failures_total{source,reason}`（收端验签失败）/
`gocell_webhook_idempotency_hits_total{source}`（收端重复投递去重）。`result` 值集
`{success, client_error, server_error, transport_error, blocked}`（冻结于
`WEBHOOK-METRIC-LABEL-VALUES-FROZEN-01`）；`reason` 值集
`{missing_header, invalid_header, unknown_source, bad_signature, timestamp_expired}`。

### WebhookDeliveryServerErrorRate

下游 5xx 持续高比率 = 目标端点过载/故障（可重试，但持续高表示对端不可用）。

```yaml
- alert: GoCellWebhookDeliveryServerErrorRate
  expr: |
    sum by (source) (rate(gocell_webhook_deliveries_total{result="server_error"}[5m]))
      / sum by (source) (rate(gocell_webhook_deliveries_total[5m])) > 0.2
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Webhook delivery 5xx rate high (source={{ $labels.source }})"
    description: |
      >20% of webhook deliveries to source={{ $labels.source }} returned 5xx over
      10m. The dispatcher requeues these (standard-webhooks aligned), but a
      sustained high ratio means the target endpoint is degraded. The result label
      separates this from client_error (endpoint misconfiguration).
```

### WebhookDeliveryClientErrorRate

4xx 持续高比率 = 端点配置/认证错误（与 5xx 区分是 status-aware result label 的核心价值）。

```yaml
- alert: GoCellWebhookDeliveryClientErrorRate
  expr: |
    sum by (source) (rate(gocell_webhook_deliveries_total{result="client_error"}[5m]))
      / sum by (source) (rate(gocell_webhook_deliveries_total[5m])) > 0.2
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Webhook delivery 4xx rate high (source={{ $labels.source }})"
    description: |
      >20% of webhook deliveries to source={{ $labels.source }} returned 4xx over
      10m — likely a target endpoint misconfiguration (auth, path, payload schema),
      NOT a transient outage. Unlike server_error these keep failing on retry.
```

### WebhookSignatureFailureSpike

验签失败速率突增 = 安全信号（密钥轮换错配、replay、或源未注册）。

```yaml
- alert: GoCellWebhookSignatureFailureSpike
  expr: |
    sum by (source, reason) (rate(gocell_webhook_signature_failures_total[5m])) > 1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Webhook signature failures (source={{ $labels.source }} reason={{ $labels.reason }})"
    description: |
      Sustained webhook signature-verification failures. reason distinguishes
      missing_header / invalid_header / unknown_source / bad_signature /
      timestamp_expired. A bad_signature or timestamp_expired spike is a
      key-rotation misconfiguration or replay signal; unknown_source means the
      configured sourceID has no registered secret (server-side only — the wire
      401 is uniform to avoid an enumeration oracle).
```

> **超时与时延分位**：`gocell_webhook_delivery_duration_seconds` 的最大 bucket（30s）等于
> 默认 delivery timeout，故超时样本（`result=transport_error`，约 30s）钉在最后一个
> bucket，`histogram_quantile` p99 在超时率高时会饱和在 30s。运维应将
> `rate(...{result="transport_error"})` 计数器与 duration 分位联合判读，而非只看 p99。

---

## Session Cache 可观测性（#794 / #795）

三个计数器由 `runtime/observability/metrics.SessionCacheCollector`（`session_cache.go`）注册，
每个 `CachingSessionStore` 实例对应一个 Collector（一 cell 一实例，今日只有 `accesscore`）。

### 计数器语义

| 指标 | 语义 |
|------|------|
| `gocell_session_cache_hits_total{cell}` | 命中：Redis 返回合法 entry，无需查内层 store |
| `gocell_session_cache_misses_total{cell}` | 未命中：Redis 无数据或数据无效，回落内层 store；**读路径 error 是 miss 的子集** |
| `gocell_session_cache_errors_total{cell}` | **读路径**缓存访问错误（Redis GET/SET 失败、JSON 损坏、schema 校验失败）；fail-safe，不传播给调用方 |
| `gocell_session_cache_revoke_del_errors_total{cell}` | **单 session logout（#796）的 post-commit cache-DEL 失败**——与读路径错误正交的独立序列（DEL 在 commit 后的 hook 里触发、不在任何 Get 内，无配对 miss）。非零速率是**安全相关**信号：被登出 session 的失效延迟从近零退化到 ≤ TTL（stale ≤ TTL，#796 已接受的兜底窗口） |

关键等式：`hits + misses = Get() 总调用次数`；`errors ⊆ misses`（每次**读路径** error 同时计一次 miss）。`revoke_del_errors` **不**计入 `errors` / `misses`——它是独立序列，刻意如此以保持读路径 `errors ⊆ misses` 不变式（折进 `errors` 会让 `errors/misses` 可 >1）。

### PromQL 示例

```promql
# 缓存命中率（近 5 分钟窗口）
rate(gocell_session_cache_hits_total{cell="accesscore"}[5m])
  /
(rate(gocell_session_cache_hits_total{cell="accesscore"}[5m])
  + rate(gocell_session_cache_misses_total{cell="accesscore"}[5m]))

# 错误率（绝对值；突增表示 Redis 降级）
rate(gocell_session_cache_errors_total{cell="accesscore"}[5m])

# 错误在 miss 中占比（区分"缓存冷"与"Redis 故障"）
rate(gocell_session_cache_errors_total{cell="accesscore"}[5m])
  /
rate(gocell_session_cache_misses_total{cell="accesscore"}[5m])

# 单 session logout 失效降级率（#796 post-commit DEL 失败；安全相关，独立序列）
rate(gocell_session_cache_revoke_del_errors_total{cell="accesscore"}[5m])
```

### 告警建议

```yaml
- alert: SessionCacheErrorRateHigh
  expr: |
    rate(gocell_session_cache_errors_total{cell="accesscore"}[5m]) > 0.1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: Session cache error rate elevated for {{ $labels.cell }}
    description: |
      session_cache_errors_total spike may indicate Redis degradation.
      Read-path errors are fail-safe (fall through to inner store), but sustained
      errors bypass the cache entirely and increase inner-store load.

# 安全相关：单 session logout 的 post-commit DEL 失败（#796）。任何非零持续速率都意味着
# 被登出的 session 在 cache 中 stale ≤ TTL（失效从近零退化到 TTL 兜底）。阈值设为 0 +
# 短 for，因为这是安全降级而非普通 cache 抖动。
- alert: SessionCacheRevokeDelFailures
  expr: |
    rate(gocell_session_cache_revoke_del_errors_total{cell="accesscore"}[5m]) > 0
  for: 2m
  labels:
    severity: warning
  annotations:
    summary: Session logout cache-eviction (DEL) failing for {{ $labels.cell }}
    description: |
      session_cache_revoke_del_errors_total is non-zero: single-session logout's
      post-commit Redis DEL is failing, so logged-out sessions remain cache-valid
      until TTL expiry (stale ≤ GOCELL_SESSION_CACHE_TTL — the accepted #796
      backstop, NOT epoch fail-closed: single-session Revoke does not bump
      users.authz_epoch). Investigate Redis availability. For zero-tolerance stale
      requirements (e.g. active breach response) disable the cache entirely by
      unsetting GOCELL_SESSION_CACHE_TTL.
```

> **golden 行为**：metrics-schema golden（`assemblies/corebundle/generated/metrics-schema.yaml`
> 及各 example assembly）在每次 `go run ./cmd/gocell generate metrics-schema --all`
> 后由工具重新生成；Help 文本变更会反映在 golden diff 中，CI 以 byte-exact 比对守卫。

---

## PostgreSQL RLS 运行时执行（#1676）

`postgres_app_role_restricted_ready` 探针验证 serving pool role 是否符合
NOSUPERUSER + NOBYPASSRLS 要求——这是 `FORCE ROW LEVEL SECURITY` 在运行时实际生效的前提。
探针失败意味着 RLS policies 在 schema 层正确定义，但在当前 serving role 下被绕过（superuser
和 BYPASSRLS role 完全忽略所有 RLS policies）。

### 信号路径（无独立 probe-status 指标）

GoCell **不**把单个 readyz probe 的状态导出为 Prometheus 指标——probe 失败的唯一
运行时信号是 `/readyz` 返回 **503**（→ k8s readiness probe 标记 pod NotReady）。因此本
probe 没有形如 `gocell_..._total` 的专属告警序列；它复用既有的 readyz 可用性信号，再由
运维查 `/readyz?verbose` 的 `dependencies.postgres_app_role_restricted_ready` 字段定位
具体失败原因。

### GoCellPostgresAppRoleNotRestricted（blackbox /readyz）

若部署了 blackbox_exporter 探测 `/readyz`（推荐用于 RLS-enabled assembly），用真实的
`probe_success` 序列告警；不要引用不存在的 `gocell_readyz_check`。`probe_success == 0`
表示 readyz 整体 503（可能由本 probe 或其它依赖触发），运维须查 `/readyz?verbose` 区分：

```yaml
# 需 blackbox_exporter module 指向 <health-listener>/readyz（job=gocell-readyz）
- alert: GoCellReadyzDown
  expr: probe_success{job="gocell-readyz"} == 0
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "Serving readiness failing — inspect /readyz?verbose"
    description: |
      /readyz is returning 503. Inspect /readyz?verbose and check the
      dependencies map. If postgres_app_role_restricted_ready is unhealthy, the
      serving PostgreSQL role is a superuser or has BYPASSRLS — RLS policies are
      defined on all seven tenant tables (incl. policies, migration 059) but bypassed at runtime (cross-tenant leak).
      Remediation for postgres_app_role_restricted_ready:
        1. Point GOCELL_CONFIGCORE_DATABASE_URL at role gocell_app (NOSUPERUSER NOBYPASSRLS).
        2. Ensure deploy/postgres/init/10-restricted-role.sh ran on the target DB
           (role must exist before corebundle connects).
        3. Restart corebundle; the probe turns green within the first readyz cycle.
      See: docs/architecture/202606071200-1676-adr-restricted-app-serving-pool.md
           docs/ops/local-docker-deploy.md §Dual-role PostgreSQL
```

> k8s 部署若已对 pod NotReady（readiness-probe 失败）配置告警，本 probe 失败会一并触发
> 该告警；blackbox 规则是给从外部黑盒探测 `/readyz` 的部署用的补充手段。

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
