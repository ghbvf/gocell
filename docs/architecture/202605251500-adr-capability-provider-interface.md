# ADR: Capability Provider 接口（runtime/cap/）

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

**架构约束**：`runtime/` 禁止 import `adapters/`（CLAUDE.md 分层规则）。capability 接口若住 `runtime/cap/`，不能在签名里出现 `*adapterpg.Pool` 等 adapter 类型。

## Decision

### 1. capability = assembly 级共享资源 provider（fx.Supply 形态）

capability 是 **assembly 一次性 provision、注入消费 cell** 的共享资源，**不是** per-cell 构造。这对齐编译期 DI（Wire/Dagger）/ uber-fx `fx.Supply`——共享值 provision 一次，多 module 消费。pool 生命周期（open/close、ManagedResource、LIFO teardown）由 assembly composition root 持有。

**为什么不是 per-cell**：per-cell `requires:[postgres]` 让每个 cell 自开 pool，破坏单 pool + LIFO shutdown，且与"一个 outbox 表 / 一个 relay"矛盾。共享资源天然是 assembly 级。

### 2. 接口 + sealed 构造（`runtime/cap/cap.go`，仅 import kernel/ + pkg/）

unexported marker method 使接口**只能在 `cap` 包内实现**——故 impl 私有 struct + 构造函数都住 `cap`，入参是 kernel 类型 + `any`（`cap` 永不 import adapters）；`cmd/` 只**调用**构造函数，传入 adapter 构造的值。

```go
package cap
type Capability string
const (
    CapabilityPostgres Capability = "postgres"
    CapabilityRedis    Capability = "redis"
    CapabilityRabbitMQ Capability = "rabbitmq"
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
pg := cap.NewPGProvider(adapterpg.NewTxManager(pool), adapterpg.NewOutboxWriter(clk), pool.DB())
```

- `DB()` / `Client()` 返回 `any` 是保持 `runtime/cap` adapter-free 的刻意 typed-erasure：consumer cell 不在 runtime/cap 层，`any → *adapterpg.Pool.DB()` 断言只发生在 `cmd/corebundle`（允许 import adapters）内的 consumer 处。
- impl 私有 + 唯一构造函数 → cell module 无法伪造 bypass provider（sealed-construction，AI-robust Hard 范本）。

### 3. SharedDeps：删 `SharedPGPool`，加 `PG`/`Redis` capability handle

- `LoadSharedDepsFromEnv` 按 `assembly.yaml capabilities:[]` provision：postgres 模式下 open pool + `verifyPGPreconditions`（schema version / shape / invalid-index——用 bundle-global `adapterpg.MigrationsFS()`，本就是整 bundle 的共享迁移集，非 configcore 私有）+ 构造 `cap.PGProvider`，写 `SharedDeps.PG`；redis 同理写 `SharedDeps.Redis`。
- **删** `SharedDeps.SharedPGPool *adapterpg.Pool`。
- pool 的 `ManagedResource` 由 assembly 在 `defaultRuntimeOptions` 输出（先于 cellOpts append）注册 → LIFO 下 pool 最后 close（晚于所有 consumer）。
- **删** `MODULE-ORDER-CONFIGCORE-FIRST-01`（`tools/archtest/module_order_test.go`）+ `main_test.go:65` 断言：前提（configcore 创建 pool）消失。

留在 SharedDeps（composition-state，非外部系统 capability）：`BootstrapLedgerStore`（audit→access cell-domain store publish-back，`MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01` 不变）、`Clock`、`JWTDeps`、`PromStack`、`EventBus`、`InternalGuard`、listener addrs、`vaultTransitMetricsOnce`。

### 4. 不变的归属

- **outbox relay 仍归 configcore**：relay 是唯一实例、metric label `configcore`、经 `WithRelay`（`RELAY-SOLE-HOLDER-01` 守）。它从 `shared.PG.DB()` 取 pool handle 构造，不再自开 pool。保留 configcore 归属避免 observability label churn；relay 不要求 module 顺序（只需 pool 存活，LIFO 已保证 pool 后 close）。
- **protocol 构造**（`cas/session/ledger.NewProtocol`）仍 composition-root-only：module 文件在 `cmd/corebundle/ package main` 内，在既有 archtest allowlist 内。

## Enforcement（funnel 预声明，下游 PR 落地）

`CAPABILITY-PROVIDER-FUNNEL-01`（`tools/archtest/capability_provider_funnel_test.go`）：

| 方向 | 形态 | 评级 |
|------|------|------|
| 上游 | `assembly.yaml capabilities` schema `enum` + parser reject；生成 capability wiring regenerate-and-diff 字节锁（`gocell generate assembly --verify`） | Hard（codegen funnel + golden） |
| 下游 | `cap.*Provider` sealed（impl 私有于 `cap`，唯一构造 `cap.NewPGProvider`）→ 包外不可伪造 provider；archtest 禁 `cmd/<id>/*_module.go` 在 funnel 外直接 `adapterpg.NewTxManager/NewPool` / `adapterredis.NewCache`（caller allowlist = assembly 构造站点 `cmd/corebundle/cap_wiring.go` + `_test.go`） | Hard（sealed construction + caller allowlist；镜像 `CAS-PROTOCOL-COMPOSITION-ROOT-01`） |

## Rejected alternatives

- **Shape B：provider 返回 concrete `*adapterpg.Pool`**——不能住 `runtime/cap/`（违反 runtime 不依赖 adapters），且不是 assembly-reusable 的抽象。`DB() any` 的 typed-erasure 是为住 runtime/cap 付的刻意代价。
- **per-cell capability 构造**——破坏单 pool + LIFO（见 Decision §1）。
- **保留 `SharedPGPool` + 加 `cap.PGProvider` read-through view**——双路径 / 两真理源，违反"不向后兼容 / 优雅"。

## DG-2 gate

006 roadmap §5 DG-2："005 W0/W1 若涉及 capability registration / module wire，P0-1 应等 W0 稳定"。调查：005 W0 = `outbox.Entry.Headers` envelope（未启动），W1 = after-commit hooks（部分落地），**均不定义 capability-registration 接口** → 条件不触发，P0-1 可立即启动。残余风险：未来 W-wave 若引入自己的 capability registration，回灌到本 ADR——`runtime/cap` 是唯一 sanctioned capability-provider 源。

## Consequences

- 三 module 的 postgres/redis 派生胶水收口到 provider，configcore 不再 publish-back / 不再返回 PoolResource（pool MR 由 assembly 注册）。
- 删一个隐式 SharedDeps 字段 + 一个 archtest + 一处 module 顺序断言。
- #855 在本接口上做 `{cell}_module.go` codegen（生成体消费 `cap.*Provider`）。
