# F1 批次：跨切面代码卫生——errcode / panic / clock / 测试纪律 / governance / CI 固定

本文件覆盖 tools/archtest/ 下 17 个源文件、共 **28 条** 去重 INVARIANT。主题是「跨切面代码卫生」，是「通用 Go 检查器」候选规则最集中的一批：其中约半数规则的思路可直接移植到任意 Go 项目，仅需替换包路径。

本文件 28 条 | Hard 12 / Medium 13 / Soft 0 / Hard↓Medium↑(funnel 双向) 3 | 通用 9 / 半通用 14 / 专属 5

---

### 批次 1：errcode 错误模型（errcode_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| ERRCODE-KIND-LITERAL-01 | Medium（推断） | callsite-allowlist + AST-pattern | 禁止在 pkg/errcode 外直接构造 errcode.Error{} 字面量，强制走 New/Wrap | pkg/errcode | 半通用：思路通用（禁裸结构体字面量），移植需替换目标类型路径 |
| ERRCODE-CARVEOUT-ADR-CONSISTENCY-01 | Hard | metadata/yaml-derive | 代码侧 carve-out 映射与 ADR registry 表严格双向一致 | pkg/errcode | 专属：双向锁的两端都是 GoCell 专属（carve-out ADR + errcode 映射） |
| MESSAGE-CONST-LITERAL-01 | Medium | AST-pattern | errcode.New/Wrap 的 message 参数必须是 const 字面量，禁止 fmt.Sprintf/拼接 | pkg/errcode | 半通用：思路通用（禁 API 参数 runtime 拼接 PII），移植需绑定目标函数签名 |
| ERROR-FIRST-API-01 | Medium | AST-pattern | 无 error 返回的函数禁止 panic（入 enrollment 文件白名单） | — | 半通用：思路通用（error-first API 约定），移植需维护 enforced file 清单 |
| ERROR-FIRST-TYPED-NIL-01 | Medium | AST-pattern | error 返回的 New* 构造函数必须对 nil-able 参数做类型安全 nil 检测 | — | 半通用：思路通用（构造函数 nil-guard），移植需适配 nil 检测形态规则 |
| DETAILS-SLOG-ATTR-01 | Medium（推断） | AST-pattern | WithDetails 参数禁止旧式 map[string]any 字面量，必须用 slog.Attr | pkg/errcode | 半通用：思路通用（类型签名切换后防回退），移植需绑 WithDetails 函数路径 |
| EXPORTED-ERROR-NEW-01 | Medium（推断） | AST-pattern | 生产代码禁止 package-scope exported `var Err* = errors.New(...)`，必须走 errcode.New | pkg/errcode | 通用：换任何有统一 error code 模型的项目均成立；仅需替换目标 New 函数路径 |

---

### 批次 2：panic 分类（panic_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| PANIC-REDACT-01 | Medium（推断） | AST-pattern | slog.Any("panic", X) 必须用 redaction.RedactAny 包裹，防 panic 值泄漏日志 | pkg/redaction | 半通用：思路通用（panic 日志脱敏），移植需替换 redact 函数路径 |
| PANIC-REGISTERED-01 | Hard（typed-marker-funnel，ai-robust.md §Hard 范本明确） | typed-marker-funnel | 每个生产 panic 调用必须用 panicregister.Approved(reason, value) 包裹，reason 为 kebab-case 字面量 | pkg/panicregister | 通用：思路完全可移植（任意项目可引入 `panicregister` 模式）；移植需创建等价 Approved 包 |

---

### 批次 3：clock 注入（clock_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CLOCK-INJECTION-TEST-CALLSITE-01 | Medium | callsite-allowlist + typed AST | 测试文件调用带 WithClock 的构造函数时必须传入 WithClock option | kernel/clock | 半通用：思路通用（测试注入 clock），移植需项目有 clock 抽象 + WithClock option 模式 |
| CLOCK-INJECTION-PROD-CALLSITE-01 | Medium（推断） | callsite-allowlist + typed AST | 生产组合根（cmd/ + examples/）调用带 WithClock 的构造函数时必须传入 WithClock | kernel/clock | 半通用：同上，生产侧检查；移植前提相同 |
| KERNEL-CLOCK-LEAF-FALLBACK-01 | Medium | callsite-allowlist + typed AST | 禁止在组合根外的生产文件调用 clock.Real()，防止叶层重新引入 wall-clock | kernel/clock | 半通用：思路通用（禁叶层 fallback 构造），移植需项目有 clock.Real() 等价入口 |
| KERNEL-CLOCK-RESET-RELATIVE-PROD-01 | Medium（推断） | typed AST | 生产代码禁止调用 Timer.Reset(duration)（相对），必须用 ResetAt(deadline) | kernel/clock | 半通用：思路通用（绝对 deadline vs 相对 duration 竞态），移植需项目有 ResetAt/Reset 拆分 |
| PROD-CLOCK-INJECTION-01 | Medium | callsite-allowlist + typed AST | 生产代码禁止直接引用 stdlib time.Now/Since/Until/NewTimer 等 wall-clock 入口 | kernel/clock | 通用：思路完全通用（生产禁裸 time.Now），移植前提：项目有 clock 抽象；仅需替换 forbidden 函数集 |
| CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01 | Medium | callsite-allowlist | 校验控制平面 clock carve-out 白名单条目真实存在（防 rename 孤立） | kernel/clock | 通用：「白名单自验证」模式与任何项目的 carve-out allowlist 完全对等；移植直接照搬 |

---

### 批次 4：生产时长常量（prod_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| PROD-DURATION-CONST-01 | Medium（推断） | typed AST | 生产代码中 time.Duration 字面量必须出现在 package-level const，禁止内联字面量 | — | 通用：不依赖任何 gocell 专属包；换任何 Go 项目均成立，实现可几乎照搬 |

---

### 批次 5：Governance 规则体系（governance_rules_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| GOVERNANCE-RULES-REGISTRATION-GUARD-01 | Hard↓ / Medium↑ | callsite-allowlist + AST-pattern | 声明的 validate*/checkDEP*/checkCH* 方法与 allRules 注册集严格相等，防漏注册 | kernel/governance | 专属：守护的是 GoCell kernel/governance 的注册机制，换项目需完全重新定义 |
| GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01 | Hard | string-typed-funnel + typed AST | governance 规则 Code 参数必须是 rulecodes.go 里的 RuleCode const，禁止字面量和拼接 | kernel/governance | 专属：RuleCode typed-string 模式绑 kernel/governance 单源文件；思路通用但载体专属 |
| GOVERNANCE-RULE-ERROR-FIX-FIELD-01 | Hard↓ / Medium↑ | typed-marker-funnel + AST-pattern | governance newError/newWarning 等构造调用的 Fix 参数必须非空且可解析为字面内容 | kernel/governance | 专属：守护 kernel/governance 内部构造函数 Fix 字段；换项目需重定义目标构造函数 |
| GOVERNANCE-RULE-CODE-DETECT-BINDING-01 | Medium | typed AST | allRules 中每个 Rule.Code 必须在其 Detect 方法体（含 BFS 同 receiver 方法）中被 newError 等引用 | kernel/governance | 专属：闭环验证的是 governance allRules 结构，换项目需重新定义 rule 注册模型 |

---

### 批次 6：cmd 侧 ValidationResult（check_result_fix_field_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CMD-VALIDATIONRESULT-FIX-FIELD-01 | Hard↓ / Medium↑ | AST-pattern + typed AST | cmd/gocell/app 中所有 ValidationResult 字面量必须有非空 Fix 字段 | kernel/governance | 专属：守护 cmd 侧对 kernel/governance.ValidationResult 的直接构造，换项目需重定义目标 |

---

### 批次 7：测试纪律——Eventually / 轮询 / Sleep（test_eventually_funnel_test.go, test_polling_external_reason_literal_test.go, test_sleep_discipline_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| TEST-EVENTUALLY-FUNNEL-01 | Hard（ai-robust.md §Hard 范本，typed-marker-funnel，上游封禁） | typed-marker-funnel | 禁止测试代码直接调用 require/assert.Eventually*，必须走 testwait.External 或 Deterministic | pkg/testutil/testwait | 通用：思路完全通用（统一 wall-clock 轮询出口）；移植需创建等价 testwait 包 + 替换 banned 函数集 |
| TEST-POLLING-EXTERNAL-REASON-LITERAL-01 | Hard（ai-robust.md §Hard 范本，typed-marker-funnel，下游锁） | typed-marker-funnel | testwait.External 调用必须传 kebab-case const 字面量 reason，禁止变量/空串 | pkg/testutil/testwait | 通用：思路完全通用（强制轮询等待要附理由字面量）；移植需替换 testwait.External 路径 |
| TEST-SLEEP-DISCIPLINE-01 | Medium（推断） | AST-pattern | 测试文件中每个 time.Sleep 必须携带 `//archtest:allow:test-sleep <reason>` 行注释 | — | 通用：不依赖任何 gocell 专属包，模式完全可移植；换任何 Go 项目照搬 |

---

### 批次 8：测试时间字面量 / testutil 边界 / tracer（test_time_literal_test.go, testutil_boundary_test.go, tracing_simpletracer_test_only_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| TEST-TIME-LITERAL-01 | Medium（推断） | typed AST | 测试代码中 time.Duration 字面量必须在 package-level const，禁止内联（与 PROD-DURATION-CONST-01 互补） | — | 通用：无 gocell 专属依赖，与 PROD-DURATION-CONST-01 互补；可直接移植 |
| TESTUTIL-BOUNDARY-01 | Medium（推断） | import-ban | 含 "testutil" 路径段的包只能被 _test.go 或测试基础设施包导入 | — | 通用：「testutil 包不得被生产代码 import」是任何 Go 项目均成立的约定；可几乎原样移植 |
| TRACING-SIMPLETRACER-TEST-ONLY-01 | Medium | import-ban + AST-pattern | 保证 runtime/observability/tracingtest 不被生产代码导入，in-process simpleTracer 仅测试可用 | runtime/observability/tracingtest | 半通用：import-ban 模式通用，但守护的是 GoCell 特定包被删除/隔离的历史决策 |

---

### 批次 9：CI 发现 / race lane（ci_integration_discovery_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CI-INTEGRATION-DISCOVERY-01 | Medium（推断） | metadata/yaml-derive | CI integration-test step 用 `go list -tags=integration` 发现包，而非硬编码 glob | — | 通用：思路完全通用（CI 测试发现机制防 hardcode），换任何 Go 项目均成立 |
| CI-RACE-LANE-SUBSET-01 | Medium（推断） | metadata/yaml-derive | CI race lane 的硬编码包列表必须是 integration 发现集的子集，防止 drift | — | 通用：同上，守护 curated 子集与发现集的包含关系；可移植 |

---

### 批次 10：CI 依赖固定 / lint 冒烟 / 集成容器 / slowgate（ci_pinning_test.go, lintgate_smoke_test.go, integration_guard_test.go, slowgate_allowlist_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CI-PINNING-WORKFLOW-DIGEST-01 | Medium（推断） | metadata/yaml-derive | 所有外部 GitHub Actions uses 固定到 SHA digest，golangci-lint 固定到 patch 版本 | — | 通用：CI workflow digest pin 是任何 GitHub Actions 项目的供应链安全基准，直接可移植 |
| DEPENDABOT-COVERAGE-GOLANGCI-01 | Medium（推断） | metadata/yaml-derive | dependabot.yml 必须同时覆盖 golangci-lint-action 和 root gomod block | — | 通用：dependabot 覆盖校验模式与项目无关；换任何使用 dependabot 的项目均成立 |
| LINT-GATE-SMOKE-01 | Medium（推断） | conformance-test | 用真实 .golangci.yml 跑合成 fixture 模块，行为级证明 lint 规则真实触发（防配置漂移） | — | 通用：lint 配置冒烟验证思路适用任何 Go 项目；移植需替换 golangci-lint 路径和 fixture 内容 |
| INTEGRATION-GUARD-01 | Medium（推断） | AST-pattern | vault 集成容器失败必须 fail-fast（调用 RequireDocker 预检），禁止 Skip 静默吞错 | — | 通用：集成测试容器启动失败应 fail-fast 而非 skip 是普适约定；移植仅需替换容器函数名 |
| SLOWGATE-ALLOWLIST-01 | Medium（推断） | AST-pattern + metadata/yaml-derive | slowgate allowlist.txt 中每条记录必须对应真实 TestXxx 函数，且有 # reason 注释 | — | 通用：「慢测试白名单防孤立」模式与 gocell 无关；换项目照搬，替换 allowlist 路径即可 |
