# GoCell 协作说明

> 本文件是 `.specify/memory/constitution.md`（GoCell 项目宪法）的实施细则。当二者冲突时，以宪法为准。

Cell-native Go 工程底座。只保留稳定的开发规则和架构约束。

## 工作方式

- 修改前先查看 README.md 与 docs/
- 提交信息遵循 Conventional Commits
- 涉及功能或行为变更时，同步更新对应文档
- 被 `.gitignore` 忽略的文件禁止 `git add -f`
- Review 和重构时不考虑向后兼容——当前只有 gocell 自身，没有外部调用方
- 新 backlog 条目：Web UI 走 template + Priority dropdown 自动贴 `pri-pX`；CLI 用 `gh issue create --label backlog --label pri-pX --label area-XX --label type-XX` 显式贴（不触发 dropdown，漏贴 `pri-pX` 会被 workflow 贴 `pri-missing` 哨兵）；Status/Estimate/Wave 在 Project v2 设；真源 = GitHub Issues + Project v2 #3，schema 见 `.github/PROJECT.md`

## 核心架构约束

### 分层结构

```
kernel/       — Cell/Slice 运行时 + 治理工具（底座灵魂）
cells/        — 平台 Cell 实现（accesscore / auditcore / configcore），每个 Cell 下含 slices/
contracts/    — 平台跨 Cell 边界契约（按 {kind}/{domain-path}/{version}/ 组织）
journeys/     — 平台 Journey 验收规格（J-*.yaml）+ status-board.yaml（动态交付状态）
assemblies/   — 物理打包配置（assembly.yaml）
fixtures/     — 测试夹具（fixture-*.yaml，供 run-journey 使用）
runtime/      — 通用运行时（http / auth / worker / observability）
adapters/     — 外部系统适配（postgres / redis / rabbitmq / websocket / s3 / oidc）
pkg/          — 共享工具包（errcode / ctxkeys / httputil / query）
cmd/          — CLI 入口（gocell validate / scaffold / generate / check / verify）
cellmodules/  — Composition Root 层：将平台 Cell 绑定到 adapter，对外暴露 Module() composition.CellModule（accesscore / auditcore / configcore + 共享 helper cellsecrets）
examples/     — 示例项目（ssobff / todoorder / iotdevice / corebundlestarter），可内置示例 cells/contracts/journeys
generated/    — 工具生成产物（codegen 契约派生，禁止手工编辑）
actors.yaml   — 外部 Actor 注册（参与 contract 但不属于 Cell 模型的系统）
```

### 依赖规则

- kernel/ 不依赖 runtime/、adapters/、cells/（只依赖标准库 + pkg/ + gopkg.in/yaml.v3）
- cells/ 依赖 kernel/ 和 runtime/，不依赖 adapters/（通过接口解耦）
- runtime/ 可依赖 kernel/ 和 pkg/，不依赖 cells/、adapters/
- adapters/ 实现 kernel/ 或 runtime/ 定义的接口
- cellmodules/ 是 Composition Root 层，可依赖所有层（绑定 cell↔adapter，对外暴露 Module() composition.CellModule）
- examples/ 可以依赖所有层

### Cell 开发规则

- 每个 Cell 必须有 cell.yaml（必填：id / type / consistencyLevel / owner / schema.primary / verify.smoke）
- 每个 Slice 必须有 slice.yaml（必填：id / belongsToCell / contractUsages / verify.unit / verify.contract / allowedFiles）
- Cell 之间只通过 contract 通信；L0 Cell（纯计算库）可被同一 assembly 内的兄弟 Cell 直接 import

### 一致性等级（L0-L4）

| 级别 | 含义 | 场景 |
|------|------|------|
| L0 LocalOnly | 单 slice 内部本地处理 | 纯计算、校验 |
| L1 LocalTx | 单 cell 本地事务 | session 创建、审计写入 |
| L2 OutboxFact | 本地事务 + outbox 发布 | session.created 事件、config.entry-upserted 事件 |
| L3 WorkflowEventual | 跨 cell 最终一致 | 查询投影、合规追踪 |
| L4 DeviceLatent | 设备长延迟闭环 | 命令回执、证书续期 |

### L3 Saga 编排

L3 覆盖两类场景——**单向蕴含**：用 saga 编排 ⟹ L3，但 L3 ≠ saga（`accesscore` / `configcore` / `examples/todoorder/ordercell` 是投影型 / CQRS 的 L3，不用 saga 引擎）。

saga 引擎分三层：状态机原语 `kernel/saga`（`Status` 8 态 / `Step` / `SagaDefinition`）+ append-only journal `kernel/saga/journal`（mem）与 `adapters/postgres/saga`（PG，lease_id CAS）+ 编排运行时 `runtime/saga`（Coordinator + leader-elect via `runtime/distlock` + executor 重试/heartbeat）。Step 编程式（Go func），Compensate 必须纯反向幂等（archtest Hard 守）。

约束 enforcement / 故障 runbook / 设计决策三处单源，不在此复制：

- archtest + governance 索引：`.claude/rules/gocell/saga.md`
- 设计决策 D1–D10 + 威胁矩阵 + 演进路径：ADR `docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md`
- 运维故障 runbook：`docs/ops/saga-runbook.md`

## Go 编码规范

- 错误用 `pkg/errcode` 包；新 `ERR_` 前缀命名空间须注册所有权并更新 golden，见 `.claude/rules/gocell/error-handling.md` §"错误码前缀所有权 (#1091)"
- 日志用 `slog`（结构化字段）
- DB 字段 `snake_case`，JSON/Query/Path `camelCase`
- 函数认知复杂度 ≤ 15
- 新增/修改代码覆盖率 ≥ 80%，kernel/ 层 ≥ 90%（table-driven test）

## 修改代码前

1. 先 `Read` 目标文件，`Grep` 搜索已有实现
2. 改完 `go build ./...`，涉及逻辑 `go test ./...`
3. 只改需要改的

## AI-robust 治理章程

主要实施者是 AI。新增/修改约束 enforcement 机制（archtest / governance rule / codegen funnel / type marker / godoc 强约定）按 AI-robust 三档（Hard / Medium / Soft）评级；Soft 严禁立项。载体决策原则、archtest 文件命名、review checklist 详见 `.claude/rules/gocell/ai-robust.md`。

archtest CI 入口是 `.github/workflows/archtest-nightly.yml`（schedule cron + workflow_dispatch，24-shard matrix）；PR / push CI 不跑完整 archtest suite（全 628+ tests），但 4 类核心 invariant 通过 `hack/verify-archtest-invariants.sh` 在 `governance.yml` 中 PR-time 执行（~30-60s 单进程，共享 SharedResolver）；完整 archtest 由 `archtest-nightly.yml` 兜底。本地反馈走开发者显式触发：`make verify` 一键全跑（含 archtest）或 `bash hack/verify-archtest.sh` 直跑；`hack/githooks/pre-push` 因 CPU/RSS 预算不跑 archtest（PR #887 round-2 撤回，详见 ADR §"pre-push archtest 撤回"）。`hack/verify-archtest.sh` 三种执行模式：`SHARD_TARGET=N` → 单 shard（CI matrix）；`SHARD_COUNT=1` → 单进程流式（local default）；`SHARD_COUNT>1` 无 `SHARD_TARGET` → 并行 fan-out。`go test ./tools/archtest/...` 行为不变。详见 ADR `docs/architecture/202605120000-adr-archtest-process-isolation.md` §Amendment 2026-05-23-pr-time-to-nightly + §Amendment 2026-05-28（K=16→24）。

## 参考框架

新建或重构层内模块时，先用 `WebFetch` 读对标源码，commit message 注明 `ref: {framework} {file}`。详见 `docs/references/framework-comparison.md`。

| 模块 | 对标框架 |
|------|---------|
| Cell/Slice 声明模型 + 生命周期 + 校验 | Kubernetes |
| Cell 运行时 | Uber fx |
| 代码生成 | go-zero goctl |
| 中间件 | Kratos |
| 配置热更新 | go-micro |
| 事件驱动 | Watermill |

## Sandbox 提权

`git push/pull/fetch` 和 `gh` 命令须用 `dangerouslyDisableSandbox: true`。

## 文档命名规则

格式：`yyyyMMddHHmm-编号-实际功能或问题.md`
示例：`202603281443-022-compliance-api-review.md`

适用范围：`docs/architecture/` (ADR)、`docs/plans/` (实施计划)、`docs/reviews/` 等时间序载体。
`docs/guides/` / `docs/ops/` 等长期参考文档按主题名命名（如 `cell-development-guide.md`），不需要时间戳前缀。
