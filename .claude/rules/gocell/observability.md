# 可观测性规范

## 日志

| Level | 使用场景 |
|-------|----------|
| Error | 正确性、安全或持久化失败 |
| Warn | 降级运行、重试预算耗尽 |
| Info | 生命周期、迁移、consumer 加入 |
| Debug | 本地诊断，生产默认关闭 |

禁止 Debug dump 完整请求、响应或 payload。错误日志必须带结构化关联字段。

## Redaction

errcode 的 Message、Public Details、Internal Details 三层分工见
`docs/architecture/202605051730-adr-errcode-message-pii-safety.md`。

trace span 和 slog sink 都必须 fail-closed redaction：

- span error 统一走 `pkg/redaction.RedactError`。
- span string attribute 先按 key 判敏感，再做 free-form scrub。
- slog sink 对敏感 attr 做统一清洗。
- last_error 持久化走同一 redaction 包。

没有业务 opt-out。需要原始诊断时走受控服务端日志，不写入 trace 或 wire。

## Readyz probe

- 依赖可用性 probe 用 `_ready` 后缀。
- 运行时操作 probe 不带 `_ready`。
- probe 名是运维契约，改名必须同步 docs/ops、dashboard、alert。
- cell repo readiness 由 cell 边界显式注册，禁止静默吞掉缺失 repo。

## Metrics cell label

HTTP 与 gRPC metrics 的 `cell` label 必须来自 closed set。合法值是 assembly
声明的 cell 集合；缺失、未知、越界归 `_runtime` 或 fail-fast，具体由 sealed resolver
定义。禁止业务代码手写裸 string label。

gRPC unary 和 stream interceptor 顺序必须保证 cell attribution 在 metrics/access log
之前完成。

## Cross-cell transport

跨 cell 同步（http）contract 调用经 `runtime/transport.CellTransport` seam 时，必须按
`transport_mode ∈ {in_proc, remote}` 二值区分——`cell_transport_requests_total{transport_mode, outcome}`
metric label + trace span attribute（ADR `202606131142-1423` D4：「透明」不得变成「不可诊断」）。
`transport_mode` 是 sealed `transport.TransportMode`（unexported 字段 + `ModeInProc()`/`ModeRemote()`
唯一构造），值集由类型系统闭合——包外不可 mint 第三值，metric Record 取 typed 参数故裸 string 不可
表达（Hard）。二值天然低基数，故 metrics **可**按 transport_mode 过滤（非 trace-only）。L0 cell 不经
此 seam 调用，豁免。新增 mode 须同步 `allTransportModes` 注册表（anti-vacuity）+ 本节。

第二个 label `outcome` 区分分发结局（#1966 review P2.6）：**每次**分发都 Record（成功 + 每条失败
出口），不只成功路径——否则失败率被低报。`outcome` 是 sealed `transport.TransportOutcome`
（unexported 字段 + accessor 唯一构造），闭值集 = `{success, dial_error, timeout, canceled,
resolver_error, rewrite_error}`：success 含任意 HTTP 响应（5xx 在传输层仍是 success，由调用方决定
重试/熔断）；失败 kind 与 remote transport 的 errcode Kind 同源分类（timeout→504 / canceled→499 /
dial→503，见 P2.8）。`error.type` 超出该有界 kind 的细节留在 trace span（`span.RecordError`），
不进 metric label，保持低基数（success + 5 个有界失败 kind × 二值 mode = ≤12 series）。新增 outcome
须同步 `allTransportOutcomes` 注册表（anti-vacuity，`TestTransportOutcome_FrozenRegistry`）+ 本节。

**span tracer 单源（#2251 P1.3，D4 span 半闭合）**：remote 调用的 span 与 metrics 同源——
metrics + tracer 由 sealed `transport.CrossCellObs`（unexported 字段，唯一 minter =
`composition.Builder`）捆绑承载，`celltransport.Resolve` 收**一个**预建捆绑参（非分离的 metrics +
tracer），故「接 metrics 忘 tracer」在 Resolve 边界类型级不可表达（Hard）。tracer 单源 = `SharedDeps.Tracer`：
Builder 同时 append `bootstrap.WithTracer`（router + in-proc + event-router）并注入捆绑（remote），
保证 remote 与 in-proc span 用同一 tracer。`SharedDeps.Tracer` 可为 nil（无 tracing，降级 NoopTracer）；
今生产 seam-only（未接真 otel）。

**remote peer readiness（#2251 P2.7）**：split topology 下 `celltransport.Resolve` 的 remote 分支
经 `ModuleResult.Resources` 注册一个 `<cell>_remote_ready` readiness probe（typed
`healthz.RemoteCellReadyProbeName`，`_ready` 依赖可用性约定）。probe 只对 resolved endpoint 做
**TCP dial**（`transport.EndpointDialTarget` 解析，与 rewriteToAbsolute 同源），**不**打远端
`/readyz`——cascade-safe：避免 A↔B 互探 readiness 死锁。peer 不可达 → 本 cell `/readyz` 降级（运维
据此摘流量），但只进 readiness aggregator、**不** kill liveness。更丰富的 peer health（HTTP /readyz
深度探测、多副本、依赖环检测）归 EPIC「类 k8s cell 运行时管理」。

## Redis namespace

Redis key namespace 使用 owner 维度表达：cell、role、resource。禁止把 service token、
outbox、projection 等跨域 key 混入 `_runtime` 前缀而丢失所有权。

**`_runtime` 哨兵 carve-out（sanctioned shared-infra）**：少数**框架级、无 cell 上下文**的
shared-infra 原语显式使用 `_runtime` 哨兵——当前仅 outbox 消费幂等 claimer
（`_runtime:{eventID}:lease|done`）与 HTTP 幂等 store（`_runtime:<tenant>:{key}:resp|lease|fp`）。
二者 key 格式**结构性互斥**（不同段数/段义），故共用哨兵**不丢所有权**、不冲突——这正是上文禁令
真正要防的（careless 混入致归属不可辨），而非禁止任何 `_runtime` 使用。单源在
`adapters/redis/keyns.go`（命名空间值集 + 格式校验）与 `cellmodules/replaydeps`
（claimer/nonce resolver，#825/#2017）。新增 shared-infra 原语欲用 `_runtime` 必须：key 格式与上述
两者结构性互斥 + 在此登记。否则用显式 role/resource namespace。

## Readyz verbose

verbose readyz 输出分四通道：wire 响应、server log、trace、metrics。wire 必须裁剪敏感
error；server log 是主诊断通道；trace 默认跳过 health endpoint。

## Outbox envelope

trace、correlation、principal、occurred_at 等 envelope 字段由 `outbox.NewEntry` 和
sealed option 注入。业务不得通过 metadata 伪造 reserved key。

## Reconcile / idempotency / adapter metrics

metric label 值集必须冻结或经 typed enum 入口。新增 label value 同步更新 schema、
tests 和 docs/ops。高 cardinality 输入不能直接进入 label。

## Audit

audit payload 中的 replayable PII 必须 hash 或 redaction。trace 反查复用 auditquery
标准分页入口，不新增后门 endpoint。审计字段写入位置由 archtest 守卫，规则文件只保留约束摘要。
