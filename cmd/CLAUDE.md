# cmd/ 层规则

cmd/ 是 Composition Root，负责组装所有 Cell、配置三个 listener、启动 bootstrap。

## 三层组装模式

```go
// 第一层：环境变量注入 + 模块工厂
shared, _ := LoadSharedDepsFromEnv(ctx)
modules, _ := corebundleModules(assemblyCellIDs)

// 第二层：BuildApp 组装 cells + bootstrap.Option（失败按 LIFO 回滚资源）
cells, cellOpts, _ := BuildApp(ctx, shared, modules...)
asm, _ := buildAssembly(shared.PromStack, assemblyID, mode, cells...)

// 第三层：三 listener + bootstrap
opts := defaultRuntimeOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
opts = append(opts, cellOpts...)
bootstrap.New(opts...).Run(ctx)
```

## Listener 配置

```go
// Primary：公开 API + JWT（phase4 自动发现 verifier）
bootstrap.WithListener(cell.PrimaryListener, shared.PrimaryHTTPAddr,
    []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)})

// Internal：控制平面 + ServiceToken（HMAC-SHA256 + replay guard）
bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr,
    []cell.ListenerAuth{cell.MustNewAuthServiceToken(guard.NonceStore(), guard.Ring())})

// Health：/healthz /readyz /metrics，显式无认证
bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr,
    []cell.ListenerAuth{cell.AuthNone{}})
```

`authChain` 必须非 nil；显式无认证用 `cell.AuthNone{}`，传 nil 在 phase0 fail-fast。

## CellModule 接口

每个模块实现 `CellModule`，提供 Cell + bootstrap.Option + ManagedResource：

```go
type CellModule interface {
    ID() string
    Provide(ctx context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, []lifecycle.ManagedResource, error)
}
```

参考实现：`access_module.go`、`config_module.go`、`audit_module.go`。

## SharedDeps 关键字段

| 字段 | 说明 |
|------|------|
| `JWTDeps` | issuer + verifier（JWT 签发/验证） |
| `InternalGuard` | HMAC ring + NonceStore（/internal/v1/* 防护） |
| `SharedPGPool` | postgres 连接池（跨 Cell 共享） |
| `ConsumerClaimer` | outbox 消费幂等键声明者 |
| `PrimaryHTTPAddr` / `InternalHTTPAddr` / `HealthHTTPAddr` | 三 listener 绑定地址 |

## 环境变量（关键）

| 变量 | 说明 | 缺失行为 |
|------|------|---------|
| `GOCELL_JWT_ISSUER` | JWT iss claim | fail-fast |
| `GOCELL_SERVICE_SECRET` | /internal/v1/* HMAC 密钥（≥32 字节） | fail-fast |
| `GOCELL_CELL_ADAPTER_MODE` | `dev`（默认）/ `real` | — |
| `GOCELL_STORAGE_BACKEND` | `memory`（默认）/ `postgres` | — |
