<!-- ref: docs/plans/202604232330-025-architecture-pr-implementation-plan.md §PR-A3 T6 -->

# PG Cell 接入模板（per-cell adapter 模型）

> 文档版本: 2026-04-24（T6 per-cell adapter 切分后重写）
> 读者: 要给某个 cell 接 PostgreSQL 的开发者
> 前置阅读: docs/architecture/ 里的分层规则 + docs/ops/env-vars.md 的 env 命名表

---

## Chapter 1 — 模型总览

T6（PR-A3）确立的 per-cell adapter 模型把依赖分成两条独立的路径：

```
operator env
     │
     ├─── LoadSharedDepsFromEnv(ctx)          ← cross-cutting 只读一次
     │         JWT / Prometheus / EventBus
     │         InternalGuard / MetricsToken / VerboseToken
     │         └─→ *SharedDeps
     │
     └─── CellModule.Provide(ctx, shared)     ← per-cell 各自读自己的 env
               GOCELL_<CELLID>_DATABASE_URL
               GOCELL_<CELLID>_CURSOR_KEY
               GOCELL_<CELLID>_KEY_PROVIDER
               └─→ (cell.Cell, []bootstrap.Option, []ManagedResource, error)

     ↓
BuildApp(ctx, shared, ModuleA{}, ModuleB{}, ...)
     ↓
buildAssembly(ps, durabilityMode, cells...)
     ↓
bootstrap.New(defaultOpts..., cellOpts...).Run(ctx)
```

两条原则：

1. **cross-cutting 只在 SharedDeps**: JWT 秘钥、Prometheus 注册表、EventBus、
   control-plane token 由 `LoadSharedDepsFromEnv` 统一构建，不在 CellModule 里重读。
2. **per-cell adapter 配置由 CellModule.Provide 自己读**: PG URL、cursor key、
   master key 等带 `GOCELL_<CELLID>_` 前缀的 env 由对应 Module 自行解析，
   互不干扰。

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

### Step 2. 新建 `cmd/corebundle/foo_module.go`

```go
// cmd/corebundle/foo_module.go
package main

import (
	"context"
	"fmt"

	foocore "github.com/ghbvf/gocell/cells/foocore"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// FooCoreModule wires foocore per-cell adapter dependencies.
type FooCoreModule struct {
	// KeyProviderOverride bypasses env-based construction in tests.
	KeyProviderOverride kcrypto.KeyProvider
}

func (FooCoreModule) ID() string { return "foocore" }

func (m FooCoreModule) Provide(ctx context.Context, shared *SharedDeps) (
	cell.Cell,
	[]bootstrap.Option,
	[]kernellifecycle.ManagedResource,
	error,
) {
	// 1. Cursor codec.
	pri, prev := LoadCursorKeys("FOOCORE")
	cursorCodec, err := buildCursorCodec(shared.Topology.AdapterMode,
		"GOCELL_FOOCORE_CURSOR_KEY", "GOCELL_FOOCORE_CURSOR_PREVIOUS_KEY",
		pri, prev,
		"foocore-cursor-key-32-byte-def!", "foo")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("foocore cursor codec: %w", err)
	}

	// 2. PG pool config (read per-cell env; validation deferred to NewPool).
	pgCfg, err := LoadPGConfig("FOOCORE")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("foocore pg config: %w", err)
	}

	// 3. Storage-backend branching via Topology.
	pgRes, cellOpts, err := buildFooCoreOpts(ctx, shared.Topology, pgCfg, shared.EventBus)
	if err != nil {
		return nil, nil, nil, err
	}

	c := foocore.NewFooCore(append([]foocore.Option{
		foocore.WithPublisher(shared.EventBus),
		foocore.WithCursorCodec(cursorCodec),
	}, cellOpts...)...)

	var opts []bootstrap.Option
	var provisional []kernellifecycle.ManagedResource
	if pgRes != nil {
		opts = append(opts, bootstrap.WithManagedResource(pgRes))
		provisional = append(provisional, pgRes)
	}
	return c, opts, provisional, nil
}

var _ CellModule = FooCoreModule{}
```

### Step 3. 注册到 `cmd/corebundle/main.go`

```go
// cmd/corebundle/main.go  (only the BuildApp call site)
cells, cellOpts, err := BuildApp(ctx, shared,
    ConfigCoreModule{},
    AccessCoreModule{},
    AuditCoreModule{},
    FooCoreModule{},   // ← add here
)
```

### Step 4. 在 `bundle.go`（或独立文件）写 `buildFooCoreOpts`

```go
// cmd/corebundle/bundle.go  (or foo_bundle.go)
func buildFooCoreOpts(
	ctx context.Context,
	topo bootstrap.Topology,
	pgCfg adapterpg.Config,
	pub outbox.Publisher,
) (kernellifecycle.ManagedResource, []foocore.Option, error) {
	switch topo.StorageBackend {
	case "postgres":
		if pgCfg.DSN == "" {
			return nil, nil, fmt.Errorf("foocore postgres mode requires GOCELL_FOOCORE_DATABASE_URL")
		}
		pool, err := adapterpg.NewPool(ctx, pgCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("foocore PG pool: %w", err)
		}
		if schemaErr := adapterpg.VerifyExpectedVersion(ctx, pool, foocore.MigrationsFS()); schemaErr != nil {
			_ = pool.Close(ctx)
			return nil, nil, fmt.Errorf("foocore PG schema guard: %w", schemaErr)
		}
		outboxWriter := adapterpg.NewOutboxWriter()
		txMgr := adapterpg.NewTxManager(pool)
		relayWorker := outboxruntime.NewRelay(adapterpg.NewOutboxStore(pool.DB()), pub,
			outboxruntime.DefaultRelayConfig())
		pgRes, resErr := adapterpg.NewPGResource(pool, relayWorker)
		if resErr != nil {
			_ = pool.Close(ctx)
			return nil, nil, fmt.Errorf("foocore PG resource: %w", resErr)
		}
		opts := []foocore.Option{
			foocore.WithPostgresDefaults(pool.DB(), outboxWriter),
			foocore.WithTxManager(txMgr),
		}
		return pgRes, opts, nil

	case "memory":
		return nil, []foocore.Option{foocore.WithInMemoryDefaults()}, nil

	default:
		return nil, nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("buildFooCoreOpts: unexpected StorageBackend %q", topo.StorageBackend))
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

## Chapter 4 — 资源生命周期（provisional rollback）

`CellModule.Provide` 打开的外部资源（pool、vault client）**必须**同时出现在两处：

1. 作为 `bootstrap.WithManagedResource(res)` 追加进 `opts` 返回值——让
   `bootstrap.Run` 在 happy path 管理生命周期（健康检查 + 后台 worker + LIFO Close）。
2. 作为 `provisional` slice 元素返回——让 `BuildApp` 在**后续模块 Provide 失败**时
   逆序 Close 已开启的连接，防止启动失败时泄漏。

```
BuildApp 内部逻辑（简化）:

module A Provide → pgResA → provisional = [pgResA]
module B Provide → pgResB → provisional = [pgResA, pgResB]
module C Provide → error
  ↓ rollback:
  pgResB.Close(ctx)   // LIFO: B 先关
  pgResA.Close(ctx)   // 再关 A
```

这是 T6 review 后（PR-A3）新增的契约。不把资源放入 `provisional` 会导致启动失败
时 PG pool 泄漏，进程退出前连接不会被释放。

---

## Chapter 5 — 测试

### 5a. 单 helper 测试（不启 pool）

用 `t.Setenv` + `LoadPGConfig` 表驱动，验证 fail-fast 行为：

```go
// cmd/corebundle/per_cell_adapter_test.go  (已有示例，新 cell 照此模式)
func TestLoadPGConfig_InvalidMaxConns_FailsFast(t *testing.T) {
	t.Setenv("GOCELL_FOOCORE_DATABASE_URL", "postgres://x/db")
	t.Setenv("GOCELL_FOOCORE_DATABASE_MAX_CONNS", "not-a-number")
	t.Setenv("GOCELL_FOOCORE_DATABASE_IDLE_TIMEOUT", "")
	t.Setenv("GOCELL_FOOCORE_DATABASE_MAX_LIFETIME", "")

	_, err := LoadPGConfig("FOOCORE")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MAX_CONNS")
}
```

### 5b. 集成测试（真 PG 连接）

Build tag `integration`，走 `BuildApp` 路径（不要直接调 `buildFooCoreOpts`）：

```go
//go:build integration

// cmd/corebundle/main_integration_test.go  (新 cell 加独立 Test* 函数)
func TestBuildFooCoreOpts_Postgres_SchemaMatched(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)  // testcontainers helper
	defer cleanup()

	ctx := context.Background()

	// 预先跑 migration
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err)
	migrator, err := adapterpg.NewMigrator(pool, foocore.MigrationsFS(), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))
	_ = pool.Close(ctx)

	t.Setenv("GOCELL_JWT_ISSUER", "test-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "test-audience")
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")
	t.Setenv("GOCELL_FOOCORE_DATABASE_URL", dsn)

	shared, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err)

	cells, opts, err := BuildApp(ctx, shared, FooCoreModule{})
	require.NoError(t, err)
	require.Len(t, cells, 1)
	assert.NotEmpty(t, opts)
}
```

运行：`go test -tags=integration -timeout=120s ./cmd/corebundle/...`

---

## Chapter 6 — 陷阱

| 陷阱 | 后果 | 正确做法 |
|---|---|---|
| `cell.yaml` 的 `id` 含 dash（如 `foo-core`） | `gocell validate --strict` 挂起，FMT-C1 违规 | 用 no-dash 格式：`foocore` |
| `Provide` 不返回 `provisional` | 后续模块失败时 PG pool 泄漏 | 参见 Chapter 4，`provisional` 必须含所有已打开资源 |
| `LoadPGConfig` 收到坏值 | 运维 typo 导致进程静默启动但连接异常 | fail-fast 已内置；不需要额外检查 |
| memory 模式下 `pgCfg.DSN` 为空 | 无问题——`buildFooCoreOpts` 走 memory 分支，不会调 `NewPool` | 确保 `StorageBackend` 判断在 `NewPool` 调用之前 |
| postgres 模式未配置 `KEY_PROVIDER` | 启动失败（不是警告） | 必须设 `GOCELL_<CELLID>_KEY_PROVIDER=local-aes`（dev/CI）或 `vault-transit`（生产） |
| 在 `SharedDeps` 外自行读取 `GOCELL_ADAPTER_MODE` | 产生 topology 不一致 | 只读 `shared.Topology`；禁止在 CellModule 内调用 `os.Getenv("GOCELL_ADAPTER_MODE")` |

---

## Chapter 7 — 迁移记录

本文档在 T6（2026-04-24，PR-A3）被彻底重写。旧版所教的 API 已全部删除：

| 已删除的符号 | 替代 |
|---|---|
| `AppDepsFromEnv` | `LoadSharedDepsFromEnv` + 各 `CellModule.Provide` |
| `BuildBootstrap` | `BuildApp` + `buildAssembly` + `bootstrap.New(opts...).Run` |
| `AppDeps` struct | `SharedDeps`（cross-cutting）+ per-cell Module 私有字段 |
| `AppDeps.PGResource` | `CellModule.Provide` 返回的 `[]ManagedResource` |
| `configCellOpts` 字段 | `ConfigCoreModule.Provide` 返回的 `[]bootstrap.Option` |

不要参考任何 git history 中旧版本的这些符号。按旧版模板接 PG cell 会导致
编译错误（这些符号已从代码库删除）。
