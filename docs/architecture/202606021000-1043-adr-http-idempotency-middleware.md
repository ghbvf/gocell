# ADR-1043: HTTP Idempotency Middleware + RecordedResponse Store

**Status**: Accepted
**Date**: 2026-06-02
**Issue**: Closes #1043 (W2 — 005 framework capability roadmap)
**Related**: ADR `202605281200-1042-outbox-wire-envelope-principal-occurred-at.md` (W0，解锁 W2 Principal ctx), `docs/plans/framework-capability-gaps/202605131500-004-capability-gap-analysis.md` §缺口1, `docs/plans/framework-capability-gaps/202605162100-005-framework-capability-roadmap-plan.md` §W2

---

## Context

005 W2 杠杆点：框架级 HTTP 幂等——消费侧事件幂等已由 `kernel/idempotency.Claimer`
（两阶段 Claim/Commit/Release）覆盖；HTTP 请求侧（`Idempotency-Key` header）无框架支持，
每个 cell 自己处理，逻辑重复且缺 response-blob 回放能力（004 缺口 1 确认）：

| 能力 | 缺口分析前 | 本 PR 后 |
|------|-----------|---------|
| consumer 侧事件幂等 | ✅ kernel/idempotency.Claimer 透明 | 不变 |
| HTTP 请求幂等（`Idempotency-Key` header） | ❌ 无框架，每 cell 自实现 | ✅ 框架级 middleware |
| HTTP response blob 回放 | ❌ consumer 幂等只 skip+Ack | ✅ RecordedResponse replay |

**与 kernel/idempotency 的关系**：HTTP 幂等不是 Claimer 的平行抽象，而是 **minimal
specialization**——ClaimState（`ClaimAcquired` / `ClaimDone` / `ClaimBusy`）和
`DefaultTTL` / `DefaultLeaseTTL` 常量直接从 `kernel/idempotency` 复用；唯一扩展点是
`Receipt.Record(ctx, resp, doneTTL)` 在 Commit 时同时持久化 response blob——这是 HTTP
场景独有的需求，consumer 的 `Receipt.Commit(ctx)` 只需 mark-done 状态，不携带 blob。

---

## Decision

### 1. 分层定位 — runtime/http/idempotency（不入 kernel）

`Store` 接口 + 密封 `RecordedResponse` + `Middleware` + in-mem fake + conformance
suite 统一放在 `runtime/http/idempotency`：

- HTTP 幂等是 HTTP-shaped 关切（`http.Header`、`net/http`），与 kernel/ 的传输无关目标冲突。
- `runtime/` 依赖规则允许 `runtime/` → `kernel/` + `pkg/`，不依赖 `adapters/`。
- Redis 实现放在 `adapters/redis.HTTPIdempotencyStore`，满足 layering 约束。

本决策拒绝"把 RecordedResponse 塞进 kernel/idempotency"的选项：kernel/ 不引入
`net/http` 依赖（会升 kernel 的依赖集，违反分层规则）。

### 2. Namespace / 身份隔离 — `(tenantID, method, path, subject, key)` 五元组

Middleware 从 `runtime/auth.Principal` 提取身份：

```
ns  = Principal.TenantID  // 空时替换为 "_notenant" sentinel
key = subject + "\x00" + method + "\x00" + path + "\x00" + Idempotency-Key header value
```

**method + path 包含在 key 中**：同一客户端提供的 `Idempotency-Key` header 值对
不同端点独立——`POST /orders` 和 `POST /payments` 用同一 header 值生成不同 key，
不会碰撞。此设计对齐 Stripe 幂等设计和 IETF idempotency-key draft §3。

**request body fingerprint**：`Store.Claim` 签名包含 `fingerprint string` 参数
（canonical blob，见下方 §"指纹 blob 与 per-field diff"）。同一 key + 不同 body 时 Store
返回 `*FingerprintMismatchError`（wraps `ErrFingerprintMismatch` = `errcode.KindUnprocessable` +
`ErrIdempotencyKeyReused`），Middleware 将其转成 **422** 并附 per-field diff（差异字段名）。
指纹计算在 BodyLimit 之后（body 已 bounded），在 Claim 之前。

只有 `PrincipalUser` + 非空 `Subject` 的请求参与幂等追踪。Service-token 主体、匿名
请求、无 Principal 请求**直接 passthrough**（不消耗 Claim，不产生 lease）——这是
有意设计：service-to-service 调用应在调用方保证幂等，框架不代劳。

`_notenant` sentinel 在单租户部署（develop 当前状态）下保证 namespace 非空，与
multi-tenant 落地后的 UUID namespace 形态在语义上兼容——sentinel 是有效 namespace
而非空字符串，不会与任何 tenant UUID 碰撞。

**决策：fingerprint mismatch 使用 422（Unprocessable Content）。** 引入 `errcode.KindUnprocessable`
→ HTTP 422，`ErrFingerprintMismatch` / 中间件 key-reused 路径映射至此，对齐 IETF idempotency-key
draft §2.7（同 key + 不同 payload 是语义不可处理的客户端错误）。`ClaimBusy`（in-flight）保持 409。
> **Amendment 2026-06-04（#1450）**：本段为 422 升级后形态，已重写。原决策「不使用 422，
> 回退 409」因 Kind 集合无 `KindUnprocessable` 而采用；#1450 引入该 Kind 后取代。详见文末
> §"Amendment 2026-06-04：422 升级 + per-field diff"。

### 3. Middleware 位序 — Auth 之后、handler 之前（BodyLimit 之内）

`buildMux` 中的完整顺序（外层 → 内层）：

```
...→ Auth（JWT/ServiceToken）→ BodyLimit → [Idempotency] → route dispatcher
```

具体实现：`router.go::buildMux` 在 `BodyLimit` 之后、`composeHandler()`
之前调用 `r.use(idemhttp.Middleware(r.clock, r.idempotencyStore))`（当 `idempotencyStore != nil` 时）。

位序理由：

1. **Auth 必须先于 Idempotency**：Middleware 需要已认证的 Principal 提取 Subject/TenantID；
   无 Principal 时直接 passthrough（fail-safe，不报错）。若 Idempotency 错误地放在 Auth
   之前，`extractIdentity` 取不到 Principal → passthrough，不构成安全漏洞，但失去幂等追踪。
2. **BodyLimit 必须先于 Idempotency**（即 BodyLimit 是 Idempotency 的外层）：BodyLimit
   在超限时直接 413 拒绝，防止超大请求体在 `recordOrRelease` 的 `bufferingWriter` 路径
   消耗 lease——lease 一旦 Claim 就算耗费，oversized 请求不应消耗 lease。注意 `shouldRecord`
   只在 2xx/3xx 时记录（见 §决策 6），4xx 不记录；但 BodyLimit 拒绝在 Idempotency 内层
   时会先 Claim 再被 413，lease 被消耗，后续重试需等待 lease 过期（`DefaultLeaseTTL`
   5 分钟）。BodyLimit 外层可完全避免此问题。
3. **body fingerprint 读取必须在 BodyLimit 之后**：`readBodyFingerprint` 用 `io.ReadAll`
   读全量 body 再还原 `r.Body`，BodyLimit 已保证 body 大小有界。

**位序不用 archtest 守护**：路径锚点是 Soft（字符串排序位置），`ai-robust.md` 禁止
Soft 立项。位序正确性由 `router_behavioral_test.go` 的 integration-style 测试覆盖（验证
BodyLimit 拒绝不消耗 lease、Auth 缺失时 passthrough）——行为测试 > 结构 AST 测试。

### 4. 激活方式 — listener-wide install + header-gated + method-gated

- **listener-wide**：`router.WithIdempotency(store)` 选项注入；单次配置，所有路由共享。
  不做 per-route 细粒度开关（避免 per-route 配置矩阵爆炸）。
- **header-gated**：`Idempotency-Key` header 缺席时直接 passthrough，无副作用。
  客户端 opt-in，框架零默认开销。
- **method-gated**：仅 `POST` / `PUT` / `PATCH` / `DELETE` 参与（`idempotentMethods` map）。
  `GET` / `HEAD` 天然幂等（无副作用），排除在外。

### 5. 并发处理 — 409 即时拒绝（不阻塞等待）

同一 (ns, key) 的并发请求（`ClaimBusy`）立即返回 **409**，附带 `Retry-After: 5` 响应头
和 `ERR_IDEMPOTENCY_IN_PROGRESS` 错误码，不阻塞等待前一请求完成。

理由：阻塞-等待策略需要连接长持有（HTTP/1.1 keep-alive 下连接占用）加 poll/notify 机制；
在无状态 HTTP 层难以可靠实现，且容易引入 goroutine 泄漏。409 + Retry-After 把等待决策
移交客户端，更符合 HTTP 语义（`429 Too Many Requests` 也是 retry 信号，但 409 Conflict
更精确地描述"同一幂等键正在处理"）。

lease 机制本身已防止重复处理：`ClaimBusy` 时 Claim 不成功，handler 不被调用，不存在
重复执行风险。

### 6. Response body cap — 256 KiB 默认，超限 Release 不记录

`defaultMaxBodyBytes = 256 * 1024`（可通过 `WithMaxBodyBytes` 调整）。

超过 cap 的响应：`bufferingWriter.isOversized()` 返回 true → 跳过 `receipt.Record`，
走 `defer Release` 路径。该响应**正常流式返回客户端**（`bufferingWriter` 在超限后直写
底层 `ResponseWriter`），只是不存储、不可回放。

`shouldRecord` 只对 2xx/3xx 状态码记录；4xx/5xx 响应直接 Release，不存储——避免
把"参数错误"永久缓存为该幂等键的回放结果（客户端修正参数后需要重新处理）。

### 7. 密封 `RecordedResponse`

`RecordedResponse` 全字段 unexported（`status int`, `body []byte`, `header http.Header`,
`recordedAt time.Time`）；包外 populated 字面量编译不可表达（**type-system Hard 上游**）。
获得 `RecordedResponse` 的路径：

- `newRecordedResponse(clk, status, body, header)` — 包内构造器，Middleware 在 handler
  执行后调用，对所有可变输入做防御性 clone。
- `UnmarshalRecordedResponse(raw []byte)` — wire decode funnel，供 Store 实现在加载
  存储 blob 时使用（验证 status ∈ [100,599] + recordedAt 非零）。

`MarshalRecordedResponse(r RecordedResponse)` 是配套序列化器，通过内部 DTO
`recordedResponseDTO` 解耦 wire schema 与 sealed type。

### 8. Redis 实现 — 双键 Lua 原子模型

`adapters/redis.HTTPIdempotencyStore` 用 dual-key Lua 脚本实现原子性：

- `<ns>:{<key>}:lease` — `SET NX PX <leaseTTL>`，值为随机 token（UUID fencing）。表示"处理中"。
- `<ns>:{<key>}:resp` — `SET PX <doneTTL>`，值为 `MarshalRecordedResponse` blob。表示"已完成"。

两个 key 共享 Redis Cluster hashtag `{<key>}`（hashtag 仅含业务 key，namespace 前缀在
hashtag 外），保证 lease + resp 落在同一 slot，使 `EVAL` multi-key 在 Cluster 模式下
合法。

**Claim 流程**（`claimRespScript`）：先检查 resp key 是否存在（`ClaimDone` 回放路径），
再尝试 `SET NX` lease key（`ClaimAcquired` 或 `ClaimBusy`）。Claim 同时携带 `fingerprint`
参数：`ClaimAcquired` 时原子存储指纹；后续同 key Claim 时比对存储指纹，不匹配时 Lua 返回
`{3, fp_stored}`，decode 为 `*FingerprintMismatchError{Stored: fp_stored}`（携带 stored blob
供中间件做 per-field diff）。

**Record 流程**（`httpRecordScript`）：token-guarded：先校验 lease key 的当前值 = token，
原子执行 `DEL lease + SET resp`。Token 不匹配（stale lease 过期后被其他 worker 重新
Claim）→ 返回 0，抛出 permanent error（不重试）。

**Release 流程**（`httpReleaseScript`）：token-guarded `DEL lease`。

`NewHTTPIdempotencyStore` 在构造期调用 `ns.Validate()` + nil client 检查，满足
`REDIS-KEY-NAMESPACE-01` archtest 约束（构造器 body 顶部强制 `ns.Validate()`）。

注意：`HTTPIdempotencyStore.Claim` 中 `ns` 参数（来自 Middleware 的 `buildNamespaceKey`
输出，值为 tenantID 或 `_notenant`）作为 Redis key namespace 的**运行时部分**，与构造器
注入的 `KeyNamespace`（标识 adapter owner，如 `"http-idempotency"`）是两个正交概念——
`KeyNamespace` 在 `REDIS-KEY-NAMESPACE-01` archtest 的 `ns.Validate()` 守卫下；Claim 的
`ns` 参数由 Middleware 生成，不走同一 funnel，但代码中对 `"{}` 字符做了额外 runtime
guard（避免破坏 hashtag 边界）。

### 9. 作用域范围

> **Amendment 2026-06-05（#1449）**：full-assembly 行由 `❌ defer` 改为 `✅`，本节按
> ai-robust「ADR amendment 落地必查：矛盾段同 PR 重写」重写为收口后形态。治理定调 +
> 结构闸 + 三层证明测试见 ADR `202606051000-1449-adr-http-idempotency-assembly-scope-namespace.md`。
> 威胁矩阵逐行重评：本 amendment **纯加性**（扩大「同一 key 跨 pod 共享」这一预期能力，
> 隔离维度 `(tenant, subject, method, path, header)` 不变），原 ✅ 行无退化；ADR-1449 §威胁矩阵
> 新增「跨 pod 回放越权 / 指纹绕过 / CROSSSLOT / 节点身份污染」四行，均 ✅。

PR #1448（本 ADR 原始 PR）的幂等作用域是**单 listener 内**。full-assembly（多 pod 共享）经
#1449 收口——其运行时机制本就就绪（`_runtime` 非 pod-specific 命名空间 + 跨 listener 共享 store +
node-agnostic key + 无内存态 store + 共享 Redis），#1449 把它从偶然 wiring 升为被结构守卫的契约。

| 作用域 | 描述 | 状态 |
|--------|------|------|
| single-listener | 同一进程同一 listener（Redis 共享） | ✅ 覆盖（本 ADR / #1448） |
| full-assembly | 多 pod 横向扩展共享幂等状态（Redis Cluster 必须） | ✅ 覆盖（#1449，治理 ADR `202606051000-1449`） |
| cross-cell | 同一进程不同 listener（primary ↔ internal）共享**同一去重槽** | ❌ defer（#1610，blocked-by #1044 命令桥） |

cross-cell（跨 cell 同槽）仍延期：依赖 HTTP `Idempotency-Key` ↔ `command_id` 映射桥（ADR
`202606040550-1044` §5 演进路径 ⑤ 延期子项）+ 新的共享 KeyNamespace 治理决策，跟踪于 #1610。

### 10. Route opt-out — `auth.Route.IdempotencyExempt`

特定路由可通过 `auth.Route{IdempotencyExempt: true}` 声明免于幂等追踪。典型场景：
登录 / 认证端点的 response 含 session cookie（`Set-Cookie`），敏感响应头过滤（
`sensitiveResponseHeaders`）已防止 cookie replay，但 opt-out 提供额外的明确豁免——
exempt 路由的请求直接 passthrough，response **永不写入 store**，无论 body 大小。

实现路径：
1. `auth.Route.IdempotencyExempt bool` 字段声明豁免意图（`runtime/auth/route.go`）。
2. `auth.Mount` 在 `AuthRouteMeta.IdempotencyExempt` 中传播该标志。
3. `Router.FinalizeAuth` 调用 `partitionAuthMetas` → `mergeIdempotencyExemptMatcher`
   编译出 `r.idempotencyExemptMatcher`（方法+路径匹配器，对齐 `CompilePasswordResetExempts` 形态）。
4. `buildMux` 通过 `lazyIdempotencyExempt` 闭包把编译好的 matcher 注入
   `idemhttp.WithExemptMatcher`，使 Middleware 在 exempt 路由直接 passthrough。

exempt 路由的 passthrough 在 method-gate 之前、body read 之前执行（`cfg.exemptMatcher`
检查是 Middleware 最早的 guard），exempt 路由零 body-buffering 开销。

#### 敏感 body route 豁免（✅ 已收口，gh #1469）

> **Amendment 2026-06-03（gh #1469）**：原 §"⚠️ 收口前置"所述两处缺口，已在 production
> wiring（`cmd/corebundle` 接通 store）**同一 PR** 内收口。本节按 ai-robust「ADR amendment
> 落地必查：矛盾段同 PR 重写，不留两套真理源」重写为收口后形态。
>
> **Amendment 2026-06-04（gh #1537 review F4+F7）**：幂等豁免标志从 auth 块迁为 **一等
> endpoint 关注点**——contract.yaml 由 `endpoints.http.auth.idempotencyExempt` 改为
> `endpoints.http.idempotency.exempt`（`HTTPIdempotencyMeta`，sibling of `HTTPAuthMeta`）。
> 它**不再**折入 `HTTPAuthMetaBoolFields` reflect-freeze 矩阵：auth-combo 空间回退 2^6→2^5
> （whitelist 14→7），因为「跳过幂等录制」是 middleware 行为而非 auth mode（F7）。同时新增
> **F4**：default-on 后中间件可发的 409（ClaimBusy / ErrIdempotencyKeyReused）由 governance
> rule **CH-07** 强制——任何非豁免 mutating（POST/PUT/PATCH/DELETE）http 契约必须在
> `auth.responses`（listener-middleware-injected 状态列表，已含非 auth 的 rate-limit 429）声明
> 409，缺失则 `gocell check contract-health` 失败。所需状态集是
> `HTTPTransportMeta.IdempotencyFrameworkStatuses()`（method + idempotency.exempt）**单源派生**
> 的 oracle，CH-07 据此判定且不可漂移；未来 mutating 路由被强制声明或显式 exempt。已给 **17**
> 个非豁免 mutating 契约补 409（手工枚举只覆盖 9 个，CH-07 捕获另 8 个——印证机器规则优于人列）。
> 注：曾尝试把 409 折入 governance `declaredErrorStatuses` 并集，但那使 CH-07 自身 vacuous（且对
> CH-04 无效——handler 不发 409），已回退；声明面由 CH-07 + 显式 `auth.responses` 承载。
> 下方三件套的字段路径已按 F7 迁移更新。

PR #1448 落地的是 **运行时机制**（`auth.Route.IdempotencyExempt` → `AuthRouteMeta` →
`mergeIdempotencyExemptMatcher` → `WithExemptMatcher`）。gh #1469 在接通生产 store 的同一 PR
内补齐了使「敏感 body 路由不会被录制」真正成立的三件套：

1. **codegen 入口**：`contractgen` 读 `endpoints.http.idempotency.exempt` 字段
   （`HTTPIdempotencyMeta.Exempt` → `httpEndpointSpec.IdempotencyExempt` → handler
   模板 emit `auth.Route{IdempotencyExempt: true}`）。它是 `HTTPAuthMeta` 的 sibling，不参与 FMT-27 auth-mode mutex 矩阵
   （F7 迁移后矩阵回退 2^5、whitelist 7；不再折入 `HTTPAuthMetaBoolFields`）。contract.yaml 经
   `idempotency: { exempt: true }` 声明豁免。
2. **应用到全部凭据响应路由**：`login` / `refresh` / `change-password`（200/201 响应 body 含
   `accessToken` / `refreshToken`）均声明 `idempotency.exempt: true` 并 regenerate。
   `change-password` 是唯一今天就处于风险的路由（已认证，middleware 会激活）；`login` /
   `refresh` 是 public（identity-gate 本不激活），声明豁免是 defense-in-depth + 与下方守卫的
   覆盖一致性。
3. **fail-closed 生成期守卫**（`CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01`，
   `tools/codegen/contractgen/credential_response_idempotency_guard.go` + 同名 archtest）：任何
   `kind: http` 契约的 response schema 在任意深度声明命中 `pkg/redaction.IsSensitiveKey` 的字段、
   且 `idempotency.exempt != true` → **生成期拒绝**。这把「未来新增的凭据响应路由必须豁免」
   从开发者纪律升为机器强制——store 默认启用对未来路由永久 fail-closed。AI-robust 评级（单源
   活在该 archtest godoc）：**上游 Hard**（schema 命中即生成期拒；hand-edit `handler_gen.go` 删
   `IdempotencyExempt: true` → `gocell verify generated` golden-drift CI 红）+ **下游 Medium 天花板**
   （must-call 覆盖，Go 不可表达，同 #851/#893/#1282 永久天花板 family）。

`cmd/corebundle` 在 `shared.Redis != nil` 时默认接通 `WithIdempotencyStore`（无 env 开关），与
`buildConsumerClaimer` / `buildServiceNonceStore` 同按拓扑派生；memory/单 pod 模式跳过（安全，
无跨 pod replay 需求）。bootstrap e2e 测试验证连发两次 `Idempotency-Key` 第二次回放 +
`Idempotency-Replayed: true`，并验证 exempt 路由在 store-active 下 **永不写入 store**（handler
重复执行、store 无 entry）。**禁止** store-active 而 exempt 未应用的中间状态——已由上述守卫机器封堵。

---

## AI-robust 评级

| Invariant ID | 摘要 | 上游评级 | 下游评级 |
|---|---|---|---|
| `HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01` | `RecordedResponse` 全字段 unexported → 包外 populated 字面量编译不可表达（type-system Hard 上游）；唯一**导出**产出函数集 `= {UnmarshalRecordedResponse}`（go/types resolver 锁定）。`newRecordedResponse` 是**未导出**的包内构造器，不在导出产出面锁集中；`MarshalRecordedResponse` 是 sole 序列化器（Hard downstream callsite uniqueness）。盲区：reconstruct 路径上的 Store 实现可以从任意 `[]byte` decode，但 `UnmarshalRecordedResponse` 的 `recordedAt` 非零和 status 范围校验是 semantic guard。 | **Hard**（type-system，全字段 unexported）| **Hard**（go/types 锁定导出产出面，`RECORDEDRESPONSE-SEALED-01` A2） |
| `HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01` | 每个满足 `idemhttp.Store` 接口的生产具名类型，其包下的 `_test.go` 必须有 `idempotencytest.RunConformanceSuite` 调用；防止新增 Store 实现跳过 conformance。上游 Medium：`types.Implements` 穷举找所有实现，但 enrollment（_test.go 调用点）archtest-bound 非 type-system 强制，同 `SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01` 形态；下游 Medium：`_test.go` 调用点解析（ResolvePackageRef → pkgPath+funcName，比名字匹配更紧但仍 archtest-bound）。Hard 升级路径：codegen golden 枚举实现，未来 work（对标 SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 的 Hard 路径 gh #1003）。 | **Medium**（types.Implements 枚举找实现，但 enrollment archtest-bound；同 SAGA 先例）| **Medium**（_test.go 调用点解析；Hard 路径 = codegen golden 枚举） |
| `REDIS-KEY-NAMESPACE-01`（已有，扩展）| Redis 构造器 body 顶部强制 `ns.Validate()` 守卫；`HTTPIdempotencyStore` 新增进 `redisConstructors` 列表 | **Hard**（archtest 锁定构造器集合，alias-proof go/types）| **Hard**（form-uniqueness callsite lock） |

**Funnel 双向锁评级（ai-robust §"Funnel 双向锁评级"）**：

`RecordedResponse` sealed construction funnel：
- **上游 Hard**（type-system）：`RecordedResponse` 全字段 unexported → 包外 populated 字面量编译不可表达。与 `OUTBOX-ENTRY-SEALED-CONSTRUCTION-01` 同形态。
- **下游 Hard**（archtest `HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01` A2）：go/types 锁定唯一**导出**产出面 `{UnmarshalRecordedResponse}`（`newRecordedResponse` 是包内未导出构造器，不在导出产出面锁集中），包外新增导出产出面即 CI 红。
- 双侧均 Hard，构成闭环 funnel。

`Store` conformance-enrollment funnel：
- **上游 Medium**：`types.Implements` 穷举，枚举范围 runtime/http/+adapters/+examples/；新增 Store 实现被自动发现。但 enrollment（`_test.go` 中调用 `RunConformanceSuite`）是 archtest-bound，非 type-system 强制——同 `SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01`（saga.md：Medium 评级，永久天花板）。
- **下游 Medium**：`_test.go` 调用点解析（`ResolvePackageRef` → pkgPath+funcName）；Go 语言层面无法强制测试文件中存在某个调用。Hard 化路径 = codegen golden 枚举实现，未来 work（见上表）。

---

## 威胁矩阵

| 威胁 | 缓解措施 | 状态 |
|------|---------|------|
| **Replay 攻击**（攻击者用他人的 `Idempotency-Key` 触发 replay）| namespace = `(tenantID, userID)`，key 中含 `subject + "\x00" + method + "\x00" + path + "\x00" + header`；A 用户的幂等键不会与 B 用户碰撞，跨用户 replay 在 Claim 阶段因 namespace 不匹配而取不到 | ✅ |
| **回放跳过当前授权再校验**（同主体在权限被回收后仍能 replay 旧响应；对标 Envoy ext_authz 把"后续 filter 改变路由缓存绕过授权"列为提权风险）| **非提权 by-design**：(1) 回放只命中**同一已认证主体本人**的已录响应——key 含 `subject + tenant`，跨主体回放结构上不可能（见上行）；非 `PrincipalUser` 主体在 `extractIdentity` passthrough（service-token 不缓存）；凭据路由 `idempotencyExempt` 不缓存。(2) 回放返回的是该主体**先前在授权状态下已执行**操作的已录响应，**不重新执行任何副作用**；对一个已成功的幂等键在回放时再跑 route Policy，会让同一 key 后续转 403，**违反 IETF Idempotency-Key 语义**（同 key 必返回原响应）——因此"回放不再校验授权"是正确行为而非缺陷。Envoy 的路由缓存以 route 为键、与主体无关，故其绕过模型不迁移到这里（本实现以 principal 为键）。**残留面**：同主体在 24h TTL 内重收自己授权撤销前的旧响应体，bounded by per-principal key + TTL，非越权 | ✅ (by-design) |
| **Cache poisoning**（伪造 response 污染 replay 缓存）| 只有原始请求者本人的成功响应被 Record；`httpRecordScript` token-guard 防止 stale-lease 的 Record 提交伪造 blob；`RecordedResponse` sealed 防止包外构造伪造结构 | ✅ |
| **Thundering herd / 并发重复**（同 key 多个飞行请求）| `ClaimBusy` 即时 409，lease 在 `ClaimAcquired` 时原子 SET NX；Lua 脚本原子性防止两个请求同时 Claim 成功 | ✅ |
| **Oversized body 无界 buffer**（超大响应体耗尽内存）| 256 KiB cap（`defaultMaxBodyBytes`，可调 `WithMaxBodyBytes`）；超限跳过 Record，响应正常 stream 给客户端，不占用 replay 存储 | ✅ |
| **跨租户泄漏**（A 租户看到 B 租户的 replay）| namespace = tenantID（或 `_notenant` sentinel）；不同租户生成不同 namespace，Claim 完全隔离 | ✅ |
| **Store 故障时 fail-open**（Redis 不可用时跳过幂等保护）| `Store.Claim` 返回 error 时 Middleware 直接 500（`httputil.WriteError` + `msgStoreUnavailable`），不 passthrough、不执行 handler；fail-**closed** by design | ✅ |
| **Principal 缺席时 handler 被幂等化**（未认证请求消耗 lease）| `extractIdentity` 检查 `PrincipalUser` + 非空 `Subject`；service-token 主体、匿名请求直接 passthrough，不产生 lease | ✅ |
| **Lease 过期后 handler 重复执行**（lease TTL 内 handler 未完成）| lease TTL 默认 5 min（`DefaultLeaseTTL`）；过期后 lease 自动释放，下一个请求可重新 Claim 并执行；Record 的 token-guard 确保过期 lease 的 Record 调用被拒绝（返回 token mismatch error） | ✅ |
| **Handler panic 持久化 lease**（panic 导致 Release 未调用）| `recordOrRelease` 的 `defer Release` 在 panic 时仍执行（Go defer 在 panic 栈展开时运行）；Release 后 panic 自然传播 | ✅ |
| **4xx 响应被缓存为永久回放**（参数错误的 response 被 Record）| `shouldRecord` 仅对 2xx/3xx 记录；4xx/5xx 走 Release 路径，客户端可修正参数后重试 | ✅ |
| **同一 key + 不同 endpoint mis-replay**（相同 header 值跨端点错误 replay）| method + path 包含在 key 组成中（`subject + "\x00" + method + "\x00" + path + "\x00" + header`）；不同 endpoint 的 Claim 使用不同 key，结构上无法碰撞 | ✅ |
| **同一 key + 不同 body mis-replay**（相同 key + 不同请求体绕过指纹检测）| `Store.Claim` 接受 canonical fingerprint blob；后续不匹配指纹的 Claim 返回 `*FingerprintMismatchError` → 422 `ErrIdempotencyKeyReused`；攻击者无法用不同 body 劫持已有 replay | ✅ |
| **per-field diff 字段名泄漏**（422 响应回字段名是否泄露原始请求）| diff 只回**字段名**（如 `amount`），绝不回字段**值**——**结构性 Hard**：store 只持 per-field 哈希（无值可回），diff helper 返回 `[]string`（签名不可回值）。幂等 key = `(subject, method, path, tenant, header)`，diff 只发生在**同一认证主体**自己先后两次请求间，回字段名给本人无泄漏 | ✅ (by-design) |
| **敏感响应体持久化到 replay store**（session cookie / credential 被 store）| **header 维度 ✅**：`sensitiveResponseHeaders`（Set-Cookie、Authorization 等）在 `filterSensitiveHeaders` 中剔除，RecordedResponse 只存安全可重放的 header。**body 维度 ✅（gh #1469 收口）**：`filterSensitiveHeaders` 只过滤 header，body 维度防护由三层组成——(1) `endpoints.http.idempotency.exempt` codegen 入口落地；(2) 全部凭据响应路由（`login` / `refresh` / `change-password`）声明豁免，exempt 路由 response **永不写入 store**；(3) fail-closed 守卫 `CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01`——response schema 含 `IsSensitiveKey` 字段而未豁免的契约生成期被拒，未来凭据路由无法 fail-open。详见下方 §"敏感 body route 豁免（✅ 已收口，gh #1469）"。 | ✅ |

---

## Consequences

**Positive**:

- 框架级 HTTP 幂等：不再需要每个 cell 手写幂等逻辑，消除 004 缺口 1 的重复代码。
- `RecordedResponse` sealed construction 提供 type-safe replay blob，无法从外部伪造。
- Redis 双键 Lua 原子模型与 `kernel/idempotency.Claimer` 的 PG outbox fencing（`OUTBOX-LEASE-ID-CAS-01`）同一 token-guard 语义，一致性模型可预测。
- `REDIS-KEY-NAMESPACE-01` 已有 archtest 自动覆盖新增的 `HTTPIdempotencyStore` 构造器，无需新 archtest 守卫 namespace 约束。
- request body fingerprint 已实现：同一 key + 不同 body → 422 即时拒绝 + per-field diff，防止 mis-replay。
- route opt-out **运行时机制**（`auth.Route.IdempotencyExempt` → matcher → `WithExemptMatcher`）已落地并有测试，允许敏感路由显式豁免 recording。

**Negative / 已知限制**:

- **敏感 body 豁免已生效**（✅ gh #1469 收口）：codegen 入口 + 3 凭据路由声明豁免 + fail-closed 守卫（`CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01`）三件套落地，store 默认启用对未来凭据路由永久 fail-closed。详见 §"敏感 body route 豁免（✅ 已收口，gh #1469）" + 威胁矩阵末行 ✅。

- **回放 best-effort**：256 KiB cap 意味着大响应不可回放。客户端应对"无回放"设计防御（幂等键的第二次请求可能重新执行，而非 replay）。
- **Service token 主体不追踪**：`PrincipalService` passthrough，service-to-service 调用的幂等需调用方自行保证。此为有意选择（§决策 2 解释）。
- **单 listener 作用域**：跨 listener / 多 pod 场景不在本 PR 覆盖（§决策 9）。
- **422 用于 fingerprint mismatch**（#1450）：引入 `errcode.KindUnprocessable` → 422，对齐 IETF idempotency-key draft §2.7。`ClaimBusy`（in-flight）仍 409。详见文末 §"Amendment 2026-06-04"。

---

## Alternatives Considered

**Block-and-wait 并发策略**（被拒绝）：对 `ClaimBusy` 时挂起当前请求 goroutine 等待前一请求完成，然后 replay。优点：客户端无需 retry 逻辑。缺点：长连接占用；需要 poll/notify 或 pubsub 机制（Redis keyspace notifications 或 SSE）；goroutine 泄漏风险；实现复杂度远超收益。选择 409 + Retry-After 更符合 HTTP 无状态语义，将等待决策移交客户端。

**直接复用 `kernel/idempotency.Claimer`**（被拒绝）：Claimer 的 `Receipt.Commit(ctx)` 不携带 response blob；添加 blob 参数会改变 kernel 接口，影响消费侧事件幂等的所有调用方（22 个 service）。HTTP 幂等需要一个独立的 `Receipt` 形状，minimal specialization 比修改 kernel 接口更符合"不考虑向后兼容——直接演化"原则（即便演化成本高，也应走独立接口而非污染 kernel 抽象）。

**独立 lease-store + blob-store（两个 Redis key 分开操作）**（被拒绝）：Claim 之后、Record 之前的窗口里，如果 lease-store 和 blob-store 不原子操作，存在"已 done 状态但 blob 为空"的间隙——后续 replay 会拿到空 blob 或 UnmarshalRecordedResponse 失败。Lua 双键原子脚本消除此间隙。

**Opt-in env flag (`GOCELL_HTTP_IDEMPOTENCY_ENABLED`) vs. default-ON when Redis present**（被拒绝）：issue #1469 要求评估这两种激活方式。选择默认启用的核心理由：opt-in env flag 完全复现了 #1469 要修复的问题——"middleware 和 store 存在，但生产中从未连接"。框架机制如果需要额外 env flag 才能激活，实质上仍然是死代码：运维文档落后、env flag 被遗忘、staging 配置与 prod 不同步，都可能导致生产 store 始终缺席。`buildConsumerClaimer` 和 `buildServiceNonceStore` 已经证明了"按拓扑自动派生、Redis 存在即激活"模式在生产中稳定可靠——本 store 遵循相同范式。fail-closed 守卫（`CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01`、`sensitiveResponseHeaders` header 过滤、`shouldRecord` 2xx/3xx 门控）的存在使 default-ON 在安全语义上与 opt-in 等价；opt-in 仅增加额外的运维配置复杂度，无额外安全收益。

**Service / 匿名主体不追踪幂等（有意设计，非 bug）**：`extractIdentity` 仅对 `PrincipalUser` + 非空 `Subject` 的请求激活幂等追踪；`PrincipalService`（service-token 主体）、匿名请求、无 Principal 请求直接 passthrough，不消耗 Claim，不产生 lease。原因：service-to-service 调用应在调用方保证幂等（调用方拥有幂等语义的完整上下文）；强行在 server 端追踪 service 主体的幂等会引入全局唯一 key 设计问题（service 主体无 per-user 身份隔离），且 service 调用方通常已在 outbox / event bus 层获得 consumer-side 幂等保护（`kernel/idempotency.Claimer`）。未来如需支持 service principal 追踪，在 `extractIdentity` 添加 `PrincipalService` 分支即可，不影响现有接口。

---

## Implementation Matrix

```
Contract: runtime/http/idempotency.Store / Receipt / RecordedResponse
Change: 框架级 HTTP 幂等 middleware + Redis 实现 + sealed RecordedResponse + router integration
        + request fingerprint (method+path+body sha256) + route opt-out (IdempotencyExempt)
Implementations:
  [x] runtime/http/idempotency/store.go    (Store interface + Receipt interface;
                                            Store.Claim now takes fingerprint string)
  [x] runtime/http/idempotency/recorded_response.go (sealed RecordedResponse + Marshal/Unmarshal)
  [x] runtime/http/idempotency/middleware.go (Middleware + shouldIntercept + extractIdentity
                                             + buildNamespaceKey (method+path in key)
                                             + readBodyFingerprint + WithExemptMatcher
                                             + recordOrRelease + shouldRecord)
  [x] runtime/http/idempotency/buffer_writer.go (bufferingWriter — response capture)
  [x] runtime/http/idempotency/mem_store.go (in-mem fake for unit tests;
                                             Claim stores+checks fingerprint)
  [x] runtime/http/idempotency/store_test.go (conformance helper RunConformanceSuite)
  [x] runtime/http/router/router.go WithIdempotency + buildMux position (after BodyLimit,
                                    before composeHandler) + lazyIdempotencyExempt closure
                                    + mergeIdempotencyExemptMatcher from FinalizeAuth
  [x] runtime/auth/route.go IdempotencyExempt bool field + AuthRouteMeta propagation
  [x] adapters/redis/http_idempotency.go (HTTPIdempotencyStore + httpReceipt + noopHTTPReceipt
                                          + Lua scripts with fingerprint comparison)
Conformance test:
  - runtime/http/idempotency/idempotencytest.RunConformanceSuite (in-mem + Redis)
  - go test ./runtime/http/idempotency/... -run 'ConformanceSuite'
  - go test -tags=integration ./adapters/redis/ -run 'HTTPIdempotencyStore'
Repro:
  go test ./runtime/http/idempotency/... ./runtime/http/router/... ./adapters/redis/...
  go test ./tools/archtest/ -run 'RecordedResponse|Idempotency|RedisKeyNamespace'
  bash hack/verify-archtest-invariants.sh
Dependent contracts (governance scan): none — middleware 是 framework 横切，不进 contract.yaml
```

---

## Follow-ups（Backlog）

以下内容在本 PR 范围之外，按 `feedback_pr_scope_carveouts_must_backlog` 规则同步登记 backlog：

- **full-assembly 幂等命名空间** — ✅ **已实现**（gh #1449）：`_runtime` 定调为 assembly-wide 命名空间 + 两道结构闸（`HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01` Hard / `HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01` Medium）+ 三层证明测试，见 §9 Amendment + ADR `202606051000-1449`。
- **cross-cell 跨 cell 同槽幂等**（gh #1610，blocked-by #1044）：同一 idempotency-key 在不同 cell/listener 间共享同一去重槽，需 HTTP `Idempotency-Key` ↔ `command_id` 映射桥（ADR-1044 §5 演进路径 ⑤）+ 新的共享 KeyNamespace 治理决策。
- **request-payload fingerprinting + 422 + per-field diff** — ✅ **已实现**（gh #1450）：`Store.Claim` 接收 canonical fingerprint blob；同一 key + 不同 body → **422** `ERR_IDEMPOTENCY_KEY_REUSED`，响应 details 列出差异的顶层字段名（Stripe 式 per-field diff，只回字段名/不回值）。详见文末 §"Amendment 2026-06-04"。gh #1450 关闭。
- **production wiring** — ✅ **已实现**（gh #1469）：`cmd/corebundle` 在 `shared.Redis != nil` 时默认接通 `bootstrap.WithIdempotencyStore(redis.NewHTTPIdempotencyStore(client, "_runtime"))`（`buildHTTPIdempotencyStore` + `defaultRuntimeOptions`）。HARD 前置三件套（codegen 入口 + 3 凭据路由豁免 + fail-closed 守卫）同 PR 落地，见 §"敏感 body route 豁免（✅ 已收口，gh #1469）"。bootstrap e2e replay + exempt-never-recorded 测试覆盖。
- **Block-and-wait 并发**（gh #1451，可选增强）：如果 409 + Retry-After 被产品侧确认为可接受，此项关闭；否则可作为 opt-in `WithWaitOnBusy(timeout)` 选项。
- **`HTTP-IDEMPOTENCY-CONFORMANCE-ENROLLMENT-01` Hard 化**：当前上游和下游均为 Medium（`types.Implements` 穷举 + `_test.go` 调用点解析），升 Hard 路径 = codegen golden 枚举 Store 实现（对标 SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 Hard 路径 gh #1003）。尚无独立 gh issue，标为 future work。

---

## Amendment 2026-06-04：422 升级 + per-field diff（gh #1450）

原 §2 将 fingerprint mismatch 状态码定为 **409**，唯一理由是 `pkg/errcode` Kind 集合无
`KindUnprocessable`。本 amendment 落地 #1450（EPIC #1489 Phase 2），把它升级为 **422** 并补齐
Stripe 式 per-field diff。矛盾段（§2 决策、§威胁矩阵「同一 key + 不同 body」行、§Consequences
Negative、§Follow-ups）已**同 PR 重写**（ai-robust「ADR amendment 落地必查」）。

### 1. `errcode.KindUnprocessable` → 422

`pkg/errcode/status.go` 新增 `KindUnprocessable`（`Kind.Status()` → `http.StatusUnprocessableEntity`）。
errcode 新 Kind 扇出三载体同 PR 同步：`Kind.Status()` switch、`kernel/governance.errcodeKindNameToStatus`
map、`pkg/errcode` `TestKindStatusAndPublicCode` 表。`IsClient()`（status-range）/ `PublicCode()` /
`PublicCodeForStatus()`（仅 5xx）无需改。`ErrFingerprintMismatch` 与中间件 key-reused 路径改用
`KindUnprocessable`；`ClaimBusy`（in-flight，`ErrIdempotencyInProgress`）**保持 409 不变**。

### 2. 指纹 blob 与 per-field diff（隐私保护）

`computeFingerprint(body)` 由 `hex(sha256(body))` 升级为 canonical blob `{b: bodyHash, f: {field: fieldHash}}`
（JSON 编码，map key 排序 → 确定性）：

- **match 决策**仍是整 blob 相等（`b` 分量保留字节级语义：reordered key / whitespace 仍 mismatch）。
- **`f` 分量**逐顶层字段存 `hex(sha256(rawValueBytes))`（`json.RawMessage` 不递归解析值，无 canonicalize 热路径/深嵌套面，与 `b` 的 byte-exact match 一致），**仅供 mismatch 时 diff**——只存哈希、不存 raw 值。
- mismatch 时 Store 经 `*FingerprintMismatchError{Stored}` 把 stored blob 带回中间件，`diffFields(stored, incoming)`
  算差异（值不同 / 新增 / 缺失）的顶层字段名（sorted，cap `maxMismatchedFields=20`），写入 422 响应
  `details`（逐条 `PublicString("mismatchedField", name)`；超 cap 加 `PublicBool("mismatchedFieldsTruncated", true)`）。
  字段 map 相等但 blob 不同（仅 key 顺序/空白）→ 无 per-field details，回 base 422。

**隐私结构性 Hard**：store 侧只有哈希（无 raw 值可回），`diffFields` 返回 `[]string`（签名层不可能回值）。
幂等 key = `(subject, method, path, tenant, header)` → diff 只发生在同一认证主体自己先后两次请求间，
回字段名给本人无泄漏。见 §威胁矩阵新增行「per-field diff 字段名泄漏」✅ (by-design)。

### 3. CH-07 oracle 扩为 {409, 422} + 契约扇出

`metadata.HTTPTransportMeta.IdempotencyFrameworkStatuses()` 由 `{409}` 改 `{409, 422}`（409=ClaimBusy，
422=key-reused）。该 kernel 字面量受分层约束（kernel/ 不可 import runtime/）须手写，由新 archtest
`IDEMPOTENCY-FRAMEWORK-STATUS-ORACLE-ALIGN-01`（**Medium**）绑定到单源
`runtime/http/idempotency.FrameworkStatuses()`（从 sentinel 派生），漂移即 CI 红。该 Medium 是分层下
可达上限（type-system Hard 不可达，同 #851/#893/#1282 天花板族）。CH-07 据此对全部非豁免 mutating
契约强制声明 422——本 PR 经 `gocell check contract-health` 机器派生给 **28** 个契约的
`endpoints.http.auth.responses` 补 422（含平台 + examples）。422 是 middleware-injected，声明在
`auth.responses`（无 typed response struct），无 codegen。

### 4. 威胁矩阵逐行重评

§威胁矩阵「同一 key + 不同 body mis-replay」行：409 → 422，✅ 不变。新增「per-field diff 字段名泄漏」
行：✅ (by-design，结构性无值)。无 ✅ → ⚠️/❌ 退化格子。
