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
   - 跨包归属 / 传递闭包 → `kernel/depgraph`（archtest 内若需要 typed load，走 `archtest.RunTyped` / `archtest.RunTypedProduction` 公开 façade）
   - 需要类型信息（receiver type / interface 实现 / exported API 类型 / 表达式求值结果，含 const 拼接、跨包 Ident、untyped const）→ `archtest.{ResolvePackageRef, ResolveMethodCall, EvaluateConstString, FlatNonDefaultTags, KnownNonDefaultTags}` + `Pass.{IsFileInScope, IsGenerated}` 公开 façade（扩节点类型时在 `internal/typeseval` 加 helper 并经 façade 暴露）
   - 纯 AST 模式 → `archtest.Run` + `archtest.{EachInSubtree, EachInChildren, FindFirstChild}` walk helper
   - 加载 fixture 子包 → `archtest.RunTypedFixture` typed funnel（`FixtureOpts` 不含 Tags 字段）
   - 元数据 / YAML 派生 → `archtest.EachContentFile` + 解析

> **防误判**：以上路由是写**新** archtest 的指引。既有 archtest 直接 import `internal/scanner` / `internal/typeseval` 的 walk + resolve helper（如 `EachInSubtree`、`ResolvePackageRef`）合法，由 ADR `docs/architecture/202605141519-adr-archtest-pass-funnel.md` §Termination criteria 明确允许，不需要批量重构。被禁用的是 `scanner.EachFile` / `typeseval.LoadPackages` / `SharedResolver` / `LoadProductionPackages` / `EachFileInPackage` 等 INV-1 / load-bearing 符号，由 PASS-FUNNEL 元治理 archtest 类型化拦截。

**工具选定后强制盲区自检**：作者在 archtest 测试函数 godoc 列出所选工具 godoc 声明范围外的 AST 形态（与 package-doc `// INVARIANT:` 分离），并对每项添加反向自检测试，断言其在 production AST 不出现。盲区清单 + 反向自检测试是 Hard/Medium 评级的前置举证材料。

**立项硬门槛**：≥ Medium。Soft 形态严禁立项。

**Soft → Hard 改造方向**：
- 字符串锚点 → typed function call
- 注释豁免 → typed marker
- 名字 convention → sealed interface / receiver type 识别
- hand-crafted fixture → real source AST capture


**Hard 范本**：

- **typed function choice for walk depth** — 当一个 API 同时承担多种语义（深度选择 / 早返模式 / 容器范围等）时，拆成多个 typed function 让"选错语义 = 选错 API 名"成为可检测层级。范例：`scanner.EachInSubtree[N]`（recursive，遍历以该节点为根的全树）vs `scanner.EachInChildren[N]`（depth=1，仅直接子节点）拆分——两个函数名语义不重叠。**保障层次分两级**：(1) **fixture-level**：`eachnode_test.go` 中 T1+T2 RED fixture 确保选错深度在测试中暴露；SCANNER-FRAMEWORK-USAGE-01 的 companion-index 精度测试也拦截；(2) **compile-level**：N 类型选错（接口而非 `*S`，例如 `EachInSubtree[ast.Expr]`）直接编译失败。注意：两个函数名都能 build，深度选错是 fixture-level 保障，不是 compile error；N 类型选错才是真正的 compile error。

- **typed function call as Hard funnel for unbounded operations** — when an operation accepts `any` at the Go type level (e.g., `panic(any)`), you can still reach Hard by routing every call site through a single typed-marker function. Range: `panic(panicregister.Approved(reason, value))` is the only approved panic shape in production GoCell code; archtest `PANIC-REGISTERED-01` enforces (a) panic arg = `*ast.CallExpr` with Fun resolving via `*types.Info` to `pkg/panicregister.Approved`, (b) reason = `*ast.BasicLit` STRING. Hard property comes from "form uniqueness": picking any other shape (bare panic, different callee name, non-literal reason) fails archtest in CI — there is no "looks like Approved but isn't" gray zone. Honest caveat: Go's `panic` keyword accepts `any` so `panic(rawValue)` compiles; the enforcement is archtest-bound, not compile-time. The charter §1 definition of "typed function call" Hard does not require compile-time blocking, only form uniqueness + archtest fail-on-deviation — which is the highest grade reachable in Go for this rule shape.

- **string-typed concept funnel**（设计范本，GoCell 内尚无严格 ship 实例）— 字符串承载独立语义（rule code / error code / event topic 等）时：(1) `type FooCode string` 把语义类型化，API 签名收口；(2) 值定义集中在 `*codes.go`，archtest 守声明位置；(3) 构造/比较点用 `*types.Info` 检查实参 resolve 到声明集合。形态锁 vs 值求值依 §3 工具原则选。

- **typed function choice with input-struct field exclusion** — typed function choice 范本的升级形态。`RunTypedFixture(t, opts FixtureOpts, patterns, rule)` + 专用 `FixtureOpts struct { Tests bool }`（**不含 Tags 字段**）让"加载 fixture 时业务自传 build tag"在 type system 上不可表达——编译失败。不仅 function name 选择参与 type system 约束，连 input struct 字段集也参与。**适用条件 — 以下三条必须 AND 满足**（日常 fixture loader 不引用此范本）：

  - (a) framework 收口 build tag / 加载 mode 等横切关注点，业务无控制权诉求；
  - (b) 业务调用方无需感知该字段（不是仅默认值，是结构上不该出现）；
  - (c) 同一加载模式有多处调用复用（≥3 处是经济性阈值，并非 Hard 评级前提；1-2 处调用时优先直接传 TypedOpts.Tags + 注释说明即可，避免过早 funnel 化）。

  **配套要求 — funnel 双向闭锁**：本范本的 outward Hard（FixtureOpts 字段缺失致编译失败）只挡"用 RunTypedFixture 自传 tag"这条路；语言层面 RunTyped / typeseval.SharedResolver 等 loader 仍接受任意 Tags 切片，业务调用方仍可写 `RunTyped(t, TypedOpts{Tags: []string{X}}, ...)` 绕过 RunTypedFixture（其中 `X` 可以是字面量、同包 const Ident、跨包 SelectorExpr、BinaryExpr 拼接等任意 EvaluateConstString-resolvable 形态）。同 PR 内**必须**补一条 meta-archtest 锁住 façade 旁路，否则只是"funnel 内 Hard / funnel 外 Soft"，与 §Funnel 双向锁评级冲突。

  **形态选择 — (callee, arg) pair form-uniqueness（必选，等价于 §Hard 范本第 2 条 panic 范本同构形态）**：仅锁 BasicLit 字面量值而不锁 callee 是常见反模式——同 PR 引入的新 const 自身会成为新的绕过路径（"Soft 上 Soft"）。正确形态：(i) callee 经 `*types.Info` 解析到 loader 集合（同 §Hard 范本第 2 条 panicregister.Approved 的 callee resolve），(ii) arg 子树经 `EvaluateConstString` 解析到禁止值集合（比 panic 范本的 "arg = BasicLit STRING" 略宽——允许 const reference 等价形态——但仍是 form uniqueness）。两个条件 **AND**，缺一不可。这样合法 identity 用途（如 `containsTag(group, FixtureBuildTag)`：callee 不在 loader set，arg 同值 → 不命中）与绕过用途（loader callee + 同值 arg → 命中）自然机器可区分。

  范例：`tools/archtest/pass_funnel_test.go` `diagsFixtureTagBypass` (PASS-FUNNEL-FIXTURE-TAG-01 R1.1) 以 (callee ∈ {archtest.RunTyped / RunTypedProduction / RunTypedDir + typeseval.SharedResolver / LoadPackages / LoadProductionPackages}, arg via EvaluateConstString → `"archtest_fixture"`) pair 拦截；配合 `archtest.FixtureBuildTag` 包级 const 给 Go-code 路径提供 typed reference 单源——下游 outward Hard + 上游 (callee, arg) form-uniqueness Hard 构成闭环双向锁（详见 ADR `docs/architecture/202605141519-adr-archtest-pass-funnel.md` §"PR #536 review R1 amendment" R1.1 段）。

  ref: `tools/archtest/fixture.go` (FixtureBuildTag const + RunTypedFixture) + `tools/archtest/pass_funnel_test.go` (diagsFixtureTagBypass / fixtureTagLoaderSet — (callee, arg) pair form-uniqueness Hard)。

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

### Funnel 双向锁评级

涉及 funnel 类约束（"集合外不能进 / 集合内必须经过"）的 finding 必须**分别**
给出"下游 Hard / 上游 Hard"两栏评级。仅一侧 Hard 不构成闭环 funnel：

- **下游 Hard**：禁止某 method 在 funnel 外被调（caller allowlist，由 archtest
  锁定调用点身份）。
- **上游 Hard**：保证某 callsite **必然**经过 funnel——典型形态 = sealed
  interface + 字段私有化（包外不可表达跳过）。

Soft 上游 + Hard 下游不算闭环 funnel；按 Soft 处理。允许 Medium 上游（archtest
caller allowlist）+ Hard 下游的过渡形态，但**必须同步登记 backlog 显式 Hard
化任务**，并在 funnel 自身的 godoc / 测试注释中点名 backlog 条目，让审查者
能直接追到升级路径。范例：`CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01`（S4d
Medium）→ backlog `AUTHZ-MUTATION-FUNNEL-UPGRADE-01`（S4e Hard）。

### ADR amendment 落地必查

ADR amendment 落地时必须回到该 ADR 的 §"威胁矩阵" / §"安全模型" / §"Threat
Model" 等覆盖表，**逐行重评**：

- 从 ✅ 变成 ⚠️/❌ 的格子必须显式列出补偿措施或回滚到 amendment 前形态。
- amendment 与原文（§D*/§3 等）矛盾的段落 **同 PR 内重写**——不接受
  "原文保留作历史脉络" 这一类豁免。原文与 amendment 出现两套真理源时，未来
  reviewer 必然漂移；重写比注释稳得多。
- 范例反面教材：ADR `202605101400-adr-credential-session-protocol.md` §0 A1
  删了 `sessions.authz_epoch_at_issue` 列但未重跑 §3 威胁矩阵，也未重写 §D4.2
  的"行内 pin 比对"描述，导致 PR #490 ship 后 review 才发现 P1 漏修
  （已由 S4d RETRACT A1 修复，原文同 PR 重写）。
