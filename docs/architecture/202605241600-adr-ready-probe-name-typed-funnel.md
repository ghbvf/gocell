# ADR: Adapter readiness-probe 名 typed-string concept funnel（Soft → Hard）

- Status: Accepted
- Date: 2026-05-24
- Tracks: gh issue #805 `OPS-CONTRACT-STRING-FUNNEL-01`
- Builds on: ai-robust.md §"Hard 范本目录" string-typed concept funnel；`kernel/governance.RuleCode` + archtest `GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01`（精确镜像对象）
- Implemented by: PR (worktree 500-ready-probe-name-funnel)

## §1 背景

Adapter dependency-availability readiness-probe 名（`postgres_ready` /
`redis_ready` / `s3_ready` / `rabbitmq_ready` / `vault_transit_ready` /
`oidc_ready` / `websocket_hub_ready` / `postgres_indexes_valid_ready`）此前以
散落的 untyped string 形态存在三种构造面：

1. `lifecycle.ManagedResource.Checkers() map[string]func(...)` 的 map 字面量 key
   （postgres ×2 / rabbitmq / vault / websocket）；
2. `adapterutil.HealthToCheckers(name string, …)` 首参（redis / oidc）；
3. s3 的一个 untyped `const ReadyProbeName = "s3_ready"`。

PR #538 为此引入的 enforcement 只到 **Soft**：

- `adapters/s3/s3_test.go::TestReadyProbeName_LiteralAnchor`（自评 "AI-robust
  rating: Soft (string convention)"）锁 `"s3_ready"` 字面量；
- archtest `health_aggregation_test.go` 的 regex 扫描器
  （`adapterCheckerNameViolationsFromPass` / `healthCheckerCallNameViolationsFromPass`）
  + `managed_resource_contract_test.go` 的两个 `…UseReadySuffix` 测试，只校验
  **值形状**（snake_case + `_ready`），不把值收口到单一声明集；
- `READYZ-PROBE-NAMING-01` 自身在 godoc 里声明 "Blind spot #2"：adapter
  Checkers / HealthToCheckers 面不被 `NewProbe` hyphen-check 覆盖。

Soft 的后果：AI co-author 可随手写一个新的裸字面量 probe 名（typo / 漂移 /
dashboard 全量重命名），regex 仍然 pass，drift 在最深处（运维 dashboard /
alert 断链）才暴露。issue #805 的 trigger 正是"第 3 个 adapter readiness const
漂移事故 / observability dashboard 全量重命名"。

## §2 决策

把 adapter probe 名升级为 **Hard 的 string-typed concept funnel**，机制与
`kernel/governance.RuleCode` 同形：

1. **类型**：`kernel/healthz.ReadyProbeName`（`type ReadyProbeName string`）。
   仅覆盖 adapter dependency-availability `_ready` probe。
2. **声明集**：各 adapter 在自己 package 声明 typed const（统一命名
   `ProbeReady`，postgres 另有 `ProbeIndexesValidReady`）。
3. **构造点收口**：
   - `adapterutil.HealthToCheckers(name healthz.ReadyProbeName, …)` 首参收 typed，
     内部一次 `string(name)` 转回 bare-string map key（funnel 边界）；
   - 直接 map 字面量 adapter 用 `string(ProbeReady)` 作 key（`map[string]` 契约不变）。
4. **archtest `OPS-CONTRACT-STRING-FUNNEL-01`**（`ops_contract_string_funnel_test.go`）：
   - `construction_funnel`（下游）：每个 sanctioned package 的 Checkers map key
     + HealthToCheckers 首参必须经 `info.Uses` 解析到声明的 `*types.Const`，
     拒 `BasicLit` / `BinaryExpr` / `string(localVar)`；
   - `declaration_site_lock`（上游）：`ReadyProbeName` typed const 只能声明在
     sanctioned package 集（6 adapter + runtime/websocket）；
   - `value_shape`：每个声明 const 值过 snake_case + `_ready` regex（每 const 一次）；
   - `golden_inventory`：冻结全量 `(pkg.const → value)`，增删改名 = 显式 golden diff；
   - 盲区自检 fixtures（bare literal / local var / decl bypass）+
     `sanctioned_set_covers_all_checkers` meta-check。

`ManagedResource.Checkers()` 接口签名 **不变**（保持 `map[string]func`）。

### §2.1 AI-robust 双向锁评级（ai-robust.md §"Funnel 双向锁评级"）

- **下游 Hard**：construction_funnel 用类型信息把构造实参解析到声明集，裸字面量
  不可表达——与 `ruleCodeArgResolvesToConst` 同机制。
- **上游 Hard**：declaration_site_lock + named type（裸 string 不能不经转换赋给
  `ReadyProbeName` const）使唯一授权路径 = "在 sanctioned package 声明 typed const"。

这是 archtest-bound Hard（非编译期 seal），与 RuleCode 同档——是 string-typed
concept funnel 形状可达的最高档。

## §3 被拒绝方案：把 `Checkers()` map key 改成 typed（Option A）

考虑过把接口改成 `Checkers() map[healthz.ReadyProbeName]func(...)`，让构造点
`{ProbeReady: fn}` 免去 `string(...)`。**拒绝**，理由：

1. **优雅简洁反噬**：`ManagedResource` 有约 25 个实现，其中约 18 个返回 nil/空 map
   或非 readiness 键（test fakes、`otel.poolMetricsResource`、
   `postgres.PGSessionStore`、`corebundle.bootstrapLimiterResource`），它们与
   ready-probe 无关，却被迫 import healthz + 改签名——ceremony 扇出到整个生态。
2. **类型说谎反例**：`runtime/outbox.Relay.Checkers()` 的 key 是
   `outbox-relay-poll` / `-reclaim` / `-cleanup`（连字符 budget checker，**非**
   `_ready` readiness probe）。typed map 会逼它写
   `healthz.ReadyProbeName("outbox-relay-poll")`——把一个非 readiness 概念强行
   套上 readiness 类型，语义错误。
3. **funnel 不依赖 map key 类型**：Hard 来自 archtest 把构造实参解析到声明集
   （RuleCode 的 `Code` 字段是 typed，但拦截力来自 archtest，不是字段类型）。
   typed map key 只改 call-site 工效，不增 Hard 强度。

代价（5 处 `string(ProbeReady)` 直接 map key）是从 typed 域穿到共享 `map[string]`
契约边界的诚实标记，可接受。该接口签名不变也意味着本变更**不触发 contract-fanout
interface 规则**；唯一签名变更 `adapterutil.HealthToCheckers` 是 helper 签名
（contract-fanout.md 明确不触发），仅 2 个 caller（redis / oidc）。

## §4 无 runtime `Validate()`

`ReadyProbeName` 不提供运行时校验方法。probe-name shape policy（snake_case +
`_ready`）由 archtest 静态守，新增 runtime regex 会制造平行治理面（见
`kernel/healthz/probe.go::Probe.Name` godoc 同款论证）。

## §5 sanctioned-set 诚实 caveat + 补偿

construction_funnel 只扫 sanctioned package 集；一个**未列入**该集的新 adapter
若用裸 `_ready` 字面量授权 probe 会逃逸。补偿：`sanctioned_set_covers_all_checkers`
meta-check 加载 production，发现任何 `Checkers()` 返回 `map[string]func` 且含
`_ready` 形状字面量 key 的 package，断言其 ∈ sanctioned 集——新 bare-literal
probe 作者会 fail loud，直到被纳入 funnel。

## §6 影响 / 迁移

- 删除（不与 Hard 并存）：`TestReadyProbeName_LiteralAnchor`、
  `TestAdapterManagedResourceCheckerNamesUseReadySuffix`、
  `TestRuntimeWebsocketCheckerNamesUseReadySuffix`、health_aggregation 的
  `adapter*/healthChecker* / checkerNamesFromFuncPass / constStringValue` 扫描器。
- `READYZ-PROBE-NAMING-01` Blind spot #2 关闭（godoc 改指向本 funnel）；该规则
  保留 `NewProbe` hyphen-check 覆盖 framework / cellgen probe 名。
- `bootstrap.WithHealthChecker` 命名扫描原本在 prod 无 callsite（vacuous），随
  health_aggregation 扫描器一并删除；未来若 composition root 用它注册 adapter
  probe，命名归 composition root 责任（已在删除点 godoc 标注）。

## §7 状态对照（覆盖表逐行）

| probe 面 | 旧 enforcement | 新 enforcement | 评级 |
|---------|---------------|---------------|------|
| adapter Checkers map key | regex 值形状（Soft） | OPS-CONTRACT-STRING-FUNNEL-01 construction_funnel | Hard |
| HealthToCheckers 首参 | regex 值形状（Soft） | 同上 + typed 签名 | Hard |
| s3 const 字面量 | TestReadyProbeName_LiteralAnchor（Soft） | golden_inventory | Hard |
| probe const 声明位置 | 无 | declaration_site_lock | Hard |
| 新 adapter 未纳入 funnel | 无 | sanctioned_set_covers meta-check | Hard（兜底） |
| framework probe（config_watcher 等） | READYZ-PROBE-NAMING-01 hyphen | 不变 | 既有 |
| cell repo probe | cellgen RegisterRepoReady | 不变 | 既有 Hard |
