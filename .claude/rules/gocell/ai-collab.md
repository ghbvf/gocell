# AI 协作章程

> 第一性原理：GoCell 主要实施者是 AI（claude code）。每 session fresh instance、无记忆累积、不学反馈、按字面翻译约定。工程治理目标必须从"对人友好"转为"AI-rebust"——违反不可表达 / 机制不可绕过 / 字面约定全部消除。

## 适用范围

本章程适用于"新增/修改约束 enforcement 机制"：

- archtest（`tools/archtest/*_test.go`）
- governance rule（`gocell validate` 规则、bootstrap 期 fail-fast 校验）
- codegen funnel（schema/marker 单源 → 派生执行体）
- type marker（typed function wrapper、sealed interface、reflect 字段冻结）
- godoc 强约定 / ADR-mandated pattern（`// INVARIANT: ID`、`// PANIC-REGISTERED-01:` 这类）

不在范围：CI 已有 lint/test/build；日常实施任务（加 endpoint、加字段、修 bug、refactor）；review finding 中的 bug 修复类。

## AI-rebust 三档分级

| 档 | 定义 | 典型载体 | AI 可绕过性 |
|---|---|---|---|
| **Hard** | 违反不可表达 | codegen funnel / type system / sealed interface / reflect 字段数 | 0 |
| **Medium** | 违反需 runtime guard / 跨多约束 cross-validate 才能识别 | archtest type-aware / runtime invariant guard | 低 |
| **Soft** | 字符串约定 / 注释豁免 / 名字 convention / hand-crafted fixture | archtest by string anchor / 注释 allowlist / method name | **高** |

## 载体决策原则

新增 enforcement 机制按下列优先级选载体：

1. **codegen funnel + golden**——schema / marker 单源 → 派生执行体（Hard）
2. **type system**——Go interface / typed struct 让违反不可表达（Hard）；PII / 安全语义并存 archtest 双重防线
3. **archtest 平铺兜底**，按规则真值类型选工具：
   - 路径级 import ban → `.golangci.yml` `depguard`
   - 跨包归属 / 传递闭包 → `kernel/depgraph`，复用 `tools/archtest/internal/typeseval.SharedResolver`
   - 需要类型信息（receiver type / interface 实现 / const 值 / exported API 类型）→ 在 `tools/archtest/internal/typeseval` 加 helper
   - 纯 AST 模式 → `tools/archtest/internal/scanner`
   - 元数据 / YAML 派生 → `scanner.EachContentFile` + 解析

**立项硬门槛**：≥ Medium。Soft 形态严禁立项。

**Soft → Hard 改造方向**：
- 字符串锚点 → typed function call
- 注释豁免 → typed marker
- 名字 convention → sealed interface / receiver type 识别
- hand-crafted fixture → real source AST capture

## archtest 文件命名

- 单条独立规则 → `{rule}_test.go`
- 同主题规则 ≥ 3 → `{theme}_invariants_test.go` 主题文件；已有单文件升到第 3 条时，重命名为 `{theme}_invariants_test.go`
- 每个 `*_test.go` 在文件头 CommentGroup 写 `// INVARIANT: <ID>`；多规则文件用 `//   - INVARIANT: <ID>` 列表续行

archtest CI 入口是 `hack/verify-archtest.sh`（process-isolated 16-shard 矩阵；`make verify` 自动发现）。`ARCHTEST-VERIFY-COVERAGE-01` 守卫 script 的 discovery 与 *_test.go AST 集合一致，防止 shard 漏 test。

## Review checklist

涉及"新增/修改约束 enforcement 机制"的 finding 必须显式给 AI-rebust 评级：

- **Hard**：保留，记录范本
- **Medium**：保留；若有低成本升 Hard 的路径，开 follow-up
- **Soft**：
  - 新引入 → 直接 reject，要求改 ≥ Medium
  - 既有 Soft 的补丁 → 优先讨论"升级到 Hard/Medium"，而非在 Soft 层打补丁
  - 允许暂留时，必须同步登记 backlog 升级条目（不能 silent carryover）
