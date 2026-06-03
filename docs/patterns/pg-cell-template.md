<!-- ref: docs/plans/202604232330-025-architecture-pr-implementation-plan.md §PR-A3 T6 -->

# PG Cell 接入模板（per-cell adapter 模型）

> 文档版本: 2026-05-31（#1085 composition API 迁移后更新）
> 读者: 要给某个 cell 接 PostgreSQL 的开发者
> 前置阅读: docs/architecture/ 里的分层规则 + docs/ops/env-vars.md 的 env 命名表

> ⚠️ **组装层已迁移（#1085）**：本文档 Chapter 1 起描述的 `BuildApp(...)` +
> `cmd/corebundle/<cell>_module.go` + `bundle_<cell>_storage.go` 模式**已废弃**。
> 平台 cell 的 composition module 现在活在 `cellmodules/<cell>/module.go`，实现公开的
> `composition.CellModule` 接口（`Provide(ctx, shared) (cell.Cell,
> []bootstrap.Option, []lifecycle.ManagedResource, error)`——Wave-1 #1423 删除了
> `ModuleExports` 入参与返回值，跨 cell 通信改走事件契约，签名由
> `MODULE-PROVIDE-NO-VALUE-HANDOFF-01` archtest 冻结），由
> `composition.New().With(modules...).Build(ctx, shared, runtimeOptsFn)` 组装；
> `SharedDeps` 经 `composition.NewSharedDeps(...)` 构造（sealed marker），`Topology`
> 经 `bootstrap.NewTopology(...)` 构造（postgres 强制 real）。**canonical 参考实现见
> `cellmodules/configcore/module.go` + `cellmodules/configcore/storage.go`**。下文的
> PG storage 接线逻辑（pool / TxManager / OutboxWriter / migration）以及 Chapter 4
> 的资源生命周期"两处"契约仍然准确，只是承载它的文件位置（`CellModule.Provide` 而非旧
> `BuildApp`/`cmd/corebundle/<cell>_module.go`）与组装入口变了——按上述新位置套用。

---

## Chapter 1 — 模型总览

per-cell adapter 模型把依赖分成两条独立的路径：

```
operator env
     │
     ├─── composition.NewSharedDeps(...)        ← cross-cutting 只构造一次
     │         JWT / Prometheus / EventBus
     │         InternalGuard / MetricsToken / VerboseToken
     │         PG capability provider (shared pool)
     │         └─→ *composition.SharedDeps
     │
     └─── CellModule.Provide(ctx, shared)       ← per-cell 各自读自己的 env
               GOCELL_<CELLID>_CURSOR_KEY
               GOCELL_<CELLID>_CURSOR_PREVIOUS_KEY
               （PG URL / TxManager / OutboxWriter 经 shared.PG 取得）
               └─→ (cell.Cell, []bootstrap.Option, []ManagedResource, error)

     ↓
composition.New().With(moduleA, moduleB, ...).Build(ctx, shared, runtimeOptsFn)
     ↓
runtimeOptsFn(cells []cell.Cell) → []bootstrap.Option
     ↓
app.Run(ctx)   // *composition.App.Run → bootstrap.New(opts...).Run(ctx)
```

两条原则：

1. **cross-cutting 只在 SharedDeps**: JWT 秘钥、Prometheus 注册表、EventBus、
   PG capability provider、control-plane token 由 `composition.NewSharedDeps(...)`
   统一构建，不在 CellModule 里重读或自行开 pool。
2. **per-cell adapter 配置由 CellModule.Provide 自己读**: cursor key 等带
   `GOCELL_<CELLID>_` 前缀的 env 由对应 Module 自行解析；PG TxManager、
   OutboxWriter 由 `shared.PG` 供给，互不干扰。

---

## Chapter 2 — Env 命名约定

命名模式：`GOCELL_<CELLID>_<RESOURCE>_<KNOB>`

| 变量 | 用途 | 必填条件 |
|---|---|---|
| `GOCELL_<CELLID>_DATABASE_URL` | PostgreSQL DSN | postgres mode |
| `GOCELL_<CELLID>_DATABASE_MAX_CONNS` | 最大连接数（正整数） | 否，默认 10 |
| `GOCELL_<CELLID>_DATABASE_IDLE_TIMEOUT` | 空闲超时（Go duration，如 `5m`） | 否 |
| `GOCELL_<CELLID>_DATABASE_MAX_LIFETIME` | 最大生存时间（Go duration） | 否 |
| `GOCELL_<CELLID>_CURSOR_KEY` | 游标 HMAC 主密钥 | real mode |
| `GOCELL_<CELLID>_CURSOR_PREVIOUS_KEY` | 游标 HMAC 前置密钥（轮换用） | 否 |
| `GOCELL_<CELLID>_KEY_PROVIDER` | 加密 KeyProvider（`local-aes` / `vault-transit`） | postgres mode |
| `GOCELL_<CELLID>_MASTER_KEY` | local-aes 模式 32 字节 hex AES 主密钥 | 当 KEY_PROVIDER=local-aes |
| `GOCELL_<CELLID>_MASTER_KEY_PREVIOUS` | 前置主密钥（轮换用） | 否 |

完整清单（含跨 cell 公共变量）见 `docs/ops/env-vars.md`。

**fail-fast 契约**:

- `LoadPGConfig` 在收到非法整数（如 `MAX_CONNS=abc`）或非法 duration（如
  `IDLE_TIMEOUT=bad`）时立即返回包含变量名的错误；进程在 `run()` 返回前就停止，
  不会进入服务循环。
- `LoadCursorKeys` 只读取字符串，不做校验；后续 `buildCursorCodec` 在
  real mode 下遇到空值才 fail-fast。
- postgres mode 下 `KEY_PROVIDER` 为空 → 启动失败，不静默降级为 NoopTransformer。

---

## Chapter 3 — 新 Cell 接入 PG 的最小步骤

以新建 `foocore` cell 为例，走完整流程。

### Step 1. 新建 cell.yaml

```yaml
# cells/foocore/cell.yaml
id: foocore
type: core
consistencyLevel: L2
owner:
  team: platform
  role: maintainer
schema:
  primary: foo_entries
verify:
  smoke: go test ./cells/foocore/... -run TestSmoke -count=1
```

### Step 2. 新建 `cellmodules/foocore/module.go`

```go
// Package foocore is the platform composition module for the foocore Cell.
// It implements [composition.CellModule] and wires all foocore-specific
// dependencies from [composition.SharedDeps].
//
// This is a composition-root-layer package: it may import cells/, adapters/,
// and cellmodules/cellsecrets/. It must NOT be imported by cells/, runtime/, or
// adapters/.
package foocore

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	foocorecell "github.com/ghbvf/gocell/cells/foocore"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
)

type module struct{}

// Module returns a composition.CellModule that wires the foocore Cell.
func Module() composition.CellModule { return module{} }

// ID returns the stable identifier used in error messages and logs.
func (module) ID() string { return "foocore" }

// Provide resolves all foocore-specific dependencies and returns the
// constructed cell, bootstrap options, and provisional resources.
//
// Reads GOCELL_FOOCORE_CURSOR_KEY, GOCELL_FOOCORE_CURSOR_PREVIOUS_KEY from
// the environment. PG DSN and pool are supplied via shared.PG.
func (m module) Provide(
	_ context.Context, shared *composition.SharedDeps,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// 1. Cursor codec.
	pri, prev := cellsecrets.LoadCursorKeys("FOOCORE")
	cursorCodec, err := cellsecrets.BuildCursorCodec(cellsecrets.CursorCodecConfig{
		AdapterMode: shared.Topology.AdapterMode(),
		EnvName:     "GOCELL_FOOCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_FOOCORE_CURSOR_PREVIOUS_KEY",
		Primary:     pri,
		Previous:    prev,
		DevDefault:  "foocore-cursor-key-32-byte-def!",
		Label:       "foo",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("foocore cursor codec: %w", err)
	}

	// 2. Storage-backend branching via shared.Topology + shared.PG.
	modResult, err := buildFooCoreOpts(shared.Clock, fooCoreModuleConfig{
		topology:  shared.Topology,
		pg:        shared.PG,
		publisher: shared.EventBus,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	baseOpts := []foocorecell.Option{
		// L2 platform cell: pub for relay fan-out; writer injected via modResult
		// (postgres path). Composition root wraps raw outbox.Publisher into
		// outbox.CellPublisher (sealed marker); cell's public WithOutboxDeps
		// signature rejects raw types at compile time.
		foocorecell.WithOutboxDeps(outbox.WrapPublisherForCell(shared.EventBus), nil),
		foocorecell.WithCursorCodec(cursorCodec),
	}
	baseOpts = append(baseOpts, modResult.cellOptions...)
	c := foocorecell.NewFooCore(shared.Clock, baseOpts...)

	var opts []bootstrap.Option
	var provisional []kernellifecycle.ManagedResource
	opts = append(opts, modResult.bootstrapOpts...)
	// ManagedResource two-places contract (Chapter 4): resources returned here
	// are the rollback channel; modResult.bootstrapOpts carries the steady-state
	// bootstrap.WithManagedResource registrations.
	provisional = append(provisional, modResult.provisional...)
	return c, opts, provisional, nil
}

var _ composition.CellModule = module{}
```

### Step 3. 在 assembly.yaml 声明 cell 并生成 modules_gen.go

在对应的 `assemblies/<assemblyid>/assembly.yaml` 中把 `foocore` 加入 cell 列表，
并确保声明了 `build.compositionAPI: true`：

```yaml
# assemblies/corebundle/assembly.yaml  （片段）
cells:
  - configcore
  - auditcore
  - accesscore
  - foocore      # ← 新增

build:
  compositionAPI: true
```

然后运行 codegen 重新生成 `cmd/<assemblyid>/modules_gen.go`，让组装入口包含
`foocore.Module()`：

```
gocell generate assembly --id=corebundle
```

生成产物 `cmd/corebundle/modules_gen.go` 会包含 `cellmodulesfoocore.Module()` 调用；
不要手动编辑该文件。组装入口 `composition.New().With(mods...).Build(ctx, shared, runtimeOptsFn)`
由 `cmd/corebundle/run.go` 统一调用，无需修改 run.go。

### Step 4. 在 `cellmodules/foocore/storage.go`（参照 `cellmodules/configcore/storage.go`）写 `buildFooCoreOpts`

```go
// cellmodules/foocore/storage.go
package foocore

import (
	"fmt"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	foocorecell "github.com/ghbvf/gocell/cells/foocore"
	"github.com/ghbvf/gocell/kernel/clock"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/capability"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
)

// fooCoreModuleConfig bundles inputs for buildFooCoreOpts.
type fooCoreModuleConfig struct {
	topology  bootstrap.Topology
	pg        capability.PGProvider
	publisher outbox.Publisher
}

// fooCoreModuleResult bundles outputs from buildFooCoreOpts.
type fooCoreModuleResult struct {
	cellOptions   []foocorecell.Option
	bootstrapOpts []bootstrap.Option
	provisional   []kernellifecycle.ManagedResource
}

// buildFooCoreOpts selects storage-adapter options based on topology.
func buildFooCoreOpts(clk clock.Clock, cfg fooCoreModuleConfig) (fooCoreModuleResult, error) {
	clock.MustHaveClock(clk, "cellmodules/foocore.buildFooCoreOpts")
	switch cfg.topology.StorageBackend() {
	case "postgres":
		if cfg.pg == nil {
			return fooCoreModuleResult{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"foocore postgres mode requires the postgres capability provider "+
					"(provisionCapabilities must run before Build)")
		}
		db, err := cellsecrets.PgxPoolFromProvider(cfg.pg)
		if err != nil {
			return fooCoreModuleResult{}, fmt.Errorf("foocore: %w", err)
		}
		// foocore ships its own migration set under its own namespace; the goose
		// tracking table is schema_migrations_foocore (derived from the typed
		// migration.Namespace — NewMigrator/VerifyExpectedVersion no longer take a
		// free table string). See docs/guides/cell-external-repo-quickstart.md.
		fooNS, err := migration.ParseNamespace("foocore")
		if err != nil {
			return fooCoreModuleResult{}, fmt.Errorf("foocore migration namespace: %w", err)
		}
		if schemaErr := adapterpg.VerifyExpectedVersion(ctx, pool, foocorecell.MigrationsFS(), fooNS); schemaErr != nil {
			return fooCoreModuleResult{}, fmt.Errorf("foocore PG schema guard: %w", schemaErr)
		}
		txMgr := cfg.pg.TxManager()
		outboxWriter := cfg.pg.OutboxWriter()

		pgStore := adapterpg.NewOutboxStore(db, clk)
		relayWorker := outboxruntime.NewRelay(clk, pgStore, cfg.publisher, outboxruntime.DefaultRelayConfig())

		// Composition root wraps raw infra types as sealed markers;
		// cell.go public Options reject raw types at compile time
		// (ADR 202605101900-adr-cell-raw-infra-sealed-marker §D1).
		cellOpts := []foocorecell.Option{
			foocorecell.WithTxManager(persistence.WrapForCell(txMgr)),
			foocorecell.WithOutboxWriter(outbox.WrapWriterForCell(outboxWriter)),
		}
		return fooCoreModuleResult{
			cellOptions:   cellOpts,
			bootstrapOpts: []bootstrap.Option{bootstrap.WithRelay(relayWorker)},
			// provisional is empty for foocore postgres path — pool is owned by
			// shared.PG (shared across cells); only cell-exclusive resources go here.
		}, nil

	case "memory":
		return fooCoreModuleResult{
			cellOptions: []foocorecell.Option{foocorecell.WithInMemoryDefaults()},
		}, nil

	default:
		return fooCoreModuleResult{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"buildFooCoreOpts: unexpected StorageBackend (topology validation bypass)")
	}
}
```

### Step 5. 更新 `docs/ops/env-vars.md`

在 "Per-Cell Session and Cursor Keys" 和 "configcore cell database" 章节后追加
`foocore` 小节，列出 `GOCELL_FOOCORE_DATABASE_URL` 等变量。

### Step 6. 更新 `.env.example`

添加 `foocore` 相关的带注释示例行：

```
# foocore cell (postgres mode only)
# GOCELL_FOOCORE_DATABASE_URL=postgres://foocore:pass@localhost:5432/foocore?sslmode=disable
# GOCELL_FOOCORE_CURSOR_KEY=<32-byte-random-hex>
```

---

## Chapter 4 — 资源生命周期（两处：steady-state opts + rollback 4th channel）

`CellModule.Provide` 打开的外部资源（pool、vault client、rate-limiter 等带后台
goroutine/连接的资源）**必须同时出现在两处**——两处服务**不同阶段**，缺一即泄漏：

1. 作为 `bootstrap.WithManagedResource(res)` 追加进 `opts`（Provide 第 3 个返回值）——
   让 `bootstrap.Run` 在**正常运行期**管理生命周期（健康检查 + 后台 worker + phase10
   shutdown 时 LIFO Close）。**漏这处** → 资源（如 rate-limiter cleanup goroutine）在
   正常停机时永不 Close（#1385 F1 缺陷的成因：accesscore module 当时返回 `opts=nil`）。
2. 作为 `[]lifecycle.ManagedResource`（Provide 第 4 个返回值）返回——让
   `composition.Builder.Build` 累积进内部 `provisional` 栈，在**后续模块 Provide 或
   `runtimeOptsFn` 失败**（即 `bootstrap.Run` 尚未启动）时逆序（LIFO）Close 已开启的
   资源。**漏这处** → 启动中途失败时资源泄漏（bootstrap 永不运行，无人 Close）。

> **不会 double-close**：rollback 路径只在**失败**时触发（此时 `bootstrap.Run` 不运行），
> WithManagedResource 路径只在**成功**时由 `bootstrap.Run` 接管——两条路径互斥。Builder
> **不**把 `provisional` 资源再转成 bootstrap opts（steady-state 注册是模块经 opts 的职责），
> 所以同一资源在 bootstrap managed 集合中至多出现一次。

后台 worker 型资源（例如 outbox relay）通过独立
`bootstrap.WithRelay(relayWorker)` 返回，但不塞进 Pool。`WithRelay` 是
relay 的 **唯一** 注册入口：相关的 `Checkers()/Worker()/Close()` 由
package-private `relayAdapter` 包装到 ManagedResource 流水线
（详见 ADR `docs/architecture/202605201400-adr-relay-managedresource-isolation.md`
+ archtest `RELAY-NOT-MANAGEDRESOURCE-01` / `RELAY-SOLE-HOLDER-01`）——
`*Relay` 自身不实现 ManagedResource，直接传给 `WithManagedResource` 是编译期
type-mismatch，二次调用 `WithRelay` 会通过 panic-taxonomy funnel 触发
`panicregister.Approved + errcode.Assertion(B 类)` panic。Pool 直接实现
`lifecycle.ManagedResource`（其 `Worker()` 为 nil，只表达 pool 健康检查和关闭职责），
所以 pool 走 `WithManagedResource`，relay 走 `WithRelay`。注册顺序必须是 pool 在前、
relay opts 在后，bootstrap 的 LIFO shutdown 才会先停 relay、再关 pool。

```
每个 module Provide 返回:
  opts = [..., bootstrap.WithManagedResource(resX)]   // steady-state（成功时 bootstrap.Run 接管）
  4th  = [resX]                                       // rollback（失败时 Builder 关）

—— 全部成功 ——
allOpts 含各 module 的 WithManagedResource(resX)
  → bootstrap.Run：phase10 shutdown 时 LIFO Close

—— 中途失败（composition.Builder.Build 内部）——
module A Provide → 4th=[pgResA] → provisional = [pgResA]
module B Provide → 4th=[pgResB] → provisional = [pgResA, pgResB]
module C Provide → error
  ↓ rollback（LIFO）:
  pgResB.Close(ctx)   // B 先关
  pgResA.Close(ctx)   // 再关 A
  （bootstrap.Run 不运行 → opts 里的 WithManagedResource 从不注册 → 无 double-close）
```

两处缺一即泄漏：漏 opts（WithManagedResource）→ 正常停机时 happy-path 资源
（rate-limiter cleanup goroutine 等）不被 Close（#1385 F1）；漏第 4 个返回值 →
启动中途失败时 PG pool 等不被回滚关闭。

---

## Chapter 5 — 测试

### 5a. 单 helper 测试（不启 pool）

用 `t.Setenv` + `cellsecrets.LoadCursorKeys` 表驱动，验证 fail-fast 行为：

```go
// cellmodules/foocore/module_test.go  (新 cell 按此模式)
func TestFooCoreModule_CursorKey_FailFast(t *testing.T) {
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "memory")
	t.Setenv("GOCELL_FOOCORE_CURSOR_KEY", "") // empty in real mode → fail-fast

	shared := buildMinimalTestSharedDeps(t) // memory topology
	_, _, _, err := foocore.Module().Provide(context.Background(), shared)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cursor")
}
```

### 5b. 集成测试（真 PG 连接）

Build tag `integration`，直接调用 `cellmodules/foocore.Module().Provide(...)`（不要
直接调 `buildFooCoreOpts`——它是包内函数，也不是测试的正确入口）：

```go
//go:build integration

// cellmodules/foocore/module_pg_integration_test.go
func TestFooCoreModule_Postgres_SchemaMatched(t *testing.T) {
	dsn, cleanup := setupPostgresForFoocore(t)  // testcontainers helper
	defer cleanup()

	ctx := context.Background()

	// 预先跑 migration
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err)
	fooNS, err := migration.ParseNamespace("foocore") // tracking table schema_migrations_foocore
	require.NoError(t, err)
	migrator, err := adapterpg.NewMigrator(pool, foocorecell.MigrationsFS(), fooNS)
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	// Provision PG capability (mirrors composition.Builder.provisionCapabilities)
	pgCap, err := capability.NewPGProvider(pool)
	require.NoError(t, err)

	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")
	t.Setenv("GOCELL_FOOCORE_CURSOR_KEY", "foocore-test-cursor-key-32byte!!")

	shared := buildMinimalTestSharedDeps(t) // supply pgCap via composition.NewSharedDeps
	c, opts, resources, err := foocore.Module().Provide(ctx, shared)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.NotEmpty(t, opts) // relay bootstrap option present
	_ = resources
	_ = pool.Close(ctx)
}
```

参照 `cmd/corebundle/integration_testhelpers_test.go` 的 `buildConfigCoreCellFromShared`
helper（调用 `cellmodulesconfigcore.Module().Provide(ctx, shared)`）。
完整集成测试通过以下命令触发：

```
go test -tags=integration -timeout=120s ./cellmodules/foocore/...
```

---

## Chapter 6 — 陷阱

| 陷阱 | 后果 | 正确做法 |
|---|---|---|
| `cell.yaml` 的 `id` 含 dash（如 `foo-core`） | `gocell validate --strict` 挂起，FMT-C1 违规 | 用 no-dash 格式：`foocore` |
| `Provide` 不返回 `provisional` | 后续模块失败时 PG pool 泄漏 | 参见 Chapter 4，`provisional` 必须含所有已打开的 cell-exclusive 资源 |
| `cellsecrets.LoadCursorKeys` 收到坏值 | 运维 typo 导致进程静默启动但连接异常 | fail-fast 已内置；不需要额外检查 |
| memory 模式下 `shared.PG` 为 nil | 无问题——`buildFooCoreOpts` 走 memory 分支，不会访问 `shared.PG` | 确保 `StorageBackend()` 判断在访问 `shared.PG` 之前 |
| postgres 模式未配置 `KEY_PROVIDER` | 启动失败（不是警告） | 必须设 `GOCELL_<CELLID>_KEY_PROVIDER=local-aes`（dev/CI）或 `vault-transit`（生产） |
| 在 `SharedDeps` 外自行读取 `GOCELL_ADAPTER_MODE` | 产生 topology 不一致 | 只读 `shared.Topology`；禁止在 CellModule 内调用 `os.Getenv("GOCELL_ADAPTER_MODE")` |
| 遗漏 `gocell generate assembly` | `modules_gen.go` 与 `assembly.yaml` 漂移，启动时 fail-fast（`assertModuleIDsMatch` 检查） | 改完 `assembly.yaml` 后立即重新生成 |

---

## Chapter 7 — 迁移记录

本文档在 T6（2026-04-24，PR-A3）被彻底重写，并于 2026-05-31（#1085）更新为当前
composition API 形态。旧版所教的 API 已全部删除：

| 已删除的符号 | 替代 |
|---|---|
| `AppDepsFromEnv` | `LoadSharedDepsFromEnv` + 各 `CellModule.Provide` |
| `BuildBootstrap` | `composition.New().With(...).Build(...)` + `app.Run(ctx)` |
| `AppDeps` struct | `SharedDeps`（cross-cutting）+ per-cell Module 私有字段 |
| `AppDeps.PGResource` | `CellModule.Provide` 返回的 `[]ManagedResource` |
| `configCellOpts` 字段 | `ConfigCoreModule.Provide` 返回的 `[]bootstrap.Option` |
| `BuildApp(ctx, shared, ModuleA{}, ...)` + `cmd/corebundle/<cell>_module.go` + `bundle_<cell>_storage.go` | `composition.New().With(mods...).Build(ctx, shared, runtimeOptsFn)` + `cellmodules/<cell>/module.go` + `cellmodules/<cell>/storage.go`（#1085） |

不要参考任何 git history 中旧版本的这些符号。按旧版模板接 PG cell 会导致
编译错误（这些符号已从代码库删除）。
