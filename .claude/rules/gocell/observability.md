# 可观测性规范

## 日志

| Level | 使用场景 |
|-------|----------|
| Error | 正确性、安全或持久化失败 |
| Warn | 降级运行、重试预算耗尽 |
| Info | 生命周期、迁移、consumer 加入 |
| Debug | 本地诊断，生产默认关闭 |

日志使用 `slog` 和结构化字段。禁止 Debug dump 完整请求、响应或 payload。
错误日志必须带与当前上下文匹配的结构化定位字段，敏感值必须先清洗。
request、tenant、cell、correlation 在对应上下文存在时必须透传；启动期、
全局错误和工具路径使用 service、component、operation、error 等可定位字段。

## Redaction

errcode 的 Message、Public Details、Internal Details 三层分工见
`docs/architecture/202605051730-adr-errcode-message-pii-safety.md`。

trace span、slog sink 和持久化 `last_error` 都必须 fail-closed redaction：

- span error 统一走 `pkg/redaction.RedactError`。
- span string attribute 先按 key 判敏感，再做 free-form scrub。
- slog sink 对敏感 attr 做统一清洗。
- `last_error` 持久化走同一 redaction 包。

没有业务 opt-out。需要原始诊断时走受控服务端日志，不写入 trace 或 wire。

## Readyz Probe

- 依赖可用性 probe 用 `_ready` 后缀。
- 运行时操作 probe 不带 `_ready`。
- probe 名是运维契约，改名必须同步 docs/ops、dashboard、alert。
- cell repo readiness 由 cell 边界显式注册，禁止静默吞掉缺失 repo。
- remote peer readiness 只探测 resolved endpoint 的 TCP 可达性，不反向调用对端 `/readyz`。
- peer 不可达只影响 readiness，不影响 liveness。

verbose readyz 输出分 wire 响应、server log、trace、metrics 四通道。wire 必须裁剪敏感
error；server log 是主诊断通道；trace 默认跳过 health endpoint。

## Metrics Label

metric label 值集必须冻结或经 typed enum 入口。新增 label value 同步更新 schema、
tests 和 docs/ops。高 cardinality 输入不能直接进入 label。

### HTTP Metrics cell Label

HTTP Metrics `cell` Label 与 gRPC metrics 的 `cell` label 必须来自 assembly 声明的
closed set。缺失、未知、越界归 `_runtime` 或 fail-fast，具体由 sealed resolver 定义。
禁止业务代码手写裸 string label。

gRPC unary 和 stream interceptor 顺序必须保证 cell attribution 在 metrics 和 access log
之前完成。

### Reconcile Metrics result Label

`reconcile_total{result}` 的 result 值集必须闭合；新增或改名必须同步 schema、
tests、dashboard、alert 与 emit site。

### HTTP Idempotency state Label

`idempotency_requests_total{state}` 的 state 值集必须闭合；新增或改名必须同步 schema、
tests、dashboard、alert 与 middleware emit site。

adapter、webhook、MQTT 等 metrics 也遵守同一 label 闭值集规则。

## Cross-cell Transport

跨 cell 同步 HTTP contract 调用经 `runtime/transport.CellTransport` seam 时，必须记录：

- `transport_mode`：仅允许 `in_proc`、`remote`。
- `outcome`：每次分发都记录，不能只记录成功路径。

`transport_mode` 与 `outcome` 都必须通过 sealed typed value 表达。metric label 保持低基数；
超出闭值集的错误细节只写 trace span，不进入 metric label。

remote 调用的 metrics 和 tracer 必须同源注入。`SharedDeps.Tracer` 为空或 typed-nil 时统一降级
NoopTracer，禁止在 remote span start 边界裸用 `== nil` 判断。

## Redis Namespace

Redis key namespace 使用 owner 维度表达：cell、role、resource。禁止把 service token、
outbox、projection 等跨域 key 混入 `_runtime` 前缀而丢失所有权。

`_runtime` 只用于框架级、无 cell 上下文的 shared-infra 原语。当前允许：

- outbox 消费幂等 claimer：`_runtime:{eventID}:lease|done`
- HTTP 幂等 store：`_runtime:<tenant>:{key}:resp|lease|fp`

新增 shared-infra 原语若使用 `_runtime`，key 格式必须与既有格式结构性互斥，并在本节登记。
否则使用显式 role/resource namespace。

## Outbox Envelope

trace、correlation、principal、occurred_at 等 envelope 字段由 `outbox.NewEntry` 和
sealed option 注入。业务不得通过 metadata 伪造 reserved key。

## Audit

audit payload 中的 replayable PII 必须 hash 或 redaction。trace 反查复用 auditquery
标准分页入口，不新增后门 endpoint。审计字段写入位置由 archtest 守卫，规则文件只保留约束摘要。
