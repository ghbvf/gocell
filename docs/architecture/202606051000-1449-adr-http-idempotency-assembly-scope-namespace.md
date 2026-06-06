# ADR-1449: HTTP Idempotency Assembly-Scope Namespace（全 assembly 共享命名空间治理）

**Status**: Accepted
**Date**: 2026-06-05
**Issue**: Closes #1449（full-assembly + 治理范围）；cross-cell carve-out → #1610
**Related**: ADR `202606021000-1043-adr-http-idempotency-middleware.md` §9（三作用域表，本 ADR 收口其 full-assembly 行）、ADR `202606040550-1044-adr-command-bus-dispatch-funnel.md` §5（idempotency 桥延期，cross-cell 依赖）、`.claude/rules/gocell/observability.md` §"Redis Key Namespace" / §"HTTP Idempotency `state` Label"

---

## Context

#1043（PR #1448）交付了框架级 HTTP 幂等中间件，但 ADR-1043 §9 显式把作用域限定在**单 listener**，并把另外两个作用域延期：

| 作用域 | 描述 | ADR-1043 |
|--------|------|----------|
| single-listener | 同一进程同一 listener（Redis 共享） | ✅ 覆盖 |
| cross-cell | 同一进程不同 listener（primary ↔ internal）共享同一幂等命名空间 | ❌ defer |
| full-assembly | 多 pod 横向扩展共享幂等状态（Redis Cluster 必须） | ❌ defer |

#1449 跟踪后两者，并声明前置：「共享 KeyNamespace 治理决策（当前 per-cell namespace 模型不支持跨 cell 同槽）」。

**探索发现（本 ADR 的事实基础）**：full-assembly 的运行时机制其实**已经就绪**——

1. `cmd/corebundle` 用 `_runtime`（非 pod-specific）`KeyNamespace` 构造唯一一个 `HTTPIdempotencyStore`，经 `bootstrap.WithIdempotencyStore` → `b.routerOpts` → `phases_http.go` 的 per-listener 循环 append 给**每个** listener router，故 store 已跨 listener 共享。
2. `runtime/http/idempotency.DeriveKey` 把身份元组编码为 `ns = tenantID（或 "_notenant"）`、`key = subject \x00 method \x00 path \x00 Idempotency-Key`——**不含 pod / cell / listener / instance 维度**。
3. `HTTPIdempotencyStore` 仅持 `{rdb cmdable, ns KeyNamespace}`，**无内存回放态**（Claim/Record/Release 全态在 Redis）。
4. Redis Cluster + 多 pod 拓扑（`loadRedisConfigFromEnv` / `Topology.RequiresDistributedReplay`）已接线；三个 Redis key（resp/lease/fp）已用 hashtag `{<key>}` 做 Cluster slot colocation。

缺的不是机制，而是**治理定调 + 证明测试 + 文档收口**——即把「`_runtime` 是 assembly-wide 幂等命名空间」从偶然 wiring 升为一条被结构守卫的契约。本 ADR 做这件事，并把 cross-cell 显式 carve-out（其依赖未构建）。

---

## Decision

### 1. `_runtime` 是框架级 **assembly-wide** HTTP 幂等命名空间（刻意豁免 per-cell 模型）

HTTP 幂等 store 的 owner `KeyNamespace` 定为 `_runtime`，语义是**整个 assembly 共享的单一去重域**，而非 per-cell 资源。

这与 per-cell namespace 模型（`.claude/rules/gocell/observability.md` §"Redis Key Namespace"：Cache 等 per-cell primitive 用 cell ID 作 namespace）**刻意不同**：HTTP 幂等是按 `(tenant, subject, method, path, Idempotency-Key)` 键控的**横切关切**——同一客户端对同一端点的重复请求必须去重，无论该请求落到哪个 pod、被哪个 cell 的路由处理。给它一个 per-cell namespace 反而会让同一逻辑请求在不同 cell 间各自去重，破坏框架语义。`REDIS-KEY-NAMESPACE-01`（per-cell `KeyNamespace.Validate()` funnel）仍治理 Cache / NonceStore / RedisDriver / IdempotencyClaimer，本决策只声明 HTTP 幂等 store 是该模型下的 `_runtime` sentinel 用例（与 consumer claimer 同沿用 sentinel 约定）。

**与 consumer claimer `_runtime` 无碰撞**：两者 key 结构不同——consumer claimer 为 `_runtime:{<eventID>}:lease|done`，HTTP store 为 `_runtime:<req-ns>:{<key>}:lease|resp|fp`（多一段 request-namespace）。详见 `cmd/corebundle/redis.go` 既有论证。

`http_requests_total{cell}` / `idempotency_requests_total{cell}` 对框架路径用 `cell="_runtime"` 哨兵，与本命名空间决策语义一致（均表达「框架 / 无 owner cell」）。

### 2. full-assembly（多 pod 共享）保证 = 四个结构事实

一个 idempotency-key 在整个 assembly 内有效（任意 pod 去重同一逻辑请求），当且仅当：

| # | 事实 | 载体 |
|---|------|------|
| a | 命名空间无 pod 维度（`_runtime` 对所有 pod 相同） | `cmd/corebundle.httpIdempotencyStoreNamespace` |
| b | 请求 key 无节点身份（pod/cell/listener/instance 不入 key） | sealed `IdempotencyKey` / `DeriveKey`，由闸 β 守 |
| c | store 无内存回放态（全态在 Redis ⟹ 任意 pod 见同一态） | `HTTPIdempotencyStore{rdb,ns}`，由闸 α 守 |
| d | 所有 pod 共享同一 Redis 后端（多 pod 强制 Redis） | `Topology.RequiresDistributedReplay` + `loadRedisConfigFromEnv` |

a/d 是 wiring/config（留本 ADR + review 守，强行 archtest 化属过度，见 §AI-robust）；b/c 是承载不变式，由两道结构闸 + 三层行为测试守（见 §Enforcement）。

> **消费者须知（namespace 定制语义）**：`_runtime` 表达「无 cell owner 的框架级去重域」。框架消费者若为 HTTP store 注入**不同** namespace，会形成**独立的去重域**——多 pod 下不同 namespace 的 pod 之间不共享幂等状态（无报错，是 `assemblyID` 级别去重域分区的预期行为，非 bug）。`cmd/corebundle` 刻意统一用 `_runtime`，使整个 assembly 落入同一去重域。

### 3. cross-cell 显式 **Out-of-scope**（blocked，→ #1610）

「同一 idempotency-key 在不同 cell/listener 间共享同一去重槽」**不在本 ADR**，原因：

- 依赖未构建：需要 HTTP `Idempotency-Key` ↔ `command_id` 映射桥（ADR-1044 §5 演进路径 ⑤ 显式延期的 #1044 子项；Command Bus 当前仅同步核心）。
- 语义未定：primary/internal 服务不同 path 前缀，key 含 `path` 故跨 listener 天然不碰撞；「跨 cell 共享同一 key」需从 key 去掉 path 维度 + 引入逻辑操作标识（= command_id）。
- 治理前置：跨 cell 同槽需新的共享 KeyNamespace 治理决策（本 ADR 只定调 assembly-wide 共享，不定调 cross-cell 同槽）。

跟踪于 **#1610**（blocked-by #1044）。

---

## Enforcement（AI-robust）

承载不变式 b/c 由两道结构闸守，full-assembly 行为由三层测试证明（结构闸 + 行为证据互补，不替代）。

| ID | 不变式 | 载体（範本） | 评级 |
|----|--------|-------------|------|
| `HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01`（α） | store 无内存回放态（事实 c） | reflect-freeze `adapters/redis.HTTPIdempotencyStore` 字段元组 `{rdb cmdable, ns KeyNamespace}`（reflect schema freeze） | **Hard** |
| `HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01`（β） | 请求 key 无节点身份（事实 b） | sealed typed `IdempotencyKey` 构造器 funnel：reflect 字段冻结 `{ns,key}` 未导出 string + go/types `DeriveKey` 唯一 producer + Unmarshal/Scan ban + `Store.Claim` 形参类型 = `IdempotencyKey`；require-isolation-tuple 由 `DeriveKey` 体 AST taint walk 守 | **node-agnostic：Hard 下游 + Hard 上游-external + Medium 上游-in-package；require-isolation：Medium（#1650）** |

- 两道闸的完整盲区清单 + 反向自检活在 `tools/archtest/http_idempotency_assembly_scope_test.go` 的 package godoc（ai-robust 单源约定）。
- 闸 β 已由 **#1610 funnel slice** 从 Medium AST **升级为 sealed typed `IdempotencyKey` 构造器 funnel**（详见 §Amendment 2026-06-06）。**node-agnostic / ban-external-source 现为 Hard**：下游 = `Store.Claim` 收 `IdempotencyKey`，裸 `(ns,key)` 串在 store 边界编译不可表达；上游-external = `IdempotencyKey{ns,key}` 未导出字段，包外不可 mint key（无法把 node-id 塞入）。**upstream-in-package 维持 Medium**——包内字面量 / 第二 producer 编译器不拦，reflect 字段冻结 + go/types sole-producer 扫描兜底（同 CellLabel / holder-seal #893 的 Hard 下游 + Medium 上游-in-package 分轴）。**require-isolation-tuple 维持 Medium**——Go 无法表达「`DeriveKey` 体消费全部 5 个隔离维度」，且 5 个同型 string 参可在唯一 callsite 转置；`DeriveKey` 体 taint walk 兜底，**won't-do 天花板跟踪 gh #1650**（同 #851/#893/#1282/#1552 family；**非** typestate-builder 可升级——builder 只锁「全部 setter 被调用」，positional 参已给该保证，不锁 body-flow）。cross-cell 同槽（key↔command_id 桥）仍 **out-of-scope，跟踪 #1610**（blocked-by #1044），与本 funnel 正交。
- 事实 a/d（共享 store / `_runtime` 命名空间 / 多 pod 强制 Redis）= wiring/config，**不立 archtest**：对单个 `_runtime` const 做字符串锁是 Soft（ai-robust 禁立项），强行 Hard 化属过度（违优雅简洁）；由本 ADR + review 守。

三层行为测试（沿用既有 Redis 测试 idiom）：

| 层 | 文件 | 触发 | 证明 |
|----|------|------|------|
| B 单元 | `adapters/redis/http_idempotency_cluster_slot_test.go`（无 tag，CI 默认） | always | resp/lease/fp 三键经真 `apply`/`applyHashtag` colocate 同 slot（Cluster EVAL 安全）+ 反向 counter-test |
| A 集成 | `adapters/redis/http_idempotency_assembly_scope_test.go`（`//go:build integration`，CI integration job） | Docker | 两 store 实例（模拟两 pod）共享 Redis → 跨 pod 回放（ClaimDone + 同响应）/ 跨 pod busy 锁 / 跨 pod 指纹失配 |
| C cluster | `adapters/redis/http_idempotency_cluster_real_test.go`（`//go:build integration_cluster`，operator 带外） | `GOCELL_TEST_REDIS_CLUSTER_ADDRS` | 真 Cluster 上 claim/record/re-claim 无 CROSSSLOT |

A 层「两 store 实例 = 两 pod」之所以faithful：store 无内存态（α 结构守），实例身份与回放态位置无关，唯一共享底物是 Redis。

---

## 威胁矩阵（继承 ADR-1043 §威胁矩阵 + full-assembly 新增行）

ADR-1043 威胁矩阵全部 ✅ 行在 assembly scope 下**不退化**（key 隔离维度 `(tenant, subject, method, path, header)` 不变；扩大的只是「同一 key 在多 pod 间共享」这一预期能力，非隔离面）。新增行：

| 威胁 | 缓解 | 状态 |
|------|------|------|
| 跨 pod 回放越权（A pod 录的响应被 B pod 回放给他人） | key 仍含 `subject + tenant`；跨 pod 共享的是**同一认证主体本人**先前在授权下已执行操作的已录响应，跨主体回放结构上不可能（同 ADR-1043「回放跳过授权再校验」by-design 行）。多 pod 不放宽隔离，只共享去重域 | ✅ (by-design) |
| 跨 pod 指纹绕过（B pod 用不同 body 劫持 A pod 录的 key） | 指纹 blob 在 Redis（共享底物），任意 pod Claim 同 key 异 fp → `ErrFingerprintMismatch` → 422；A 层集成测试 `CrossPodFingerprintMismatch` 覆盖 | ✅ |
| Cluster CROSSSLOT（多 KEY EVAL 跨 slot 失败 → 幂等静默失效） | 三键共 hashtag `{<key>}` colocate；B 层单元静态守 + C 层真 cluster 守 | ✅ |
| 节点身份污染 key（未来改动把 pod/cell id 拼进 key → 破坏跨 pod 去重） | 闸 β（sealed `IdempotencyKey`：包外无法 mint key / 无法把 node-id 塞入；`Store.Claim` 收 sealed 类型）+ 闸 α（无内存态）结构拦截；#1610 funnel slice 后 node-agnostic 轴由 Medium AST **升级为 type-system Hard** | ✅（Hard 强化） |
| 单租户→多租户迁移 namespace 碰撞（`_notenant` 期录的 key 在分配 tenant UUID 后被误回放） | 结构性无碰撞——request-ns 前缀 `_notenant:…` 与任何 `<uuid>:…` 字节不相等，是不同 Redis key；旧 `_notenant` key 在 24h `done` TTL 内自然过期，无需主动清理 | ✅ (结构性) |
| 日志 `tenant_id` 明文（replay/busy/mismatch 路径 slog 携带 `tenant_id=ns`） | by-design——对齐 outbox observability 规范「actor/subject/tenant opaque 明文」；`tenant_id` 非密钥（opaque UUID 或 `_notenant`），`pkg/redaction.IsSensitiveKey` 刻意不含 `tenant_id`；assembly scope 未改变该日志面 | ✅ (by-design，不变) |

无 ✅ → ⚠️/❌ 退化格子（#1610 funnel slice amendment 仅强化「节点身份污染 key」格 Medium→Hard，无退化）。

---

## 覆盖范围

本决策覆盖**走 compositionAPI assembly 的生产装配**——当前 `cmd/corebundle`（+ `examples/corebundlestarter`），它们经 `bootstrap.WithIdempotencyStore(redis.NewHTTPIdempotencyStore(client, "_runtime"))` 接通。legacy-form 示例 assembly（todoorder / iotdevice / orderfulfillment）当前未 wire HTTP 幂等 store，不在范围；它们采用 HTTP 幂等是后续工作（与其 compositionAPI 迁移一并）。

---

## Consequences

**Positive**：

- full-assembly 多 pod 幂等回放从「偶然成立的涌现属性」升为「被两道结构闸 + 三层测试守的契约」，未来改动无法静默破坏。
- 治理定调消除 #1449 前置缺口：`_runtime` = assembly-wide 是显式决策，per-cell 模型与本 sentinel 用例的边界清晰。
- 零生产签名 / 命名空间模型改动——纯证明 + 定调 + 结构锁，无回归面。

**Negative / 已知限制**：

- cross-cell（跨 cell 同槽）仍未交付，blocked on #1044 命令桥（#1610）。
- 闸 β 的 node-agnostic 轴已由 #1610 funnel slice 升级为 sealed `IdempotencyKey` type-system Hard（下游 + 上游-external）；upstream-in-package + require-isolation-tuple 维持 Medium（Go 天花板，won't-do gh #1650）。
- legacy-form 示例 assembly（todoorder / iotdevice / orderfulfillment）未接通 HTTP 幂等 store（见 §覆盖范围）。**消费者风险**：以这些示例为模板构建自定义 assembly 时，需手动参照 `cmd/corebundle.buildHTTPIdempotencyStore` + `bootstrap.WithIdempotencyStore` 接线，否则幂等中间件**静默 skip**（未注入 store → router 无 store → Middleware 不安装），生产缺去重保护而无报错。

---

## Alternatives Considered

- **给 HTTP 幂等 store per-cell namespace**（被拒）：破坏框架语义——同一逻辑请求会在不同 cell 各自去重；HTTP 幂等是横切关切非 per-cell 资源。
- **新建独立 `_assembly` / `_cluster` namespace tier**（被拒）：`_runtime` sentinel 已表达「框架 / 无 owner cell」且与 metrics `cell=_runtime` / consumer claimer 同源；新增 tier 是无收益的命名分裂。
- **对 `_runtime` const 立字符串锁 archtest 守事实 a**（被拒）：Soft（字符串锚点），ai-robust 禁立项；wiring/config 由 ADR + review 守。
- **本 PR 一并做 cross-cell**（被拒）：依赖 #1044 命令桥未构建，强行做会引入 vaporware 抽象。

---

## Implementation

```
Change: 治理定调 _runtime=assembly-wide + 两道结构闸 + 三层证明测试 + 文档收口（零生产签名改动）
Gates:
  [x] tools/archtest/http_idempotency_assembly_scope_test.go
      - HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01 (α, reflect freeze, + reverse self-check)
      - HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01 (β, sealed IdempotencyKey funnel: reflect freeze + go/types sole-producer + Store.Claim param + DeriveKey taint walk; + reverse self-checks) [#1610 funnel slice]
Tests:
  [x] adapters/redis/http_idempotency_cluster_slot_test.go        (B, no tag)
  [x] adapters/redis/http_idempotency_assembly_scope_test.go      (A, //go:build integration)
  [x] adapters/redis/http_idempotency_cluster_real_test.go        (C, //go:build integration_cluster)
  [x] runtime/http/idempotency/idempotencytest: export BuildRecordedResponse (single-source replay fixture)
Docs:
  [x] docs/architecture/202606021000-1043-...middleware.md §9 + 威胁矩阵 (full-assembly ❌→✅ amendment)
  [x] docs/ops/redis-cluster-deployment.md (full-assembly multi-pod section)
  [x] docs/ops/listener-topology.md (idempotency assembly-scoped note)
  [x] cmd/corebundle/redis.go + runtime/http/idempotency/middleware.go (godoc → this ADR)
Repro:
  go test ./tools/archtest/ -run 'StatelessFrozen01|KeyNodeAgnostic01'
  go test ./adapters/redis/ -run 'HTTPIdempotency.*Slot'
  go test -tags=integration ./adapters/redis/ -run 'AssemblyScope'   (needs Docker; CI integration job)
Out-of-scope: cross-cell (idempotency-key ↔ command_id) → #1610 (blocked-by #1044)
```

---

## Amendment 2026-06-06 — #1610 funnel slice（闸 β Medium AST → sealed `IdempotencyKey` Hard）

#1610 的核心交付（cross-cell 同槽 = idempotency-key ↔ command_id 桥）被 #1044（Command Bus dispatcher + outbox 桥）**硬阻塞**——`command_id` 在 develop 不存在，无法在 #1610 内造。本次只交付 #1610「彻底」方向中**独立可交付**的一块：把 key 派生收口成 sealed typed `IdempotencyKey` 构造器 funnel（原 §Enforcement 注列为闸 β 的 Hard 升级路径）。**因此本次 PR `Refs #1610`（partial，非 Closes）——cross-cell 核心仍 open，blocked-by #1044。**

**改动**（零 wire 改动；签名内部演化）：

- 新 sealed 类型 `runtime/http/idempotency.IdempotencyKey{ns,key}`（未导出字段）+ 唯一构造器 `DeriveKey(tenantID, subject, method, path, idemKey string)` + 取值器 `Namespace()/Key()`。删 `buildNamespaceKey`（自由函数）。
- `Store.Claim` 形参 `(ns, key string)` → `(k IdempotencyKey)`；`MemStore` / `adapters/redis.HTTPIdempotencyStore` / middleware callsite / conformance / 单元 + 集成测试同步。
- `DeriveKey` 改收**扁平 string**（非 `*auth.Principal`）：5 个隔离维度即字面签名，消除原 `*auth.Principal` selector/alias AST 盲区（`Principal.TenantID` 本就是 `string`）。
- Redis 适配器 brace/空守卫保留（Redis-Cluster hashtag 约束，store-specific，**不**折入 store-agnostic `DeriveKey`）。

**评级重评**（诚实、非 flat Hard）：node-agnostic / ban-external-source = **Hard 下游**（typed `Store.Claim` sink）+ **Hard 上游-external**（未导出字段 sealed construction）+ **Medium 上游-in-package**（reflect 字段冻结 + go/types sole-producer 兜底，同 #893）；require-isolation-tuple = **Medium**（`DeriveKey` 体 taint walk；Go 无法 type-Hard「体消费全部 5 参」+ 同型参可转置；won't-do 天花板 **gh #1650**，非 typestate-builder 可升级）。

**威胁矩阵重评**：「节点身份污染 key」格 Medium→Hard 强化，无 ✅→⚠️/❌ 退化（见 §威胁矩阵）。

**演进缺口**：cross-cell 落地（#1044 桥就绪后）将新增兄弟构造器（如 `DeriveCommandKey`）产出同一 sealed `IdempotencyKey`——funnel 基建是耐久层，届时只需把新构造器加入 β archtest 的 sole-producer allowlist。设计真值源仍为本 ADR + ai-robust 章程；funnel 符号清单 + 完整盲区清单活在 `tools/archtest/http_idempotency_assembly_scope_test.go` package godoc。
