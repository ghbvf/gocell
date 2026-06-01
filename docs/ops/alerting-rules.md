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

> 该信号"每个 drop 路径必记"由 archtest `MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01` 机器守卫，
> 不会被代码改动静默移除。真正的 no-loss 保证应由消费 cell 在本地事务捕获 poison 消息
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
# are absent for the same cell — the "stuck skipping, not advancing" signal #1109
# was created to surface without log scraping.
#
# `unless` (set difference) — NOT `and ... == 0`: the worst case this alert
# targets is a coordinator that NEVER drives, in which case the
# gocell_saga_drive_total{result="ok"} series does not exist at all. With
# `and ... == 0` the right side is an empty vector and the alert silently never
# fires. `unless <right> > 0` keeps every contended-heavy cell that has no
# matching cell with a positive ok-drive rate — including absent series.
- alert: GoCellSagaInstanceStuckSkipping
  expr: |
    sum(rate(gocell_saga_leader_elect_skip_total{reason="contended"}[5m])) by (cell) > 0.1
    unless
    sum(rate(gocell_saga_drive_total{result="ok"}[5m])) by (cell) > 0
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Saga instances skipping (contended) but not advancing"
    description: "Cell {{ $labels.cell }} has sustained leader-elect contended skips with zero successful drives over 10min. A peer may hold a stale distlock. Runbook: docs/ops/saga-runbook.md"
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
