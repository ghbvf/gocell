# ADR-1085: CellModule / SharedDeps / Builder / App 公开 Composition API 与 platform/ 层

**状态**：Accepted  
**日期**：2026-05-31  
**关联 Issue**：#1085（sub-issue of bundle-parent #1081 "cell development in independent repository"）

---

## 背景

在 PR-1085 之前，GoCell 平台 cell 的 Composition Root 完全封装在 `cmd/corebundle`（`package main`，不可被外部包 import）中。任何希望在独立仓库中组装这三个平台 cell（accesscore / auditcore / configcore）的外部调用方，既无法使用公开 API，也无法复用现有的 cell-to-adapter 绑定逻辑。

这直接阻塞了 #1081 "cell development in independent repository" epic——外部仓库必须从零重写所有 adapter 绑定代码，而不是直接消费官方 composition API。

---

## 决策

### 提取公开 Composition API（`runtime/composition`）

引入以下公开类型：

| 类型 | 职责 |
|---|---|
| `CellModule` | 接口：`ID() string` + `Provide(ctx, *SharedDeps, ModuleExports) (cell.Cell, ModuleExports, []Option, []ManagedResource, error)` |
| `ModuleExports` | 模块间 typed handoff（如 `BootstrapLedgerStore`）；Builder 左→右累积，下游模块经 `in` 参数消费（取代可变 `*SharedDeps` 字段突变） |
| `SharedDeps` | 跨 cell 共享依赖的公开 bag（接口字段，无 adapter 具体类型）；**sealed construction**——经 `NewSharedDeps(...)` 构造盖 marker，`Build` 拒未盖戳实例 |
| `Builder` | `New() / With(...) / Build(ctx, *SharedDeps, RuntimeOptionsFunc) (*App, error)`；`Build` 校验 marker + `runtimeOptsFn` 非 nil |
| `App` | `Run(ctx) error`（委托 bootstrap.New(...).Run） |
| `RuntimeOptionsFunc` | `func(cells []cell.Cell) ([]bootstrap.Option, error)` |

`SharedDeps` 字段全部为接口或 kernel/runtime 类型（`kernelmetrics.Provider`、`idempotency.Claimer`、`*auth.JWTIssuer` 等），**不包含**任何 `adapters/prometheus` 或 `prometheus/client_golang` 具体类型，使 `runtime/composition` 不向 adapters/ 引入依赖。

### 新建 `platform/` Composition Root 层

将原 `cmd/corebundle/*_module.go` 中的 cell-to-adapter 绑定逻辑提取到新的 `platform/<cell>/module.go`：

- `platform/accesscore.Module() composition.CellModule`
- `platform/auditcore.Module() composition.CellModule`
- `platform/configcore.Module(opts ...ModuleOption) composition.CellModule`
- `platform/cellsecrets/`：共享 helper（cursor codec、HMAC key、env loader、demo key reject 等）

`platform/` 是与 `cmd/` 平行的 Composition Root 层，可依赖所有层（kernel/cells/runtime/adapters）。

### `cmd/corebundle` depguard 升级

`corebundle-no-cells` depguard rule：`cmd/corebundle` 禁止直接 import `cells/`，所有 cell 业务 wiring 必须通过 `platform/` 暴露的 `Module()` 消费。

### M11 Dogfood：`examples/corebundlestarter`

新建 `examples/corebundlestarter`，证明外部 `main.go` 可以仅通过：

```go
composition.New().
    With(auditcore.Module(), accesscore.Module(), configcore.Module()).
    Build(ctx, shared, runtimeFn)
```

在零外部基础设施（memory mode）下完整启动三个平台 cell，并通过 `/healthz` 探针验证。

---

## 与 issue 字面方案的三处刻意偏差

### (a) `Module()` 定义在 `platform/<cell>` 而非 `cells/<cell>`

**issue 建议**：让 `cells/<cell>` 直接暴露 `Module()`。

**偏差理由**：`cells/` 依赖规则（`go-standards.md`、`cells-isolation` depguard）明确禁止 `cells/` 依赖 `adapters/`。`platform/accesscore.Module()` 构造了 `adapterpg.NewSessionStore`、`adapterredis.NewCache` 等 adapter 具体类型——这些调用必须在允许依赖 adapters 的层中发生。把 `Module()` 放在 `cells/` 会直接破坏 cells-isolation 约束。`platform/` 层作为 Composition Root 可依赖所有层，是正确的承载位置。

### (b) error-first `Build()` 而非 `MustBuild()`

**issue 建议**（隐含）：提供 `MustBuild` 便利 API。

**偏差理由**：ADR `202605171800-adr-kernel-mustctor-removal.md`（B2-K-02）已从 GoCell 删除所有 `Must*` 构造器，将 programmer-error panic 模式替换为 error-first。`Build()` 遵循同一规范，composition root（cmd/ 或 examples/）负责处理返回的错误。

### (c) listener / auth wiring 由调用方通过 `RuntimeOptionsFunc` 提供

**issue 建议**：在 composition 内部构建 listener + auth。

**偏差理由**：`AUTH-PLAN-04`（archtest `LAYER-09`）明确禁止 `runtime/composition` 构造 `auth.NewAuthJWT` / `auth.NewAuthServiceToken` 等 auth plan。listener 拓扑与 auth scheme 是 deployment-specific 关注点，必须留在 composition root（cmd/ 或 examples/）中。`RuntimeOptionsFunc` 类型使这一约定在 API 层面可见。

---

## Enforcement

| 约束 | 载体 | 评级 |
|---|---|---|
| `cmd/corebundle` 不直接 import `cells/` | `.golangci.yml` `corebundle-no-cells` depguard | Hard（depguard，CI fail-closed） |
| `runtime/composition` 不 import `adapters/` | `.golangci.yml` `runtime-isolation` depguard | Hard（depguard，CI fail-closed） |
| `cells/` / `runtime/` / `adapters/` / `kernel/` / `pkg/` 不反向 import `platform/` | `.golangci.yml` 各 isolation rule 的 `deny: platform` 条目 | Hard（depguard，CI fail-closed） |
| wrapper 函数只在 `platform/*` / `cmd/*` / `examples/<demo>/{main,app,run}.go` 调用 | archtest `CELL-RAW-INFRA-WRAPPER-LOCATION-01`（`tools/archtest/wrapper_location_test.go`，`isWrapperCallerAllowed` 已含 `platform/` 前缀） | Medium（archtest type-aware，nightly CI） |
| prom 构造只在允许位置调用 | archtest `PROM-CALLER-*`（nightly CI allowlist 含 platform/） | Medium（同上） |
| `Build()` error-first，无 Must | type system（函数签名，无 MustBuild 导出符号） | Hard |
| `platform/` 独立层分类（`LayerCellModules = "platform"`，区别于 `LayerCmd`） | `kernel/depgraph/layer.go` + `tools/archtest/archtest_test.go` LAYER-06 豁免扩展 | Medium（archtest LAYER-06，nightly CI） |

**注**：`platform/` 与 `cmd/` 同属 composition-root 层，既有 composition-root archtests（cas/session/ledger 协议位置、wrapper 调用点）已通过 `platform/` 前缀覆盖，不存在扫描盲区。

**已落地（#1085 review 收口）**：原先 `BootstrapLedgerStore` 通过 `*SharedDeps` 字段突变在 auditcore module → accesscore module 之间传递的可变共享状态，已重构为 typed `ModuleExports` return：auditcore.Provide 返回 `ModuleExports{BootstrapLedgerStore: ...}`，Builder 左→右累积并经 `in` 参数喂给 accesscore.Provide（缺失时 fail-fast）。`SharedDeps` 同步去除该字段，成为构造后不再被 mid-Build 突变的 sealed bag。module 顺序仍由 `MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01` archtest 守卫；handoff 值类型 `*audit.BootstrapLedgerStore` 本身 sealed（NewBootstrapLedgerStore 非 nil + 命名空间校验），顺序门控为运行时 nil-check（Medium，Go 无法编译期表达「module X 必先于 Y」）。

---

## 安全覆盖说明

**无安全回归**：

- auth plan（`NewAuthJWTFromAssembly`、`NewAuthServiceToken`）的构造仍在 composition root（`cmd/corebundle/bundle_options.go`、`examples/corebundlestarter/run.go`），严格符合 AUTH-PLAN-04。
- `SharedDeps.InternalHMACRing` 字段由 cmd/ 在 env 解析阶段构造（`internalGuardFromEnv`），platform/ 消费该接口字段但不构造 HMAC key，符合单源原则。
- `SharedDeps` 仅携带接口字段，不暴露 adapter 实现细节，secrets 不在 wire layer 泄漏。

---

## 参考框架

- **uber-go/fx** `fx.Module(name, ...)` / `fx.Supply` — self-contained module + shared supply，对标 `CellModule.Provide` + `SharedDeps`。
- **controller-runtime** `pkg/manager/manager.go` `Manager.Start(ctx) error` — 单 context 启动模型，对标 `App.Run(ctx) error`。
- **Caddy global-registry**（reject）— global singleton 注册模式，与 per-Builder module list 相悖；刻意拒绝，不引入全局注册表。

---

## 文件清单

| 文件 | 类型 |
|---|---|
| `runtime/composition/{cell_module,shared_deps,builder,app}.go` | 新建（公开 API） |
| `platform/accesscore/module.go` | 新建（platform 层） |
| `platform/auditcore/module.go` | 新建 |
| `platform/configcore/{module,storage}.go` | 新建 |
| `platform/cellsecrets/{env,secrets}.go` | 新建（共享 helper） |
| `examples/corebundlestarter/{main,run,run_test}.go` | 新建（M11 dogfood） |
| `cmd/corebundle/modules_gen.go` | 修改（调用 platform.Module()） |
| `CLAUDE.md` | 修改（platform/ 层入 分层结构 + 依赖规则） |
| `.claude/rules/gocell/go-standards.md` | 修改（platform/ 行入依赖表） |
| `.claude/rules/gocell/cell-patterns.md` | 修改（platform/* 入 wrapper-location 允许列表） |
| `cmd/CLAUDE.md` | 修改（platform/ 业务 wiring 说明 + corebundle-no-cells depguard 记录） |
