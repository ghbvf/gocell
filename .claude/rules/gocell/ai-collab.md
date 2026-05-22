# AI 协作章程

> 第一性原理：GoCell 主要实施者是 AI（claude code）。工程治理目标必须从"对人友好"转为"AI-rebust"——违反不可表达 / 机制不可绕过 / 字面约定全部消除。
>
> 本文件是约束 enforcement 的权威真值源。原则在此终结，不向 ADR / 代码 "详见"；落地实例与符号清单活在代码 godoc，规则文件不复制也不指向。

## 适用范围

本章程适用于"新增/修改约束 enforcement 机制"：

- archtest（`tools/archtest/*_test.go`）
- governance rule（`gocell validate` 规则、bootstrap 期 fail-fast 校验）
- codegen funnel（schema/marker 单源 → 派生执行体）
- type marker（typed function wrapper、sealed interface、reflect 字段冻结）
- godoc 强约定 / ADR-mandated pattern（`// INVARIANT: ID` 这类）

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
   - 跨包归属 / 传递闭包 → `kernel/depgraph`；archtest 内需 typed load 走 `archtest.RunTyped` / `RunTypedProduction` 公开 façade
   - 需要类型信息（receiver type / interface 实现 / exported API 类型 / 表达式求值，含 const 拼接、跨包 Ident、untyped const）→ `archtest` typed façade（`ResolvePackageRef` / `ResolveMethodCall` / `EvaluateConstString` 等）+ `Pass` scope/generated 判定
   - 纯 AST 模式 → `archtest.Run` + walk helper（`EachInSubtree` / `EachInChildren` / `FindFirstChild`）
   - 加载 fixture 子包 → `archtest.RunTypedFixture` typed funnel（`FixtureOpts` 不含 Tags 字段）
   - 元数据 / YAML 派生 → `archtest.EachContentFile` + 解析

以上路由仅约束**新增** archtest；既有直接 import internal helper 的用法合法，不触发批量重构。

**工具选定后强制盲区自检**：作者在 archtest 测试函数 godoc 列出所选工具 godoc 声明范围外的 AST 形态（与 package-doc `// INVARIANT:` 分离），并对每项添加反向自检测试，断言其在 production AST 不出现。盲区清单 + 反向自检测试是 Hard/Medium 评级的前置举证材料。

**立项硬门槛**：≥ Medium。Soft 形态严禁立项。

**Soft → Hard 改造方向**：
- 字符串锚点 → typed function call
- 注释豁免 → typed marker
- 名字 convention → sealed interface / receiver type 识别
- hand-crafted fixture → real source AST capture

**Hard 范本目录**（形态原则；选错形态即非 Hard。落地实例在对应 archtest package godoc，本表不复制）：

- **typed function choice** — 一个 API 承担多语义时拆成多个 typed function，使"选错语义 = 选错 API 名"成为编译期或 fixture 可检测层级，而非埋在参数里的运行时分支。
- **typed marker funnel for unbounded ops** — `any` 类型操作（panic、空接口入参）全部过单一 typed-marker 构造函数；Hard 来自 (callee, arg) form-uniqueness——任何其他形态 archtest 即失败，不存在"像但不是"的灰区。enforcement 是 archtest-bound 而非编译期，但形态唯一性已是 Go 该规则形状可达的最高档。
- **string-typed concept funnel** — 字符串承载独立语义（rule code / error code / event topic）时：`type FooCode string` 类型化签名收口 + 值集中声明 + 构造比较点用类型信息解析实参到声明集。
- **input-struct field exclusion** — framework 收口的横切字段（build tag / 加载 mode）从公开 input struct 删除，使"业务自传"在 type system 上编译不可表达。只锁字面量值不锁 callee 是反模式（同 PR 新 const 即新绕过路径），必须 (callee resolve 到 loader 集) AND (arg 求值到禁止值集) 双锁，并同 PR 补 meta-archtest 锁 façade 旁路，否则只是 funnel 内 Hard / funnel 外 Soft。
- **single sanctioned holder** — 仅一个 struct 可持有某 raw 基建字段时，用类型信息解析字段类型 + 断言宿主 struct 名 = 唯一许可名，无需 hand-maintained allowlist。包级可见性使包内绕过不可阻挡（上游 Medium）；闭环上游需 seal interface + 私有构造使包外不可表达跳过。

- **JSON-wire-decode struct sealing** — wire envelope 通过 encoding/json 解码时，把 envelope 结构体本身 unexport（lowercase struct name），公开 `Marshal/Unmarshal` 函数 signature 不暴露 envelope 类型，对外只过领域类型（`Entry`）/ 字节流（`[]byte`）。包外直接构造或 unmarshal-target 至 envelope 类型 = 编译期不可表达 = Hard 上游。**任意名 re-export 是 first-class 关注点**——`type Envelope = wireMessage`（alias）和 `type Envelope struct{...同字段集}`（re-shape）都会让外部 `outbox.Envelope{}` 重新可构造；exact-name `WireMessage` lookup 不足以闭环。配套 archtest 三道锁：(a) `Lookup("wireMessage")` 必返回非 nil 且 unexported；(b) 任意 exported scope name 经 `types.Unalias` 后 Type identity ≠ wireMessage（拦 alias re-export，含 Go 1.22+ materialized `*types.Alias`）；(c) 任意 exported struct 不得有 `SchemaVersion + ≥N/M` canonical wireMessage 字段集（拦 re-shape re-export，含 defined-type 共享 underlying）+ 反向 AST 自检扫源文件无 `type WireMessage` declaration。下游 Hard 复用 string-typed concept funnel（字段类型化）。落地实例：`kernel/outbox.wireMessage` + `SAFEID-UPSTREAM-FUNNEL-HARD-01` + `SAFEID-WIREMESSAGE-USAGE-01`，见 `tools/archtest/safeid_funnel_test.go` godoc。ref: go-kratos/kratos `transport/grpc/codec.go`（zero-size unexported codec struct——最贴近的工业对标：unexported 类型 + 单一注册点 gating 解码）；etcd-io/etcd `server/wal/wal.go` `WAL`（factory-only sealed handle pattern，handle 级而非 wire 级 `wal.Record`，框架等价）。**Watermill `message.Message` 不是此范本的 Hard 等价先例**——其 UUID/Metadata/Payload 字段 exported，仅 ack 生命周期通过 unexported channels 封装，envelope 构造跨包并非编译期不可表达。

- **typed function call for test-side wall-clock polling** — `pkg/testutil/testwait.External` is the sole sanctioned entry for synchronous polling waits in tests, paired with `testwait.Deterministic` for channel-blocking waits. Double-locked: archtest `TEST-POLLING-EXTERNAL-REASON-LITERAL-01` (downstream) enforces (callee, arg) form-uniqueness on (`External`, kebab-case `*ast.BasicLit` STRING reason); archtest `TEST-EVENTUALLY-FUNNEL-01` (upstream) bans the testify `(require|assert).Eventually*` surface module-wide — banned funcs identified by prefix predicate (pkg path ∈ {require,assert} AND name starts with `Eventually`), banned shapes include qualified-ident calls, `*Assertions` method-selector calls, and indirect references (var assignment, function-pointer pass, reflect, struct-field, method value, method expression). Callee resolver consults both `*types.Info.Uses` and `*types.Info.Selections`; reverse blind-spot self-test walks both maps. Surface drift is locked by `TestEventuallyFunnel_SymbolSentinel` (testify v1.11.1 baseline; prefix predicate auto-covers future `EventuallyXyz` variants). Per §"Funnel 双向锁评级" both sides Hard → closed Hard funnel; sibling of `panicregister.Approved` under "typed marker funnel for unbounded ops". Both archtests are archtest-bound (not Go compile-time) — same condition as the parent 范本 entry: form-uniqueness on (callee, arg) is the highest tier reachable for this rule shape, but a future removal of `require.Eventually` from testify would make these archtests redundant guards rather than load-bearing locks. See `pkg/testutil/testwait/testwait.go` godoc.

- **typed function funnel for metrics instrument construction** — `adapters/prometheus/internal/promwrap.{NewCounter,NewCounterVec,NewGauge,NewGaugeFunc,NewGaugeVec,NewHistogramVec}` and `adapters/otel/internal/otelwrap.{Float64Counter,Float64Gauge,Float64Histogram}` are the sole sanctioned entries for Prometheus client and OTel meter instrument construction, protected by Go `internal/` compile-time closure: only code within the respective adapter subtrees can import these packages, making external bypass inexpressible at the type system level. Double-locked: downstream archtest `METRICS-GAUGEVEC-FUNNEL-01` enforces callee form-uniqueness (prom.New* function resolution via `*types.Info.Uses` + OTel `otelmetric.Meter` method resolution via `*types.Info.Selections`; prom side uses prefix predicate `^New(Counter|Gauge|Histogram|Summary|Untyped)(Vec|Func)?$` against resolved `bannedPromPkg` callees — full synchronous construction surface, no literal set drift); upstream archtest `METRICS-GAUGEVEC-UPSTREAM-HARD-01` via `TestMetricsFunnel_SymbolSentinel` locks each wrap package's exported function set so additions require an explicit sentinel update — AI co-authors cannot silently expand the funnel surface. Callee resolver walks both `*types.Info.Uses` and `*types.Info.Selections`; reverse blind-spot self-tests (BS-1 reflect, BS-3 function-value capture) assert these forms are absent in production. Observable meter variants (`Int64ObservableUpDownCounter`, etc.) are intentionally excluded from the ban: they use a callback registration pattern that cannot be cleanly funneled synchronously, and their callsites in `adapters/otel/*.go` are legitimate implementation sites. Both archtests are archtest-bound (not Go compile-time) for the callee-set lock side; the `internal/` package closure is the compile-time Hard gate. Both sides Hard → closed Hard funnel. See `tools/archtest/observability_metrics_test.go` + `adapters/prometheus/internal/promwrap/promwrap.go` + `adapters/otel/internal/otelwrap/otelwrap.go` package godoc.

## archtest 文件命名

- 单条独立规则 → `{rule}_test.go`
- 同主题规则 ≥ 3 → `{theme}_invariants_test.go` 主题文件；已有单文件升到第 3 条时重命名
- 每个 `*_test.go` 在文件头 CommentGroup 写 `// INVARIANT: <ID>`；多规则文件用 `//   - INVARIANT: <ID>` 列表续行

archtest CI 入口是 `.github/workflows/archtest-nightly.yml`（schedule + workflow_dispatch，16-shard matrix）；PR / push CI 不再跑 archtest，失败靠 GHA 默认邮件通知 + Actions UI + `workflow_dispatch` rerun。本地反馈走开发者显式触发：`make verify` 一键全跑（含 archtest）或 `bash hack/verify-archtest.sh` 直跑；`hack/githooks/pre-push` 因 CPU/RSS 预算不跑 archtest（详见 ADR §"pre-push archtest 撤回"）。`hack/verify-archtest.sh` 三种 execution mode 由 env shape 派生：`SHARD_TARGET` 设 → 单 shard（CI matrix）；`SHARD_COUNT=1` → 单进程流式（local default）；`SHARD_COUNT>1` 无 `SHARD_TARGET` → 并行 fan-out。CI 显式 `SHARD_COUNT=16`（GHA 7 GB RSS 约束）由 `ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01` archtest 守卫（step-scoped match-all）。`ARCHTEST-VERIFY-COVERAGE-01` 守卫 script discovery 与 *_test.go AST 集合一致，防止 shard 漏 test。

## Review checklist

涉及"新增/修改约束 enforcement 机制"的 finding 必须显式给 AI-rebust 评级：

- **Hard**：保留，记录范本
- **Medium**：保留；若有低成本升 Hard 的路径，开 follow-up
- **Soft**：
  - 新引入 → 直接 reject，要求改 ≥ Medium
  - 既有 Soft 的补丁 → 优先讨论"升级到 Hard/Medium"，而非在 Soft 层打补丁
  - 允许暂留时，必须同步登记 backlog 升级条目（不能 silent carryover）

### Funnel 双向锁评级

涉及 funnel 类约束（"集合外不能进 / 集合内必须经过"）的 finding 必须**分别**给出"下游 Hard / 上游 Hard"两栏评级。仅一侧 Hard 不构成闭环 funnel：

- **下游 Hard**：禁止某 method 在 funnel 外被调（caller allowlist，由 archtest 锁定调用点身份）。
- **上游 Hard**：保证某 callsite **必然**经过 funnel——典型形态 = sealed interface + 字段私有化（包外不可表达跳过）。

Soft 上游 + Hard 下游不算闭环 funnel，按 Soft 处理。允许 Medium 上游（archtest caller allowlist）+ Hard 下游的过渡形态，但**必须同步登记 backlog 显式 Hard 化任务**，并在 funnel 自身的 godoc / 测试注释中点名 backlog 条目，让审查者能直接追到升级路径。

### ADR amendment 落地必查

ADR amendment 落地时必须回到该 ADR 的 §"威胁矩阵" / §"安全模型" / §"Threat Model" 等覆盖表，**逐行重评**：

- 从 ✅ 变成 ⚠️/❌ 的格子必须显式列出补偿措施或回滚到 amendment 前形态。
- amendment 与原文矛盾的段落 **同 PR 内重写**——不接受"原文保留作历史脉络"这一类豁免。原文与 amendment 出现两套真理源时，未来 reviewer 必然漂移；重写比注释稳得多。
