# GoCell 协作说明

Cell-native Go 工程底座。只保留稳定的开发规则和架构约束。

## 工作方式

- 修改前先查看 README.md 与 docs/
- 提交信息遵循 Conventional Commits
- 涉及功能或行为变更时，同步更新对应文档
- 被 `.gitignore` 忽略的文件禁止 `git add -f`

## 核心架构约束

### 分层结构

```
kernel/     — Cell/Slice 运行时 + 治理工具（底座灵魂）
cells/      — 内置 Cell（access-core / audit-core / config-core）
runtime/    — 通用运行时（http / auth / worker / observability）
adapters/   — 外部系统适配（postgres / redis / oidc / s3 / rabbitmq / websocket）
pkg/        — 共享工具包（errcode / ctxkeys）
cmd/        — CLI 入口（gocell validate / scaffold / generate / check / verify）
examples/   — 示例项目（sso-bff / todo-order / iot-device）
```

### 依赖规则

- kernel/ 不依赖 runtime/、adapters/、cells/（只依赖标准库 + pkg/）
- cells/ 依赖 kernel/ 和 runtime/，不依赖 adapters/（通过接口解耦）
- runtime/ 不依赖 cells/、adapters/
- adapters/ 实现 kernel/ 或 runtime/ 定义的接口
- examples/ 可以依赖所有层

### Cell 开发规则

- 每个 Cell 必须有 cell.yaml（含 cellId / type / consistencyLevel / owner / ownedSlices / authoritativeData / contracts / verify）
- 每个 Slice 必须有 slice.yaml（含 sliceId / belongsToCell / consistencyLevel / journeys / verify / allowedFiles）
- Cell 之间只通过 contract 通信，禁止直接 import 另一个 Cell 的 internal/
- 动态状态（state / risk / blocker）只在 Status Board，不在 cell.yaml / slice.yaml

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
- Cell 生命周期 → Uber fx
- 代码生成 → go-zero goctl
- 中间件 → Kratos
- 配置热更新 → go-micro
- 事件驱动 → Watermill
