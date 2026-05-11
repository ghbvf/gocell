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

| 档 | 定义 | 典型载体 | AI 可绕过性 | 例 |
|---|---|---|---|---|
| **Hard** | 违反不可表达 | codegen funnel / type system / sealed interface / reflect 字段数 | 0 | typed response envelope；`AuthComboLegal` single oracle + 5 层 mirror；scanner fail-closed by construction |
| **Medium** | 违反需 runtime guard / 跨多约束 cross-validate 才能识别 | archtest type-aware / runtime invariant guard | 低 | BFS reachability + `assertEmitterMethodsRestrictedToLocator` |
| **Soft** | 字符串约定 / 注释豁免 / 名字 convention / hand-crafted fixture | archtest by string anchor / 注释 allowlist / method name | **高** | `// INVARIANT: ID` 锚点；`// PANIC-REGISTERED-01: ADR-approved:` 注释；按方法名识别 emitter |

## 载体决策原则

新增约束时按优先级选载体（同时决定 AI-rebust 等级）：

1. **funnel + codegen**：能否用 schema/marker 单源 → codegen 派生执行体？能 → 走这条（Hard）
2. **type system 自然拦**：能否用 Go interface / typed struct 让违反不可表达？能 → 走这条（Hard）
   - 注：type system 与 archtest 可并存——涉及 PII / 安全语义的约束，即使已有类型拦截，仍须评估 archtest 双重防线（例 `MESSAGE-CONST-LITERAL-01` / `DETAILS-SLOG-ATTR-01`）
3. **archtest 平铺兜底**：上两条都不行 → `tools/archtest/{theme}_invariants_test.go`（Medium 或 Soft，看是否 type-aware）

**立项硬门槛**：≥ Medium。Soft 档严禁立项；既有 Soft 按"实际事故密度 × AI 暴露面"排队升级，不强制一次清零。

**Soft → Hard 改造方向**：
- 字符串锚点 → typed function call（`archtest.Invariant("ID")`）
- 注释豁免 → typed marker（`panicregister.Approved("reason")`）
- 名字 convention → sealed interface / receiver type 识别
- hand-crafted fixture → real source AST capture（AI 难造假）

## archtest 文件命名

- 同主题规则数 ≥ 3 → 新建或扩展 `{theme}_invariants_test.go` 主题文件；每个规则函数前 godoc 加 `// INVARIANT: {ID}` 锚点 + 不能 funnel 的理由
- 单条独立规则 → 保留 `{rule}_test.go` 单文件命名
- 已有 `{rule}_test.go` 单文件且新增同主题第 3 条规则 → 重命名为 `{theme}_invariants_test.go` 并补完 anchor

**不准建 Registry / 中心化注册表**。多份文档用 grep 锚点串联（grep `INVARIANT: {ID}` 跳全套）。主流对照（K8s / CockroachDB / Linux / Rust / Go 工具链）都接受 funnel 不到的残留，平铺管理。

archtest CI 入口是 `hack/verify-archtest.sh`（process-isolated 16-shard 矩阵；`make verify` 自动发现）。`ARCHTEST-VERIFY-COVERAGE-01` 守卫 script 的 discovery 与 *_test.go AST 集合一致，防止 shard 漏 test。

## Review checklist

涉及"新增/修改约束 enforcement 机制"的 finding 必须显式给 AI-rebust 评级：

- **Hard**：保留，记录范本
- **Medium**：保留；若有低成本升 Hard 的路径，开 follow-up
- **Soft**：
  - 新引入 → 直接 reject，要求改 ≥ Medium
  - 既有 Soft 的补丁 → 优先讨论"升级到 Hard/Medium"，而非在 Soft 层打补丁
  - 允许暂留时，必须同步登记 backlog 升级条目（不能 silent carryover）
