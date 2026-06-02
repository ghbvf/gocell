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
| `CellModule` | 接口：`ID() string` + `Provide(ctx, *SharedDeps) (cell.Cell, []Option, []ManagedResource, error)` |
| `SharedDeps` | 跨 cell 共享依赖的公开 bag（接口字段，无 adapter 具体类型）；**sealed construction**——经 `NewSharedDeps(...)` 构造盖 marker，`Build` 拒未盖戳实例 |
| `Builder` | `New() / With(...) / Build(ctx, *SharedDeps, RuntimeOptionsFunc) (*App, error)`；`Build` 校验 marker + `runtimeOptsFn` 非 nil |
| `App` | `Run(ctx) error`（委托 bootstrap.New(...).Run） |
| `RuntimeOptionsFunc` | `func(cells []cell.Cell) ([]bootstrap.Option, error)` |

`SharedDeps` 字段全部为接口或 kernel/runtime 类型（`kernelmetrics.Provider`、`idempotency.Claimer`、`*auth.JWTIssuer` 等），**不包含**任何 `adapters/prometheus` 或 `prometheus/client_golang` 具体类型，使 `runtime/composition` 不向 adapters/ 引入依赖。

**Amendment 2026-06-02 #1423**：`CellModule.Provide` 签名已从 `Provide(ctx, *SharedDeps, in ModuleExports) (cell.Cell, ModuleExports, []bootstrap.Option, []ManagedResource, error)` 简化为 `Provide(ctx, *SharedDeps) (cell.Cell, []bootstrap.Option, []ManagedResource, error)`。`ModuleExports` 类型已完全删除（详见本文末尾 §Amendment 2026-06-02）。

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
| `CellModule.Provide` 签名冻结：2 入参（context.Context, *SharedDeps）/ 4 出参（cell.Cell, []Option, []ManagedResource, error），无跨 module 值传递通道 | archtest `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`（`tools/archtest/module_provide_signature_frozen_test.go`）——reflect 冻结接口方法签名；重新引入 handoff 通道 = 签名变 → CI 红 | **Hard**（reflect interface method 签名；重引入 handoff 使签名变，archtest pin 即断——上下游均 Hard） |

**注**：`cellmodules/` 与 `cmd/` 同属 composition-root 层，既有 composition-root archtests（cas/session/ledger 协议位置、wrapper 调用点）已通过 `cellmodules/` 前缀覆盖，不存在扫描盲区。

**退役（Amendment 2026-06-02 #1423）**：`MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01` archtest 已退役。事件消费者经 EventRouter 异步路由，Provide 时不存在顺序依赖（auditcore 不再向 accesscore 传递 `BootstrapLedgerStore` 构造实例）。签名冻结由 `MODULE-PROVIDE-NO-VALUE-HANDOFF-01` 接管。

---

## 安全覆盖说明

**无安全回归**：

- auth plan（`NewAuthJWTFromAssembly`、`NewAuthServiceToken`）的构造仍在 composition root（`cmd/corebundle/bundle_options.go`、`examples/corebundlestarter/run.go`），严格符合 AUTH-PLAN-04。
- `SharedDeps.InternalHMACRing` 字段由 cmd/ 在 env 解析阶段构造（`internalGuardFromEnv`），cellmodules/ 消费该接口字段但不构造 HMAC key，符合单源原则。
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
   - **新增** `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`（`tools/archtest/module_provide_signature_frozen_test.go`）：Hard，reflect 冻结 `CellModule.Provide` 签名（2 入参 / 4 出参）——重新引入跨 module 值传递通道必须改签名，archtest 即断。
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
