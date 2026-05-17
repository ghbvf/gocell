# archtest 入口合并方案：Pass-Driver 范式 + 零二次返工迁移

**最后更新**：2026-05-16（Stage 1.8 façade 完备性 + USAGE-02 升 Hard，吸收 037 PR #505；PR #522 consolidated-batch 清零 LegacyAllowlist，F1-F3 ADR/plan 真理源同步）

## 进度状态

| 阶段 | PR 数 | 状态 | 备注 |
|------|------|------|------|
| 1 — Pass 框架 + 三重 Hard 防线 | 1 | ✅ PR #492 (2026-05-14) | 业务文件 0 改动；review 三轮 in-PR 收口 |
| **1.5 — Pass 框架完备化 + 单路径 enforcement** | 1 | ✅ PR #495 (2026-05-15) | Stage 2/3/dual 全后续 PR 零框架返工前置；见下方摘要 |
| **1.6 — RunTypedDir fixture-module driver 补全** | 1 | ✅ PR #500 (2026-05-15) | Stage-1.5 漏盘点 standalone fixture-module 扫描；随 PR-6 同 ship |
| **1.7 — RunTypedProduction production-only façade** | 1 | ✅ PR #507 (2026-05-16) | auth_bootstrap 迁移需要 production-only package set；随 PR-7 同 ship |
| **1.8 — FindFirstChild façade + USAGE-02 升 Hard** | 1 | ✅ PR #511 (2026-05-16) | 吸收 037 PR #505 治理产物（含 F6 同包裸调用 post-merge review 闭环）；不计入 Stage 3 实质迁移进度 |
| 2 — A 类 EachFile 主题分批迁移 | 4 | ✅ 全部 ship | PR-2 ✅ #496 (metadata)；PR-3 ✅ #493 (contract/codegen)；PR-4 ✅ #498 (observability)；PR-5 ✅ #497 (lifecycle/errcode) |
| 3 — E 类 for-range 主题分批迁移 | 5→3 | ✅ PR-8~10 合并入 PR #522 consolidated-batch 交付（2026-05-16）| PR-6 ✅ #500；PR-7 ✅ #507；余 37 文件合并为单一 PR #522（实测文件数 37 > 原估 27；shared-helper 图谱强耦合导致 batch 非独立，consolidated 更可行）；LegacyAllowlist 已清零 |
| 4 — 收尾（删 allowlist + scanner/typeseval 深 internal 化） | 1 | ✅ PR #PENDING (2026-05-17) | — |

**阶段 4 ship 摘要（PR #PENDING，2026-05-17）**：

- **删除 `tools/archtest/internal/archtestmeta/` 整个包**（迁移期 scaffold 终结；LegacyAllowlist 已在 PR #522 清零）
- **新增 `RunTypedFixture` + `FixtureOpts` typed funnel**（Hard 范本：业务调用方在 type system 上无法表达"传 Tags 加载 fixture"——编译失败；`FixtureOpts` 不含 Tags 字段）
- **`TestPassFunnelGuardListSync` 单一等式简化**：删旧 (A)(B)(C) 三段 LegacyAllowlist 交叉验证；改为 `yamlExempt == passFunnelPermanentExempt` 和 `packagesImport == passFunnelPermanentExempt` 两条精确等式断言（`maps.Equal` + `cmp.Diff`）
- **`passFunnelPermanentExempt` Medium 评级显式声明**：godoc 加 AI-rebust Medium 评级说明 + mechanical sync via double-declaration + 不升 Hard 的 ADR-mandated 理由
- **`.golangci.yml` 收尾**：删除迁移期注释块（archtestmeta.LegacyAllowlist / 27 files / stage 2/3 PRs migrating 等历史话术）；保留 deny 规则 + 3 个永久 self-exemption + ADR §Termination criteria 引用
- **`ai-collab.md` §载体决策原则**：第 3 条路由改写为公开 façade 引导；加防误判说明（既有文件直接 import `internal/scanner` / `internal/typeseval` helper 合法）；新增 §Hard 范本条目「typed function choice with input-struct field exclusion」
- **5 处注释更新**：`resolve.go` / `adapter_error_classification_test.go` / `passfunnelfixture/redfixture.go` / `basesliceredfixture/base_slice_literal.go` / `basesliceredfixture/slice_meta_literal.go`——删 archtestmeta 引用，改指 `RunTypedFixture` / `FixtureOpts`
- **静态反向锁 `TestArchtestmetaPackageDeleted`**：防 scaffold 回退（`os.Stat` 断言目录不存在）

**阶段 4 review R1 closure（同 PR post-first-cut commits，2026-05-17，R1 + R1.1 合并叙事）**：

> **根因（R1）**：PR #536 first-cut 把 `RunTypedFixture` + `FixtureOpts` 评为 "Hard 范本"，承诺业务调用方在 type system 上无法表达"传 Tags 加载 fixture"。但实际只锁了下游 outward Hard（FixtureOpts 缺字段致编译失败），**上游 Soft**：`RunTyped` 仍接受任意 Tags，业务可写 `RunTyped(t, TypedOpts{Tags: []string{"archtest_fixture"}}, ...)` 直接绕过 `RunTypedFixture`。review 指出当前 PR 内 `http_contract_visibility_type_segregation_01_test.go:352` 就是活样本，且无 meta-archtest 阻止后续业务复制此形态。ai-collab.md §Review checklist 「新引入 Soft → reject，要求改 ≥ Medium」要求同 PR 内升 Hard，不接受局部 Hard / 全局 Soft 的 ship 形态。
>
> **根因（R1.1）**：R1 第一次实现 detector 时只锁 `BasicLit STRING == "archtest_fixture"`，丢掉了 callee 维度——本应同构 §Hard 范本第 2 条 panicregister.Approved 的 (callee, arg) pair 形态，结果退化为 "arg literal value only"。同一 R1 commit 引入的 `archtest.FixtureBuildTag` const 自身成了新绕过路径（业务可写 `RunTyped(t, TypedOpts{Tags: []string{FixtureBuildTag}}, ...)`，BasicLit-only detector 视而不见）。这是典型 "Soft 上 Soft + patch" 反模式。R1.1 同 PR 内 detector 形态重写：升级到 (callee, arg) pair type-aware ban，对齐 panic 范本 Go ceiling Hard 完整形态。

- **新增 `archtest.FixtureBuildTag` 包级 const**（`fixture.go`）：typed-reference 单源，给 Go-code 路径识别 fixture tag（如 `panic_invariants_test.go` 跳过 fixture tag 组）用；`RunTypedFixture` body 改用 `Tags: []string{FixtureBuildTag}`，archtest 源内 "archtest_fixture" 字面量只出现 1 次（const RHS）；godoc 把原 "no FixtureBuildTag const" 论断重写为 "Go build directive 不能引用 const → //go:build 那侧硬编 + Go-code 那侧用 const" 双源 by construction
- **新增 `PASS-FUNNEL-FIXTURE-TAG-01`**（`pass_funnel_test.go::diagsFixtureTagBypass` + `TestPassFunnelFixtureTagBypass01` + `TestPassFunnel_FixtureCoverage` 4-form per-form 子断言）：**R1.1 终态形态** = type-aware (callee, arg) pair：(i) callee 经 `typeseval.ResolvePackageRef` 解析到 `fixtureTagLoaderSet`（`archtest.{RunTyped, RunTypedProduction, RunTypedDir}` + `typeseval.{SharedResolver, LoadPackages, LoadProductionPackages}`；EachFileInPackage 不接受 tags 不在集合内），(ii) 每个 arg 子树 `ast.Inspect`-walk + `typeseval.EvaluateConstString` 解析到 `"archtest_fixture"`。EvaluateConstString 已实现的解析格栈（BasicLit / Ident → const / SelectorExpr → 跨包 const / BinaryExpr → 拼接）统一覆盖 4 形态，无 per-shape 分支。exempt = 既有 `passFunnelPermanentExempt`（3 entry，fixture.go 由 `*_test.go` 后缀过滤自动排除）。AI-rebust evidence：archtest-bound (callee, arg) form-uniqueness Hard，同构 §Hard 范本第 2 条 panicregister.Approved Go ceiling 形态。Blind spots = 同文件 var escape（Tags arg 是 `*ast.Ident` 解到 var 而非 const）/ cross-func var / reflect / fixtureTagLoaderSet 枚举维护 — 全同 PASS-FUNNEL-LOADPACKAGES-01 / RESOLVE-01 同型 accept 等级
- **fixture 文件 V' RED 重构**（`internal/passfunnelfixture/redfixture.go`）：删 R1 加的 `_ = "archtest_fixture"` 裸字面量（V 形态 RED，V' 不命中——按"不向后兼容"原则删）；新增 `fixtureTagBypassRedForms` 函数体内 4 个 `typeseval.SharedResolver` 调用，分别覆盖 Form A（BasicLit）/ B（local const Ident，新加 `const localFixtureTag = "archtest_fixture"`）/ C（BinaryExpr 拼接）/ D（跨包 SelectorExpr，新加 `import "github.com/ghbvf/gocell/tools/archtest"` — 无 cycle，archtest 通过 runtime load 加载 fixture 而非 import）；`TestPassFunnel_FixtureCoverage` per-form trip-wire 用 comment-anchored line lookup（`// Form A` 等注释 + 行号 + 1），单 form fixture 删除即对应 form 断言失败
- **业务 callsite 迁移**：
  - `http_contract_visibility_type_segregation_01_test.go`：helper `runHTTPContractVisibilityCheck(tags []string, patterns)` → `(fixture bool, patterns)`；fixture=true 走 `RunTypedFixture`，fixture=false 走 `RunTyped`；两 phase 仍共享 SharedResolver cache（与 façade 入口无关）
  - `panic_invariants_test.go:367`：`containsTag(tagGroup, "archtest_fixture")` → `containsTag(tagGroup, FixtureBuildTag)`（callee 不在 fixtureTagLoaderSet，detector 自然放行）
- **6 处 fixture 包 godoc 示例文本同步**（`rawparamfixture`/`auditledgerfixture`/`inspectorredfixture`/`wrapfixture/violation`/`sessionprotocolfixture`/`refreshinvariantsfixture`）：示例改为 `archtest.RunTypedFixture(...)` 而非 `typeseval.SharedResolver(..., []string{"archtest_fixture"}, ...)`
- **3 处 fixture 包 godoc 文字同步**（`basesliceredfixture` × 2 + `passfunnelfixture` × 1）：原 "must agree with the 'archtest_fixture' literal (single source: RunTypedFixture helper)" 改为 "must agree with the literal value of `archtest.FixtureBuildTag`（//go:build 不能引用 const 是结构性 hard-code 理由）"
- **F2 注释修正**（review 同时发现）：`pass_funnel_test.go:258` 旧 "single-source (LegacyAllowlist)" → "single-source (passFunnelPermanentExempt)"
- **章程同步（R1.1 终态形态）**：`ai-collab.md` §Hard 范本第 4 条「typed function choice with input-struct field exclusion」末尾追加 **配套要求 — funnel 双向闭锁** 段——以本 R1.1 闭环为先例，明示该范本必须同 PR 内补 meta-archtest 锁 façade 旁路；**形态选择段强制 (callee, arg) pair form-uniqueness**，警告"只锁 BasicLit 字面量值"是常见反模式（同 PR 引入的新 const 自身会成新绕过路径）
- **ADR amendment 落地必查**：ADR `202605141519` §"PR #536 review R1 amendment — façade bypass closure" 段原地重写为 R1 + R1.1 合并叙事 + 完整三轨威胁矩阵（before R1 / R1 first attempt / R1.1 final）；§Hard-line three-defense 表第 4 行措辞改 type-aware (callee, arg) pair ban；§Termination criteria (d) 同步 — 按 ai-collab.md §"ADR amendment 落地必查" 同 PR 内重写，不留 R1 旧措辞作"历史脉络"
- **验证**：`go build ./...` 绿；`go test ./tools/archtest/... -count=1` 全绿；`hack/verify-archtest.sh` 16-shard process-isolated PASS（TOTAL=458，旧 457+1 新 `TestPassFunnelFixtureTagBypass01`，由 ARCHTEST-VERIFY-COVERAGE-01 自动 discovery）；业务 archtest *_test.go 内零字面量 `"archtest_fixture"` 残留（仅 fixture.go const RHS / pass.go godoc / passFunnelPermanentExempt 3 文件 + passfunnelfixture redfixture.go 内 V' RED）

**阶段 1.8 ship 摘要（本 PR，2026-05-16）**：

> **根因**：037 治理项 PR #505 引入 `scanner.FindFirstChild` 和 `SCANNER-FRAMEWORK-USAGE-02`，但 (i) `tools/archtest/walk.go` 未补 `FindFirstChild` façade 出口，Stage 4 封 internal/scanner 后业务侧无路可走；(ii) USAGE-02 检测器 typeseval target 仅 `scannerPkgPath.EachInChildren`，040 façade 端态 `archtest.EachInChildren` 上写出 done/found sentinel 会 silent miss；(iii) PR #505 fixture pipeline 走 inline-source + `empty := &types.Info{}` 启用 syntactic fallback（`scannerLocalName` + `id.Name == ...` 分支）——typeseval 主路径 + syntactic 兜底并存，是 PR #505 godoc AI-rebust 评级（form-ban Medium = Go ceiling，与 fallback 不同轴）未覆盖到的 Soft 盲点。Stage 4 封 internal/scanner 前一次性补齐 façade + 升 Hard，不留 follow-up。

- 新增 `archtest.FindFirstChild[S,N]` walk.go 薄委托（040 façade 端态完备性收尾；Scope 构造 façade 已在 PR #507 / 更早 PR 中补齐）
- `SCANNER-FRAMEWORK-USAGE-02` 升 Hard：删 `scannerLocalName` 函数 + `isScannerEachInChildren` 的 syntactic 分支；改名 `isScannerEachInChildren` → `isMonitoredEachInChildren`，参数去 `file *ast.File`；typeseval target 扩到 `{scannerPkgPath, archtestPkgPath}`（双 callee，archtest.EachInChildren 是 scanner.EachInChildren 的薄 façade，两者形态等价禁用 sentinel）
- fixture pipeline 升 typed：原 `TestScannerFrameworkUsage02_Fixture` 的 6 个 inline-source case + `TestScannerFrameworkUsage02_BlindSpotForwardFixtures` 的 3 个 BS forward fixture 全部转 `tools/archtest/internal/usage02fixtures/<case>.go` 真实 Go 文件；新 helper `loadFixture02(caseName, detector)` 复用 `typeseval.SharedResolver` typed 加载，与 live scan 同源；fixture 与 live 共用同一 `*types.Info` 来源，pure detector 不分叉。fixture 子包路径深一级，被 USAGE-02 live filter（`Dir(rel) == "tools/archtest"` && `_test.go` 后缀）自动排除，无自检测循环
- fixture 案例增 archtest 形态：`red_archtest_done_sentinel.go`（archtest.EachInChildren + sentinel，必 RED → typeseval 解析至 archtestPkgPath.EachInChildren → 命中 1 hit）+ `green_archtest_findfirstchild.go`（archtest.FindFirstChild → 0 hits），共 11 个 fixture 文件（8 主 case + 3 BS forward case，另 doc.go 1 个）
- 040 plan façade 列表更新：walk.go = `EachInSubtree / EachInChildren / FindFirstChild / StringLitValue / ReceiverTypeName`（共 5 个，FindFirstChild 是本 Stage 新增）；scope.go = `Scope/ScopeOption/FileContext/Diagnostic` type alias + `ModuleScope/DirsScope/IncludeTests/ExcludeRels/MatchRels/IncludeTestdata/IncludeGenerated/Report`（已在 Stage 1.7 PR #507 中补齐，不在本 Stage 改动范围）
- AI-rebust 评级：USAGE-02 检测器升至 typeseval 单路径 Hard（删 fallback）；fixture-live 同源 typed pipeline Hard；上游 form-ban Medium "Go ceiling"（PR #505 godoc 原文延续，与 fallback Soft 是不同轴）；syntactic fallback Soft 盲点关闭
- **Stage 2/3 迁移 checklist 增条**：业务测试 import 切换时，`scanner.FindFirstChild` 必须随 `scanner.*` → `archtest.*` 切换一并改成 `archtest.FindFirstChild`，单文件一次迁移完成不留半态
- **验证**：`go build ./...` 绿；`go test ./tools/archtest/...` 全绿（含新加 USAGE-02 fixture cases）；`hack/verify-archtest.sh` 16 shard PASS；golangci-lint 0 issues；既有 24 处 `scanner.FindFirstChild` 调用点 + 9 处 USAGE-02 迁移点不退化（fixture 子包路径过滤排除）

**F6 同包裸调用补救（PR #511 post-merge review F6）**：原 `eachInChildrenCalleeSel` 把 IndexExpr 解泛型后只接 `*ast.SelectorExpr`，导致同包内裸 `EachInChildren[...]` 调用（package archtest 内部既有形态——`assembly_invariants` / `auth_bootstrap_invariants` / `cli_unimpl_hide` / `codegen_invariants` / `errcode_invariants` / `kernel_metadata_no_wire` / `pass_test` 共 14+ 处现存裸调用）解到 `*ast.Ident` 后直接返回 nil，typeseval 永远不被询问。**这是 PR-505 时代留下的隐性 qualified-import 假设**，与本 Stage 升 Hard 的 typeseval 单路径声明矛盾。同 PR 一并修复：合并 `eachInChildrenCalleeSel` + `isMonitoredEachInChildren` 为单一 helper `monitoredEachInChildrenCallee(info, call) bool`，解泛型后按 `*ast.SelectorExpr` / `*ast.Ident` 双路 fast-path skip，统一通过 `typeseval.ResolvePackageRef` 解析 callee declaring-package；fixture 加 `red_archtest_bare_done_sentinel.go`，用 dot-import 模拟同包裸调用的 AST + typeseval 形态（仅此一行 `//nolint:revive,staticcheck`，全局 dot-import ban 不变）。F6 修复后 14+ 现存裸调用站点经检查全为纯迭代，live scan 不退化。

**设计决策 D1（Stage 1.8）— 删 syntactic fallback 而非扩 fallback 集合**：

> 用户原议案（PR-505 args 字面）建议扩 fallback 识别集合 `OR(scanner, archtest)`，让 inline-source fixture 也能测 archtest 形态。这是在 PR #505 既有 Soft 兜底之上扩集合——违反 ai-collab.md §"Review checklist"对既有 Soft 的处理原则（"优先讨论升级到 Hard/Medium，而非在 Soft 层打补丁"）。本 Stage 选择把 fixture pipeline 一次升 typed，删 syntactic fallback、扩 typeseval target，让 USAGE-02 检测器只剩单一 Hard 路径。代价是 fixture 不能用 inline source（必须 testdata typed module 或 internal 子包），收益是 fixture-live 同源 anti-drift 形态对齐 typeseval Hard，无双轨。

**阶段 1.7 ship 摘要（2026-05-16，随 Stage 3 PR-7 同 ship）**：

> **根因**：`auth_bootstrap_invariants_test.go` 的 5 条规则（含 `AUTH-BOOTSTRAP-CLIENTS-MUTEX-01`）需要加载生产包集合（`Tests: false`），同时排除 `generated/` 下 codegen 产物，以防生成代码误触规则。直接使用 `RunTyped("./...")` + 手动 `pass.IsGenerated` 跳过是一种合法绕过（作者可以忘记调用 IsGenerated），等效于把 Hard 防线退化回 Soft。正确做法是封装 `RunTypedProduction` façade，将 production-only 语义收进单一入口，迁移期每条 auth_bootstrap 规则都自然经过该路径。

- 新增 façade `RunTypedProduction(t *testing.T, opts TypedOpts, rule Rule) []Diagnostic`：内部经 `typeseval.LoadProductionPackages`（`Tests: false`，仅生产包）加载，`generated/` 在加载期由 `ProductionResolver` 排除（非 caller 手动 `pass.IsGenerated`），随后复用 `runRulePasses` 共享遍历；production-only 入口加载全量生产包，故无 `patterns []string` 参数（与 `RunTyped`/`RunTypedDir` 不同），并用 `*testing.T`（生产入口，无 `tbFatalSpy` 单测诉求）
- 抽取 `runRulePasses(t, pkgs, rule)` 共享内部 helper，`RunTyped` / `RunTypedDir` / `RunTypedProduction` 三个入口共享同一 pass 构造和遍历逻辑，单一来源不分叉
- 新增 `moduleImportPath(dir string) (string, error)` 读取所在目录的 `go.mod` module 声明，用于 `generated/` import path 前缀匹配（替代 hard-coded module path 字符串）
- `PASS-FUNNEL-LOADPACKAGES-01` 禁止集合**加宽**：新增 `typeseval.LoadProductionPackages` 进入禁止列表；此后 production-only 扫描的唯一合法入口是 `RunTypedProduction`
- 新增 `testdata/errorfirsttypednilfixture/` RunTypedDir fixture module，覆盖 `errcode_invariants_test.go` 测试 #5（ERROR-FIRST-TYPED-NIL-01）的 Soft→Hard 升级，用真实 fixture 替代字符串锚点匹配
- `pass_test.go` 增 proof 测试：`TestRunTypedProduction_ExcludesGeneratedPackages` / `TestRunTypedProduction_IncludesProductionCode`，TDD RED→GREEN
- **AI-rebust 评级**：下游 Hard（`PASS-FUNNEL-LOADPACKAGES-01` type-aware 拦截 `LoadProductionPackages` 直调，caller 必须改 allowlist）；上游 Medium（rule 作者仍可选择 `RunTyped("./...")` + 手动 `pass.IsGenerated` 跳过代替 `RunTypedProduction`，软逃逸面存在）。上游升 Hard 路径登记为 backlog `PASS-PRODUCTION-UPSTREAM-HARD-01`，已在 `RunTypedProduction` godoc 中点名。
- **验证**：`go build ./...` + `go build -tags=integration ./...` 绿；`go test ./tools/archtest/...` 全绿；`hack/verify-archtest.sh` 16 shard PASS；golangci-lint 0 issues

**设计决策 D2（Stage 1.7）— RunTypedProduction 不合并进 RunTyped flag 参数**：

> 若为 `RunTyped` 增加 `ProductionOnly bool` 字段到 `TypedOpts`，则全部已 ship 的调用点默认值为 `false`（行为不变），不引起返工——技术上可行。但这会把"production-only 语义"变成一个可选标志，AI 写新规则时默认漏填，等效于静默选 `Tests: true`（混入测试包）。`RunTypedProduction` 作为独立入口，选错语义 = 选错 API 名（与 Stage 1.6 D1 设计范本一致），且 `PASS-FUNNEL-LOADPACKAGES-01` 仅 ban `LoadProductionPackages` 直调，不 ban `RunTypedProduction` 本身，不产生调用方返工。

**阶段 3 PR-7 ship 摘要（PR #507，2026-05-16）**：

- **批次 A**（commit `4c741980`）：`errcode_invariants_test.go` 8 条规则全量迁移——`ERRCODE-KIND-LITERAL-01` / `ERRCODE-KIND-DEFINED-01` / `MESSAGE-CONST-LITERAL-01` / `DETAILS-SLOG-ATTR-01` / `ERROR-FIRST-TYPED-NIL-01` / `EXPORTED-ERROR-NEW-01` / `ERRCODE-CONSTRUCTOR-CALL-01` / `ERRCODE-CATEGORY-LITERAL-01`；import 切换 `typeseval.LoadPackages` → `archtest.RunTyped` / `RunTypedDir`；LegacyAllowlist 删 `errcode_invariants_test.go` 条目
- **批次 B**（commit `92c778ff`）：`auth_bootstrap_invariants_test.go` 5 条规则全量迁移——`AUTH-BOOTSTRAP-CLIENTS-MUTEX-01` / `AUTH-ROUTE-BYPASS-COMPAT-01` / `AUTH-BOOTSTRAP-ROUTE-EXPR-01` / `AUTH-BOOTSTRAP-INLINE-LITERAL-01` / `AUTH-BOOTSTRAP-LOCAL-VAR-ASSIGN-01`；使用 `RunTypedProduction` 两阶段加载（production-only set + generated/ 自动排除）；LegacyAllowlist 删 `auth_bootstrap_invariants_test.go` 条目
- **批次 C**（commit `b4609875`）：Stage 1.7 框架补全——`RunTypedProduction` façade / `runRulePasses` 共享 loop / `moduleImportPath` go.mod reader / `PASS-FUNNEL-LOADPACKAGES-01` 加宽 / redfixture coverage / `pass_test.go` proof tests；LegacyAllowlist 39→37（删 2 条目）；`.golangci.yml` −2 条 allowlist 行
- **Stage 1.7 commit**（commit `08f2be17`）：`RunTypedProduction` 入口与 `TestPassFunnelGuardListSync` A/B/C 三向同步断言全绿
- **latent bug 修复**：`runtime/distlock/locker.go` 存在 `ERROR-FIRST-TYPED-NIL-01` 潜伏 bug（error-first 函数返回 typed nil interface 而非 untyped nil），在迁移 `errcode_invariants_test.go` 后被首次检测到并同 PR 修复
- **TestPassFunnelGuardListSync A/B/C 全绿**：三向同步断言（`yaml-exempt ⊆ LegacyAllowlist ∪ {self}` / `packages-importers ⊆ yaml-exempt` / `yaml-exempt ∖ {self} ⊆ packages-importers`）在 37 条目基准下持续通过

**阶段 1.6 ship 摘要（本 PR，2026-05-15，随 Stage 3 PR-6 同 ship）**：

> **根因**：Stage-1.5 定型 `RunTyped` API 时未盘点 `testdata/` 下独立 fixture-module（自带 `go.mod` + `replace` 指令，故意隔离 intentional-violation fixture）的扫描需求。`clock_invariants_test.go` 迁移（PR-6）是 32 个 E-class 中第一个撞上该形态的文件，PR-6 内一次补完框架端态（命名 Stage 1.6），不分离单独 PR。

- 新增 façade `RunTypedDir(t testing.TB, dir string, opts TypedOpts, patterns []string, rule Rule) []Diagnostic`：`dir` 必须绝对路径（`filepath.IsAbs` fail-fast），对标 `analysistest.Run(t, dir, ...)` 的 `dir` 位置参数喂 `packages.Config.Dir`，支持加载 `testdata/*/go.mod` 独立 module（ref: `golang.org/x/tools go/analysis/analysistest/analysistest.go`）
- `RunTyped` 内部委托 `runTypedWithRoot(t, findModuleRoot(t), ...)`；`RunTypedDir` 委托 `runTypedWithRoot(t, dir, ...)`；单一构造路径 `runTypedWithRoot` 不变，双入口语义正交
- 参数类型选 `testing.TB`（非 `*testing.T`），以支持 fatal-path spy 测试及 `TestMain` 场景，与 `analysistest` 的 `Testing` 接口对齐
- AI-rebust **Hard**：三重防线逐条不变——①`Pass.Pkg` 仍 `*types.Package`，archtest 作者拿不到 `.Syntax`；②depguard 不变；③`PASS-FUNNEL-LOADPACKAGES-01` funnel **加宽**（`RunTypedDir` 是 fixture-module 扫描此后唯一合法入口，fixture-module 形态不构成新绕过面，仅把之前缺乏入口的形态纳入合法 funnel）
- **后续 4 个 E-class fixture-module 文件**（`exported_error_new_fixtures_test.go` / `goose_session_locker_fixtures_test.go` / `prod_duration_fixtures_test.go` / `test_time_literal_fixtures_test.go`）此后只需 `RunTypedDir`，零框架返工（`prod_clock_injection_fixtures_test.go` 与 `clock_invariants_test.go` 共享 `scanProdClockInjectionAST`，PR-6 改 clock 必然连带改它，plan 原未预见此耦合，故并入 PR-6 一并迁移，消除半迁移二次返工）
- **验证**：`RunTypedDir` 单元测试（`TestRunTypedDir_*`）TDD RED→GREEN；`clock_invariants_test.go` 全量 typed-load 迁移绿；`hack/verify-archtest.sh` 16 shard PASS；golangci-lint 0 issues

**设计决策 D1（Stage 1.6）— 不收敛为单一 `RunTyped(t, dir, ...)` 入口**：

> 若将两个函数合并为 `RunTyped(t, dir, ...)`（dir 可选或 `""`），则 PR #492 / #493 / #496 及框架自测（`pass_test.go`）已 ship 的全部调用点须二次返工改签名，违反「0 二次返工」硬不变式。双入口语义正交（`RunTyped` = 主树 `findModuleRoot`；`RunTypedDir` = 调用方指定 dir），单一 driver 构造路径 `runTypedWithRoot` 不分叉，不引入新复杂度。

**阶段 1.5 ship 摘要（本 PR，2026-05-15）**：

> **根因**：PR #492 定型 `Pass`/`Run`/`RunTyped`/façade 时未基于全部存量 archtest（24 A-class + 32 E-class + 6 dual-class）真实取数需求完整盘点就定型 API。只补表面 gap 是 L1 补丁思维（PR #493 contract/codegen 迁移已被迫在 `codegen_invariants`/`listener_dx` 内手写 `parser.ParseFile(ParseComments)` 绕过，即将产生二次返工）。本阶段一次定型完备端态 + 同 PR 封死旧路径。

- `archtest.Run`（AST 路径）`collectASTFiles` parse mode 增 `parser.ParseComments`；typed 路径**已带注释**（go/packages 默认 ParseFile = `parser.AllErrors|parser.ParseComments`，**不改 typeseval**，仅加 `TestRunTyped_CommentsRegressionLock` 回归锁定）
- `Pass` 加 `Abs func(*ast.File) string`（与 `Rel` 同源 `fset.Position`，零新状态）+ `(*Pass).IsFileInScope` / `IsGenerated` 方法（收 typeseval build-constraint helper，对齐 plan 轴 4）
- 新建 `tools/archtest/resolve.go`：`type ImportBan = scanner.ImportBan` + `ResolvePackageRef`/`ResolveMethodCall`/`EvaluateConstString`/`FlatNonDefaultTags`/`KnownNonDefaultTags` 薄委托；**6 个 loader 符号故意不重导出**——经 `RunTyped` 唯一可达（funnel Hard 防线本体）
- 新增元治理 `PASS-FUNNEL-RESOLVE-01`（type-aware via `*types.Info`，复用 `diagsLoadPackages` 符号集机制，`// AI-rebust: Medium` + 盲区清单 + reverse self-check）：ban 业务 archtest 直引 8 个 typeseval helper + `scanner.ImportBan`；豁免存量（LegacyAllowlist +2 → `build_constraint_test.go` / `ci_integration_discovery_invariants_test.go`），Stage 4 清零。**勘误（PR #495 后修）**：`RESOLVE-01` 初始实现的 `resolveBarePkgSymbol` 仅处理 `*types.Func`，dot-import `ImportBan{}` 的 bare-Ident 被 `*types.TypeName` 而非 `*types.Func` resolve，导致 `ResolvePackageRef` 对该形态返回 `("","",false)`；`TestPassFunnel_FixtureCoverage` 因 qualified+alias 两路已产生诊断而误报通过。PR #495 在 `resolveBarePkgSymbol` 加 `*types.TypeName` 分支修复，并将 ImportBan 断言从 `≥1` 升为精确计数 `==3`（qualified+alias+dot-import），确保单形态回归即失败。
- `TestFacadeDoesNotLeakLoaders`（防线 #1 Hard 反向盲区自检）：静态断言 façade 零 loader / 零 `*packages.Package` 暴露
- 端态不变式：此后 24 A-class + 32 E-class + 6 dual-class 迁移**只需** import `tools/archtest`，零 `internal/*`/`x/tools/go/packages` 直引，零框架返工
- 验证：`pass_test.go` +10 TDD（RED→GREEN）；verify-archtest.sh PASS（16 shard / 372 test）；golangci-lint 0 issues；build incl `-tags=integration` 绿

**勘误（基于 develop@2fd2976e 复核）**：dual-class 实测 **6** 文件（auth_bootstrap / errcode_invariants / outbox_invariants / panic_invariants / production_loader_funnel / role_admin_literal），非原预估 ≤3，归后续单一 PR 整体迁移；LegacyAllowlist 当前 **47** 条目（PR #493 删 10 个已迁移 contract/codegen 文件后）。

**勘误 — RESOLVE-01 façade 出口完备性（PR #495 补修）**：Stage 1.5 未对 RESOLVE-01 被禁 8 个 symbol 的 façade 出口作穷举验证，导致两处 gap：`ParseBuildConstraint` — `build_constraint_test.go` / `ci_integration_discovery_invariants_test.go` 两个文件调用时需获取原始 `constraint.Expr` 做三路求值，`Pass.IsFileInScope` 只返回单 bool、无法覆盖；`IsGeneratedRelPath` — `outbox_invariants_test.go::TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3` 传入 raw string rel，`Pass.IsGenerated(f *ast.File)` 无法覆盖。PR #495 在 `resolve.go` 补 `func ParseBuildConstraint(filePath string) (constraint.Expr, error)` 和 `func IsGeneratedRelPath(rel string) bool` 两个薄委托自由函数，两符号继续留在 RESOLVE-01 禁止映射（ban 的是 `typeseval.` 直调，业务侧改用 `archtest.` 门面）。当前不变式：**RESOLVE-01 每个被禁符号均有语义充分的门面出口，覆盖所有已知调用形态**。

**后续迁移强制约定**：Stage 2/3 迁移后 Rule 必须返回 `[]Diagnostic` + `archtest.Report`（对标 go/analysis 端态，禁止保留 inline `t.Errorf` 形态，确保每文件一次到位 0 二次返工）。

**阶段 1 PR-1 ship 摘要（PR #492，2026-05-14）**：

- 新建 `tools/archtest/{pass,walk,scope,content,module_root}.go`（façade 重导出 + Run/RunTyped driver；`Pass.Pkg *types.Package`，**不暴露** `.Syntax`）
- 新建 `tools/archtest/internal/archtestmeta/legacy_allowlist.go`（53 文件 + `FixtureBuildTag` const）
- 新建 `tools/archtest/internal/passfunnelfixture/redfixture.go`（build-tag 隔离的 red fixture，覆盖 qualified-import / alias-import / dot-import 三路 + var-reference 形态）
- 新建元治理 archtest `tools/archtest/pass_funnel_test.go`：5 条规则 (a) `PASS-FUNNEL-EACHFILE-01` / (b) `LOADPACKAGES-01` / (c) `PACKAGES-IMPORT-01` / (d) `FIXTURE-COVERAGE` 反向自检 / (e) `GUARD-LIST-SYNC`（解析 `.golangci.yml` 与 Go LegacyAllowlist 三向同步断言）
- 新建 `tools/archtest/pass_test.go`：11 个单元测试覆盖 Run / RunTyped / buildTypedPass / newPackageRel / isPackageWithTestFiles，archtest 生产代码覆盖率 ~12% → ~71%
- `.golangci.yml` 增 `archtest-no-direct-packages-load` depguard rule，仅 deny `golang.org/x/tools/go/packages`（符号级 scanner/typeseval 由元治理 archtest 拦截，避免 38 个仅用 walk helper 的合法文件误报）
- 新建 ADR `docs/architecture/202605141519-adr-archtest-pass-funnel.md`：业界对标 + 三重 Hard 防线 + 迁移路径

**Round-1 review follow-up（F1-F5，PR #492 round-1 in-PR）**：
- F1：depguard 防线 #2 收窄至仅 `packages` 包（scanner/typeseval 符号级由防线 #3 元治理 archtest 通过 `*types.Info` resolve 精确拦截），消除 38 文件误报
- F2：新增 `TestPassFunnelGuardListSync` 解析 `.golangci.yml` 在测试时断言三向不变式（`yaml-exempt ⊆ LegacyAllowlist ∪ {self}` / `packages-importers ⊆ yaml-exempt` / `yaml-exempt ∖ {self} ⊆ packages-importers`），消除阶段 2/3 迁移漂移盲点
- F3：`Pass.TypesInfo` godoc 删除误导性链接
- F4：`newPackageRel` 加 `abs == ""` synthetic file 守卫
- F5：`scope.go` FileContext 重导出 godoc 标注 legacy 用途

**Round-2 review follow-up（F1-F6，PR #492 round-2 in-PR）**：
- F1：redfixture 扩 import 形态覆盖（qualified / aliased / dot-import 三路 × banned symbol 全交叉）；改用 var-reference 形态去除 testing 依赖
- F2：`Pass.Files` 语义统一为 ONE Pass per package（与 RunTyped + go/analysis 一致），消除"per-file callback + `pass.Files[0]` 在 typed mode 静默丢文件"风险；引入 `collectASTFiles` + `scanner.Scope.ModRoot()` accessor
- F3：`buildTypedPass` dedup 按"test-variant pkgs 优先"sort 保证确定性
- F4：抽 `archtestmeta.FixtureBuildTag` const，消除 build-tag 字面量重复
- F5：passfunnelfixture 删除 `testing` 依赖（var-reference only）
- F6：补 `pass_test.go` 11 单元测试，archtest 生产代码覆盖 12% → 71%

**Develop 同步**（最后一次 round in-PR）：
- 把 PR #490 派生的两个新 archtest 加入 LegacyAllowlist：`credential_invalidate_funnel_invariants_test.go`（用 scanner.EachFile + typeseval.SharedResolver + 直接 import packages，三处都进 yaml/Go 双 allowlist）+ `sessionvalidate_epoch_compare_test.go`（不用 banned symbol，仅进 Go allowlist 不进 yaml）
- `pass_test.go` 加 `// INVARIANT: ARCHTEST-PASS-DRIVER-UNIT-01` 合成 anchor（参 `helpers_test.go ARCHTEST-HELPERS-01` 范本）

**当前 LegacyAllowlist 总数**：54 文件（53 baseline + 1 PR #490 派生），等阶段 2/3 PR 一一删除。

## 实施纪要：consolidated 执行调整（PR #522，2026-05-16）

原计划 Stage 3 余量分 3 批（PR-8 / PR-9 / PR-10）各自独立迁移约 9 个文件，Stage 4 清零。实际执行中发现如下结构性约束导致 3-PR 计划无法独立执行：

**为何 3 PR 合并为 1**：

1. **shared-helper 图谱强耦合**：PR-9 尝试单批迁移时，因多个迁移文件共享同一组 `internal/typeseval` helper，allowlist 三向同步断言（`TestPassFunnelGuardListSync`）要求 yaml-exempt ↔ LegacyAllowlist ↔ packages-importers 三集合同步更新，导致任何一批"中间态"都不满足同步断言。各批并非独立事务——必须整批或跨批同时更新 yaml + Go allowlist + 删文件条目，实际 PR-9 级联到 46 个文件。
2. **3 个 ESCALATE 条目跨批共享**：`archtest_test.go`（reclassify 为永久豁免）、`governance_rules_invariants_test.go` 与 `scanner_framework_usage_test.go`（真迁移）三个原计划标注为 ESCALATE 的条目与剩余文件存在共享 helper 依赖，分批后每批都需要先解决这三个条目的状态，不如一次性处理。
3. **LegacyAllowlist 提前清零比 Stage 4 边界更干净**：若分三批合并，Stage 4 仍需单独 PR 删 `archtestmeta` 包；consolidated 执行后 Stage 4 只剩包删除 + yaml 收尾，边界清晰。

**PR #522 实际迁移结果**：

- 实际迁移文件数：37（原估 27，差异来自 plan 未计入 `archtest_test.go` reclassify 和多出的 dual-class 文件）
- `archtest_test.go`：从 LegacyAllowlist 移至 `passFunnelPermanentExempt`（永久豁免）；理由：`checkCellPublicAPIAdapterTypes`（LAYER-10）经 `depgraph.FromPackages([]*packages.Package)` 用类型化输入，Pass funnel 隐藏 `.Syntax` 使该形态结构性不可表达；backlog `ARCHTEST-LAYER10-PASS-MIGRATION-01` 跟踪后续是否可消除该永久豁免
- `governance_rules_invariants_test.go`：真迁移完成，LegacyAllowlist 条目删除
- `scanner_framework_usage_test.go`：真迁移完成，LegacyAllowlist 条目删除
- LegacyAllowlist 清零：PR #522 merge 后 `archtestmeta.LegacyAllowlist` 为空

**Stage 4 边界**：`archtestmeta` 包删除 + `.golangci.yml` allowlist 段收尾（保留 3 个永久豁免 glob）。独立 Stage 4 PR 待排期。

## Context

GoCell 的 archtest 体系当前有两个并列入口：

- `scanner.EachFile(t, scope, mode, fn)` — 纯 AST 遍历，秒级，按目录树作用域，36 处调用 / 25 个 archtest
- `typeseval.LoadPackages(modRoot, tests, tags, patterns...)` + 裸 `for _, file := range pkg.Syntax` — 全类型图加载，分钟级，按 import path，33 个 archtest 用 LoadPackages、其中 48 处裸 for-range 分布在 30 个文件

两个入口在 Go 闭包层是合法可组合的，曾出过 INV-1 类 bug：archtest 作者用 scanner 入口遍历文件、却闭包捕获 typeseval 一侧的 `*types.Info`，AST 节点指针不同源 → `info.Types[node]` 静默 miss、规则永远不触发。当前 PR 通过引入 `typeseval.EachFileInPackage` 把 typeseval 一侧的"裸 for-range + 外部 info 捕获"挤掉，是 Medium 形态的局部收口。

业界 Go 静态分析框架（go/analysis、staticcheck、golangci-lint、wire）的共识做法是 **Pass-Driver 范式**：单一 Pass struct 把 Files / TypesInfo / Fset 同源注入，**用户拿不到自由构造 Pass 的能力，只能写 Rule 函数让框架调度**。本方案把 archtest 收敛到 `archtest.Pass + Rule` 单一编程模型，**业务文件零二次返工** —— 每个文件只在它的语义迁移 PR 中被改写一次。

## 业界对标关键发现

| 项目 | 入口设计 | INV-1 防御 |
|------|---------|-----------|
| **go/analysis** | `analysis.Pass` 字段公开（Files / TypesInfo / Fset / Pkg），driver-private construction（`checker/checker.go` 单 struct literal 同源注入），**Pkg 类型是 `*types.Package` 不是 `*packages.Package`** | 用户写 `Analyzer.Run(pass)` 接收 Pass、不能自由 new；driver 唯一构造路径同源 |
| **staticcheck** | 沿用 `analysis.Pass`，helper `IsCallTo(pass, ...)` 函数式入口 | 函数只接 Pass、不接独立 TypesInfo |
| **golangci-lint** | 不额外封装 | 经济效益（多次 packages.Load 显著变慢） |
| **wire** | `*gen{pkg *packages.Package}` 单字段 source of truth | 所有方法通过同一 pkg 访问 AST + TypesInfo |
| **ArchUnit (Java)** | `JavaClasses` 物理表示（.class 字节码合并） | 源头消除双视图 |

**关键观察**：go/analysis 的 `Pass.Pkg` 类型是 `*types.Package`（go/types 标准库），**不是 `*packages.Package`（golang.org/x/tools）**。这关键——用户拿到 Pass 看不到 `.Syntax` 字段，也就无法写出"裸 for-range pkg.Syntax + 外部捕获 info"的 INV-1 形态。

## 目标端态

```go
package archtest

// Pass is driver-constructed; users only receive it as Rule parameter.
// Fields are exported (per go/analysis convention); INV-1 defense comes from:
//   (1) Pkg type is *types.Package, NOT *packages.Package — no .Syntax access
//   (2) Construction path is single — Run / RunTyped / RunTypedDir driver
//   (3) depguard bans archtest *_test.go from importing packages directly
type Pass struct {
    Fset      *token.FileSet
    Files     []*ast.File
    Pkg       *types.Package    // go/types — exposes Name/Path/Imports/Scope, NO .Syntax
    TypesInfo *types.Info       // nil in AST-only mode
    Rel       func(*ast.File) string  // fset-bound rel-path helper
}

type Rule func(*Pass) []Diagnostic

// Driver entries — framework controls packages.Load / parser.ParseFile timing.
func Run(t *testing.T, scope Scope, rule Rule)                       // AST-only
func RunTyped(t *testing.T, opts TypedOpts, patterns []string, rule Rule) []Diagnostic  // Typed, main tree; 内部 = runTypedWithRoot(t, findModuleRoot(t), ...)（frozen 签名不变）
func RunTypedDir(t testing.TB, dir string, opts TypedOpts, patterns []string, rule Rule) []Diagnostic // Typed, standalone fixture-module @ dir
func RunTypedProduction(t *testing.T, opts TypedOpts, rule Rule) []Diagnostic // Typed, production-only (Tests: false, generated/ excluded); 无 patterns（全量生产包）

type TypedOpts struct {
    Tests bool
    Tags  []string
}
```

**Hard 防线三重组合**（满足 AI-rebust 章程 ≥ Medium 立项门槛，组合后构成 Hard）：

| # | 防线 | 等级 | 违反代价 |
|---|------|------|---------|
| 1 | `Pass.Pkg` 是 `*types.Package`，**Pass struct 不暴露 `*packages.Package`** | **Hard**（违反不可表达：archtest 作者拿到 Pass 写不出 `.Syntax`，编译失败） | 必须显式 import `golang.org/x/tools/go/packages` 并自己 Load |
| 2 | depguard 配置：`tools/archtest/*_test.go` 禁 import `golang.org/x/tools/go/packages`、`tools/archtest/internal/scanner`、`tools/archtest/internal/typeseval` | **Hard**（违反需修改 `.golangci.yml` allowlist，构成显著扩大改动面 + 声明意图） | 必须同 PR 改 depguard 配置，review 必抓 |
| 3 | 元治理 archtest `PASS-FUNNEL-01`（type-aware）：archtest 包外 `*_test.go` 调用 `scanner.EachFile` / `typeseval.LoadPackages` / `typeseval.LoadProductionPackages` 触发 fail，迁移期 allowlist 暂豁免 | **Hard**（type-aware 检查，调用 target 经 `*types.Info` resolve；allowlist 是 declared path constant set，参考 ai-collab.md "string-typed concept funnel" 范本） | 不可绕过；新增违反必须改 allowlist |

#1 + #2 同时拦住"在新文件写 INV-1 形态"——AI 必须同时编辑业务文件 + depguard config 才能引入双视图捕获，构成不可表达级别的护栏。#3 是迁移期过渡，阶段 4 后清空（无 enforcement 残留）。

## 六轴取数现状与去向

| 轴 | 现状入口 | Pass 端态去向 |
|----|---------|--------------|
| 1 Go 源码 | `scanner.EachFile` + `typeseval.LoadPackages` + 裸 for-range | **合并到 `archtest.Pass + Run/RunTyped/RunTypedDir/RunTypedProduction`** |
| 2 非 .go 内容 | `scanner.EachContentFile` / `LoadContentFiles` | 重命名 `archtest.EachContentFile`，签名不变 |
| 3 模块元数据 | `modfile.Parse` 读 go.mod | 独立保留 |
| 4 Build constraint | `typeseval.ParseBuildConstraint` / `IsGeneratedRelPath` | 收进 `Pass.IsFileInScope(file)` 方法 |
| 5 依赖图视图 | `kernel/depgraph` | 独立保留 + ADR 划界 |
| 6 depguard 配置 | `.golangci.yml` 解析 | 独立保留 |

原始 IO 已被 `SCANNER-FRAMEWORK-USAGE-01` 在 Path A/A'/B 三路 type-aware 收口，不在合并范围。

## 实施计划（4 阶段、~10 PR）

### 阶段 1 — PR-1 落地 Pass 框架 + 三重 Hard 防线（Cx3，业务文件 0 改动）— ✅ shipped as PR #492 (2026-05-14)

**关键设计**：业务 *_test.go 在阶段 1 **完全不被 touch**。enforcement 推到 `Pass` 类型设计 + depguard 配置 + 元治理 archtest 三层，allowlist 临时豁免存量。阶段 3 每个迁移 PR 是对应业务文件的**首次 + 唯一**改写。

**范围**：

1. 新建 `tools/archtest/pass.go`：`Pass` struct（`Pkg *types.Package`，**不是** `*packages.Package`）、`Rule` 类型、`Run` / `RunTyped` driver、`TypedOpts`、`Diagnostic`、`Report`
2. 新建 `tools/archtest/walk.go`：导出 `EachInSubtree[N]` / `EachInChildren[N]` / `StringLitValue` / `ReceiverTypeName`（实现委托到 internal/scanner，对外仅 archtest 包导出）
3. 新建 `tools/archtest/scope.go`：导出 Scope + helper（同上委托）
4. 新建 `tools/archtest/content.go`：导出 `EachContentFile` / `LoadContentFiles`
5. **scanner / typeseval 包保持原状**——保留导出 API，由 depguard + 元治理 archtest 在调用方阻挡新使用
6. 新建 `tools/archtest/internal/archtestmeta/legacy_allowlist.go`：

```go
package archtestmeta
// LegacyAllowlist lists archtest files that have not yet migrated to
// archtest.Pass. Each entry is deleted by the PR that migrates the
// corresponding file. MUST be empty by stage-4 completion.
var LegacyAllowlist = []string{
    "tools/archtest/accesscore_facade_test.go",
    // ... 30 个文件（脚本自动列出阶段 1 时点的存量）
}
```

7. 新建 `tools/archtest/pass_funnel_test.go`：元治理 archtest，三条规则
   - `PASS-FUNNEL-EACHFILE-01`：archtest *_test.go 调用 `scanner.EachFile` → 若文件不在 LegacyAllowlist 则 fail（type-aware via `*types.Info`）
   - `PASS-FUNNEL-LOADPACKAGES-01`：调用 `typeseval.LoadPackages` / `typeseval.LoadProductionPackages` / `typeseval.SharedResolver` → 同上规则
   - `PASS-FUNNEL-PACKAGES-IMPORT-01`：`tools/archtest/*_test.go` 文件 import `golang.org/x/tools/go/packages` → 若文件不在 LegacyAllowlist 则 fail（覆盖未来潜在绕过）

8. 修改 `.golangci.yml`：depguard rule
```yaml
linters-settings:
  depguard:
    rules:
      archtest-no-direct-packages-load:
        files: ["tools/archtest/*_test.go"]
        deny:
          - pkg: "golang.org/x/tools/go/packages"
        # allowlist exception: 30 legacy files (synchronized with LegacyAllowlist)
```

**注**：`internal/scanner` 和 `internal/typeseval` 从未加入 depguard deny 列表（上方示例为原始草稿，实际未实施）。这两个包的 walk/resolve helper（`EachInSubtree`、`EachInChildren`、`ResolvePackageRef`、`EvaluateConstString` 等）允许业务 archtest 直接 import 和使用；symbol 级别的精确拦截（`EachFile`、`LoadPackages`、`SharedResolver` 等）由元治理 archtest 防线 #3 通过 `*types.Info` resolve 完成，lint 路径级别只封 `golang.org/x/tools/go/packages` 这个 load-bearing INV-1 重构原语。见 ADR §Why-depguard。

允许暂存 allowlist 在 yaml 注释 + Go 代码两处冗余声明，Stage-4 清零时一并删。

9. 新建 ADR `docs/architecture/<ts>-adr-archtest-pass-funnel.md`：业界对标 + 三重 Hard 防线 + 迁移路径

**业务 archtest 改动**：**零**。

**验证**：
- `go build ./...` + `go test ./tools/archtest/...` 全绿
- 元治理 archtest 三条规则全绿（allowlist 内文件被豁免，allowlist 外文件目前无违反）
- 故意在新建 archtest 中调 `scanner.EachFile` → 三重防线（depguard + PASS-FUNNEL-EACHFILE-01 + Pass.Pkg 不暴露 .Syntax）任一触发 fail

### 阶段 2 — PR-2 ~ PR-5：A 类 36 处 EachFile 主题分批迁移（Cx3 × 4 PR，可并行）— ✅ 全部 shipped (2026-05-15)

按 archtest 主题分批，每 PR 约 8-10 个文件，**与阶段 3 互相独立**（不同代码模式，可同期推进）：

- **PR-2**: governance / metadata 主题 — ✅ shipped as PR #496
- **PR-3**: contract / codegen 主题 — ✅ shipped as PR #493
- **PR-4**: observability / health 主题 — ✅ shipped as PR #498
- **PR-5**: lifecycle / errcode / panic 等其余 — ✅ shipped as PR #497

**每个迁移 PR 的完整动作**（业务文件的首次 + 唯一改写）：

1. 把目标 archtest 从 `scanner.EachFile(t, scope, mode, fn)` 改写为 `archtest.Run(t, scope, func(pass *Pass) { ... })`
2. 同时改 import：去掉 `tools/archtest/internal/scanner`，加 `tools/archtest`（B/D 类 EachInSubtree / Scope / Diagnostic 等同时换前缀，**该文件相关的机械重命名一次性完成**）
3. 从 `archtestmeta.LegacyAllowlist` + `.golangci.yml` 注释 allowlist 删除对应文件
4. 元治理 archtest 立即对该文件生效

**关键性质**：每个文件的 import 路径 + API 前缀 + 语义迁移 **在同一 commit 中完成**，业务文件 **0 二次返工**。

### 阶段 3 — PR-(N+1) ~ PR-(N+5)：E 类 48 处裸 for-range 主题分批迁移（Cx3 × 5 PR，可并行）

按 archtest 主题分批，每 PR 约 6-8 个文件：

- **PR-6**: `clock_invariants_test.go`（8 typed-load site：5 主树 RunTyped + 3 fixture-scan RunTypedDir；含 Stage 1.6 框架补全，单 PR）— ✅ shipped as PR #500
- **PR-7**: `errcode_invariants_test.go` + `auth_bootstrap_invariants_test.go`（含 Stage 1.7 RunTypedProduction 框架补全）— ✅ shipped as PR #507
- **PR-8 ~ PR-10 → PR #522 consolidated-batch** ✅ shipped 2026-05-16: 原估 27 个文件，实测 37 文件（PR-9 单批级联 46 文件，证明各批之间 shared-helper 图谱强耦合，batch 非独立）。三路并行评估后决策合并为单一 PR #522 顺序迁移。3 个 ESCALATE 条目同 PR 处理：`archtest_test.go` reclassify 为永久豁免（加入 `passFunnelPermanentExempt`）；`governance_rules_invariants_test.go` 与 `scanner_framework_usage_test.go` 真迁移完成（LegacyAllowlist 删除条目）。LegacyAllowlist 清零。

每 PR 范式同阶段 2：业务文件首次 + 唯一改写，`typeseval.LoadPackages` → `archtest.RunTyped` / `RunTypedProduction`，import 切换 + allowlist 删除一次完成。

**性能**：`RunTyped` / `RunTypedDir` / `RunTypedProduction` 内部继承现有 `typeseval.SharedResolver` 的 singleflight + process-wide cache，多 archtest 共享一次 packages.Load。迁移不会导致 CI 时长退化。

**与阶段 2 的关系**：阶段 2 (EachFile) 与阶段 3 (for-range) 改的是不同代码模式，**可整体并行**。同一 archtest 同时用两种入口的情况罕见（盘点：≤ 3 个文件）；这些文件归入主题对应的 PR 一次性完整迁移。

### 阶段 4 — PR-(N+6) 收尾（Cx1）

1. 验证 `archtestmeta.LegacyAllowlist` 与 `.golangci.yml` allowlist 均为空（应当如此）
2. 删除 `archtestmeta` 包整体（仅服务于迁移期）
3. 删除 `.golangci.yml` 的 allowlist 例外（保留 deny 规则）
4. 删除元治理 archtest `pass_funnel_test.go` 中的 allowlist 引用（保留 `Pass.Pkg 不暴露 packages.Package` 与 depguard 作为永久防线）
5. 把 `tools/archtest/internal/scanner` + `tools/archtest/internal/typeseval` 包名重命名（如 `_legacy_*`）或直接挪进更深的 internal——可选，因为 depguard 已经永久封死外部访问
6. 更新 `.claude/rules/gocell/ai-collab.md`：把 archtest.Pass 列为 Go 源码扫描的唯一推荐路径

## 并行分析

| 阶段 | PR 数 | 内部并行 | 阶段间并行 |
|------|------|---------|-----------|
| 1 | 1 | — | — |
| 2 (EachFile 主题分批) | 4 | **可全并行**（不同 archtest 文件 PR） | **可与阶段 3 并行** |
| 3 (for-range 主题分批) | 5 | **可全并行** | **可与阶段 2 并行** |
| 4 | 1 | — | 等阶段 2 + 3 全部完成 |

**并行度峰值**：阶段 2 + 阶段 3 同时进行时理论 9 个 PR 互相并行。

**实际推荐节奏**：
- 阶段 1 单 PR，merge 后启动阶段 2/3
- 阶段 2/3 维持 2-3 个并发 PR（受 review 资源限制）
- 9 个 PR 滚动推进，约 2-3 周完成阶段 2+3
- 阶段 4 单 PR 收尾

**冲突点**：同一 archtest 文件若同时被阶段 2 + 阶段 3 改（罕见，≤3 个文件），归入 EachFile 主题的 PR 一次性完整迁移，不分两次。

## 原则符合性核对

| 原则 | 符合性 | 说明 |
|------|-------|------|
| **彻底** | ✅ | 阶段 4 已完成：archtestmeta 包整体删除，LegacyAllowlist 过滤逻辑清零，depguard yaml 迁移期注释收尾，TestArchtestmetaPackageDeleted 静态反向锁防回退（PR #PENDING 2026-05-17） |
| **不向后兼容** | ✅ | 无 deprecation 别名、无 FixtureBuildTag re-export、无双路径并存；archtestmeta 整体删除不留过渡通道 |
| **0 二次返工** | ✅ | 业务文件只在它的语义迁移 PR 中被 touch 一次；Stage 1.6 双入口设计 D1 + Stage 1.7 RunTypedProduction 明确保护已 ship PR 调用点；RunTypedFixture 收口 fixture 加载，6 处调用一次完成迁移 |
| **优雅简洁** | ✅ | Pass + Rule + Run/RunTyped/RunTypedDir/RunTypedProduction/RunTypedFixture 五个核心入口；TestPassFunnelGuardListSync 三段合并为两条单一等式断言 |
| **AI HARD** | ✅ | 三重防线 + 新 Hard 范本：(1) Pass.Pkg 不暴露 `*packages.Package` 让 INV-1 形态在新代码中编译失败；(2) depguard 锁住 packages 直接 import（终态仅 3 个 self-exemption）；(3) 元治理 archtest type-aware 拦截存量入口；(4) `RunTypedFixture + FixtureOpts{Tests}` 不含 Tags 字段——业务调用方在 type system 上无法表达"自传 build tag" |

## 关键设计决策

### D1 — `Pass.Pkg` 用 `*types.Package`，不用 `*packages.Package`

业界共识（go/analysis、staticcheck）。`*types.Package` 暴露 Name / Path / Imports / Scope 等类型层信息，**不暴露 `.Syntax`**。用户拿到 Pass 在 archtest 包外**写不出**裸 for-range Syntax 的代码——编译失败而非 archtest 拦截，是真正的 Hard。

### D2 — 不私有化 `*types.Info`、不挂 Pass 方法

业界共识。`pass.TypesInfo` 公开访问，helper 函数 `archtest.ResolveSelector(info, expr)` 等接 info 而非 Pass，与现有 `typeseval.ResolvePackageRef` 范式延续。私有化方案改造面大且 INV-1 真正护栏在 D1（不暴露 `*packages.Package`），D2 是简洁性选择。

### D3 — allowlist 用文件路径常量数组（非 // 注释豁免）

写在 `archtestmeta.LegacyAllowlist` 单文件 Go 数组中：

- 单源、可 grep、可 diff 跟踪
- 删一项是单行 diff，review 直观
- 不污染业务 archtest 文件
- 与 ai-collab.md "string-typed concept funnel" 设计范本一致

### D4 — depguard 与元治理 archtest 双层并存

depguard 是 lint 期路径级拦截（Hard，需修配置才能绕）；元治理 archtest 是 type-aware 调用拦截（Hard，需修 allowlist）。两层独立失效模式：

- 若 AI 试图通过 vendor 或 type alias 绕过 depguard，元治理 archtest 的 type-aware resolve 仍然抓到
- 若 AI 试图修改元治理 archtest，depguard 拦住 import 第一步

冗余防线在生产代码中是 over-engineering，在元治理中**是正确做法**——元治理失效成本极高（INV-1 类 bug 会污染所有依赖的规则）。

### D5 — 不需要"AST-only Pass 也能调 ResolveSelector"的伪类型路径

AST-only 模式下 `pass.TypesInfo == nil` / `pass.Pkg == nil`，helper 函数自然短路返回 `(zero, false)`。调用方必须在选择 `Run` (AST-only) vs `RunTyped` / `RunTypedDir` / `RunTypedProduction` 时明确 input domain，编译时不可混淆。

### D6（Stage 1.6）— 双入口 RunTyped / RunTypedDir，不收敛单函数

见上方「阶段 1.6 ship 摘要 · 设计决策 D1」。核心：`RunTyped` 调用方（24 A-class + 已 ship PR）零返工；`RunTypedDir` 是新增 fixture-module 形态的唯一合法入口，两者共享 `runTypedWithRoot` 单一构造路径，不分叉。

### D7（Stage 1.7）— RunTypedProduction 独立入口，不合并进 TypedOpts flag

见上方「阶段 1.7 ship 摘要 · 设计决策 D2」。核心：production-only 语义作为独立入口名，选错语义 = 选错 API 名；不为已 ship 调用点引入任何返工。

## 验收方式

### 单元验证（每 PR 必跑）

```bash
go build ./...
go build -tags=integration ./...
hack/verify-archtest.sh
golangci-lint run --new-from-rev=main  # 验 depguard 规则正确触发
```

### 阶段切换验证

```bash
go test -tags=integration ./...
make verify
```

### 范式回归验证

每个迁移 PR：
1. production source pass 行为零变化（migration 是重写不是改逻辑）
2. negative fixture pass：构造 INV-1 形态 mock，确认 D1 编译失败 + D2 / D3 拦截
3. 性能基线：`go test ./tools/archtest/...` wall-clock 不超过现状 1.1x

### 阶段 4 收尾端到端验证

1. 新建 archtest 试图 `import "golang.org/x/tools/go/packages"` → depguard fail
2. 新建 archtest 试图 `Pass.Pkg.Syntax` → 编译 fail（D1 Hard）
3. `archtestmeta.LegacyAllowlist == nil` 静态断言
4. `git log --grep "allowlist"` 应能逐 PR 追踪每个文件的迁移轨迹

## 范围外（明确不做）

1. 私有化 `*types.Info`（D2 决策）
2. 收口 `kernel/depgraph` 视图到 Pass（轴 5 独立 ADR 划界）
3. 合并 `.golangci.yml` depguard 配置到 Pass 体系（轴 6 独立）
4. 重写 `SCANNER-FRAMEWORK-USAGE-01` 元治理（已是 Hard 形态）
5. 引入"per-file 回调"形态（用户在 Rule 内自己 `for _, file := range pass.Files`，与 go/analysis 一致）

## 关键文件清单

**新建**:

| 文件 | 阶段 | 内容 |
|------|------|------|
| `tools/archtest/pass.go` | 1 | Pass struct + Rule + Run / RunTyped / RunTypedDir / RunTypedProduction driver + TypedOpts |
| `tools/archtest/walk.go` | 1 | EachInSubtree / EachInChildren / StringLitValue / ReceiverTypeName 重导出 |
| `tools/archtest/scope.go` | 1 | Scope + 配置 helpers 重导出 |
| `tools/archtest/content.go` | 1 | EachContentFile / LoadContentFiles 重导出 |
| `tools/archtest/diagnostic.go` | 1 | Diagnostic / Report 重导出 |
| `tools/archtest/internal/archtestmeta/legacy_allowlist.go` | 1 | LegacyAllowlist 数组（阶段 4 删除） |
| `tools/archtest/pass_funnel_test.go` | 1 | PASS-FUNNEL-01/02/03 元治理 archtest |
| `docs/architecture/<ts>-adr-archtest-pass-funnel.md` | 1 | 范式 ADR |
| `tools/archtest/testdata/errorfirsttypednilfixture/` | 1.7 | RunTypedDir fixture module for ERROR-FIRST-TYPED-NIL-01 Soft→Hard upgrade |

**修改**:

| 文件 | 阶段 | 改动 |
|------|------|------|
| `.golangci.yml` | 1 | 新增 depguard rule `archtest-no-direct-packages-load` + allowlist |
| `tools/archtest/*_test.go`（约 30 个迁移 + 30 个仅 import 切换） | 2/3 | import 切换 + EachFile / for-range 改写 + allowlist 删除（每文件一次性 commit） |
| `.claude/rules/gocell/ai-collab.md` | 4 | 把 archtest.Pass 列入载体决策原则推荐路径 |

**复用**（已有、不需改）:

| 文件 | 用法 |
|------|------|
| `tools/archtest/internal/scanner/parse.go` `EachFile` | Run driver 内部委托 |
| `tools/archtest/internal/scanner/eachnode.go` `EachInSubtree` / `EachInChildren` | walk.go 转发 |
| `tools/archtest/internal/scanner/scope.go` `Scope` / `ModuleScope` / `DirsScope` | scope.go 转发 |
| `tools/archtest/internal/typeseval/typeseval.go` `SharedResolver` | RunTyped / RunTypedDir / RunTypedProduction driver 内部复用 |
| `tools/archtest/scanner_framework_usage_test.go` | 已有 Path A/A'/B 元治理范式，pass_funnel_test.go 参考其结构 |

## 风险与权衡

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 阶段 1 PR-1 体量过大 | 中 | review 负担 | 拆为 1a (Pass + Rule + Run/RunTyped + ADR) / 1b (walk + scope + content + diagnostic 重导出) / 1c (allowlist + 元治理 archtest + depguard) 三个小 PR，串行推进 |
| 阶段 2/3 并行 PR git 冲突 | 低 | merge 痛苦 | 主题分批本身减少冲突面；同文件冲突的 ≤3 个 archtest 归入单 PR |
| 元治理 archtest 误判 false positive | 低 | CI 误阻 | 阶段 1 PR-1 merge 后先观察 3-5 天再启动阶段 2 |
| RunTyped 性能退化 | 极低 | CI 时长 | SharedResolver singleflight 不变，packages.Load 仍一次性 |
| Allowlist 遗忘条目残留 | 中 | 长期半成品 | 阶段 4 必须 `archtestmeta.LegacyAllowlist == nil` 静态断言；project-manager 跟踪每个迁移 PR 是否删 allowlist 项 |
| `*types.Package` 不够用、需要回到 `*packages.Package` | 极低 | 设计退化 | 业界 go/analysis 验证 `*types.Package` 已覆盖所有 archtest 用例；真有特例可加 `Pass.Imports() []string` 等具名方法暴露所需子集，不暴露 `*packages.Package` 整体 |
| fixture-module 形态（testdata/ 独立 go.mod）未被 RunTyped 覆盖 | 已发生 | PR-6 clock 首撞 | Stage 1.6 RunTypedDir 端态补全；后续 5 个 E-class fixture-module 文件直接用 RunTypedDir，零框架返工 |
| production-only 扫描作者遗漏 RunTypedProduction 改用 RunTyped+IsGenerated | 中 | 上游 Medium 软逃逸 | Stage 1.7 RunTypedProduction 入口 + PASS-FUNNEL-LOADPACKAGES-01 ban LoadProductionPackages 直调；上游 Hard 化登记 backlog PASS-PRODUCTION-UPSTREAM-HARD-01 |
