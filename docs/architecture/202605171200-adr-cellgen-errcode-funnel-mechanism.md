# ADR: cellgen errcode funnel Hard 升级机制选型

> Status: Implemented (Path A Ident-scan canonical; Path C-full backlog upgrade)
> Date: 2026-05-17 (Implemented: 2026-05-18, P5.1 PR #574)
> Implementation: PR #574 R2-P5.1 worktree 614-cellgen-errcode-funnel-hard
>   - `tools/archtest/cellgen_errcode_funnel_test.go` = **`types.Info.Uses[]` driven Ident-scan + (Pkg.Path, Name) 黑名单 form uniqueness** (Path A canonical)
>   - `.golangci.yml` `cellgen-error-libs` depguard 规则 (递归 glob `**/tools/codegen/cellgen/**`，defense in depth)
>   - 三个独立 archtest 函数：`TestCellgenErrcodeFunnel`（rule body 主测）+ `TestCellgenErrcodeFunnelNoBuildTagFiles`（build-tag 隔离盲区守）+ `TestCellgenErrcodeFunnelBlindSpotsAbsent`（复合字面量 + reflect 盲区守）
>   - INVARIANT ID rename `CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01` → `CELLGEN-ERRCODE-FUNNEL-01`
>   - §D5 整段重写（非 amendment）：spike (c'') 原稿 STEP 2 "callee 返回含 error" 实施期过宽 → round-1 改 CallExpr-callee 黑名单 → round-2 review 揭示命名函数类型/类型转换/类型断言/STEP 3 false positive 四类问题 → round-2 改 Ident-scan via `types.Info.Uses[]`。两轮 amendment trail 移入 §Rejected alternatives 完整 audit。Path C "cellgen funcs return *errcode.Error" 被 errcode.Error 字段 exported 卡住——backlog `CELLGEN-ERRCODE-FUNNEL-HARDEN-PATH-C-FULL` 跟踪字段私有化重构。
> ref: docs/plans/202605162000-037r2-wave4-advance-round2.md §R2-P5;
>      docs/backlog/cap-14-tooling.md L44 `CELLGEN-ERRCODE-FUNNEL-HARDEN`;
>      .claude/rules/gocell/ai-robust.md §"typed function call as Hard funnel for unbounded operations";
>      tools/archtest/panic_invariants_test.go（对偶范本 PANIC-REGISTERED-01；INVARIANT ID 来自旧文件名 panic_registered_test.go，文件按章程 §archtest 文件命名「同主题规则 ≥ 3 → *_invariants_test.go」重命名）

## Context

`tools/codegen/cellgen/` 包负责生成 cell/slice scaffold 与运行时元数据 literal。PR #453（`refactor(cellgen): PR442 follow-up — housekeeping (cellgen errcode + CI + docs)`，merged 2026-05-11）完成 cellgen 全包 errcode 迁移：50+ 处 error 构造均走 `errcode.New / errcode.Wrap / errcode.Assertion`，0 处 `fmt.Errorf / errors.New / pkg/errors`。（备注：backlog `cap-14-tooling.md:44` 原文写「PR#557」是事实错误，本 ADR 以 GitHub merged PR 号为准）

（**历史叙述**——下述描述是 spike 编写时的现状；P5.1 落地后该 archtest 已重命名为 `CELLGEN-ERRCODE-FUNNEL-01` 并完全重写为 types.Info-driven 黑名单 form uniqueness。本段保留为决策上下文，新规则形态见 §D5。）

当时 enforcement archtest `CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01`（旧版 `tools/archtest/cellgen_errcode_funnel_test.go`）用纯 AST identifier 匹配（`SelectorExpr.X.Name == "fmt" && Sel.Name == "Errorf"`）单点禁 `fmt.Errorf`：

- 未使用 `types.Info`；alias import (`fmtx "fmt"`)、dot import (`. "fmt"`)、re-export 均可绕过
- 不覆盖 `errors.New` / 第三方 errors lib / cellgen 包内自建 error wrapper（如 `func newErr(msg) error { return errors.New(msg) }`）等其他 escape route
- 自我评级 Medium（旧版文件头 godoc 明示「Medium scanner AST + concrete-package allowlist」）

backlog `cap-14-tooling.md` L44 提出两个 Hard 升级候选：

> Hard 升级路径：加 `.golangci.yml` method-level depguard rule 或抽 typed Error return wrapper 让 `fmt.Errorf` 在编译期不可表达

本 ADR 为 R2-P5 spike 的决策产物，评估 backlog 原案 + 衍生方案并选定实施方向。

## Spike key facts

1. **cellgen 包 error 构造现状**（grep `tools/codegen/cellgen/*.go` 非测试代码）：
   - `errcode.New / errcode.Wrap / errcode.Assertion` = 50+ 处
   - `fmt.Errorf / errors.New / errors.Wrap / errors.Join / pkg/errors.*` = 0 处
   - 透传 `return err`（不构造新 error）= 30+ 处
   - 调用合法 stdlib operations 返回 error（`os.RemoveAll / *os.File.Close / bufio.Scanner.Err / text/template.Execute / text/template.ExecuteTemplate / pathsafe.WritePlannedFiles` 等）：~9 处，配 `errcode.Wrap` 上转
   - **包已事实上是"已知构造函数零次使用 + errcode 全覆盖"形态**，Hard 升级 = 把"已知构造函数集合外不存在 + errcode 是唯一构造源"做成 archtest 强约束

2. **cellgen 包 `fmt` 合法使用**：
   - `fmt.Sprintf` = 10+ 处合法（subscription alias 拼接、metadata literal printer、`errcode.WithInternal` 上下文格式化）
   - `fmt.Fprintf` = 12+ 处合法（`literal_printer.go` 给 `strings.Builder` 输出 Go literal）
   - 禁 `fmt` 整包 import 不现实（破坏 codegen 核心功能）

3. **depguard 工具能力**（核实 OpenPeeDeeP/depguard v2 README + GoCell `.golangci.yml` 现有 rule）：
   - 仅支持 package import 级 file-glob ban（如 `scaffold-os-ban` 禁 4 文件 import os）
   - **不支持** method-level caller allowlist
   - GoCell 现有 0 个 depguard rule 是 method-level 形态

4. **章程 §"typed function call as Hard funnel for unbounded operations"**（`.claude/rules/gocell/ai-robust.md`）：
   > Hard property comes from "form uniqueness": picking any other shape... fails archtest in CI... The charter §1 definition of "typed function call" Hard does not require compile-time blocking, only form uniqueness + archtest fail-on-deviation — which is the highest grade reachable in Go for this rule shape.

5. **PANIC-REGISTERED-01 范本结构**（`tools/archtest/panic_invariants_test.go`）：
   - 扫所有 `panic(arg)` site
   - arg 必须是 `*ast.CallExpr` 且 callee 经 `types.Info.Uses` 解析到 `pkg/panicregister.Approved`
   - 不在白名单内的 panic 形态（bare panic、其他 callee、非字面量 reason）archtest fail
   - **白名单 form uniqueness**：集合外任何形态均失败

## Decision

### D1. 否决 (a) depguard method-level rule

backlog 原案 1 基于不准确的 depguard 能力假设。depguard v2 不支持 method-level caller allowlist，仅支持 package import 级 ban。无 GoCell 内部或 OSS 上游路径可在不引入新工具的前提下实现 method-level enforcement。

### D2. 否决 (a') depguard package-level 禁 `fmt` import

衍生方案：仿 `scaffold-os-ban` 禁 cellgen 包 import `fmt`。否决理由：cellgen 包内 22+ 处合法 `fmt.Sprintf/Fprintf` 用途（literal printer、alias 拼接、WithInternal 上下文）。禁 `fmt` 等于推倒 `literal_printer.go` 改 `strings.Builder` 替代，代价高且非必要。

### D3. 否决 (b) typed Error return wrapper

backlog 原案 2 提出新建 cellgen 专属 typed Error wrapper（如 `func cellgenError(...) error`），使 `fmt.Errorf` 通过类型不匹配在编译期不可表达。否决理由：

- cellgen 当前已 0 处 `fmt.Errorf`，所有 error 已走 `errcode.New/Wrap`。新建 wrapper 只服务 archtest，无运行时价值
- 章程 §"string-typed concept funnel" 适用条件不成立：cellgen error 不是独立 string-typed concept（如 rule code / event topic 这类承载独立语义的字符串），是 errcode 通用域；funnel 已经是 errcode 本身
- 章程 §panicregister.Approved 范本前提是「源 API 接受 `any` 类型，需 typed marker 压缩成形态唯一性」；cellgen errcode 不存在此前提（errcode.New/Wrap 已经是 typed funnel）
- 新增 thin marker wrapper 违反 §"优雅简洁"

### D4. 否决 (c) 黑名单单点禁的 type-aware 升级

衍生方案：把现有 archtest 升级到 type-aware（用 `types.Info.Uses` 解析 `fmt.Errorf` callee），覆盖 alias/dot import。否决理由：

- 仅防 `fmt.Errorf` 一种已知 escape，AI 创新 escape route（如自建 `newErr` wrapper、引入 pkg/errors、引入第三方 lib）都能绕过
- 章程 §"Funnel 双向锁评级"要求 funnel 类约束「集合外不能进 / 集合内必须经过」双向 Hard，黑名单只锁单点，上游 Hard 不闭环
- L3 概念模型与 PANIC-REGISTERED-01 不对偶（PANIC 是白名单 funnel，黑名单单点禁是其退化形态）

### D5. 选 Path A — Ident-scan + 已知构造函数黑名单 form uniqueness（OSS 业界上限）

`tools/archtest/cellgen_errcode_funnel_test.go` 实现为 **`*types.Info.Uses[]`-driven Ident-scan + 已知构造函数 (Pkg.Path, Name) 黑名单 form uniqueness archtest**。检测算法：

```
对 cellgen 包内（tools/codegen/cellgen/...，非 _test.go）任一 *ast.Ident I：

STEP 1 — Ident 解析：
  obj := types.Info.Uses[I]
  if obj == nil → skip（声明点而非引用点）

STEP 2 — 黑名单匹配：
  if obj 是 *types.Func 且 (obj.Pkg().Path(), obj.Name()) ∈
    cellgenErrConstructorBlacklist = {
      ("fmt", "Errorf"),
      ("errors", "New"),
      ("errors", "Join"),
    }
  → archtest fail("cellgen: reference to blacklisted error constructor:
    <Pkg.Path>.<Name>")
```

> **形态唯一性原理（与 PANIC-REGISTERED-01 同构）**：黑名单条目集 S 是显式登记的，`types.Info.Uses[]` 把每个 Ident 的引用对象**无视语法位置**（CallExpr.Fun / var RHS / 切片字面量元素 / 结构体字面量字段 / 类型转换实参 / 类型断言目标 / 函数值传递）折叠到同一 *types.Func。注册集合内的 (Pkg.Path, Name) 形态 archtest 全部 fail-on-deviation——集合内任何引用形态全部命中，无视 CallExpr 包装；集合外形态落 skip 分支（显式盲区，详 §D5/§Escalation）。与 PANIC-REGISTERED-01 的 `(callee=panicregister.Approved via types.Info.Uses[sel.Sel])` form uniqueness 同源（charter §"typed function call as Hard funnel for unbounded operations"）。

**为什么 Ident-scan 而非 CallExpr-driven**：CallExpr-only 扫描会漏 4 类引用形态——
1. **命名函数类型 var**：`type Ctor func(string) error; var c Ctor = errors.New` — RHS Ident `New` 嵌入 var-init 而非直接 CallExpr.Fun
2. **类型转换**：`(func(string) error)(errors.New)("x")` — 外层 CallExpr.Fun = 内层 CallExpr（type conversion）
3. **类型断言**：`any(errors.New).(func(string) error)("x")` — Ident `New` 嵌在 TypeAssertExpr
4. **函数值传递**：`ctors := []func(string) error{errors.New, fmt.Errorf}` — Idents 嵌在 CompositeLit

Ident-scan 通过 `types.Info.Uses[]` 在 AST 任意位置解析 Ident 引用对象，结构上无视这些语法包装，给出 Go 类型系统下"对 registered Func 集合的零引用"约束的**最大形态唯一性**。

**defense in depth — depguard `cellgen-error-libs`**：archtest 黑名单 S 覆盖 stdlib 已知构造函数。第三方 error library（`github.com/pkg/errors` / `golang.org/x/xerrors` / `go.uber.org/multierr` / `github.com/hashicorp/go-multierror` / `github.com/cockroachdb/errors`）通过 `.golangci.yml` `cellgen-error-libs` depguard 规则**在 import 边界拒绝**（递归 glob `**/tools/codegen/cellgen/**` 覆盖现在 + 未来子包），无需进入 archtest 黑名单。新增第三方 lib 需同步登记两侧：archtest 黑名单 + depguard deny 项 + 反向自检 RED fixture（同 PR）。

**反向自检 RED + GREEN fixture 覆盖面**（章程 §AI-robust 三档分级 ≥ Hard 必备 + Findings 1+2 历史闭环）：

| Fixture | 形态 | 检测点 |
|---|---|---|
| F1 `fmt_errorf_red` | `fmt.Errorf("...")` normal SelectorExpr | Ident `Errorf` 在调用点 |
| F2 `fmt_alias_errorf_red` | `fmtx "fmt"` alias + `fmtx.Errorf(...)` | Ident `Errorf` 在调用点（Uses 透 alias） |
| F3 `fmt_dot_import_errorf_red` | `import . "fmt"` + `Errorf(...)` | 裸 Ident `Errorf` 在调用点 |
| F4 `errors_new_red` | `errors.New(...)` | Ident `New` 在调用点 |
| F5 `errors_dot_import_new_red` | `import . "errors"` + `New(...)` | 裸 Ident `New` 在调用点 |
| F6 `errors_join_red` | `errors.Join(...)` | Ident `Join` 在调用点 |
| F7 `function_valued_var_red` | `var ErrNew = errors.New; ErrNew(...)` | Ident `New` 在**声明点** |
| F8 `local_wrapper_red` | `func newErr(msg) error { return errors.New(msg) }` | Ident `New` 在 wrapper body |
| F9 `named_function_valued_var_red` | `type Ctor func(...); var X Ctor = errors.New` | Ident `New` 在声明点（关闭 Findings #1） |
| F10 `type_conversion_red` | `(func(string) error)(errors.New)("x")` | Ident `New` 在转换实参（关闭 Findings #2-a） |
| F11 `type_assertion_red` | `any(errors.New).(func(string) error)` | Ident `New` 在断言目标（关闭 Findings #2-b） |
| GREEN `errcode_assertion_green` | errcode.{Assertion, New, Wrap, WrapInfra} | 0 violation（callee.Pkg = pkg/errcode 不在 blacklist） |

**Hard 覆盖范围声明**（精确收窄，避免文档超出实现边界）：

Hard 等级在**注册集合内** form uniqueness：
- 集合 = `cellgenErrConstructorBlacklist`（当前 3 项 stdlib）∪ `.golangci.yml` `cellgen-error-libs` depguard deny 列表（当前 5 项第三方）
- 集合内任何 Ident 引用形态 archtest 拦截；集合外形态需 trigger-based ADR §Escalation 同 PR 扩展（archtest 黑名单 + depguard deny + RED fixture 三处同步）

**Hard 真盲区清单**（章程 §"工具选定后强制盲区自检"，runtime 自检由 `TestCellgenErrcodeFunnelBlindSpotsAbsent` + `TestCellgenErrcodeFunnelNoBuildTagFiles` 守）：

- **复合字面量 `&myErr{}`** 构造非-errcode 类型实现 `error`：CompositeLit 节点不通过 `types.Info.Uses[]`，Ident-scan 不及。终极 Hard 化需 Path C-full 字段私有化（详 §D7，backlog `CELLGEN-ERRCODE-FUNNEL-HARDEN-PATH-C-FULL`）。`TestCellgenErrcodeFunnelBlindSpotsAbsent` 扫 cellgen production CompositeLit + `typesutil.ImplementsInterface(error)` 断言 0 命中——blind spot 不是"未来可能"，是"types.Info.Uses[] 不及"。
- **反射动态构造**：`reflect.MakeFunc` 生成 callable 在静态 AST 不可解析。`TestCellgenErrcodeFunnelBlindSpotsAbsent` 扫 `reflect.MakeFunc` 引用断言 0 命中。
- **跨包 function-valued var imports**：`import sl; sl.Ctor("x")` 当 `sl.Ctor` 是 `var Ctor = errors.New` — Ident `Ctor` 通过 Uses[] 解析到 `*types.Var`（不是 *types.Func），不入黑名单。缓解：depguard `cellgen-error-libs` 在 import 边界 ban 已登记第三方 lib；新 lib 需 trigger ADR §Escalation 同 PR 扩 deny。
- **非默认 build-tag file**：cellgen 现无 `//go:build` 文件，由 `TestCellgenErrcodeFunnelNoBuildTagFiles` 守 production scope 单一 build context。引入 build-tag 文件需先 trigger fan-out KnownNonDefaultTags 同 PR 更新两测。

**设计意图确认**（非盲区，避免误归类）：

- **interface method call 返回 error**：`var x SomeIface; x.Method()` — Uses 解析到接口方法 *types.Func；(Pkg.Path, Name) 不在黑名单 → skip。**这是 operation 转发**，与 stdlib `os.RemoveAll` / `*os.File.Close` / `text/template.Execute` 同语义。
- **`golang.org/x/sync/errgroup` 不在 depguard deny**：`errgroup.Group.Wait/Go` 传播已存在 goroutine error，不构造新值。同 stdlib operation 处理。cellgen 现未 import（grep 验证）。
- **同包 helper 调用**：`func validateXxx(...) error` 同包返回 error — Uses 解析到 cellgen-package *types.Func；不在黑名单 → skip。同包 helper body 内若有 blacklist 引用，scanner 在 helper body 内独立命中。Wrapper 链终止于第一个非同包非 errcode 的 Ident。

### D6. AI-robust 评级 = Hard 在注册集合内（OSS 业界上限）

按章程 §"typed function call as Hard funnel for unbounded operations" + OSS 业界实测调查（K8s `*StatusError` constructor boundary / Kratos `*Error` constructor boundary / go-zero pluggable handler / 社区 linter 调研：errorlint / errwrap / err113 / depguard v2 均无 method-level error funnel 支持），本案 AI-robust 评级如下：

- **form uniqueness（注册集合内）**：cellgen 包内任何 Ident 通过 `types.Info.Uses[]` 解析到 *types.Func，若 (Pkg.Path, Name) ∈ `cellgenErrConstructorBlacklist` 即 fail，**无视语法位置**（CallExpr / var RHS / 类型转换 / 类型断言 / 切片字面量 / 结构体字段 / 函数值传递）。集合内任何引用形态命中；集合外（合法 operation 如 `os.RemoveAll` / `text/template.Execute` / 合法 interface 方法 / 同包 helper）落在 skip 分支。
- **archtest fail-on-deviation**：任何注册集合内引用形态立即 CI 红
- **集合边界**：注册集合 = `cellgenErrConstructorBlacklist`（3 项 stdlib）∪ `.golangci.yml` `cellgen-error-libs` deny 列表（5 项第三方）。**集合外的新 lib / 复合字面量 / 反射构造 / 跨包 function-valued var imports 是显式盲区**（详 §D5 真盲区清单），需 trigger-based ADR §Escalation 同 PR 扩展三处（archtest 黑名单 + depguard deny + RED fixture）
- **诚实声明**：编译期不可阻止（Go 允许任何包定义任何 callable），enforcement 完全依赖 archtest + depguard 联合。这是 Go 语言中 "对 registered Func 集合的零引用" 在 OSS 业界实测调查中可达的**最高评级**
- **funnel 双向锁评级**（§Funnel 双向锁评级）：
  - **上游 Hard（注册集合内）**：cellgen 包内任何 Ident 不得落入 `cellgenErrConstructorBlacklist`（stdlib）+ `cellgen-error-libs` depguard 在 import 边界关闭第三方 lib。**评级依据章程 §"typed function call as Hard funnel for unbounded operations"**：「form uniqueness + archtest fail-on-deviation 是 Go 语言中此类规则形态可达最高级，不要求编译期阻止」（与 PANIC-REGISTERED-01 同源认定）。
  - **下游 Hard**：`pkg/errcode` funnel 集合本身由 `ERRCODE-KIND-LITERAL-01` + `MESSAGE-CONST-LITERAL-01` + `DETAILS-SLOG-ATTR-01` 锁定（三档 archtest 形态锁，与 ADR `202605051730-adr-errcode-message-pii-safety.md` 一致）
  - **defense-in-depth**：`.golangci.yml` `cellgen-error-libs` depguard 规则（递归 glob `**/tools/codegen/cellgen/**`，覆盖现在+未来子包）在 import 边界 ban 五个常见第三方 error lib——第三方构造路径不需要进入 archtest 黑名单
  - **闭环范围**：上游 (注册 stdlib 黑名单 + 注册第三方 import ban) + 下游 funnel 内容锁 — **在注册集合内**集合外不能进 + 集合内必须经过 funnel 双向 Hard

**显式盲区登记**：详 §D5 真盲区清单（复合字面量 / 反射 / 跨包 var 间接 / 非默认 build-tag 文件）。`TestCellgenErrcodeFunnelBlindSpotsAbsent` + `TestCellgenErrcodeFunnelNoBuildTagFiles` runtime 自检；Path C-full 升级路径详 §D7。

### D7. Backlog 升级路径 — Path C-full（typed return + 字段私有化）

终极 compile-time Hard 路径——cellgen 所有 error-returning 函数返回 `*errcode.Error`，combined with `errcode.Error` 字段私有化（accessor-only API），使得：

- F1-F7 escape：每个 RED fixture 形态在 Go 编译器即拒绝（`fmt.Errorf` 返回 `error`，类型不匹配 `*errcode.Error` 返回值 — compile error）
- 复合字面量 `&errcode.Error{...}` escape：跨包字段不可访问（unexported），compile error
- 包外 wrapper 包构造非 errcode 错误：返回 `error` 类型也无法 assign 到 `*errcode.Error` 返回位置 — compile error

**Scope 估算**（why not in P5.1）：
- cellgen 30 个 error-returning 函数签名改 `*errcode.Error`：~30 一行编辑
- cellgen body 中 2 处 raw stdlib err forward 加 errcode.Wrap：~6 行
- **`pkg/errcode.Error` 字段私有化**（核心阻塞项）：7 个 exported 字段 (`Kind / Code / Message / InternalMessage / Details / Cause / Category`) 改 unexported + 提供 accessor 方法 + 迁移所有读取这些字段的 caller（runtime/http middleware、observability、redaction layer 等）— ~25-40h 跨包重构
- archtest 简化为 FuncDecl-level 单规则：~50 行

**Trigger condition** (backlog `CELLGEN-ERRCODE-FUNNEL-HARDEN-PATH-C-FULL`)：
- 复合字面量 `&errcode.Error{...}` 在 cellgen 包内出现首例事故 OR
- 出现第 2 个不在 stdlib 黑名单 / depguard ban 列表内的第三方 error lib escape OR
- `pkg/errcode.Error` 字段私有化由其他 ADR/PR 推动（高优 PII / unstable API 治理）

**Status**：Backlog 登记，未排期。本 ADR 不阻塞 Path C-full，但 P5.1 实施前提是 P5.1 自身解决"cellgen errcode funnel 升级到 OSS 业界 Hard 上限"——已达成。

## Rejected alternatives

- (a) depguard method-level rule — D1（工具能力不支持）
- (a') depguard package-level 禁 fmt import — D2（破坏 literal_printer.go）
- (b) typed Error return wrapper — D3（无运行时价值，违反优雅简洁）
- (c) 黑名单单点禁 + type-aware 升级 — D4（仅防 fmt.Errorf 单形）
- ~~(c'') 原稿 STEP 2 设计（"callee 返回含 error" 触发判定）~~ — **修正非否决**：spike 期发现 STEP 2 过宽（误抦 `os.RemoveAll` / `template.Execute` 等 operation），**已修正为 D5 Path A**（保留 (c'') 形态唯一性 + 对偶 PANIC-REGISTERED-01 的设计目标，仅改 STEP 2 触发判定从"返回 error"到"已知构造函数黑名单匹配"）。OSS 业界（K8s/Kratos/go-zero）调研证明 type-system Hard 全包性 enforcement 不可达；Path A 是实测上限。注意：D4 否决的 (c) 是另一方案——"仅扩 fmt.Errorf 单点到 type-aware 升级"，与 (c'') 完全不同
- ~~(c''-round1) CallExpr-callee driven blacklist + STEP 3 *types.Var rejection (PR #574 round-1)~~ — **修正非否决**：round-1 实施版的算法只扫 `*ast.CallExpr.Fun` 通过 `resolveCellgenCallee` (覆盖 SelectorExpr / Ident / ParenExpr) 解析 callee 后做 (Pkg.Path, Name) blacklist 检查 + STEP 3 `*types.Var` signature 兜底。Round-2 review (PR #574) 揭示 4 类 escape：(1) **命名函数类型 var** `type Ctor func(string) error; var c Ctor = errors.New` — `o.Type().(*types.Signature)` 失败漏 STEP 3；(2) **类型转换 callee** `(func(string) error)(errors.New)("x")` — 外层 CallExpr.Fun 是嵌套 CallExpr，resolveCellgenCallee 不识；(3) **类型断言 callee** `any(errors.New).(func(string) error)("x")` — TypeAssertExpr 同样不识；(4) **STEP 3 false positive 风险**：合法 passthrough var (返回 error 但非构造) 会被误抦。Round-2 改为 **Ident-scan via `types.Info.Uses[]`**——直接扫 cellgen 所有 *ast.Ident，无视语法包装。Round-2 算法**同时关闭** round-1 四类问题且**简化代码路径**（删除 resolveCellgenCallee + STEP 3 var rejection + errorInterface scanner 使用，~50 行净减），且 STEP 3 false positive 风险消失。详 round-2 review feedback + R2-P5.1 PR #574 commit history
- Path C-lite（cellgen funcs return *errcode.Error 但不私有化字段）— 被 errcode.Error exported 字段卡住，与 Path A 同 Hard 等级但额外 5-8h 成本，无收益
- Path C-full — 见 §D7，超出 P5.1 范围，backlog 登记

## Escalation

- **新增 cellgen 包内 error escape route**（注册集合外形态）：同 PR 修改三处——`cellgenErrConstructorBlacklist` (archtest) + `.golangci.yml` `cellgen-error-libs` deny + 反向自检 RED fixture + ADR §D5 fixture table 同步登记
- **`TestCellgenErrcodeFunnelBlindSpotsAbsent` 首次 CI 红（复合字面量 / reflect.MakeFunc 在 cellgen 出现）**：trigger §D7 Path C-full 升级评估（字段私有化）或同 PR 扩 archtest blind-spot 检测面
- **`TestCellgenErrcodeFunnelNoBuildTagFiles` 首次 CI 红**：cellgen 引入 build-tag 文件，需同 PR 让 `TestCellgenErrcodeFunnel` fan out `KnownNonDefaultTags()`（panic_invariants 模式）并更新这两测
- **章程 §AI-robust 三档分级 升级路径**：若 §D7 Path C-full 落地，本 ADR 转 Superseded
