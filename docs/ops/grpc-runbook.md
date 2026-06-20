# gRPC 运维 Runbook

GoCell gRPC 监听器（默认 `:9095`，always-on，承载 `grpc.auth.session.verify.v1`
admin/super-admin JWT 令牌自省）的故障诊断与处置手册。三个互补载体：

- **本文件** — 故障场景的诊断命令、决策树、人工处置流程。
- `docs/ops/alerting-rules.md` §gRPC Metrics `cell` Label 和 §gRPC 保护拒绝计数器 — 告警规则与 SLO 注意事项。
- `docs/ops/listener-topology.md` §Default Addresses — 端口与 NetworkPolicy 部署建议。

相关参考：

- `docs/ops/env-vars.md` §gRPC Listener — 环境变量完整清单。
- `docs/ops/graceful-shutdown-k8s.md` — shutdown 预算公式与 `bootstrap_shutdown_phase_duration_seconds` 指标。
- `docs/ops/readyz.md` §Saga probes — `grpc_ready` probe 语义。
- ADR `docs/architecture/202605260000-adr-grpc-transport-adapter.md` — gRPC 传输层设计决策。

> **网络暴露警告**：gRPC 监听器承载 admin/super-admin JWT 令牌自省，属集群内部接口。
> 不得通过公网 LoadBalancer 或 Ingress 暴露 `:9095`。在 NetworkPolicy 中将该端口限定到
> 授权 caller pods（参见 `docs/ops/listener-topology.md` §Kubernetes NetworkPolicy）。

---

## 场景 1：启动失败 — TLS 证书缺失或格式错误

**症状**：进程启动即退出，日志含形如下面的错误：

```
durable mode requires gRPC TLS (set GOCELL_GRPC_TLS_CERT_FILE + GOCELL_GRPC_TLS_KEY_FILE,
optionally GOCELL_GRPC_TLS_CLIENT_CA_FILE for mTLS) or an explicit
GOCELL_GRPC_ALLOW_INSECURE=true to run plaintext behind a TLS-terminating sidecar
```

或者：

```
read GOCELL_GRPC_TLS_CERT_FILE: open /path/to/tls.crt: no such file or directory
```

**根因**：`grpclistener.tlsConfigFromEnv` 按 `outbox.DurabilityMode` fail-closed：

- `DurabilityDurable`（`GOCELL_ADAPTER_MODE=real` + `GOCELL_CELL_ADAPTER_MODE=postgres`）：
  未设 TLS 证书且未设 `GOCELL_GRPC_ALLOW_INSECURE=true` → 启动即拒绝。
- `DurabilityDemo`（dev / memory 模式）：允许明文，但 adapter 在非 loopback 地址上明文
  绑定时会输出 Warn 日志。

**诊断**：

```bash
# 检查环境变量是否注入
printenv GOCELL_GRPC_TLS_CERT_FILE GOCELL_GRPC_TLS_KEY_FILE GOCELL_GRPC_ALLOW_INSECURE

# 检查文件是否存在且可读（cert + key 必须同时设置）
test -r "$GOCELL_GRPC_TLS_CERT_FILE" && echo "cert OK" || echo "cert NOT FOUND"
test -r "$GOCELL_GRPC_TLS_KEY_FILE"  && echo "key OK"  || echo "key NOT FOUND"

# 检查 PEM 格式
openssl x509 -in "$GOCELL_GRPC_TLS_CERT_FILE" -noout -subject -dates
openssl rsa  -in "$GOCELL_GRPC_TLS_KEY_FILE"  -check -noout
```

**决策树**：

1. **durable/real 模式 + 自管 TLS**：同时设置 `GOCELL_GRPC_TLS_CERT_FILE` 和
   `GOCELL_GRPC_TLS_KEY_FILE`（cert + key 必须成对，缺一即拒）。可选加
   `GOCELL_GRPC_TLS_CLIENT_CA_FILE` 启用 mTLS（见场景 2）。

2. **durable/real 模式 + TLS 终止侧车**（如 Envoy / Istio / Linkerd）：设置
   `GOCELL_GRPC_ALLOW_INSECURE=true`。**仅在确认有 TLS 终止侧车的情况下**使用；
   否则流量以明文在网络上传输。

3. **dev/demo 模式**：无需操作，默认明文，non-loopback 地址会有 Warn 日志，属预期行为。

---

## 场景 2：mTLS 握手失败

**症状**：客户端收到 TLS 握手错误；服务端 slog 出现 `tls: bad certificate` 或
`certificate signed by unknown authority`。`grpc_ready` probe 仍健康（监听器已启动），
但具体 RPC 调用失败。

**前提**：本场景仅在 `GOCELL_GRPC_TLS_CLIENT_CA_FILE` 已配置时适用。该变量指向一个
PEM 格式的 CA 证书文件，用于验证 gRPC 客户端证书。未设置时，服务端不验证客户端证书。

**根因**：

- 客户端未提供证书，或证书被 CA 拒绝。
- `GOCELL_GRPC_TLS_CLIENT_CA_FILE` 指向的 CA 证书与客户端证书链不匹配。
- 证书已过期（服务端证书或客户端证书）。

**诊断**：

```bash
# 检查客户端 CA 文件是否有效
test -r "$GOCELL_GRPC_TLS_CLIENT_CA_FILE" && echo "CA OK" || echo "CA NOT FOUND"
openssl x509 -in "$GOCELL_GRPC_TLS_CLIENT_CA_FILE" -noout -subject -dates

# 检查服务端证书有效期
openssl x509 -in "$GOCELL_GRPC_TLS_CERT_FILE" -noout -dates

# 从外部测试 TLS 握手（需要 grpc_cli 或 openssl）
openssl s_client -connect <host>:9095 -CAfile "$GOCELL_GRPC_TLS_CLIENT_CA_FILE" \
  -cert <client-cert.pem> -key <client-key.pem> -showcerts </dev/null

# 或用 grpcurl（不验证证书，仅测通达性）
grpcurl -insecure <host>:9095 list
```

**处置**：

1. 证书过期：替换证书文件后重启进程（进程启动时读取 PEM 文件，不支持热更新）。
2. CA 不匹配：在 `GOCELL_GRPC_TLS_CLIENT_CA_FILE` 中包含完整的客户端 CA 链；
   或更换客户端证书使其由正确 CA 签发。
3. 临时放宽：将 `GOCELL_GRPC_TLS_CLIENT_CA_FILE` 清空（取消 mTLS），改为仅服务端 TLS；
   操作后须计划恢复。

---

## 场景 3：启动失败 — 端口绑定失败

**症状**：进程启动即退出，日志含：

```
grpc: bind :9095: bind: address already in use
```

或：

```
grpc: bind :9095: permission denied
```

**根因**：`GOCELL_GRPC_ADDR` 所指定的地址（默认 `:9095`）被占用，或进程无权绑定该端口。

**诊断**：

```bash
# 查看是否有进程已占用 9095
lsof -i :9095
# 或
ss -tlnp | grep 9095

# 确认当前配置的地址
printenv GOCELL_GRPC_ADDR   # 未设则为 :9095

# 检查端口格式是否合法（必须是 net.Listen("tcp", ...) 接受的形式）
# 错误示例: "9095"（缺冒号）、"::" （缺端口）
```

**处置**：

1. **端口被占**：先用 `lsof` 确认占用方，判断是僵尸进程（`kill`）还是另一个 gocell
   实例（配置冲突）。调整 `GOCELL_GRPC_ADDR` 使用空闲端口。
2. **权限不足**：低编号端口（< 1024）在 Linux 需要 `CAP_NET_BIND_SERVICE`；
   建议使用 1024 以上的端口，或通过 `setcap` 赋权。
3. **地址格式错误**：`GOCELL_GRPC_ADDR` 必须是 `host:port` 形式，如 `:9095`、
   `0.0.0.0:9095`、`127.0.0.1:9095`。

---

## 场景 4：优雅关闭超时 — gRPC drain 阻塞

**症状**：进程收到 SIGTERM 后，超过预期 shutdown 时间才退出；或在 K8s 中被 SIGKILL
强制终止（`OOMKilled=false` 而 exit code 为非零）。可能伴随指标：

```
gocell_bootstrap_shutdown_phase_duration_seconds{phase="lifo_teardown"} 高水位
gocell_bootstrap_shutdown_total{outcome="timeout"} > 0
```

**根因**：gRPC 服务端通过 `grpc-go`  `GracefulStop()` 等待 in-flight RPC 完成。`GracefulStop`
本身只等待，不主动取消 handler context。框架通过 `DrainSignal` 关闭 gap：`Trigger()` 在
`GracefulStop` 开始时触发，让 `StreamDrain` 拦截器取消活跃流的 context，使 handler 能感知
`ctx.Done()` 提前返回。如果某个 handler 不响应 ctx 取消（例如阻塞在外部 IO、不传播 ctx），
就会卡住 drain 预算。

gRPC drain 在 bootstrap phase10 stage2 执行，预算与 HTTP drain 分离：默认继承全局
`shutdownTimeout`（默认 30s）。总 shutdown 预算公式：

```
terminationGracePeriodSeconds >= 2 × shutdownTimeout + 10s  # 默认 >= 70s
```

详见 `docs/ops/graceful-shutdown-k8s.md`。

**诊断**：

```bash
# 查看 shutdown 阶段耗时（p99，按 phase label 区分）
# gRPC drain 计入 lifo_teardown
promql: histogram_quantile(0.99, rate(gocell_bootstrap_shutdown_phase_duration_seconds_bucket{phase="lifo_teardown"}[10m]))

# 查看 shutdown outcome 分布
promql: gocell_bootstrap_shutdown_total

# 在进程日志中定位 gRPC drain 相关条目
kubectl logs <pod> | grep -E 'grpc.*drain|grpc.*stop|grpc.*shutdown'

# 定位 in-flight 阻塞的 RPC method（仅在 drain 窗口内）
# grpc_server_requests_total 在 RPC 完成时才计数；drain 期间停止增长的 method 即阻塞方
promql: rate(gocell_grpc_server_requests_total[1m])
```

**处置**：

1. **偶发、随后正常退出**：drain 预算偏紧，通过 `bootstrap.WithGRPCListenerShutdownGrace`
   延长单个 listener 的 drain 预算，或通过 `bootstrap.WithShutdownTimeout` 加大全局预算，
   并同步更新 K8s `terminationGracePeriodSeconds`。
2. **反复出现**：排查阻塞的 handler 是否传播 ctx。handler 应在 `select` 中监听
   `ctx.Done()` 并提前返回。框架 `DrainSignal` 只取消 server-streaming RPC 的流 ctx；
   unary RPC 取消依赖 handler 自身在 `GracefulStop` 超时前完成或响应 ctx。
3. **K8s 被 SIGKILL**：将 `terminationGracePeriodSeconds` 调高至 `2 × shutdownTimeout + 10s`。

---

## 场景 5：grpc_ready probe 不健康

**症状**：`GET /readyz?verbose=true`（需要 `X-Readyz-Token` header）返回：

```json
"dependencies": {
  "grpc_ready": { "status": "unhealthy", "duration_ms": ... }
}
```

`/readyz` 返回 503，kubelet 将 Pod 标记 NotReady。

**probe 语义**：`grpc_ready` 是 `adapters/grpc.Server` 暴露的依赖可用性 probe（`_ready` 后缀
= 依赖可用性 probe，见 `.claude/rules/gocell/observability.md`）。它报告 gRPC server 是否处于
serving 状态（`grpc.Server.GetServiceInfo()` 成功 + 服务已注册）。unhealthy 含义：gRPC 监听器
未进入 serving 状态，或已主动停止（drain 中）。

probe 名 `grpc_ready` 在所有 gRPC 监听器（当前只有一个）的探针之间 AND 聚合——任一不健康
即 unhealthy。不能通过 probe 区分哪个监听器失败，需通过日志（`"grpc: server is not serving"`）
定位。

**诊断**：

```bash
# 确认 grpc_ready 当前状态
curl -s -H "X-Readyz-Token: $GOCELL_READYZ_VERBOSE_TOKEN" \
  "http://127.0.0.1:9091/readyz?verbose=true" | jq '.data.dependencies.grpc_ready'

# 查看 gRPC server 日志
kubectl logs <pod> | grep -E 'grpc.*serving|grpc.*not serving|grpc.*bind'

# 确认监听器是否在目标端口监听
ss -tlnp | grep $(printenv GOCELL_GRPC_ADDR | tr -d ':')
```

**处置**：

- **启动阶段**：若进程仍在启动，等待 phase7b（gRPC serve phase）完成；未完成前 probe 不健康
  是预期行为。
- **启动后立即失败**：通常是 TLS 或 bind 错误，见场景 1 / 场景 3。
- **drain 中**：进程收到 SIGTERM 后 probe 翻红（drain 开始），这是 K8s 从 LB 摘除 Pod 的
  正常信号，不是故障。

---

## 场景 6：gRPC 请求错误率偏高

**症状**：

```promql
rate(gocell_grpc_server_requests_total{code=~"Internal|Unknown"}[5m]) 持续升高
```

或：

```promql
rate(gocell_grpc_server_requests_total{code="InvalidArgument"}[5m]) > 阈值
```

**指标说明**：

| 指标 | 类型 | 含义 |
|---|---|---|
| `gocell_grpc_server_requests_total{method,code,cell}` | Counter | 按 gRPC method、status code（string 形如 `OK` / `NotFound`）、cell 归属计数 |
| `gocell_grpc_server_request_duration_seconds{method,code,cell}` | Histogram | 单次 RPC 耗时 |

`cell` label 来自 assembly 声明的 closed set（通过 `UnaryCellAttribution` 拦截器），越界
降级到 `_runtime`。业务 SLO 过滤用 `{cell!="_runtime"}`。

**PR-12 errcode 映射迁移注意事项**（`docs/ops/alerting-rules.md` §PR-12）：
`handler` 返回 `*errcode.Error` 后，wire 上已映射到正确 code（非 `Unknown`）：

| code | 触发条件 |
|---|---|
| `InvalidArgument` | 请求参数校验失败 |
| `NotFound` | 资源不存在或资源已永久删除（`KindGone` lossy 映射，见下注） |
| `ResourceExhausted` | 限流保护拒绝（`Deps.RateLimiter` opt-in）或业务 errcode `KindPayloadTooLarge`/`KindRateLimited` |
| `Unavailable` | 熔断器打开（`Deps.Allower` opt-in，用 `gocell_grpc_protection_rejected_total{type="circuit"}` 与真实服务不可用区分） |
| `PermissionDenied` | PDP 授权拒绝 |
| `Unauthenticated` | 未认证 |

> **注（KindGone lossy 映射）**：`codes.NotFound` 可能来自资源不存在（`KindNotFound`），也可能来自资源已永久删除（`KindGone`，lossy 映射至 `NotFound`）。消费者不应仅凭 `codes.NotFound` 判断可重试性，需结合业务 errcode 或 app-level 信号（与 ADR 202605260100 D1 一致）。

以 `{code="Unknown"}` 为错误代理的 SLO 告警在 PR-12 后**可能静默漂移**——建议改为
`{code=~"Internal|Unknown"}` 或按需拆成 per-code 告警。

**诊断**：

```bash
# 按 method / code 分布
promql: sum(rate(gocell_grpc_server_requests_total[5m])) by (method, code, cell)

# p95 延迟
promql: histogram_quantile(0.95, sum(rate(gocell_grpc_server_request_duration_seconds_bucket[5m])) by (le, method, cell))

# 查看 access log 中具体错误（gRPC access log 的状态字段为 code，字符串形式）
kubectl logs <pod> | jq 'select(.msg == "grpc request" and .code != "OK")'
```

**处置**：按具体 code 对应的失败域处置。`Internal` / `Unknown` 须看服务端 slog 中的
`errcode` 结构字段定位根因；`PermissionDenied` 见 `docs/ops/alerting-rules.md`
§GoCellAuthPDPDenyRateHigh。

---

## 场景 7：限流或熔断导致请求被拒绝

**症状**：

```promql
rate(gocell_grpc_protection_rejected_total{type="ratelimit"}[5m]) > 0
rate(gocell_grpc_protection_rejected_total{type="circuit"}[5m]) > 0
```

对应告警：`GRPCRateLimitHighRejectionRate`（warning）、`GRPCCircuitBreakerOpen`（warning），
见 `docs/ops/alerting-rules.md` §gRPC 保护拒绝计数器。

**指标说明**：

| `type` | 来源 | 触发条件 | gRPC code |
|---|---|---|---|
| `ratelimit` | `interceptor.Deps.RateLimiter`（opt-in） | `Allow(key)` 返回 false | `ResourceExhausted` |
| `circuit` | `interceptor.Deps.Allower`（opt-in） | `Allow()` 返回 false（断路器 open） | `Unavailable` |

**opt-in 说明**：`RateLimiter` 和 `Allower` 是 `interceptor.Deps` 的可选字段。当字段为
`nil` 时，对应拦截器是透明 pass-through，不拒绝任何请求，也不发射拒绝计数。当前
`cmd/corebundle` 未注入这两个字段，即平台 assembly **默认不启用**限流/熔断。组装自定义
assembly 的消费方可在 composition root 向 `interceptor.Deps` 传入实现了 `RateLimiter`
（`Allow(key string) bool`）/ `Allower`（`Allow() bool`）接口的对象来启用。

**确认 limiter/allower 是否已接线**：查看 composition root（如 `cmd/corebundle/run.go`）
构造 `interceptor.Deps{}` 时是否包含 `RateLimiter` / `Allower` 字段。若这两个字段未出现在
`Deps{}` 字面量中，则对应拦截器为 pass-through，`gocell_grpc_protection_rejected_total`
无 sample 属预期行为，不代表保护功能故障。

**诊断**：

```bash
# 区分拒绝类型
promql: sum(rate(gocell_grpc_protection_rejected_total[5m])) by (type, method, cell)

# 限流：确认是否客户端突增
promql: rate(gocell_grpc_server_requests_total[1m])

# 熔断：区分保护拒绝与真实服务不可用
# circuit 打开 → Unavailable 上升，但这是保护行为不是故障
promql: rate(gocell_grpc_server_requests_total{code="Unavailable"}[5m])
# 与 protection 拒绝对比：若两者数量接近，Unavailable 基本都是熔断；
# 若 server_requests Unavailable >> protection circuit，则有非熔断的真实不可用。
```

**处置**：

1. **限流 spike（`type="ratelimit"`）**：确认是正常流量突增还是客户端配置错误。
   调整 `RateLimiter` 参数（在对应 composition root 的 `interceptor.Deps` 注入处）；
   若是异常流量，结合上层网关限速。

2. **熔断打开（`type="circuit"`）**：断路器打开期间 `Unavailable` 尖峰是预期的保护行为，
   **不代表服务自身不可用**。先确认下游依赖（DB / Redis / 外部服务）是否有故障。
   `gocell_grpc_protection_rejected_total{type="circuit"}` 持续 > 0 且下游恢复后仍高，
   检查 `Allower` 的半开/恢复配置（由消费方在 composition root 决定）。

3. **当前平台 assembly 未启用保护**：若指标没有任何 sample，说明 `RateLimiter`/`Allower`
   均未注入，保护未启用——这是预期行为，不需处置。

---

## 场景 8：gRPC 端口的关闭与网络限制

**问题**：运维人员询问"能否关闭 gRPC 端口 9095？"

**架构约束**：`:9095` 是 always-on 监听器。`accesscore` cell 无条件注册
`grpc.auth.session.verify.v1` 服务，若 gRPC 监听器未接线，bootstrap **fail-fast**（
`checkOrphanGRPCServices` 检查），进程无法启动。

**框架不提供**通过 env 禁用 gRPC 监听器的开关。

**实际可用的限制手段**：

1. **NetworkPolicy（推荐）**：限制 `:9095` 的 ingress 只允许特定 caller pods。这是
   `docs/ops/listener-topology.md` §Kubernetes NetworkPolicy 中的推荐做法：

   ```yaml
   ingress:
     - from:
         - podSelector:
             matchLabels:
               gocell.io/caller-cell: accesscore
       ports:
         - protocol: TCP
           port: 9095
   ```

2. **绑定到 loopback**：设置 `GOCELL_GRPC_ADDR=127.0.0.1:9095`，使监听器仅接受来自
   同一 Pod 内部的连接，无法被其他 Pod 访问。此方式下跨 Pod 客户端无法访问该接口。

3. **不支持的方式**：无法通过任何 env 变量完全禁用 gRPC 监听器。

---

## 可观测信号速查

| 信号 | 类型 | 用途 |
|---|---|---|
| `grpc_ready` | probe（`/readyz?verbose`） | gRPC 服务端 serving 状态 |
| `gocell_grpc_server_requests_total{method,code,cell}` | counter | RPC 计数，按 method/code/cell 分组 |
| `gocell_grpc_server_request_duration_seconds{method,code,cell}` | histogram | RPC 耗时 |
| `gocell_grpc_protection_rejected_total{type,method,cell}` | counter | 限流/熔断拒绝计数，`type` 闭值集：`ratelimit` / `circuit` |
| `gocell_bootstrap_shutdown_phase_duration_seconds{phase="lifo_teardown"}` | histogram | LIFO teardown（含 gRPC drain）阶段耗时 |
| `gocell_bootstrap_shutdown_total{outcome}` | counter | shutdown 结果（`success`/`teardown_error`/`timeout`/`signal_error`） |

### 常用 PromQL

```promql
# gRPC 错误率（排除运行时哨兵）
sum(rate(gocell_grpc_server_requests_total{code!="OK", cell!="_runtime"}[5m])) by (method, code, cell)

# gRPC p95 延迟
histogram_quantile(0.95,
  sum(rate(gocell_grpc_server_request_duration_seconds_bucket{cell!="_runtime"}[5m]))
  by (le, method, cell)
)

# 保护拒绝速率（按类型区分）
sum(rate(gocell_grpc_protection_rejected_total[5m])) by (type, method, cell)

# shutdown lifo_teardown p99（关注是否超出 shutdownTimeout）
histogram_quantile(0.99,
  sum(rate(gocell_bootstrap_shutdown_phase_duration_seconds_bucket{phase="lifo_teardown"}[10m]))
  by (le)
)
```
