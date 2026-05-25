# ADR: Capability Provider 接口（runtime/capability/）

- **状态**：Accepted
- **日期**：2026-05-25
- **Issue**：#854（P0-1）
- **来源路线**：`docs/plans/framework-capability-gaps/202605221303-006-spring-comparison-and-simplification-roadmap.md` §4 P0-1
- **相关**：#855（P0-2，复用本接口）；ADR `202605201400-adr-relay-managedresource-isolation.md`（relay 归属不变）

## Context

composition root（`cmd/corebundle/*_module.go`）的 infra wiring 全手写。三个平台 cell 在 postgres 模式各自从一个共享 pool 派生 `adapterpg.NewTxManager` / `NewOutboxWriter` / `pool.DB()`，pool 由 configcore 创建后经 `SharedDeps.SharedPGPool` publish-back 给 access/audit。这套耦合带来：

- `SharedPGPool` 是 composition-root bag 上的一个隐式 happens-before 字段（configcore 写、其余读）。
- `MODULE-ORDER-CONFIGCORE-FIRST-01` archtest 仅为保护 "configcore 先创建 pool" 而存在（`cmd/corebundle/main_test.go:65`）。
- 每个 module 重复 `NewTxManager(pool)` / `NewOutboxWriter(clock)` / `pool.DB()` 派生胶水。

P0-1 目标：把 adapter 派生胶水收口到标准 capability provider，composition root ~13k→~4k LOC。

**架构约束**：`runtime/` 禁止 import `adapters/`（CLAUDE.md 分层规则）。capability 接口若住 `runtime/capability/`，不能在签名里出现 `*adapterpg.Pool` 等 adapter 类型。

## Decision

### 1. capability = assembly 级共享资源 provider（fx.Supply 形态）

capability 是 **assembly 一次性 provision、注入消费 cell** 的共享资源，**不是** per-cell 构造。这对齐编译期 DI（Wire/Dagger）/ uber-fx `fx.Supply`——共享值 provision 一次，多 module 消费。pool 生命周期（open/close、ManagedResource、LIFO teardown）由 assembly composition root 持有。

**为什么不是 per-cell**：per-cell `requires:[postgres]` 让每个 cell 自开 pool，破坏单 pool + LIFO shutdown，且与"一个 outbox 表 / 一个 relay"矛盾。共享资源天然是 assembly 级。

### 2. 接口 + sealed 构造（`runtime/capability/capability.go`，仅 import kernel/ + pkg/）

unexported marker method 使接口**只能在 `capability` 包内实现**——故 impl 私有 struct + 构造函数都住 `capability`，入参是 kernel 类型 + `any`（`capability` 永不 import adapters）；`cmd/` 只**调用**构造函数，传入 adapter 构造的值。

```go
package capability
type Kind string
const (
    Postgres Kind = "postgres"
    Redis    Kind = "redis"
    RabbitMQ Kind = "rabbitmq" // recognized, but NO provider/provisioning yet (fail-fast)
)
type PGProvider interface {
    TxManager() persistence.TxRunner   // kernel/persistence
    OutboxWriter() outbox.Writer       // kernel/outbox
    DB() any                           // *adapterpg.Pool.DB()；仅 cmd/ 内 type-assert
    isPGProvider()                     // unexported marker → 包外不可实现
}
type pgProvider struct { tx persistence.TxRunner; writer outbox.Writer; db any }
func (p pgProvider) TxManager() persistence.TxRunner { return p.tx }
func (p pgProvider) OutboxWriter() outbox.Writer     { return p.writer }
func (p pgProvider) DB() any                          { return p.db }
func (pgProvider) isPGProvider()                      {}
// NewPGProvider 是唯一构造路径（sealed）。
func NewPGProvider(tx persistence.TxRunner, writer outbox.Writer, db any) PGProvider {
    return pgProvider{tx: tx, writer: writer, db: db}
}
// RedisProvider 同形：私有 redisProvider struct + NewRedisProvider(client any) RedisProvider。
```

`cmd/corebundle` 构造站点：

```go
pg := capability.NewPGProvider(adapterpg.NewTxManager(pool), adapterpg.NewOutboxWriter(clk), pool.DB())
```

- `DB()` / `Client()` 返回 `any` 是保持 `runtime/capability` adapter-free 的刻意 typed-erasure：consumer cell 不在 runtime/capability 层，`any → *adapterpg.Pool.DB()` 断言只发生在 `cmd/corebundle`（允许 import adapters）内的 consumer 处。
- impl 私有 + 唯一构造函数 → cell module 无法伪造 bypass provider（sealed-construction，AI-robust Hard 范本）。

### 3. SharedDeps：删 `SharedPGPool`，加 `PG`/`Redis` capability handle

- `runCorebundle` 在 `LoadSharedDepsFromEnv` 之后、`BuildApp` 之前调用 `provisionCapabilities`（`cap_wiring.go`），遍历 codegen 派生的 `generatedCapabilities()`（单源 = `assembly.yaml capabilities:[]`）逐项 provision：postgres 模式下 open pool + `verifyPGPreconditions`（schema version / shape / invalid-index——用 bundle-global `adapterpg.MigrationsFS()`，本就是整 bundle 的共享迁移集，非 configcore 私有）+ 构造 `capability.PGProvider` 写 `SharedDeps.PG`；redis 包装 `buildSharedReplayDeps` 已建的 client 写 `SharedDeps.Redis`（client 本身仍在 `buildSharedReplayDeps` 构造，claimer/nonce 同源）。memory 模式 `SharedDeps.PG` 保持 nil，cell 走 in-memory 路径。
- **删** `SharedDeps.SharedPGPool *adapterpg.Pool`（+ `RedisClient` 公开字段，改 `Redis capability.RedisProvider` + unexported `redisClient`）。
- pool 的 `ManagedResource`（`SharedDeps.poolMR`）由 `runtimeBaseOptions`（`defaultRuntimeOptions` 的一部分，先于 cellOpts append）注册 → LIFO 下 pool 最后 close（晚于所有 consumer）。`provisionCapabilities` 与 `bootstrap.Run` 之间的失败窗口由 `runCorebundle` 的 `handedToBootstrap` defer 守卫关池。
- **删** `MODULE-ORDER-CONFIGCORE-FIRST-01`（`tools/archtest/module_order_test.go`）+ `main_test.go:65` 断言：前提（configcore 创建 pool）消失。

留在 SharedDeps（composition-state，非外部系统 capability）：`BootstrapLedgerStore`（audit→access cell-domain store publish-back，`MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01` 不变）、`Clock`、`JWTDeps`、`PromStack`、`EventBus`、`InternalGuard`、listener addrs、`vaultTransitMetricsOnce`。

### 4. 不变的归属

- **outbox relay 仍归 configcore**：relay 是唯一实例、metric label `configcore`、经 `WithRelay`（`RELAY-SOLE-HOLDER-01` 守）。它从 `shared.PG.DB()` 取 pool handle 构造，不再自开 pool。保留 configcore 归属避免 observability label churn；relay 不要求 module 顺序（只需 pool 存活，LIFO 已保证 pool 后 close）。
- **protocol 构造**（`cas/session/ledger.NewProtocol`）仍 composition-root-only：module 文件在 `cmd/corebundle/ package main` 内，在既有 archtest allowlist 内。

## Enforcement（已落地）

四个机制协同；下表是 amendment 后的真实形态（取代落地前的"funnel 预声明、上游 Hard codegen funnel + 下游 Hard"草案）：

| # | 约束 | 载体 | 评级 |
|---|------|------|------|
| 1 | `assembly.yaml capabilities` ∈ 封闭集 `{postgres,redis,rabbitmq}`、无重复 | `gocell validate` governance rule **FMT-35** + 单源 `metadata.CapabilityEnum` + `IsKnownCapability`；schema enum 由 `TestSchemaConstantsMatchSchemaLiterals` 字节对齐 | **Medium**（governance rule + 测试守卫；3 值封闭集不值得引 codegen funnel——K8s 旧 enum 模式同构，校验落 admission 层而非 parser） |
| 2 | `modules_gen.go`（含 `generatedCapabilities()`）= `assembly.yaml` 派生 | `gocell verify codegen-assembly` regenerate-and-diff 字节锁 | **Hard**（codegen funnel + golden；既有机制，现覆盖 capabilities 分支） |
| 3 | cell module 不能伪造 bypass provider | `capability.PGProvider`/`RedisProvider` sealed（unexported marker + 私有 impl + 唯一构造 `capability.NewPGProvider`/`NewRedisProvider`），包外不可表达 | **Hard**（sealed construction，type system） |
| 4 | `cmd/<id>/*_module.go` 不直接构造**共享基建** | archtest **CAPABILITY-PROVIDER-FUNNEL-01**（`tools/archtest/capability_provider_funnel_test.go`）：ban `adapterpg.NewPool`/`NewTxManager`/`NewOutboxWriter` + `adapterredis.NewClient`，caller allowlist = `cmd/corebundle/cap_wiring.go` + `_test.go`；**不 ban** per-cell 派生（`NewSessionStore`/`NewCache`/`NewRedisDriver`/…，从注入 handle 构造，CLAUDE.md observability §per-cell 资源约定）；镜像 `CAS-PROTOCOL-COMPOSITION-ROOT-01` | **Medium**（type-aware caller-allowlist，非编译期） |

机制 3（上游 Hard）+ 机制 4（下游 Medium）= ai-robust.md §"Funnel 双向锁评级"允许的 **Hard 上游 + Medium 下游过渡形态**；下游→Hard 升级路径（把 `cmd/corebundle` module 文件移出 `package main` 使共享基建构造 import-unreachable）由 gh issue **#988** 跟踪。

落地前草案把上游写成"Hard codegen funnel"——**经评估撤回**：capabilities 是 3 值封闭集，建 codegen funnel（工具链 + 模板 + meta-archtest）的成本不匹配收益，改用项目既有 `DeployTemplateEnum`/`FMT-30` 同构的 governance + test-guard（机制 1，Medium）。同理把下游单格"Hard（sealed + caller allowlist）"拆为机制 3（sealed=Hard）与机制 4（archtest caller-allowlist=Medium）两栏，因为 caller-allowlist archtest 本身不是编译期 Hard。

## Rejected alternatives

- **Shape B：provider 返回 concrete `*adapterpg.Pool`**——不能住 `runtime/capability/`（违反 runtime 不依赖 adapters），且不是 assembly-reusable 的抽象。`DB() any` 的 typed-erasure 是为住 runtime/capability 付的刻意代价。
- **per-cell capability 构造**——破坏单 pool + LIFO（见 Decision §1）。
- **保留 `SharedPGPool` + 加 `capability.PGProvider` read-through view**——双路径 / 两真理源，违反"不向后兼容 / 优雅"。

## DG-2 gate

006 roadmap §5 DG-2："005 W0/W1 若涉及 capability registration / module wire，P0-1 应等 W0 稳定"。调查：005 W0 = `outbox.Entry.Headers` envelope（未启动），W1 = after-commit hooks（部分落地），**均不定义 capability-registration 接口** → 条件不触发，P0-1 可立即启动。残余风险：未来 W-wave 若引入自己的 capability registration，回灌到本 ADR——`runtime/capability` 是唯一 sanctioned capability-provider 源。

## Consequences

- 三 module 的 postgres/redis 派生胶水收口到 provider，configcore 不再 publish-back / 不再返回 PoolResource（pool MR 由 assembly 注册）。
- 删一个隐式 SharedDeps 字段 + 一个 archtest + 一处 module 顺序断言。
- #855 在本接口上做 `{cell}_module.go` codegen（生成体消费 `cap.*Provider`）。
