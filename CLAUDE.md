# GoCell 协作说明

> 本文件是 `.specify/memory/constitution.md`（GoCell 项目宪法）的实施细则。当二者冲突时，以宪法为准。

Cell-native Go 工程底座。只保留稳定的开发规则和架构约束。

## 工作方式

- 修改前先查看 README.md 与 docs/
- 提交信息遵循 Conventional Commits
- 涉及功能或行为变更时，同步更新对应文档
- 被 `.gitignore` 忽略的文件禁止 `git add -f`

## 核心架构约束

### 分层结构

```
kernel/       — Cell/Slice 运行时 + 治理工具（底座灵魂）
cells/        — Cell 实现（access-core / audit-core / config-core），每个 Cell 下含 slices/
contracts/    — 跨 Cell 边界契约（按 {kind}/{domain-path}/{version}/ 组织）
journeys/     — Journey 验收规格（J-*.yaml）+ status-board.yaml（动态交付状态）
assemblies/   — 物理打包配置（assembly.yaml）
fixtures/     — 测试夹具（fixture-*.yaml，供 run-journey 使用）
runtime/      — 通用运行时（http / auth / worker / observability）
adapters/     — 外部系统适配（postgres / redis / oidc / s3 / rabbitmq / websocket）
pkg/          — 共享工具包（errcode / ctxkeys）
cmd/          — CLI 入口（gocell validate / scaffold / generate / check / verify）
examples/     — 示例项目（sso-bff / todo-order / iot-device）
generated/    — 工具生成产物（indexes / 派生视图，禁止手工编辑）
actors.yaml   — 外部 Actor 注册（参与 contract 但不属于 Cell 模型的系统）
```

### 依赖规则

- kernel/ 不依赖 runtime/、adapters/、cells/（只依赖标准库 + pkg/）
- cells/ 依赖 kernel/ 和 runtime/，不依赖 adapters/（通过接口解耦）
- runtime/ 不依赖 cells/、adapters/
- adapters/ 实现 kernel/ 或 runtime/ 定义的接口
- examples/ 可以依赖所有层

### Cell 开发规则

- 每个 Cell 必须有 cell.yaml（必填：id / type / consistencyLevel / owner / schema.primary / verify.smoke）
- 每个 Slice 必须有 slice.yaml（必填：id / belongsToCell / contractUsages / verify.unit / verify.contract）
  - owner、consistencyLevel 缺省时继承 cell.yaml；allowedFiles 缺省时按目录约定 `cells/{cell-id}/slices/{slice-id}/**`
- Cell 之间只通过 contract 通信，禁止直接 import 另一个 Cell 的 internal/
  - 例外：L0 Cell（纯计算库）可被同一 assembly 内的兄弟 Cell 直接 import，无需 contract
- 动态交付状态（readiness / risk / blocker / done / verified / nextAction / updatedAt）只在 `journeys/status-board.yaml`，禁止出现在 cell.yaml / slice.yaml / contract.yaml / assembly.yaml
- `lifecycle`（draft / active / deprecated）是治理字段，允许写在 contract.yaml 中
- cell.yaml 不维护 slices、journeys、contracts 反向索引；如需汇总视图，由工具生成
- 禁止使用旧字段名：cellId / sliceId / contractId / assemblyId / ownedSlices / authoritativeData / producer / consumers / callsContracts / publishes / consumes（详见 metadata-model-v3.md 迁移附录）

### 一致性等级（L0-L4）

| 级别 | 含义 | 场景 |
|------|------|------|
| L0 LocalOnly | 单 slice 内部本地处理 | 纯计算、校验 |
| L1 LocalTx | 单 cell 本地事务 | session 创建、审计写入 |
| L2 OutboxFact | 本地事务 + outbox 发布 | session.created 事件、config.changed 事件 |
| L3 WorkflowEventual | 跨 cell 最终一致 | 查询投影、合规追踪 |
| L4 DeviceLatent | 设备长延迟闭环 | 命令回执、证书续期 |

## Go 编码规范

- 错误用 `pkg/errcode` 包，禁止裸 `errors.New` 对外暴露
- 日志用 `slog`（结构化字段），禁止 `fmt.Println` / `log.Printf`
- DB 字段 `snake_case`，JSON/Query/Path `camelCase`
- 函数认知复杂度 ≤ 15
- 新增/修改代码覆盖率 ≥ 80%，kernel/ 层 ≥ 90%（table-driven test）

## 修改代码前

1. 先 `Read` 目标文件，`Grep` 搜索已有实现
2. 改完 `go build ./...`，涉及逻辑 `go test ./...`
3. 只改需要改的

## 参考框架

开发时参考对标框架解决方案，见 `docs/references/framework-comparison.md`：
- Cell/Slice 声明模型 + 生命周期 + 校验 → Kubernetes
- Cell 运行时 → Uber fx
- 代码生成 → go-zero goctl
- 中间件 → Kratos
- 配置热更新 → go-micro
- 事件驱动 → Watermill

### 对标对比规则

新建或重构 kernel/、cells/、runtime/、adapters/ 下的模块时，**必须**先在线读取对标框架的对应源码再动手：

1. 查 `docs/references/framework-comparison.md` 找到当前模块的 primary/secondary 对标文件路径
2. 用 `WebFetch` 从 GitHub 拉取对标源码（`https://raw.githubusercontent.com/{owner}/{repo}/main/{path}`）
3. 提取接口签名、生命周期钩子、错误处理等关键设计决策
4. 编码时在 PR 描述或 commit message 中注明：`ref: {framework} {file}` + 采纳/偏离理由

## 文档命名规则
格式：`yyyyMMddHHmm-编号-实际功能或问题.md`（ date "+%Y%m%d%H%M" 后缀按内容选择，不限 `.md`）
示例：`202603281443-022-compliance-api-review.md`

