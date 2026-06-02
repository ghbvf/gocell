# ADR: errcode 前缀所有权注册表 — closed-set prefix ownership registry (#1091)

- Status: Accepted
- Date: 2026-06-03
- Tracks: gh issue #1091
- Builds on: `202605051730-adr-errcode-message-pii-safety.md`（errcode Message const literal + sealed Details）；`ai-robust.md` §"Hard 范本目录" string-typed concept funnel
- Implemented by: PR resolving issue #1091 (worktree 156-errcode-prefix-registry)

## Context

### 问题（R11）：无前缀冲突注册表

GoCell 是一个 Cell-native 框架，设计上允许外部 Cell 以独立 Go module 的形式 import 框架并定义自己的 `errcode.Code` 常量。在 #1091 之前，`pkg/errcode` 对 `ERR_` 前缀命名空间没有任何注册机制：

1. **静默碰撞风险**：外部 cell module 若定义 `ERR_AUTH_VENDOR_TIMEOUT`，与平台的 `ERR_AUTH_` 命名空间静默碰撞——HTTP 错误报告、日志归因、告警过滤都依赖 code 字符串前缀识别归属 module，碰撞导致误路由且不易察觉。
2. **无 CI gate**：即使平台侧代码中出现拼写错误或未注册的前缀，也不会被任何静态分析拦截，只在 code review 阶段人工发现。
3. **无所有权查询 API**：框架工具（scaffold、linter、未来 bundle 工具）无法在运行时查询"某个 code 属于哪个 module"。

当前现实（CLAUDE.md §"当前只有 gocell 自身，无外部调用方"）：#1091 落地时仓库内没有独立的外部 cell module，唯一消费方是平台自身 + `examples/corebundlestarter` 演示。注册表 + archtest 为未来外部 Cell 接入建立地基，但此时不声称"已有外部消费方"。

## Decision

### D1 — module 级 CLOSED-SET 注册（单一 owner）

平台所有前缀注册为同一个 owner（`github.com/ghbvf/gocell`），不做 per-cell 粒度细分。理由：

- `ERR_VALIDATION_`、`ERR_INTERNAL`、`ERR_CONFLICT` 等泛型前缀被多个 cell 共用；若按 cell 粒度注册，`accesscore`/`configcore`/`auditcore` 等多 cell 共用 `ERR_VALIDATION_` 会互相冲突，触发 owner-conflict panic。
- archtest `ERRCODE-PREFIX-OWNERSHIP-01` 验证的不变式是"每个 code 的前缀 ∈ 注册集合"，而非 per-cell 所有权——共用 code 不应导致 CI 红。
- module 级单一 owner 是正确抽象边界：独立 Go module 是 Go 工具链认可的隔离域；cell 是框架内逻辑概念，不是 module 边界。

外部 cell（未来独立 Go module）通过 `errcode.RegisterPrefix` 注册自己的前缀命名空间，在 `init()` 中调用，平台会在 owner-conflict 时 panic fail-fast 暴露碰撞。

### D2 — 派生规则：namespace entry vs whole-code entry

扫描全部 256 个生产 `Code` 常量后，按以下规则选择注册形态：

| 形态 | 选用条件 | 示例 |
|------|---------|------|
| **Namespace entry**（`ERR_<SEG>_`，尾部下划线） | 子系统有 ≥1 个 code，且 `ERR_<SEG>` 字符串本身不是独立 code | `ERR_AUTH_`、`ERR_SESSION_` |
| **Whole-code entry**（`ERR_NOT_FOUND`，无尾部下划线） | `ERR_NOT_` 系列——`ERR_NOT_FOUND` / `ERR_NOT_IMPLEMENTED` 不是 namespace，`ERR_NOTE_*` 将来不属于本 module | `ERR_NOT_FOUND`、`ERR_NOT_IMPLEMENTED`、`ERR_INTERNAL`、`ERR_CONFLICT`、`ERR_RATE_LIMITED`、`ERR_VERSION_CONFLICT` 等 13 个泛型单概念 code |

`ERR_NOT_` 是关键陷阱：若注册 `ERR_NOT_` namespace，则 `ERR_NOTICE_*`、`ERR_NOTE_*` 等未来外部前缀都会被误判为平台所有。Whole-code 形态精确到 code string，最长匹配下不误伤。

### D3 — API 语义

```go
// 注册
errcode.RegisterPrefix(prefix, owner string)
// 查询（最长前缀匹配）
errcode.OwnerOfCode(code errcode.Code) (string, bool)
// 快照（sorted，供 golden 生成 / 调试）
errcode.RegisteredPrefixes() []PrefixOwner
```

- `RegisterPrefix` 对同一 `(prefix, owner)` 对幂等；同 prefix 不同 owner 触发 `errcode.Assertion` + `panicregister.Approved` fail-fast，符合 `PANIC-REGISTERED-01`。
- 空 prefix / 空 owner / 非 `ERR_` 前缀同样 panic fail-fast（programmer error）。
- `OwnerOfCode` 使用**最长前缀匹配**：若同时注册 `ERR_AUTH_` 和 `ERR_AUTH_FORBIDDEN`（不同 owner），后者优先——允许精细化所有权，不破坏 namespace 注册的 module 整体覆盖。
- 并发安全：`sync.RWMutex`，`RegisterPrefix` 写锁，`OwnerOfCode` / `RegisteredPrefixes` 读锁。预期使用模式是 `init()` 期写入（无竞争），mutex 仅为正确性保证。
- 平台 65 个 prefix 条目（52 namespace + 13 whole-code）在 `pkg/errcode/prefix_registry.go` 的 `init()` 中自注册。

### D4 — Golden byte-lock

`pkg/errcode/testdata/prefix_set.golden`：所有平台注册条目的字节级快照（`prefix\towner` per line，按 prefix 排序）。测试用 `ERRCODE_PREFIX_GOLDEN_UPDATE=1` 重新生成，缺此变量时任何漂移触发 CI 失败。Golden 与 archtest `ERRCODE-PREFIX-OWNERSHIP-01` 的上游形成双重保证：golden 锁定注册集合；archtest 验证生产代码中所有 mint callsite 和 sentinel 都在注册集合内。

## AI-robust 评级

以下评级从 archtest `ERRCODE-PREFIX-OWNERSHIP-01` 的 godoc 如实引用，不单方面升级为 unqualified Hard。

| 检查形态 | 评级 | 说明 |
|---------|------|------|
| `errcode.New`/`Wrap` 第 1 个位置参 — 字符串字面量 (`BasicLit`) | **下游 Hard** | EvaluateConstString 类型感知，别名无效 |
| `errcode.New`/`Wrap` 第 1 个位置参 — const selector（`errcode.ErrAuthForbidden` 等）| **下游 Hard** | EvaluateConstString 跨包 const 求值 |
| 导出 package-scope `Code` sentinel 声明（Target B） | **下游 Hard** | reflect + string BasicLit 识别 |
| `errcode.New`/`Wrap` 第 1 个位置参 — 直接 runtime 组装（`errcode.Code(non-const)` / `"ERR_"+x`）| **下游 Hard（hard fail）** | 直接拒绝，要求使用命名 sentinel |
| 通过 forwarding helper 传递的非 const `Code` 参数/变量 | **Medium residual（已知盲区）** | 变量/参数引用在 mint site 被跳过；蓄意构造一个转发 helper 可绕过，但不是意外漂移路径；data-flow tracing 可关闭但会对 parse/compare-side 产生 false positive；已在 PR-body backlog 中追踪 |
| 上游 golden byte-lock | **Hard（review-gated `-update` 天花板）** | 与仓库所有 golden 一致；adversarial 绕过需要同 PR 改 golden 并通过 code review |

**Medium residual 意义**：`ERRCODE-PREFIX-OWNERSHIP-01` 是 archtest-bound，不是 type-system seal，所以上游 Hard 形态不可达（无法在 Go 类型系统层面强制所有 `errcode.Code` 字面量必须来自注册集）。该设计与 `SPAN-SETATTR-HOLDER-SEAL`(#851) / `HEALTHZ-HOLDER-SEAL`(#893) 的 Go 永久天花板形态同类。forwarding-helper 绕过路径是故意构造才能触发的（不是 AI co-author 意外产生的 drift），为可接受的 residual gap。

## 威胁矩阵

| 威胁 | 形态 | 覆盖状态 |
|------|------|---------|
| 平台 cell 使用未注册前缀 | 字面量 / const selector mint | ✅ Hard（A 路径：literal + const-eval） |
| 平台 cell 使用未注册前缀 | 导出 Code sentinel | ✅ Hard（B 路径：sentinel scan） |
| 外部 cell 与平台前缀碰撞 | `RegisterPrefix` 不同 owner | ✅ 运行时 panic fail-fast（init 期） |
| 蓄意通过 forwarding helper 绕过 | non-const Code var 传入 mint | ⚠️ Medium residual（已知；需 data-flow tracing 关闭） |
| 恶意修改 golden | golden byte-lock + review | ✅ review-gated；PR 内单侧漂移 CI 红 |
| 平台 golden 漂移（增删 prefix） | golden diff 输出 CI 红 | ✅ Hard（`ERRCODE_PREFIX_GOLDEN_UPDATE=1` review gate） |
| AllCallExpr code arg（safe mapper function 返回值）| 当前无此 callsite；future 需 sentinel 或显式 allowlist | ⚠️ 已记录盲区 #3，0 callsite 今日无 false-positive |

## 备选方案

### 备选一：per-cell 粒度注册

每个 cell 注册自己的 code 前缀（如 `accesscore` → `ERR_SESSION_`）。

否决原因：`ERR_VALIDATION_`、`ERR_INTERNAL`、`ERR_CONFLICT` 被多 cell 共用，导致多 cell 争注同一前缀触发 owner-conflict panic，或强制这些泛型前缀挂在某一 cell 名下产生误导性归属。module 级单一 owner 是正确抽象粒度。

### 备选二：compile-time 枚举（`//go:generate` + `iota`）

用 codegen 从代码常量生成 prefix 集合，注册表由 generated 代码维护。

否决原因：`errcode.Code` 是 `string` 类型，不是 iota 整型；codegen 路径需要额外工具步骤，并不能提升 type-system Hard 档位（本质仍是字符串比对）。golden byte-lock + archtest EvaluateConstString 已经是该规则形状下可达的最高档，codegen 路径增加复杂度但不提升安全性。

### 备选三：运行时 JSON Schema 验证

在 `errcode.New` 内部实时校验 code 是否在注册集中，返回 error 或 panic。

否决原因：`errcode.New` 是高频热路径；运行时校验引入 RWMutex 读锁开销（每次 error 构造都加锁）。archtest 的静态扫描在 CI 期间一次性完成，不影响生产热路径。

## 后续演进

1. **外部 Cell 接入**（未来 epic #1081 bundle 触发）：外部 cell module 在 `init()` 调用 `errcode.RegisterPrefix`；scaffold / starter 模板（`examples/corebundlestarter`）已包含 `RegisterPrefix` 示例作为参考模式。仓库今日无外部消费方（CLAUDE.md），注册表 API 是地基，不是已有外部用户的 production path。
2. **forwarding-helper Medium residual 关闭**：若未来出现通过 forwarding helper 传递未注册 code 的需求，考虑在 archtest 引入 data-flow 追踪（go/ssa）将 forwarding path 也纳入扫描；或要求所有 non-const `errcode.Code(x)` 转换只在 parse/compare site 发生，ban 掉 mint site 上的全部 non-literal 形态（需评估 false-positive 影响）。

## 后果

- 所有新增 `ERR_` 前缀 namespace 必须在同一 PR 内：(1) 在 `gocellPlatformPrefixes` 或外部 module `init()` 中调用 `RegisterPrefix`；(2) 重新生成 `prefix_set.golden`（`ERRCODE_PREFIX_GOLDEN_UPDATE=1`）；(3) 通过 `ERRCODE-PREFIX-OWNERSHIP-01` archtest。三步缺一则 CI 红。
- 外部 cell module 在 `init()` 中调用 `RegisterPrefix`，init 顺序保证注册先于 service 逻辑。如果两个 module 注册同一前缀，启动时 panic（fail-closed，不静默继续）。
- `OwnerOfCode` 可被 scaffold / linter / bundle 工具在 build 期查询，为 "code 归属哪个 module" 提供标准 API。

## 参考

- `pkg/errcode/prefix_registry.go` — 注册 API 实现
- `pkg/errcode/testdata/prefix_set.golden` — 平台注册集 golden snapshot
- `tools/archtest/errcode_invariants_test.go` — `ERRCODE-PREFIX-OWNERSHIP-01` archtest（AI-robust 评级权威真值源）
- Issue #1091
- `.claude/rules/gocell/error-handling.md` §"错误码前缀所有权 (#1091)"
