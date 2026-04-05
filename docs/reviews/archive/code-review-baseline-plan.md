# 全仓基线 Code Review 计划

## 摘要

- 目标：对当前仓库 `src/` 做一次全量基线 code review，输出可执行的 findings、优先级和耦合点结论。
- 当前基线：现有 Go 包测试 `go test ./...` 已通过；`adapters/`、`runtime/`、`examples/` 目前主要是占位内容。
- 交付物：每个 review 流一份 findings 清单，最后合并成一份 P0/P1/P2 级别的总报告。

## Review 拆分

- 流 A，完全可独立：基础原语与通用包。范围：`pkg/ctxkeys`、`pkg/errcode`、`kernel/cell`、`kernel/idempotency`、`kernel/outbox`。重点看接口契约、错误语义、并发/重入、状态边界。
- 流 B，条件独立：元数据模型与解析。范围：`kernel/metadata`、schema 资源、`actors.yaml`。先冻结 `ProjectMeta` 和各 `*Meta` 类型，再审路径匹配、YAML 解析、错误包装、schema/类型一致性。
- 流 C，条件独立：治理与查询逻辑。范围：`kernel/governance`、`kernel/registry`、`kernel/journey`、`kernel/slice`。在流 B 的模型假设稳定后并行审，重点看漏检、误报、引用完整性、拓扑合法性、verify 闭包、索引和排序一致性。
- 流 D，部分独立：Assembly 与公开运行时入口。范围：`gocell.go`、`kernel/assembly` 的生命周期逻辑。可与流 A 并行，重点看注册约束、Start/Stop 状态机、失败回滚、健康检查、顶层 API 是否过薄或泄漏内部约束。
- 流 E，不建议独立终审：生成、脚手架、CLI。范围：`kernel/scaffold`、`kernel/assembly` 中生成路径、`cmd/gocell`。这部分必须建立在流 B、C、D 结论之上，重点看模板产物是否符合 schema 和目录约定、命令到内核的错误传播、生成与校验行为是否一致。
- 流 F，资产层可并行：`cells/access-core`、`cells/audit-core`、`cells/config-core`、`contracts/`、`journeys/`、`assemblies/`。可按单个 cell、contract 家族、journey/assembly 三块并行 review，但结束前必须做一次全局引用一致性复核。
- 低优先级仅补查：`adapters/`、`runtime/`、`examples/` 当前是占位目录，只做完整性检查，不作为本轮主 review 流。

## 重点公共接口与类型

- `gocell.NewAssembly`：确认顶层 API 是否最小且语义清晰。
- `cell.Cell`、`cell.Dependencies`、`cell.HealthStatus`：确认生命周期契约、依赖注入边界和健康语义。
- `metadata.ProjectMeta` 及各 `*Meta` 类型：确认它们是 parser、governance、registry、journey、generator 共用的单一事实源。
- `governance.ValidationResult`：确认错误码、严重级别、字段路径和文件路径语义一致。
- CLI 子命令 `validate`、`scaffold`、`generate`、`check`、`verify`：确认用户可见行为、退出码和错误信息一致。

## 测试与验收

- 入口门槛：先记录当前 `go test ./...` 全绿基线，review 不接受现状未知的结论。
- 每个 review 流都要输出：问题级别、证据、影响范围、是否阻塞合流。
- 必查场景：合法和非法 metadata 树解析、跨文件引用断裂、治理规则误报和漏报、assembly 启停与回滚、CLI 参数错误与错误透传、模板和生成结果是否满足现有 schema。
- 合流标准：没有未解释的 P0/P1 风险；所有跨流问题都能指回具体接口、规则或资产文件；资产层结论必须经过一次全局一致性复核。
- 最后一轮只审耦合点：`metadata -> governance/registry/journey/slice`、`cell -> assembly`、`metadata/schema -> scaffold/generator -> CLI`。

## 默认假设

- 本计划针对当前仓库全量基线审查，不是某一个 PR 的 diff review。
- 审查优先级按正确性、接口契约、跨文件一致性、失败模式排序，样式和注释问题只在影响理解时记录。
- 默认多人并行；如果 reviewer 不足，优先保留流 A、B、C、F，把流 D、E 合并到最后做集成复核。
