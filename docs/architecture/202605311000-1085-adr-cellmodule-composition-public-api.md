# ADR-1085: CellModule / SharedDeps / Builder / App 公开 Composition API 与 cellmodules/ 层

**状态**：Accepted  
**日期**：2026-05-31  
**关联 Issue**：#1085（sub-issue of bundle-parent #1081 "cell development in independent repository"）  
**实现说明**：本 ADR 原提案层名为 `platform/`，落地时重命名为 `cellmodules/`（helper pkg `platformshared` → `cellsecrets`）以避免与「平台 cell」概念及外部仓库产品名（如 zerotrust-platform）混淆；下文已统一为最终名 `cellmodules/`。

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
| `CellModule` | 接口：`ID() string` + `Provide(ctx, *SharedDeps) (ModuleResult, error)`（出参形态见 `ModuleResult`，Amendment 2026-06-03 #1420 单源化） |
| `ModuleResult` | 结构体 `{Cell cell.Cell; Opts []bootstrap.Option; Resources []lifecycle.ManagedResource}`——`Provide` 的单源出参；Builder 从 `Resources` 派生 happy-path `WithManagedResource` + pre-Run rollback 两条通道（Amendment 2026-06-03 #1420） |
| `SharedDeps` | 跨 cell 共享依赖的公开 bag（接口字段，无 adapter 具体类型）；**sealed construction**——经 `NewSharedDeps(...)` 构造盖 marker，`Build` 拒未盖戳实例 |
| `Builder` | `New() / With(...) / Build(ctx, *SharedDeps, RuntimeOptionsFunc) (*App, error)`；`Build` 校验 marker + `runtimeOptsFn` 非 nil |
| `App` | `Run(ctx) error`（委托 bootstrap.New(...).Run） |
| `RuntimeOptionsFunc` | `func(cells []cell.Cell) ([]bootstrap.Option, error)` |

`SharedDeps` 字段全部为接口或 kernel/runtime 类型（`kernelmetrics.Provider`、`idempotency.Claimer`、`*auth.JWTIssuer` 等），**不包含**任何 `adapters/prometheus` 或 `prometheus/client_golang` 具体类型，使 `runtime/composition` 不向 adapters/ 引入依赖。

**Amendment 2026-06-02 #1423**：`CellModule.Provide` 签名已从 `Provide(ctx, *SharedDeps, in ModuleExports) (cell.Cell, ModuleExports, []bootstrap.Option, []ManagedResource, error)` 简化为 `Provide(ctx, *SharedDeps) (cell.Cell, []bootstrap.Option, []ManagedResource, error)`。`ModuleExports` 类型已完全删除（详见 §Amendment 2026-06-02）。

**Amendment 2026-06-03 #1420 / #1413**：`Provide` 出参进一步收口为单一 `ModuleResult` 结构体——`Provide(ctx, *SharedDeps) (ModuleResult, error)`，`ModuleResult{Cell, Opts, Resources}`。资源生命周期改为**单源**：模块只在 `Resources` 列资源，`Builder.Build` 从该一处同时派生 happy-path `bootstrap.WithManagedResource` 注册与 pre-Run rollback 栈，消除原「同一资源双写 opts + provisional」漂移面。同时（#1413）`SharedDeps` 移除两个 configcore 专属字段（`EventbusCacheCollector` / `ConfigStaleCipherInc`），configcore 经 kernel `MetricsProvider` 自建（详见本文末尾 §Amendment 2026-06-03）。

### 新建 `cellmodules/` Composition Root 层

将原 `cmd/corebundle/*_module.go` 中的 cell-to-adapter 绑定逻辑提取到新的 `cellmodules/<cell>/module.go`：

- `cellmodules/accesscore.Module() composition.CellModule`
- `cellmodules/auditcore.Module() composition.CellModule`
- `cellmodules/configcore.Module(opts ...ModuleOption) composition.CellModule`
- `cellmodules/cellsecrets/`：共享 helper（cursor codec、HMAC key、env loader、demo key reject 等）

`cellmodules/` 是与 `cmd/` 平行的 Composition Root 层，可依赖所有层（kernel/cells/runtime/adapters）。

### `cmd/corebundle` depguard 升级

`corebundle-no-cells` depguard rule：`cmd/corebundle` 禁止直接 import `cells/`，所有 cell 业务 wiring 必须通过 `cellmodules/` 暴露的 `Module()` 消费。

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

### (a) `Module()` 定义在 `cellmodules/<cell>` 而非 `cells/<cell>`

**issue 建议**：让 `cells/<cell>` 直接暴露 `Module()`。

**偏差理由**：`cells/` 依赖规则（`go-standards.md`、`cells-isolation` depguard）明确禁止 `cells/` 依赖 `adapters/`。`cellmodules/accesscore.Module()` 构造了 `adapterpg.NewSessionStore`、`adapterredis.NewCache` 等 adapter 具体类型——这些调用必须在允许依赖 adapters 的层中发生。把 `Module()` 放在 `cells/` 会直接破坏 cells-isolation 约束。`cellmodules/` 层作为 Composition Root 可依赖所有层，是正确的承载位置。

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
| `cells/` / `runtime/` / `adapters/` / `kernel/` / `pkg/` 不反向 import `cellmodules/` | `.golangci.yml` 各 isolation rule 的 deny `github.com/ghbvf/gocell/cellmodules` 条目 | Hard（depguard，CI fail-closed） |
| wrapper 函数只在 `cellmodules/*` / `cmd/*` / `examples/<demo>/{main,app,run}.go` 调用 | archtest `CELL-RAW-INFRA-WRAPPER-LOCATION-01`（`tools/archtest/wrapper_location_test.go`，`isWrapperCallerAllowed` 已含 `cellmodules/` 前缀） | Medium（archtest type-aware，nightly CI） |
| prom 构造只在允许位置调用 | archtest `PROM-CALLER-*`（nightly CI allowlist 含 cellmodules/） | Medium（同上） |
| `Build()` error-first，无 Must | type system（函数签名，无 MustBuild 导出符号） | Hard |
| `cellmodules/` 独立层分类（`LayerCellModules = "cellmodules"`，区别于 `LayerCmd`） | `kernel/depgraph/layer.go` + `tools/archtest/archtest_test.go` LAYER-06 豁免扩展 | Medium（archtest LAYER-06，nightly CI） |
| `CellModule.Provide` 签名冻结：2 入参（context.Context, *SharedDeps）/ 2 出参（ModuleResult, error）+ `ModuleResult` 字段集冻结 `{Cell, Opts, Resources}`，无跨 module 值传递通道（Amendment 2026-06-03 #1420 由 4-out 收口为 ModuleResult） | archtest `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`（`tools/archtest/module_provide_signature_frozen_test.go`）——reflect 冻结接口方法签名 + ModuleResult struct 字段（NumField()==3 + 字段名/类型）；重新引入 handoff 字段/通道 = 形态变 → CI 红 | **Hard**（reflect interface method 签名 + struct 字段集；重引入 handoff 使形态变，archtest pin 即断——上下游均 Hard） |
| cellmodules/ 不得调 `bootstrap.WithManagedResource`（资源经 `ModuleResult.Resources` 单源，Builder 是唯一漏斗） | archtest `WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01`（`tools/archtest/withmanagedresource_cellmodule_funnel_test.go`）（Amendment 2026-06-03 #1420 新增） | 下游 **Hard**（cellmodules 内零容忍 caller-ban）/ 上游 **Medium**（Go 无法令「调某导出 func」编译不可表达，同 #851/#893/#1282 天花板） |

**注**：`cellmodules/` 与 `cmd/` 同属 composition-root 层，既有 composition-root archtests（cas/session/ledger 协议位置、wrapper 调用点）已通过 `cellmodules/` 前缀覆盖，不存在扫描盲区。

**退役（Amendment 2026-06-02 #1423）**：`MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01` archtest 已退役。事件消费者经 EventRouter 异步路由，Provide 时不存在顺序依赖（auditcore 不再向 accesscore 传递 `BootstrapLedgerStore` 构造实例）。签名冻结由 `MODULE-PROVIDE-NO-VALUE-HANDOFF-01` 接管。

---

## 安全覆盖说明

**无安全回归**：

- auth plan（`NewAuthJWTFromAssembly`、`NewAuthServiceToken`）的构造仍在 composition root（`cmd/corebundle/bundle_options.go`、`examples/corebundlestarter/run.go`），严格符合 AUTH-PLAN-04。
- `SharedDeps.InternalHMACRing` / `SharedDeps.NonceStore` 字段由 cmd/ 在 env 解析阶段构造（`buildInternalHMACRing` + `buildServiceNonceStore`，Amendment 2026-06-03 #1410 起；原 `internalGuardFromEnv` 已 dissolve），cellmodules/ 消费这两个接口字段但不构造 HMAC key / 防重放 store，符合单源原则。
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
| `cellmodules/accesscore/module.go` | 新建（cellmodules 层） |
| `cellmodules/auditcore/module.go` | 新建 |
| `cellmodules/configcore/{module,storage}.go` | 新建 |
| `cellmodules/cellsecrets/{env,secrets}.go` | 新建（共享 helper） |
| `examples/corebundlestarter/{main,run,run_test}.go` | 新建（M11 dogfood） |
| `cmd/corebundle/modules_gen.go` | 修改（调用 cellmodules.Module()） |
| `CLAUDE.md` | 修改（cellmodules/ 层入 分层结构 + 依赖规则） |
| `.claude/rules/gocell/go-standards.md` | 修改（cellmodules/ 行入依赖表） |
| `.claude/rules/gocell/cell-patterns.md` | 修改（cellmodules/* 入 wrapper-location 允许列表） |
| `cmd/CLAUDE.md` | 修改（cellmodules/ 业务 wiring 说明 + corebundle-no-cells depguard 记录） |

---

## Amendment 2026-06-02 #1423 — ModuleExports 删除与事件解耦

**背景**：Epic #1423 Wave-1 将 `composition.ModuleExports.BootstrapLedgerStore` 这一跨 module 进程内 Go handle 传递通道替换为事件驱动（`event.auth.bootstrap-failed.v1`）。违例根因：auditcore 构造 bootstrap 链 store 后经 `ModuleExports` 传给 accesscore；accesscore 在 `/setup/admin` 返 401/429 后直接通过该 handle 写 auditcore 的 ledger，违反「L1+ 跨 cell 交互 MUST 经 contract」。

**变更**：

1. `composition.ModuleExports` 类型已**完全删除**（`ModuleExports` 结构体 + `merge` 方法均已移除）。
2. `CellModule.Provide` 签名从
   `Provide(ctx, *SharedDeps, in ModuleExports) (cell.Cell, ModuleExports, []bootstrap.Option, []ManagedResource, error)`
   更改为
   `Provide(ctx, *SharedDeps) (cell.Cell, []bootstrap.Option, []ManagedResource, error)`
   每个实现均已机械更新（accesscore / auditcore / configcore + 所有 examples）。
3. Bootstrap auth-fail 审计写入路径变更：
   - **旧**：accesscore 持有 auditcore 的 `*audit.BootstrapLedgerStore`，直接调 `AppendBootstrapAuthFail`（进程内跨 cell 直写）。
   - **新**：accesscore observer 调 `setup.Service.RecordBootstrapAuthFail`，在 tx 内 emit `event.auth.bootstrap-failed.v1`（持久 L2 outbox）→ relay 异步投递 → auditcore `auditappendbootstrap` subscriber 消费 → `AppendBootstrapAuthFail` 写 bootstrap-namespace ledger。
4. `*audit.BootstrapLedgerStore` 类型保留，现由 auditcore cell 通过 `auditcell.WithBootstrapStore(bootstrapWrapped)` 在 auditcore 内部持有（不再 export）。Namespace/HMAC 隔离保持不变（独立 `"bootstrap"` namespace + 独立 HMAC key `GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY`，见 ADR-1121）。
5. **archtest 变更**：
   - **新增** `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`（`tools/archtest/module_provide_signature_frozen_test.go`）：Hard，reflect 冻结 `CellModule.Provide` 签名（2 入参 / 出参——#1423 时为 4-out，后经 #1420 收口为 `ModuleResult` 2-out + struct 字段集冻结，见 §Amendment 2026-06-03）——重新引入跨 module 值传递通道必须改形态，archtest 即断。
   - **退役** `MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01`：事件消费者经 EventRouter 路由，Provide 时不存在顺序依赖，前提消失。
   - **退役** `BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-{DOWNSTREAM-HARD,UPSTREAM-MEDIUM}-01`：observer 不再写 ledger；emit 路径由 `EMIT-DECL-COVER-01`（已存在）+ event contract + auditcore subscriber conformance test 覆盖。

**威胁矩阵重评**（对应 ADR-1121 §Threat model，影响行逐项列出）：

| 威胁行 | ADR-1121 结论（#1085 时） | Amendment 后结论 |
|---|---|---|
| 组合根忘记构造 bootstrap chain | ✅ archtest AUDIT-NS-DISJOINT-01 + typed field 失快 | ✅ 不变——`cellmodules/auditcore.Provide` 内部调 `audit.NewBootstrapLedgerStore` + `VerifyBootstrapTailOnStartup`；auditcore 构造失败则整个 `Builder.Build` 失败 |
| accesscore 拿到 nil BootstrapLedgerStore | ✅ Module order archtest + nil-check | ✅ 结构上不可达——accesscore 不再持有该 handle；`MODULE-PROVIDE-NO-VALUE-HANDOFF-01` 确保 ModuleExports 通道无法被重新引入 |
| 进程内跨 cell 直写 auditcore ledger | ✅ 已限制（只经 BootstrapLedgerStore handle） | ✅ 结构上消除——`ModuleExports` 通道删除 + `IMPL-DECL-COVER-01`（archtest）禁止 `cells/accesscore` import `cells/auditcore`；直写路径在 type system 层不可表达 |
| bootstrap audit 记录丢失（observer 调用链断裂） | ✅ 失快 fail-fast | ✅ 持久性提升——旧路径是 detached best-effort 直写（失败仅 log）；新路径是 L2 outbox tx 持久化，relay 保证投递；`auditappendbootstrap` handler unmarshal 失败 Reject，写失败 Requeue |
| 单个 HMAC key 泄漏伪造两链条目 | ✅ 独立 key（ADR-1121 D3） | ✅ 不变——两 key 仍独立，auditcore 内部持有，不在 Provide 签名传递 |
| MultiStore 误作写 store 注入 | ✅ 编译错误（D4） | ✅ 不变 |
| Auditcore namespace store 误作 bootstrap observer 入参 | ✅ 编译错误（`*BootstrapLedgerStore` 类型） | ✅ 不变——`AppendBootstrapAuthFail` 仍接受 `*BootstrapLedgerStore`；改为 auditcore subscriber 内部调用，隔离不弱化 |

**不变的安全属性**：namespace 物理隔离（`"auditcore"` vs `"bootstrap"`）、独立 HMAC key（D3）、`*BootstrapLedgerStore` sealed handle（D2）、`ledger.MultiStore` 只实现 `QueryStore`（D4）——均保持原 ADR-1121 保证。

**参考**：
- ADR-1121 `docs/architecture/202605270230-1121-audit-chain-bootstrap-namespace-isolation.md`（bootstrap namespace 隔离原始决策）
- Archtest `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`：`tools/archtest/module_provide_signature_frozen_test.go`（符号清单与盲区清单活在该文件的 package godoc）
- Plan：`.claude/plans/1423-issues-foamy-orbit.md`（设计裁决历程 + HTTP 方案被否原因 + DEP-02 环分析）

---

## Amendment 2026-06-03 #1420 / #1413 — 单源 ModuleResult + configcore 字段收口

**#1420（资源双写单源化）**：`CellModule.Provide` 原出参 `(cell.Cell, []bootstrap.Option, []ManagedResource, error)` 要求每个模块把同一个 `ManagedResource` 写进**两条**通道——`opts` 经 `bootstrap.WithManagedResource`（happy-path probe/worker/LIFO-Close）+ 第 3 返回值 `provisional`（pre-Run rollback）。双写仅靠 godoc 约定（Soft），漏写任一半 → 资源泄漏或 /readyz 缺 probe。

收口为单一 `ModuleResult{Cell, Opts, Resources}`：模块只在 `Resources` 列资源，`Builder.Build` 从该一处**同时**派生 `WithManagedResource` 注册（`managedResourceOpts` 助手）与 provisional rollback 栈，两通道结构上不可能分叉。模块禁止自行调 `bootstrap.WithManagedResource`（cellmodules 内由 `WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01` 零容忍拦截）。

Enforcement 演进：
- `MODULE-PROVIDE-NO-VALUE-HANDOFF-01` 由「reflect 冻结 4-out 返回列表」升级为「冻结 2-out（`ModuleResult`, error）+ `ModuleResult` struct 字段集（`NumField()==3` + 字段名 `{Cell,Opts,Resources}` + 类型）」。**威胁矩阵强化**：原 #1423 矩阵「accesscore 拿到 nil BootstrapLedgerStore」「进程内跨 cell 直写」两行依赖「ModuleExports 通道无法被重新引入」——该保证此前只覆盖「返回列表新增 handoff 出参」，现 `NumField()==3` 冻结**额外**封闭「把 Exports 夹带为 `ModuleResult` 结构体字段」这一新向量（✅ → ✅ 强化）。其余矩阵行不受影响（资源生命周期与跨 cell handoff 正交）。
- **新增** `WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01`（下游 Hard cellmodules 内 caller-ban / 上游 Medium，同 #851/#893/#1282 天花板）。

**#1413（configcore 字段收口）**：`SharedDeps` 移除 `EventbusCacheCollector`（required）+ `ConfigStaleCipherInc`（optional）两个 configcore 专属字段——configcore 经 kernel `MetricsProvider` 用 `obmetrics.NewProviderEventbusCacheCollector` / `NewProviderConfigStaleCipherCollector` 自建（这两个 collector 走 kernel Provider，非 raw `client_golang`，不违反「cellmodules 不 import client_golang」治理姿态）。

威胁/正确性再评：
- **Prometheus wire 不变，但 generated metrics-schema 是可见 contract 载体且本 PR 改了它**（F5 披露补全——原表述只说「wire 名不变」不完整）：`gocell_config_stale_cipher_total` / `gocell_eventbus_cache_tombstone_evicted_total` 的 **fqName（Prometheus series 名）+ label 与迁移前字节一致**——运维侧按 series 名 key 的 dashboard/alert 不受影响。但 `gocell generate metrics-schema` 派生的 golden（contract-fanout 5 载体之一、消费方可读的 inventory）有两类**可感知**变化：
  1. **corebundle**：schema `name` 字段由 `stale_cipher_total` **rename** 为 `config_stale_cipher_total`（新 Provider collector `Name="config_stale_cipher_total"`，旧 raw-prom 为 `Subsystem="config" Name="stale_cipher_total"`；二者 fqName 同为 `gocell_config_stale_cipher_total`），外加 `file` 字段从 `cmd/` 移到 `runtime/observability/metrics/`。
  2. **examples/iotdevice / orderfulfillment / todoorder**：collector 移入 shared `runtime/observability/metrics` 包后，`config_stale_cipher_total` 成为这三个 assembly schema 的**净新增条目**（迁移前不在其 inventory），与 sibling `eventbus_cache_tombstone_evicted_total` 同行为。
  4 份 golden 已 `gocell generate metrics-schema --all` regen + `go test ./tools/metricschema/...` 绿；无 wire 改名分支。
- **无双注册**：两 collector 各仅 configcore 消费；configcore 自建一次 + corebundle 停建 = 恰一次。
- **`ConfigEventCollector` 前提纠正**：原表述「configcore 专属」有误——它由 accesscore + configcore + corebundle config-event middleware 共同消费且 `Validate` required，是真·跨 cell 字段，**保留**在 `SharedDeps`。
- **`ConfigKeyProvider` 已收口（#885 + #1413 同 PR 落地，2026-06）**：#885 把 `adapters/vault.TransitMetrics` 从 raw `client_golang` registry 迁到 kernel `metrics.Provider`，vault 非 test 代码 client_golang-free；由此 `cellmodules/configcore` 可直接 import vault 并**自建** key provider（env + `shared.MetricsProvider`，与已自建的 cursor codec / collector 同范式），无 client_golang 入 cellmodules。`ConfigKeyProvider` 字段从 `SharedDeps` 移除，cmd 不再构造/透传，`SharedDeps` 即**完全 cell-agnostic**。选 self-build 而非「通用 inbound typed-dep 框架」：本 ADR §参考框架已 reject 全局 registry，且 generated module list 使构造 option 路径与 codegen funnel 冲突；self-build = fx.Private self-provide 范式。
- **adapter-public prom funnel 已删除（#885）**：5 个 outer-ring passthrough wrapper（`RegisterOrReuseCounter` / `NewCounter` / `NewCounterVec` / `NewGauge` / `NewGaugeFunc`）连同 `adapterPromCallerAllowlist` 条目删除；funnel 仅剩 inner ring `adapters/prometheus/internal/promwrap`，**upstream 评级 Medium→Hard**（Go `internal/` 可见性，外部不可 import）。`TestMetricsFunnel_SymbolSentinel` 收窄到两个工厂导出（`NewMetricProvider` / `NewHookObserver`）。

**参考**：
- 单源生命周期对标：uber-go/fx `fx.Lifecycle.Append(Hook{OnStart,OnStop})`——一次注册派生 start/stop 双向，对应 `ModuleResult.Resources → {WithManagedResource, rollback}`。
- Archtest：`WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01`（`tools/archtest/withmanagedresource_cellmodule_funnel_test.go`）；`MODULE-PROVIDE-NO-VALUE-HANDOFF-01`（已扩展 ModuleResult 字段冻结）；`SHAREDDEPS-FIELDSET-FROZEN-01`（`tools/archtest/shareddeps_fieldset_frozen_test.go`，reflect 冻结 `SharedDeps` 导出字段集，把 cell-agnostic invariant 从 Soft 升 Hard——任何字段增/删/改触发 golden diff，强制在本 godoc 论证 cross-cutting；推进 #1412）。
- gh #885（vault metrics → kernel Provider，funnel Medium→Hard）+ gh #1413（ConfigKeyProvider 收口，SharedDeps cell-agnostic）：**已落地**（#885 + #1413 合并 PR）。对标 ref：uber-go/fx `supply.go` / `module.go`（fx.Private self-provide）、opentelemetry-specification `metrics/api.md`（subscribe-to-change → sync gauge）。

---

## Amendment 2026-06-03 #1410 — control-plane 校验前移 + internalGuard dissolve

#1085 review（PR #1385）seal 公开 composition 面后保留的 **F4**：health 可达性 +
nonce/claimer-kind 等 control-plane 生产安全校验仍只活在 cmd 私有层
`cmd/corebundle/shared_deps_validate.go`（依赖 cmd 私有类型 `internalGuard` /
`consumerClaimerKind`），外部 composition 消费者（非 `cmd/corebundle`）构造 `SharedDeps`
时这些校验被绕过。本 amendment 把它们前移进 `runtime/composition.SharedDeps.validate`。

**改动**：

1. **新增 `SharedDeps.NonceStore kauth.NonceStore` 字段**（紧邻 `InternalHMACRing`）——
   从 cmd 私有 `internalGuard.nonceStore` 提升。`runtime/composition` import `kernel/auth`
   （runtime → kernel 允许，`runtime-isolation` depguard allow `kernel/...`），**不碰 adapters/prometheus 不变式**。
2. **新增 `kernel/idempotency.ClaimerKind` + `Claimer.Kind()` 方法**（镜像 `kauth.NonceStore.Kind()`）——
   claimer 自报类型（`in_memory` / `distributed`），control-plane 校验经
   `shared.ConsumerClaimer.Kind()` 读取。**这是 CP8（real 多 pod 要求 distributed claimer）
   fail-closed 的关键**：消费者无法构造「redis claimer 自报 in_memory」的类型（type-system Hard），
   而 SharedDeps 上加一个消费者自填的 kind 字段是可撒谎的 Soft。删除 cmd 私有 `consumerClaimerKind` 枚举。
   contract-fanout 事件（interface 方法签名变化）：2 生产实现 + ~13 测试 fake 加 `Kind()`，无 Claimer conformance suite。
3. **control-plane 校验移入 `SharedDeps.validate`**（`validateControlPlane` →
   always-on `validateInternalListenerGuard` + `validateVerboseEndpoint` / real-mode
   `validateProductionControlPlane`）：verbose 网关、metrics token、internal-listener guard、
   nonce-store noop 拒绝、in-memory-nonce 多 pod 拒绝、claimer-distributed 要求。每个
   `NewSharedDeps` 消费者自动 fail-closed。health 可达性（HR1）已在更早的 #1085 follow-up 移入。
4. **`internalGuard` 全部 dissolve**（delete-dead）：nonceStore + ring 提升后，`internalGuard`
   的 `mw`/`Middleware()`（仅测试引用，生产 listener 走 `NewAuthServiceToken` 现造）+
   `if store == nil` 兜底（`replay.NonceStore` 恒非 nil）均为死代码。删除 `internalGuard` 结构体 +
   `Middleware()` + `mw`；`internalGuardFromEnv` 收缩为 `buildInternalHMACRing(adapterMode)`
   （只 build ring）。replay/no-header/nonce-kind 行为覆盖全部活在 `runtime/auth`。
5. **cmd 决策点答案**：`SampleVerbosePlaceholder`（CP2）检查留 cmd 作「部署合约」——它引用
   cmd `.env.example` 制品，非可移植 composition 合约；`validateCorebundleDeps(shared)` 收缩为只此一检。

**威胁/正确性再评（无安全回归，净 enforcement 升级）**：

| 关注点 | 迁移前 | 迁移后 |
|--------|--------|--------|
| 外部消费者跑 control-plane 校验 | ❌ cmd 私有，够不到（实质 Soft：cmd 手动调，重构可静默丢） | ✅ Hard sealed-marker 门控的 `NewSharedDeps→validate` 路径（上游 Hard / 下游 Medium，见下方残留 Medium 说明） |
| claimer-distributed 真实性（CP8） | ⚠️ cmd 私有 `consumerClaimerKind`（topology 派生，自身不可绕但外部够不到） | ✅ 可达性升级（所有 `NewSharedDeps` 消费者都跑），**评级 Medium（非 type-system Hard）**：`Claimer.Kind()` 是实现自报，绑定到实现*类型*消除了「调用方自填 kind 字段撒谎」这一 Soft 面（kind 随值走，消费者无法填一个与所接 claimer 矛盾的字段），但方法本身可返回错值——impl 仍可撒谎（`shared_deps_test.go` 的 fake 正是如此）。残留由 (a) 生产实现为固定 in-repo 闭集 + (b) 每实现 Kind() 返回值 pin 测试（`TestInMemClaimer_Kind_ReportsInMemory` / `TestIdempotencyClaimer_Kind_ReportsDistributed`）+ (c) 校验侧对未知 kind fail-closed 封闭。**#1410 review F7 更正**：原写「type-system Hard（不可伪造）」不准确。 |
| nonce-store noop / in-mem-multipod（CP6/CP7） | ⚠️ cmd 私有 introspect `internalGuard.NonceStore()` | ✅ 可达性升级，**评级 Medium（同上）**：`shared.NonceStore.Kind()` 实现自报，由 `runtime/auth` / `adapters/redis` 的 Kind() pin 测试 + `validateProductionNonceStore` 的 fail-closed default（#1410 F2）兜底，非 type-system Hard。 |
| composition 不 import adapters/prometheus | ✅ | ✅（仅新增 `kernel/auth` import，kernel 层允许） |
| AUTH-PLAN-04（auth plan 构造留 cmd） | ✅ | ✅（`buildInternalAuthChain` 仍在 cmd，读 SharedDeps 字段） |

残留 Medium ×2（均非本 amendment 引入的新 funnel）：

1. 「`validate()` 体内必须调 `validateControlPlane`」无 archtest 强制（只能 Soft archtest，charter 禁；
   unit test backstop）——与既有 required-field / HR1 检查同档同 ceiling，是 `validate()` 类 runtime
   guard 的固有天花板。
2. **`Kind()` 实现自报失真**（CP6/CP7/CP8）：Go 无法在类型系统层强制「Redis-backed claimer 必返回
   `distributed`、in-memory 必返回 `in_memory`」。补偿 = 固定 in-repo 实现闭集 + 每实现 Kind() 返回值
   pin 测试 + 校验侧未知 kind fail-closed（#1410 F2/F3/F7）；与 #851/#893/#1282 family 的「self-report /
   holder seal」永久天花板同形态，不立 Soft archtest。**注意上文第一行「外部消费者跑 control-plane 校验」
   的「上游 Hard」专指 `SharedDeps` 的 sealed `valid` 构造门（`NewSharedDeps→validate` 不可绕），不延伸到
   各 `Kind()` 自报的真实性——后者即本条残留 Medium。**

**参考**：archtest 全绿（`MODULE-PROVIDE-NO-VALUE-HANDOFF-01` 只冻结 `ModuleResult` 字段，
不冻结 `SharedDeps` 字段，加 `NonceStore` 不触发）；`runtime/composition/shared_deps_test.go`
`TestSharedDeps_Validate_ControlPlane` table-driven 覆盖 V1/V2/CP1/CP3/CP5/CP6/CP7/CP8/IL1/IL2。

### Amendment 2026-06-04（#1410 review F1 — runtimeOptsFn bypass 闭环）

**问题**：上文 control-plane 校验读的是**声明的** `SharedDeps.NonceStore.Kind()`，但真正守 `/internal/v1/*` 的 store 由调用方 `RuntimeOptionsFunc`（opaque callback）放进 listener auth plan——二者可不一致。`SharedDeps.NonceStore` 是合法 distributed store 而 `RuntimeOptionsFunc` 用另一个 single-process in-memory store 构造 `AuthServiceToken` 时，原校验放行（fail-open 边界）。这是「校验平行声明字段，非实际供给图」的反模式，与 fx `ValidateApp`（dry-run 实际 wiring）背离。

**修复**：在**实际使用点**复核——

1. `composition.Builder.Build` 把 trusted `SharedDeps.Topology`（sealed，调用方不可伪造）经新 option `bootstrap.WithControlPlaneTopology` 注入 bootstrap（append 进 `cellOpts`，排在 `runtimeOpts` 之后 → 调用方无法覆盖）。
2. bootstrap phase0 `validateAuthServiceTokenPlan` 对 listener 实际持有的 `AuthServiceToken.Store.Kind()` 按注入 topology 复核（in-memory 多 pod 拒、未知 kind fail-closed 拒、noop 一直拒）。
3. accept/reject 单源 = `kernel/auth.NonceStoreKind.ReplaySafe(requireDistributed)`，composition 配置期与 bootstrap 使用期共读同一谓词，杜绝「哪些 kind 算 safe」漂移；`Topology.RequiresDistributedReplay()` 单源化原 composition + cmd/redis 的并行副本。

**威胁矩阵补格**：「外部消费者跑 control-plane 校验」一行此前的隐含主张（外部 fail-closed）现真正闭环——既校验声明字段（composition），也校验实际 plan store（bootstrap）。

**AI-robust 评级（B）**：bootstrap-side 复核是 **Medium**（runtime guard / archtest 不参与；phase0 fail-closed）。真正 type-system Hard——让「internal listener auth plan 只能由 `SharedDeps.NonceStore` 构造」编译期不可绕（即 C「`NewAuthServiceToken` internal-path sealing」）——**不可达**：Go 无法表达「只有某几个包能调某导出构造器」，且 `kernel/auth` 不可 import composition（分层），与 #851/#893/#1282 同族永久天花板。C 能做到的最强形态（archtest caller-allowlist）与本 Medium 同档、覆盖更窄，故不做；**won't-do 跟踪 gh #1552**。

**覆盖**：`kernel/auth.TestNonceStoreKind_ReplaySafe`（谓词真值表）/ `runtime/bootstrap.TestValidateAuthServiceTokenPlans`（usage-time 复核，real 多 pod in-mem/unknown 拒、single-pod in-mem 收、distributed 收、dev skip）/ `runtime/composition.TestBuilder_InjectsControlPlaneTopology_RejectsDivergentInternalStore`（端到端：distributed SharedDeps.NonceStore + 另造 in-mem auth plan → App.Run phase0 拒）。
