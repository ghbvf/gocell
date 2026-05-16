# GoCell 框架能力盘点

> 系列文档 2/4 · 配套文档：[001 DDD 适用范围](./202605131500-001-ddd-scope-analysis.md) · [003 交互模式](./202605131500-003-capability-interaction-patterns.md) · [004 缺口分析](./202605131500-004-capability-gap-analysis.md)

按"能力域"组织（而非目录），每条注明承载层与关键 API。

## A. 应用模型 / Cell 框架（kernel/）

| 能力 | 承载 | 说明 |
|---|---|---|
| **Cell / Slice 声明模型** | `cell.yaml` / `slice.yaml` + `kernel/metadata` | 必填 id / type / consistencyLevel / owner / verify；对标 K8s declarative |
| **Cell 生命周期** | `kernel/cell.{Cell, BaseCell, Init, Start, Stop}` + `lifecycle.go` | Init/Stop 对称契约；phase 化启动（bootstrap phase 0–7） |
| **Registry builder** | `kernel/cell.Registry`：`MountRouteGroup` / `Subscribe` / `RegisterContract` | Init 期声明意图，bootstrap drain 到 router |
| **Outbox / EventBus** | `kernel/outbox.{Entry, EntryHandler, HandleResult, Ack/Requeue/Reject, ConsumerBase, Emit, Subscription}` | L2 事件 emit + consume；不用碰 idempotency.Claimer，Settlement 由 transport 注入 |
| **Contract Spec** | `kernel/contractspec.ContractSpec{ID, Kind, Transport, Topic}` | 描述订阅 / 发布契约 |
| **Persistence** | `kernel/persistence.TxRunner` / `TxManager` | service 必填 `TxRunner`（nil → `NewXxx` fail-fast） |
| **Clock** | `kernel/clock.Clock` | 测试用 `clockmock` 注入 |
| **Crypto** | `kernel/crypto` | KeyProvider 接口 / envelope 加密原语 |
| **Observability** | `kernel/observability/metrics` | 注册业务 metric（cell label 自动） |
| **State machine vocab** | `kernel/cellvocab` | 一致性等级 L0–L4、disposition 词汇 |
| **Metadata** | `kernel/metadata` | cell.yaml / slice.yaml 元数据访问 |
| **Verify** | `kernel/verify` | smoke / contract / unit verify hook |
| **Idempotency** | `kernel/idempotency.Claimer`（透明） | 由 ConsumerBase 内部使用，handler 不接触 |

## B. 事件 / 消息（kernel/outbox + runtime/eventbus + runtime/outbox）

- **Outbox emit**：`outbox.Emit` 在 service 事务内写入，relay 异步发布
- **PG outbox fencing**：lease_id CAS（`OUTBOX-LEASE-ID-CAS-01`，ADR 202605051600）
- **ConsumerBase**：两阶段 Claim/Commit/Release 幂等 + 退避重试 + DLX 路由
- **HandleResult vocabulary**：`Ack()` / `Requeue(err)` / `Reject(err)`；PermanentError 标签
- **Subscription**：`reg.Subscribe(spec, handler, consumerGroup, cellID, opts...)`，4 参 cellID 位置必填
- **Settlement / Receipt**：transport 层独立注入，handler 无感知
- **EventRouter**：bootstrap 接管所有订阅 goroutine 生命周期
- **Relay collector + metrics**：outbox 发布观测
- **失败开放 tracker**（`failopen_tracker`）：consumer 退化保护

## C. HTTP / API（runtime/http + contracts/http + codegen）

- **Router** (`runtime/http/router`)：`RouteGroup`、`MountRouteGroup`、`MountCodegen`、auth.Mount
- **CellAttribution middleware**：listener-root 注入 `ctxkeys.CellID`，metrics/log/trace 一致
- **Middleware 链**：Recovery、Metrics、Tracing、AccessLog、Auth、RateLimit、CircuitBreaker、BodyLimit、CORS
- **codegen handler**：contract.yaml → typed `Get200JSONResponse` / `*ErrorResponse` 信封（ADR 202605061500）
- **错误兜底**：`pkg/httputil.WriteError` + 共享 `error-response-v1.schema.json`
- **API 版本策略**：`/api/v1/` 前缀；response 只增不删；request strict（FMT-20）

## D. 认证 / 授权（runtime/auth）

| 子能力 | API |
|---|---|
| JWT 签发/校验 | `auth.JWT` + audience/issuer 校验 + intent claim |
| Session | `runtime/auth/session.Store`（mem / redis / pg） |
| Refresh token | `runtime/auth/refresh.Plan` + GC + storetest |
| Service Token | `auth.ServiceToken` + Nonce（NonceStore Redis） |
| Principal / Guard | 角色 / 权限 / Exempt list |
| Authz middleware | `auth.Middleware` + RBAC 校验（rbacassign / rbaccheck slice） |
| Auth Plan | `kernel/cell.AuthPlan` + bootstrap apply/validate/describe |

## E. 数据 / 持久化（adapters/postgres + kernel/persistence）

- **TxManager**：savepoint / nested tx / repanic（C-class panic）
- **adapters/postgres**：driver、迁移、错误映射、tx_manager
- **CAS / 乐观锁**：`runtime/state/cas`
- **ValueTransformer**：`runtime/crypto` envelope 加密落库（DEK/KEK）
- **pg-migrate 工具**：`tools/pg-migrate`
- **Cell-local repo 模式**：`cells/<c>/internal/adapters/postgres/`

## F. 缓存 / 状态（adapters/redis）

- **Cache primitive**（per-cell namespace）
- **IdempotencyClaimer**（`_runtime` sentinel）
- **NonceStore**（role-named namespace）
- **RedisDriver** + KeyNamespace 校验（`REDIS-KEY-NAMESPACE-01`）
- **DistLock**：`runtime/distlock`（leader-elect）

## G. 加密 / 密钥（kernel/crypto + runtime/crypto + adapters/vault）

- **KeyProvider 接口**（local AES / Vault Transit）
- **AEAD utility**（`pkg/aeadutil`）
- **SecureCookie**（`pkg/securecookie`）
- **HMAC key 管理**（bootstrap hmac_key）
- **Vault Transit adapter** + readyz probe `vault_transit_ready`

## H. 可观测性（runtime/observability + kernel/observability）

| 维度 | 能力 |
|---|---|
| **Metrics** | Prometheus provider；`cell` label（业务）/ `_runtime` 哨兵；HTTP/outbox/auth/worker 指标 |
| **Logging** | `slog` 结构化；error/warn/info/debug 级别约束；禁止 Debug dump body |
| **Tracing** | OTEL provider；trace propagation；span error redaction（fail-closed） |
| **Audit Ledger** | `runtime/audit/ledger` hash chain + payload redaction 出站 |
| **Redaction 单源** | `pkg/redaction` (RedactError / RedactPayload) |
| **Readyz probes** | snake_case `_ready` 后缀（rabbitmq_ready / vault_transit_ready） |

## I. 配置 / Feature Flag（cells/configcore + runtime/config）

- **Config publish/read/subscribe/write** slice
- **Feature flag write**（configcore/slices/flagwrite）
- **Config event metrics wiring**（per-cell adapter）
- **热更新**：configsubscribe slice

## J. 外部适配（adapters/）

| 适配 | 用途 |
|---|---|
| postgres | PG driver + tx |
| redis | 4 primitives（Cache / Idempotency / Nonce / Driver） |
| rabbitmq | event bus（AMQP） |
| oidc | OIDC provider 集成 |
| vault | KMS / Transit |
| s3 | 对象存储 |
| websocket | WS adapter |
| otel | OTEL exporter |
| prometheus | metrics exporter |
| circuitbreaker | HTTP 熔断 |
| ratelimit | HTTP / token bucket |

**约束**：cells 不直接 import adapters/，由 `cmd/corebundle` 注入接口实现。

## K. 装配 / 启动（runtime/bootstrap + cmd/corebundle）

- **Bootstrap phases**（phase 0–7）：grace period / managed resource / assembly / events / workers / http / shutdown
- **Shutdown barrier + grace**：metrics 化的 shutdown ordering
- **Dual listener**（business + internal port）
- **Managed resource lifecycle**（reload gate）
- **Module wiring**（access_module / audit_module / config_module / cell_module）
- **Per-cell adapter binding**
- **Assembly.yaml** → 物理拓扑

## L. CLI 工具（cmd/gocell）

| 子命令 | 功能 |
|---|---|
| `gocell validate` | governance 规则全集（FMT / ADV / VERIFY / TOPO / HTTP / REF / strict） |
| `gocell scaffold` | cell / slice / contract / assembly / journey 脚手架 |
| `gocell generate` | codegen 入口（contract / cell / catalog / assembly） |
| `gocell check` | 静态检查聚合 |
| `gocell verify` | 验证 codegen 产物 / drift |
| `gocell export` | 元数据导出 |
| `gocell graph` | 依赖图 |

## M. Codegen funnel（tools/codegen + generated/）

- **contractgen**：HTTP contract → typed handler + Subscription helper
- **cellgen**：cell.yaml + slice.yaml → cell_gen.go（meta / providers）
- **markergen**：marker 派生
- **generatedcatalog**：cell/slice/contract 索引
- **golden test**：scaffold/codegen 输出 byte-equal 校验
- **gofumpt render**：统一格式化

## N. Governance（kernel/governance + tools/archtest + .golangci.yml）

| 工具 | 职责 |
|---|---|
| `kernel/governance` rules | FMT-20 strict request / ADV-06 subscribers / VERIFY-01 contract usage / TOPO / HTTP / REF |
| `tools/archtest` (16-shard process-isolated) | PANIC-REGISTERED / MESSAGE-CONST-LITERAL / DETAILS-SLOG-ATTR / OUTBOX-* / REDIS-KEY-NAMESPACE / EXPORTED-ERROR-NEW / REGISTRY-SUBSCRIBE-CELLID 等 50+ invariant |
| `tools/depgraph` | 跨包依赖闭包 |
| `tools/metricschema` | metric schema drift |
| `tools/generatedverify` | 派生产物 drift |
| `tools/nogo` | go-only lint |
| `tools/e2egate` / `tools/slowgate` | E2E / 慢测分流 |
| `.golangci.yml` | depguard 路径级 import 禁令 |

## O. 错误 / Panic / Redaction 单源（pkg/）

- `pkg/errcode`：三层 redaction（Message const / Details slog.Attr / Internal）
- `pkg/panicregister.Approved(reason, value)`：production panic 唯一形态
- `pkg/redaction`：err + payload 单源
- `pkg/httputil.WriteError`：HTTP 兜底

## P. 测试基础设施（pkg/testutil + */celltest + */authtest + */sessiontest）

- **Clock mock**（`clockmock`）
- **Slog capture**（`sloghelper`）
- **Cell test harness**（`kernel/cell/celltest`）
- **Outbox conformance**（`kernel/outbox/outboxtest`）
- **Auth/Session test helpers**
- **Postgres test container helpers**
- **Codegen golden test framework**

## Cells 可消费的能力清单（按 import 实际统计）

```
kernel/cell, cellvocab, clock, contractspec, crypto, idempotency(透明),
metadata, observability/metrics, outbox, persistence, verify

runtime/audit/ledger, auth, auth/refresh, auth/session, crypto, eventbus,
http/router, observability/metrics, state/cas

pkg/ctxcancel, ctxkeys, errcode, panicregister, pgquery, query, redaction,
testutil/sloghelper, testutil/testtime, validation

adapters/postgres   ← 仅 cell 自有 repo 实现，且仅引用 driver / Tx 类型，不跨 cell
```

## 一致性等级 → 可用结构对照

| 级别 | 典型 import | 结构特征 |
|---|---|---|
| **L0** LocalOnly | `pkg/*`、`kernel/crypto` | 纯函数，无 Tx / 无 outbox |
| **L1** LocalTx | + `kernel/persistence` | 单 cell 事务，handler→service→repo |
| **L2** OutboxFact | + `kernel/outbox` / `runtime/eventbus` | service 在同事务内 `outbox.Emit`，relay 异步发布 |
| **L3** WorkflowEventual | + ConsumerBase + `runtime/state/cas` | 订阅事件投影，幂等 Claim/Commit |
| **L4** DeviceLatent | + `runtime/distlock` + scheduler | 长延迟回执，去重窗口 |

## 强制 invariant（review / archtest 守护，开发者必须知晓）

- **panic** 必走 `panicregister.Approved(...)`（`PANIC-REGISTERED-01`）
- **`errcode.New` message 必须 const literal**（`MESSAGE-CONST-LITERAL-01`）
- **`WithDetails` 只接 `slog.Attr`**（`DETAILS-SLOG-ATTR-01`）
- **service nil TxRunner fail-fast**（`OUTBOX-SERVICE-01`）
- **`reg.Subscribe` 第 4 参数 cellID 位置必填**（`REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01`）
- **HandleResult 字面量构造**仅限 kernel allowlist，业务路径用 `Ack/Requeue/Reject` factory（`OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01`）
- **L2 consumer 必须配 DLX exchange**
- **export 包级 `var Err* = errors.New(...)` 禁用**，必须 `errcode.New`（`EXPORTED-ERROR-NEW-01`）
- **outbox lease CAS** 由 store 透传（`OUTBOX-LEASE-ID-CAS-01`），handler 无感知
- **Redis key namespace** 由构造期 `KeyNamespace.Validate()` 守护（`REDIS-KEY-NAMESPACE-01`）

## 显式范围外（当前未实现）

- 多租户隔离（tenant 维度的 namespace）
- 服务网格 / gRPC adapter（目前 HTTP/AMQP only）
- 分布式追踪 sampling 策略管理
- 多 region / failover 拓扑

详细未实现项 → [004 缺口分析](./202605131500-004-capability-gap-analysis.md)
