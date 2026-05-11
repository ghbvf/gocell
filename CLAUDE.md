# GoCell 协作说明

> 本文件是 `.specify/memory/constitution.md`（GoCell 项目宪法）的实施细则。当二者冲突时，以宪法为准。

Cell-native Go 工程底座。只保留稳定的开发规则和架构约束。

## 工作方式

- 修改前先查看 README.md 与 docs/
- 提交信息遵循 Conventional Commits
- 涉及功能或行为变更时，同步更新对应文档
- 被 `.gitignore` 忽略的文件禁止 `git add -f`
- Review 和重构时不考虑向后兼容——当前只有 gocell 自身，没有外部调用方

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
examples/     — 示例项目（ssobff / todoorder / iotdevice），可内置示例 cells/contracts/journeys
generated/    — 工具生成产物（indexes / 派生视图，禁止手工编辑）
actors.yaml   — 外部 Actor 注册（参与 contract 但不属于 Cell 模型的系统）
```

### 依赖规则

- kernel/ 不依赖 runtime/、adapters/、cells/（只依赖标准库 + pkg/ + gopkg.in/yaml.v3）
- cells/ 依赖 kernel/ 和 runtime/，不依赖 adapters/（通过接口解耦）
- runtime/ 可依赖 kernel/ 和 pkg/，不依赖 cells/、adapters/
- adapters/ 实现 kernel/ 或 runtime/ 定义的接口
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

## Go 编码规范

- 错误用 `pkg/errcode` 包
- 日志用 `slog`（结构化字段）
- DB 字段 `snake_case`，JSON/Query/Path `camelCase`
- 函数认知复杂度 ≤ 15
- 新增/修改代码覆盖率 ≥ 80%，kernel/ 层 ≥ 90%（table-driven test）

## 修改代码前

1. 先 `Read` 目标文件，`Grep` 搜索已有实现
2. 改完 `go build ./...`，涉及逻辑 `go test ./...`
3. 只改需要改的

## AI 协作章程

主要实施者是 AI。新增/修改约束 enforcement 机制（archtest / governance rule / codegen funnel / type marker / godoc 强约定）按 AI-rebust 三档（Hard / Medium / Soft）评级；Soft 严禁立项。载体决策原则、archtest 文件命名、review checklist 详见 `.claude/rules/gocell/ai-collab.md`。

archtest CI 入口是 `hack/verify-archtest.sh`（process-isolated shards，K=16）；本地 `go test ./tools/archtest/...` 行为不变。详见 ADR `docs/architecture/202605120000-adr-archtest-process-isolation.md`。

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
