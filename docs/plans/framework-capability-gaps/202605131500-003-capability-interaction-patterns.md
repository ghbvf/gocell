# 框架能力的交互模式

> 系列文档 3/4 · 配套文档：[001 DDD 适用范围](./202605131500-001-ddd-scope-analysis.md) · [002 能力盘点](./202605131500-002-framework-capability-inventory.md) · [004 缺口分析](./202605131500-004-capability-gap-analysis.md)

按"典型场景"展示能力如何串联，产生什么效果。

---

## 场景 1：L2 业务请求生命周期（HTTP → 事务 → 事件 → 消费）

最核心的组合：

```
Client
  │  POST /api/v1/sessions
  ▼
[runtime/http listener]
  └─ CellAttribution mw      ← 注入 ctxkeys.CellID="accesscore"（看 RouteGroup ownership）
      └─ Tracing mw          ← span 起点，绑 cell label
          └─ Metrics mw       ← http_requests_total{cell="accesscore"}
              └─ AccessLog mw ← 结构化 slog，含 cell 字段
                  └─ Auth mw (JWT)  ← runtime/auth.Authenticator + JWKS + intent claim
                      └─ Guard mw   ← Principal + RBAC（rbaccheck slice）
                          └─ RateLimit / CircuitBreaker / BodyLimit
                              ▼
              [codegen handler] (Get200JSONResponse / Post201ErrorResponse)
                  ▼
        ┌──────────────────────────────────────────────────────┐
        │  service.go: sessionlogin.Service.Login(ctx, dto)    │
        │    ├─ validation.Struct(dto)                          │
        │    ├─ kernel/persistence.TxRunner.RunTx(ctx, func){   │
        │    │     ├─ ports.UserRepo.FindByEmail(tx, ...)       │
        │    │     ├─ sessionmint.Mint(...)                     │
        │    │     ├─ session.Store.Put(tx, ...)                │
        │    │     └─ outbox.Emit(tx, "session.created.v1",     │
        │    │                    eventID, payload)             │
        │    │           ← 同事务写入 outbox 表（B 类：lease_id) │
        │    └─ 返回 dto                                        │
        └──────────────────────────────────────────────────────┘
                  ▼
              [codegen handler 返回 typed Response]
                  ▼
              Tracing span 关闭（错误经 pkg/redaction.RedactError 后写入）

[runtime/outbox.Relay]（后台 goroutine，独立于请求）
  ├─ ClaimPending(lease_id CAS) → adapters/rabbitmq.Publisher
  ├─ 成功 → MarkPublished(lease_id CAS)
  └─ 失败 → MarkRetry / 退避 → 预算耗尽 → MarkDead → DLX

[Subscriber] auditcore-auditappendsession
  └─ ConsumerBase.Wrap：
      ├─ kernel/idempotency.Claimer.Claim(eventID)  ← Redis IdempotencyClaimer
      │     ↳ KeyNamespace="_runtime"（sentinel）
      ├─ handler(ctx, entry) → outbox.HandleResult
      │     ├─ Ack() → broker Ack → Claimer.Commit
      │     ├─ Requeue(err) → 退避 → Claimer.Release
      │     └─ Reject(err) → Nack(requeue=false) → DLX
      └─ Settlement 由 Subscriber transport 注入（handler 不接触）
```

**产生的作用**：

- **业务原子性**：DB 状态 + 事件发布同事务（outbox pattern），不丢、不重
- **消费幂等**：Claimer 两阶段 + lease CAS，consumer 崩溃/重投不重复处理
- **完整可观测**：同一 trace 跨 HTTP → tx → relay → consumer；metrics 标 cell；error 经 redaction
- **失败兜底**：瞬态走重试，永久走 DLX，预算耗尽自动转 Reject

---

## 场景 2：启动装配（bootstrap 把所有 phase 串起来）

```
cmd/corebundle/main.go
  └─ bundle_options.go → bundle_assembly.go
      ▼
runtime/bootstrap.Run(ctx, opts):

  Phase 0: Grace period               ← 给依赖一个稳定时间窗
  Phase 1: Managed resources          ← Vault/Redis/PG/RabbitMQ 连接 + readyz 接线
            ├─ KeyProvider (Vault Transit 或 local AES)
            ├─ HMAC key 加载
            ├─ ValueTransformer 装配
            └─ adapter readyz probe (vault_transit_ready, rabbitmq_ready ...)
  Phase 2: Assembly → 解析 assembly.yaml
            └─ Topology：cell → listener → namespace 归属
  Phase 3: Cell.Init(ctx, registry)
            ├─ 各 Cell 在 Init 内调用 reg.MountRouteGroup / reg.Subscribe
            ├─ Registry 收集 RegistrySnapshot
            └─ 校验 cellID positional / contract spec / cell.yaml 一致
  Phase 3b: AuthPlan apply              ← 各 cell 声明 auth 要求 → 编译为 middleware
  Phase 4: Events
            └─ drainCellSubscriptions：snapshot → EventRouter
                ↳ EventRouter 起 goroutine + ConsumerBase 包装
  Phase 5: Workers (runtime/worker)
            └─ outbox.Relay / refresh GC / audit verify 等后台 worker
  Phase 6: HTTP listeners
            ├─ business listener (8080)：业务 RouteGroup
            ├─ internal listener (8090)：/metrics /healthz /readyz /debug
            └─ CellAttribution middleware 安装
  Phase 7: Health aggregation
            └─ 聚合 cell.Health + adapter readyz → /readyz

Shutdown：逆序 + shutdown barrier + grace + metrics 化（shutdown_duration_seconds）
```

**产生的作用**：

- **确定性启动**：每 phase 有 fail-fast 校验（cellID positional / TxRunner nil / KeyNamespace.Validate 都在构造期触发）
- **声明对齐闭环**：slice.yaml.contractUsages ↔ contract.yaml.endpoints ↔ Init 期 reg.Subscribe，运行前已检查
- **依赖可观测**：readyz 把 adapter 故障映射到 K8s probe，运维直接消费

---

## 场景 3：错误传播 / Redaction 单源（错误流水线）

```
domain service 抛错
  └─ errcode.New(ErrXxx, "const literal msg",
        WithDetails(slog.String("userId", id), slog.Int("attempts", n)),
        WithInternal(fmt.Sprintf("sql=%q stack=%s", q, stack)))
        │
        ▼
codegen handler
  ├─ 4xx → typed *ErrorResponse{Body: errcode.Error{...}}（details 下发）
  └─ 未声明 5xx → return nil, err → httputil.WriteError 兜底
        │
        ▼
HTTP middleware (Recovery / response writer)
  ├─ 检查 status：5xx → strip details；internal 永不下发
  └─ 序列化 wire JSON ↘
        │              到客户端
        ▼
slog (server side)
  └─ 记录完整 Message + Details + Internal（含 stack / sql）
        │
        ▼
Tracing span.RecordError
  └─ kernel/wrapper.WrapConsumer 与 runtime/http/middleware.Recovery
       无条件经 pkg/redaction.RedactError →
         mask password/secret/token/api_key/authorization/private_key/signing_key/dsn
         value boundary 到下一空白（fail-closed）
        │
        ▼
adapters/otel exporter → backend
        ↑
Audit Ledger 同源
  └─ runtime/outbox.SanitizeError 也走 pkg/redaction（单源治理）

业务 cell 出站 audit payload
  └─ pkg/redaction.RedactPayload（递归剔除同源 key 列表）
```

**产生的作用**：

- **三层隔离**：客户端只看 Message（4xx 加 Details）；服务端 slog 看全；trace backend 看 redact 版
- **PII 不外泄**：trace 经 fail-closed value boundary，过度 mask 是接受代价
- **单源治理**：所有 redaction 走 `pkg/redaction`，避免多处规则漂移

---

## 场景 4：Cell 作为隔离单位（owner 维度横切）

cell 不仅是代码组织，**`cell` 是所有横切维度的 owner key**：

```
                 cell="accesscore"
                       │
        ┌──────────────┼──────────────┬──────────────┬──────────────┐
        ▼              ▼              ▼              ▼              ▼
  HTTP metrics    slog field    trace span    Redis namespace   Audit owner
  http_*{cell=}   {cell:...}    cell.id=...   accesscore:<k>    entry.cell
        │              │              │              │              │
        └──────────────┴──────────────┴──────────────┴──────────────┘
                              │
                       由 ctxkeys.CellID 串联
                       由 CellAttribution mw 注入
                       由 RouteGroup ownership 派生
```

**产生的作用**：

- **运维语义统一**：dashboard 用 `cell` 一个维度过滤就能切出业务/框架（业务 `cell != "_runtime"`，运行时探针 `cell="_runtime"`）
- **故障隔离单位**：accesscore 故障 → Redis key、metrics、日志、trace 全部能定向
- **多租户雏形**：未来 tenant 可作为 cell 之上正交维度（当前未实现）

---

## 场景 5：Codegen funnel — 单源驱动多产物

```
contracts/http/access/v1/sessions.contract.yaml
  ├─ openapi schema (request/response)
  ├─ endpoints.subscribers / publishers
  └─ codegen: true
        │
        ▼ tools/codegen/contractgen
        ├─ generated typed handler interface（业务实现）
        ├─ generated Get200JSONResponse / Post201ErrorResponse structs
        ├─ generated Subscription helpers (NewSubscription with cellID)
        └─ generated catalog 索引

cells/accesscore/cell.yaml + slices/*/slice.yaml
        │
        ▼ tools/codegen/cellgen
        ├─ cell_gen.go (meta / providers)
        └─ providers wired with contractgen artifacts

并行验证：
  ├─ kernel/governance.validate
  │     ├─ FMT-20 strict request
  │     ├─ ADV-06 endpoints.subscribers ↔ contractUsages 双向
  │     ├─ VERIFY-01 contract usage ↔ verify.contract 闭环
  │     ├─ TOPO 依赖规则
  │     └─ HTTP /api/v1 前缀 / response 只增不删
  ├─ tools/archtest (50+ invariants, 16 shard)
  │     ├─ PANIC-REGISTERED-01 typed wrap
  │     ├─ MESSAGE-CONST-LITERAL-01
  │     ├─ DETAILS-SLOG-ATTR-01
  │     ├─ OUTBOX-* (lease CAS / service nil-check / HandleResult factory)
  │     ├─ REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01
  │     ├─ REDIS-KEY-NAMESPACE-01
  │     └─ ROUTER-ATTRIBUTION / RUNTIME-SENTINEL
  ├─ tools/generatedverify (golden drift)
  └─ tools/depgraph (跨包闭包)
```

**产生的作用**：

- **单源**：一份 contract.yaml 决定 handler 类型、订阅声明、verify 闭环、catalog 索引
- **改不漏**：删字段 → openapi 校验失败；缺 subscriber → ADV-06；忘 verify.contract → VERIFY-01
- **AI-rebust**：违反不可表达（typed struct）或运行时拒绝（archtest），不靠注释/命名 convention

---

## 场景 6：事务 + outbox + 幂等的三角

L2/L3 最关键的组合：

```
        Producer 侧                  │     Consumer 侧
                                     │
PG Tx (kernel/persistence)           │  Subscriber + ConsumerBase
  ├─ business state mutation         │  ├─ recv from broker
  └─ outbox.Emit(tx, evt)            │  ├─ kernel/idempotency.Claimer
        ↓ same row                   │  │   .Claim("{cg}:{entry.ID}")
        ↓ lease_id=NULL              │  │   ↓ Redis SET NX EX
        ↓                            │  │   ← 已 claim → skip + Ack
        ▼                            │  ├─ handler(ctx, entry)
PG outbox table                      │  │   → HandleResult
        ▼                            │  ├─ broker Ack / Nack
runtime/outbox.Relay                 │  │   ├─ Ack → Claimer.Commit
  ├─ ClaimPending → lease_id CAS     │  │   ├─ Reject → Nack(no requeue) → DLX
  ├─ adapters/rabbitmq.Publish       │  │   └─ Requeue → 退避 → Claimer.Release
  ├─ MarkPublished (lease CAS)       │  │
  ├─ MarkRetry / MarkDead            │  └─ Settlement 由 Subscriber 注入
  └─ ReclaimStale (lease CAS)        │      （handler 无感知）
```

两个独立的"防重"机制叠加：

- **producer 侧 lease_id**：防多 relay worker 抢同一行（旧 worker CAS 必失败，ADR 202605051600）
- **consumer 侧 Claimer**：防同事件多次投递（broker at-least-once 语义补偿）

**产生的作用**：

- end-to-end exactly-once-effect（不是 exactly-once delivery）
- relay 横向扩展安全（多 worker 不重复发布）
- consumer 横向扩展安全（同 cg 多实例不重复处理；不同 cg fanout）

---

## 场景 7：观测三件套 + 业务语义对齐

```
                请求进入
                   │
   ┌───────────────┼───────────────┐
   ▼               ▼               ▼
Metrics         Logging         Tracing
   │               │               │
   ├ http_*       ├ slog          ├ span
   │  {cell,       │  {cell,       │  {cell,
   │   route,      │   trace_id,   │   trace_id,
   │   status}     │   span_id,    │   ...}
   │               │   error}      │
   │               │               │
   │               │               ├ RecordError → redaction
   │               │               │
   ▼               ▼               ▼
prometheus     stdout(JSON)    OTEL exporter

业务事件：
   audit ledger（hash chain，append-only）
      ├─ 订阅业务事件作为 audit 源
      ├─ payload 出站经 RedactPayload
      └─ verify 端点：Append/Verify chain integrity
```

**产生的作用**：

- 同 trace_id 跨 HTTP → tx → outbox relay → consumer 链路追踪
- 同 cell label 跨 metrics/log/span 三栈对齐
- audit ledger 独立保留完整业务事件流（合规 / 排障）

---

## 场景 8：CLI + Governance + archtest 形成的开发者闭环

```
开发新 cell：
  1. gocell scaffold cell <name>     ← 生成 cell.yaml + 目录
  2. gocell scaffold slice <c>/<s>   ← 生成 slice.yaml + handler/service 骨架
  3. 写 contract.yaml
  4. gocell generate contract        ← codegen 派生 typed handler
  5. gocell generate cell            ← 派生 cell_gen.go
  6. 实现 service.go（TxRunner / repository）
  7. gocell validate                 ← FMT/ADV/VERIFY/TOPO/HTTP/REF 规则
  8. go test ./...                   ← unit + contract + intent + outbox
  9. hack/verify-archtest.sh         ← 16-shard archtest 全集
 10. make verify                     ← 总入口

CI（每个 PR）：
  ├─ golangci-lint (depguard 路径级)
  ├─ archtest 16-shard 矩阵
  ├─ generated drift verify
  ├─ governance validate
  ├─ unit + integration test
  ├─ codegen golden
  └─ journey runner（J-*.yaml 验收剧本）
```

**产生的作用**：

- 开发者只写 contract.yaml + service.go，其他派生 + 校验自动化
- AI co-author（fresh session 无记忆）也能通过 archtest 在 CI 被拒，约束不可绕过

---

## 组合产生的"涌现能力"

| 组合 | 涌现 |
|---|---|
| **PG outbox + Claimer + ConsumerBase** | End-to-end exactly-once-effect 事件驱动 |
| **CellAttribution + ctxkeys.CellID + redaction** | 单一 owner 维度贯穿三栈观测 |
| **codegen + governance + archtest** | 单源 contract → 多产物 + 闭环校验，违反不可表达 |
| **errcode 三层 + middleware strip + redaction** | PII fail-closed，开发者写一次错误 4xx/5xx 自动分级 |
| **Cell.Init 声明 + Registry drain + bootstrap phase** | 声明式装配，启动期 fail-fast，无 runtime 漂移 |
| **per-cell adapter + KeyNamespace + readyz** | 故障隔离 + 运维定向 + 单 cell 灰度可行 |
| **AuthPlan + Mount + Guard + JWT/Session/Refresh** | 声明式 auth，cell 只声明意图，编译为 middleware |
| **TxRunner nil fail-fast + lease CAS + panicregister.Approved** | 构造期 + 静态 + 运行时三层防误用 |

## 核心设计哲学

每个能力都是**接口 + 单源 + 治理守卫**三件套。能力之间通过 `ctxkeys.CellID` / `kernel.Registry` / `outbox.Entry` / `errcode.Error` 这几个**公共信封**串联，避免点对点耦合。

组合产生的力量不是能力总和，而是 **"声明一次，全栈生效"** 的杠杆。
