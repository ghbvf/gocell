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

## 新增 invariant 决策原则

新增任何"约束"（archtest / governance rule / godoc 强约定）前，按以下优先级决策载体：

1. **funnel + codegen**：能否用 schema/marker 单源 → codegen 派生执行体？能 → 走这条
2. **type system 自然拦**：能否用 Go interface / typed struct 让违反不可表达？能 → 走这条（注：type system 与 archtest 可并存——凡涉及 PII / 安全语义的约束，即使已有类型拦截，仍须评估是否需要 archtest 双重防线，例如 `MESSAGE-CONST-LITERAL-01` / `DETAILS-SLOG-ATTR-01`）
3. **archtest 平铺兜底**：上面两条都不行 → 一个 `tools/archtest/{theme}_invariants_test.go` 主题文件，每个规则函数前 godoc 加 `// INVARIANT: {ID}` 锚点 + 不能 funnel 的理由

**文件命名分支**：同主题规则数 ≥ 3 → 新建或扩展 `{theme}_invariants_test.go` 主题文件；单条独立规则 → 保留 `{rule}_test.go` 单文件命名。已有 `{rule}_test.go` 单文件且新增同主题第 3 条规则时，重命名为 `{theme}_invariants_test.go` 并补完 anchor。

**不准建 Registry / 中心化注册表**。多份文档用 grep 锚点串联（grep `INVARIANT: {ID}` 跳全套）。

主流对照（K8s / CockroachDB / Linux / Rust / Go 工具链）都接受 funnel 不到的残留，平铺管理。详见 `docs/plans/202605070431-pr403-funnel-fix-roadmap.md`。

## 依赖选择原则

实现外部协议/标准（密码学、签名、OIDC、migration、可观测性导出等）必须优先使用官方或成熟开源库，禁止自建；实现 GoCell 领域逻辑（Cell/Slice 模型、治理规则、outbox 接口等）保留自建。详见 `docs/reviews/202604061630-dependency-replacement-plan.md`。

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
