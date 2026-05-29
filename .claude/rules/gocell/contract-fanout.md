# 契约变更扇出闭环

> 契约变更的真值同时活在 5 处载体；任一处漏同步，bug 就在最深的载体暴露。本规则约束扇出必须在同 PR 内被强制同步、被机器验证。

## 触发条件

以下任一变更触发本规则，PR 必须出 implementation matrix：

- Go interface 方法签名 / error sentinel / 返回值 metadata 变化
- contract.yaml endpoint / payload / event schema 变化（含 outbox / event payload v1 → v2 演化）
- DB schema / migration 列约束或语义变化
- DB schema **DROP COLUMN** / **`schema_guard.forbiddenColumns` 新增 entry**：必须附 "该列原本支撑的 invariant inventory"（一致性域 / 攻击面 / 串行化机制）+ 逐项替代证明。`schema_guard.forbiddenColumns` 新 entry 必须引用 ADR explicit 决议章节号，不接受 ADR amendment §0 单段论证作为 enforcement 来源。
- errcode 新 Kind / Category / Sentinel
- saga.Status 新常量 / journal.EventKind 新 Kind（扇出载体 = readyz 状态表 / conformance 终态覆盖 / alerting kind legend / TerminalEventKind 映射，由 archtest `SAGA-STATUS-FANOUT-COVERAGE-01` 机器守卫）

> 不触发：纯内部 helper 签名、未导出类型、CLI flag、observability label 调整。

> **DROP COLUMN 反面教材**：migration `025_drop_sessions_authz_epoch_at_issue.sql`
> 仅以"行内 pin 提供零额外防御"作论证 DROP，未列 invariant inventory（漏列
> refresh 路径 epoch provenance + login vs Invalidator 串行化），导致 PR #490
> ship 后 review 才发现 P1。S4d migration 026 撤回该 DROP（详见 ADR
> `202605101400-adr-credential-session-protocol.md` §0 A1 RETRACTED + §A8）。

## 5 个必查载体 + 强制手段

| 载体 | 必须同步 | Enforcement |
|------|---------|-------------|
| 1. interface / schema 定义 | godoc 或 schema doc 写明新约束 | 人审 |
| 2. 全部实现 | 每个实现满足新约束 | conformance test 挂 interface 包，跑遍所有实现；archtest 守"新增实现自动接入 conformance" |
| 3. 各层 test | unit + integration + conformance 三层覆盖 | PR 描述 verbatim 列出失败复现命令；merge gate 跑过才能 close |
| 4. 测试夹具 | fixture seed 满足新契约前提 | fixture diff 是 review 一等公民 |
| 5. 公开 contract / docs | contract.yaml + API schema + ADR 同步 | governance scan：新增 status / error / payload 字段必须能枚举所有受影响 contract（archtest） |

## M4 反向覆盖兜底（archtest 机器守卫）

5 个载体的同步靠人审容易漂移；archtest 在机器层提供双向闭环兜底。以下 5 条 invariant
活在 `tools/archtest/reverse_coverage_invariants_test.go`，在 archtest-nightly 每次运行：

| Invariant ID | 方向 | 规则摘要 |
|---|---|---|
| **IMPL-DECL-COVER-01** | impl → contract | `cells/<A>` 生产代码禁止直接 import `cells/<B>`（不同 cell），跨 cell 通信必须经 contract；唯一例外是 `cells/<B>/<B>test/*` 公开测试 helper |
| **HANDLER-DECL-COVER-01** | impl → contract | `cells/*` / `examples/*` 中实现了 `generated/contracts/http/.../Service` 接口的具体类型，必须在对应 contract.yaml 存在且 `lifecycle: active`（无孤儿 impl） |
| **EMIT-DECL-COVER-01** | impl → contract | `kernel/outbox.Emit` 调用方所在 cell，必须在 contract.yaml `endpoints.publisher` 或 `triggers` 中声明；topic 常量必须能被 const-eval 解析 |
| **DEAD-CONTRACT-01** | contract → impl | `lifecycle: active` 的 contract.yaml 必须有对应入口：http → 有 cell impl；event → 有 publisher / subscriber slice / actorSubscriber；其他 → 有 ownerCell 或 endpoints.server（无孤儿契约） |
| **DEAD-CODE-01** | contract → impl | production Go 代码不得 import `lifecycle: deprecated` contract 的 generated 包，也不得含 deprecated `contract.id` 字符串字面量（今日 0 deprecated 故 vacuous pass） |

> **触发说明**：任何修改 contract.yaml `endpoints` / `ownerCell` / `kind` 字段，或新增/删除 slice，
> 或修改 `cells/*/slices/*/` 下的接口实现，都可能触发上述 invariant 红灯。
> 修 contract.yaml 时同步检查 5 条 invariant，无需额外步骤——archtest 会在 nightly 自动验证。

## Implementation matrix 模板

PR 描述强制包含：

```
Contract: <interface or schema>
Change: <one line>
Implementations: [ ] memstore  [ ] PG  [ ] fake
Conformance test: <package.TestName>
Repro: <go test -tags=... -run='...'>
Dependent contracts (governance scan): <list or "none">
Invariant inventory (DROP COLUMN / forbiddenColumns 新 entry only):
  - <invariant 1>: <replacement evidence>
  - <invariant 2>: <replacement evidence>
  ...
```
