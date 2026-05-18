# ADR: cellgen errcode funnel Hard 升级机制选型

> Status: Implemented (Path A canonical; Path C-full backlog upgrade)
> Date: 2026-05-17 (Implemented: 2026-05-18, P5.1 PR)
> Implementation: PR R2-P5.1 worktree 614-cellgen-errcode-funnel-hard
>   - 重写 `tools/archtest/cellgen_errcode_funnel_test.go` 到 types.Info-driven 黑名单 form uniqueness (Path A)
>   - 新增 `.golangci.yml` `cellgen-error-libs` depguard 规则 (defense in depth)
>   - 重命名 INVARIANT ID `CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01` → `CELLGEN-ERRCODE-FUNNEL-01`
>   - §D5 amendment: 原稿 STEP 2 "callee 返回含 error" 被实施期发现过宽（误抦 `os.RemoveAll` / `text/template.Execute` 等 operation-that-may-fail），按 OSS 业界 (K8s / Kratos / go-zero) 实践改为已知构造函数黑名单 form uniqueness。Path C "cellgen funcs return *errcode.Error" 被 errcode.Error 字段 exported 卡住——backlog `CELLGEN-ERRCODE-FUNNEL-HARDEN-PATH-C-FULL` 跟踪字段私有化重构。
> ref: docs/plans/202605162000-037r2-wave4-advance-round2.md §R2-P5;
>      docs/backlog/cap-14-tooling.md L44 `CELLGEN-ERRCODE-FUNNEL-HARDEN`;
>      .claude/rules/gocell/ai-collab.md §"typed function call as Hard funnel for unbounded operations";
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

4. **章程 §"typed function call as Hard funnel for unbounded operations"**（`.claude/rules/gocell/ai-collab.md`）：
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

### D5. 选 Path A — 已知构造函数黑名单 form uniqueness（OSS 业界上限）

> **Amendment (2026-05-18, P5.1 实施期)**：原稿 D5 设计 STEP 2 "callee 的 Signature.Results() 含至少一个 type 满足 types.Implements(t, errorInterface)" 在实施期被发现**过宽**——会误抦 `os.RemoveAll(path) error` / `text/template.Execute(buf, data) error` / `*os.File.Close() error` / `bufio.Scanner.Err() error` 等 operation-that-may-fail 函数，因为 Go 类型系统**无法区分** "pure error constructor (fmt.Errorf, errors.New)" 与 "operation 返回 error"——两者签名结构完全一致。
>
> 进一步发现 Path C "强制 cellgen funcs 返回 *errcode.Error" 被 `pkg/errcode.Error` **字段 exported** 卡住：跨包 `&errcode.Error{Code:"x",Message:"y"}` 复合字面量构造在编译期合法，single archtest 规则无法关闭。Path C 升级需先做字段私有化（~25-40h pkg/errcode 重构），不在本 PR 范围。详见 §D7 backlog 路径。
>
> OSS 业界（K8s `*StatusError` / Kratos `*Error` / go-zero）实测调查显示：**没有项目做 Path C 全包性 typed return enforcement**。原因是 Go stdlib 函数返回 `error` 而非 `*MyError`，假设所有 error-returning cellgen 函数返回 *errcode.Error 后，每个 stdlib `if err != nil { return ... }` 位点都需加 wrap shim——不仅落地成本高，且与 K8s/Kratos 文档中 typed return 仅限于 named constructor boundary 的设计冲突。
>
> OSS **真正的 Hard 上限** = 自研 `golang.org/x/tools/go/analysis` 分析器，在**已知构造函数集合** `S` 上做 (Pkg.Path, Name) form uniqueness。`S` 必然是显式登记的（无 type-system 自动派生）；新增 lib 通过 depguard import ban 关闭。本 ADR 据此修订为下述 Path A。

重写 `tools/archtest/cellgen_errcode_funnel_test.go` 为 **types.Info-driven 已知构造函数黑名单 form uniqueness archtest**。检测算法：

```
对 cellgen 包内（tools/codegen/cellgen/*.go 非 _test.go）任一 *ast.CallExpr c：

STEP 1 — callee 解析（覆盖所有 import 形态）：
  根据 c.Fun 形态用 *types.Info 解析到 callee object：
    a. SelectorExpr (pkg.Func)：types.Info.Uses[sel.Sel] →
       *types.Func 或 *types.Var
    b. Ident（dot import 后裸 Ident / 同包符号 / function-valued var）：
       types.Info.Uses[ident] → *types.Func 或 *types.Var
    c. ParenExpr：递归 unwrap
  其他形态（FuncLit / 类型转换 / builtin 如 make/new/panic）→ skip

STEP 2 — 已知构造函数黑名单匹配：
  若 callee 是 *types.Func 且 (callee.Pkg().Path(), callee.Name()) ∈
    cellgenErrConstructorBlacklist = {
      ("fmt", "Errorf"),
      ("errors", "New"),
      ("errors", "Join"),
    }
  → archtest fail("cellgen: error constructor outside errcode funnel:
    <pos>: callee=<Name> from <Pkg.Path>")

STEP 3 — function-valued *types.Var 拒绝：
  若 callee 解析到 *types.Var 且 var 的 signature 返回单一 error
  类型（typesutil.ImplementsInterface(result0, errorInterface)）
  → archtest fail("cellgen: error constructor via function-valued
    variable forbidden: <pos>: var=<name>")
  rationale: 间接寻址通道是 AI 创新 escape，cellgen 现无此模式
```

> **形态唯一性原理（与 PANIC-REGISTERED-01 同构）**：黑名单条目集 S 是显式登记的，types.Info 把所有 import 形态（normal SelectorExpr / 别名 SelectorExpr / dot import Ident / 跨包间接）折叠到同一 (Pkg.Path, Name) pair。集合外的 (Pkg.Path, Name) 形态 archtest 全部 fail-on-deviation；集合内任何形态全部命中。与 PANIC-REGISTERED-01 的 (callee=panicregister.Approved, arg=BasicLit STRING) 双条件 form uniqueness 同构（charter §"typed function call as Hard funnel for unbounded operations" 同源认定）。

**defense in depth — depguard cellgen-error-libs**：archtest 黑名单 S 覆盖 stdlib 已知构造函数。第三方 error library（`github.com/pkg/errors` / `golang.org/x/xerrors` / `go.uber.org/multierr` / `github.com/hashicorp/go-multierror` / `github.com/cockroachdb/errors`）通过 `.golangci.yml` `cellgen-error-libs` depguard 规则**在 import 边界拒绝**，无需进入 archtest 黑名单。新增第三方 lib 需要同步登记两侧：archtest 黑名单 + depguard deny 项。

**反向自检 RED fixture 覆盖面**（章程 §AI-rebust 三档分级 ≥ Hard 必备）：

1. `fmt.Errorf("...")` — normal SelectorExpr import，1 violation 在调用点
2. `fmtx "fmt"` alias + `fmtx.Errorf(...)` — types.Info 解析覆盖 alias，1 violation 在调用点
3. `import . "fmt"` + `Errorf(...)` — dot import 裸 Ident（STEP 1.b 覆盖），1 violation 在调用点
4. `errors.New(...)` — stdlib 第二 escape route，1 violation 在调用点
5. `import . "errors"` + `New(...)` — dot import 第二 lib，1 violation 在调用点
6. `var ErrNew = errors.New` + `ErrNew(...)` — function-valued var re-export，1 violation 在调用点（STEP 3 显式 fail）
7. cellgen 包内自建 wrapper `func newErr(msg string) error { return errors.New(msg) }` — AI 创新 escape。**1 violation 在 wrapper body 的 `errors.New` 调用**（STEP 2 黑名单匹配）；外层 `newErr("x")` 调用点 callee 是同包函数，不在黑名单 → 不被独立标记，但 wrapper body 必报 → 转发链终结于第一个非白名单 stdlib 构造函数，Hard 覆盖完备

每个 RED fixture 都验证 archtest fail，1 GREEN fixture 验证 errcode.{Assertion, New, Wrap} 三个 callable 不被误抦（callee.Pkg = pkg/errcode，不在黑名单）。

**Hard 覆盖完备性论证**（修订）：cellgen 包内任何最终构造 error 的形态必然落入下述三类之一：
- (A) `*types.Func` 且 (Pkg.Path, Name) ∈ S 黑名单 — STEP 2 fail
- (B) `*types.Var` 且 signature 返回单一 error — STEP 3 fail
- (C) callee 在白名单（pkg/errcode）或合法 operation（stdlib `os.X / template.Y / pathsafe.Z` 等返回 error 但非构造）— skip ✓

第三方构造函数路径由 depguard 在 import 边界关闭。`reflect.MakeFunc` 反射构造路径见盲区清单。

**盲区清单**（章程 §"工具选定后强制盲区自检"，Hard 评级前置举证）：

- **复合字面量构造 `&myErr{}`**：`type myErr struct{ msg string }; func (e *myErr) Error() string { return e.msg }; return &myErr{}` 形态。CompositeLit 节点非 CallExpr，本 archtest 不扫。cellgen 当前无此模式（grep `func.*Error\(\) string` 在 cellgen 非 _test.go 文件中 0 命中）。终极 Hard 化需 Path C-full 字段私有化重构 errcode.Error → backlog `CELLGEN-ERRCODE-FUNNEL-HARDEN-PATH-C-FULL`（详 §D7）。
- **反射动态构造**：`reflect.MakeFunc` / `reflect.Value.Call(...).Interface().(error)` 在静态 AST 不可解析。cellgen 当前 0 处（grep 验证）；未来引入反射 codegen，加 `reflect` 包 import ban 关闭。
- **build-tag 隔离 file**：cellgen 现无 `//go:build` 文件（grep 验证），由 `ARCHTEST-VERIFY-COVERAGE-01` 守护 archtest 注册一致性。
- **新构造 lib 未登记**：AI 通过 `import "alt/errlib"` 引入并 `alt.NewErr(...)` 构造。`alt/errlib` 不在 depguard deny 项即编译通过，且不在 archtest 黑名单。**这是 Path A 的本质盲区**（OSS 业界普遍接受）。Trigger-based mitigation：发现新 escape 同 PR 加 depguard + 黑名单 entry + RED fixture。
- **function-valued var 误抦（false positive）潜在面**：STEP 3 拒绝任何 `*types.Var` callee 且签名返回单一 error。当前 cellgen 0 处此模式（grep `var \w+ = .*` 中 RHS 是返回 error 的 func 均无命中），所以无误抦。但若未来引入合法 passthrough var（如 `var transform = pathsafe.Validate; transform(...)`，`pathsafe.Validate` 返回 error 但非"构造"），STEP 3 会误抦。Mitigation：保留作"设计已知误抦面"，未来出现合法 var 时考虑：(a) 加 var-allowlist 显式登记，或 (b) 改 inline call。

**设计意图确认**（非盲区）：

- **interface method call 返回单 error**：如 `var x SomeIface; x.Method() error`。STEP 1.a 解析到 `*types.Func`（interface method），(Pkg.Path, Name) 不在黑名单则不报——与合法 stdlib `os.RemoveAll` 等 operation 同处理。**这是设计意图**：cellgen 调用接口方法返回 error 是合法 operation 转发，不是构造点。Hard 性质不受影响。
- **`golang.org/x/sync/errgroup` 不在 depguard deny 列表**：`errgroup.Group.Wait() error` 与 `errgroup.Group.Go(func() error)` 返回的是已存在 goroutine 错误的传播，不是新构造。`errgroup` 不在 cellgen `cellgen-error-libs` ban 列表是正确选择（与 stdlib `os/template` operation 同理）。同样 `cellgen` 现也未 import errgroup（grep 验证）。

### D6. AI-rebust 评级 = Hard（OSS 业界上限，存在显式登记盲区）

按章程 §"typed function call as Hard funnel for unbounded operations" + OSS 业界实测调查（K8s `*StatusError` constructor boundary / Kratos `*Error` constructor boundary / go-zero pluggable handler / 社区 linter 调研：errorlint / errwrap / err113 / depguard v2 均无 method-level error funnel 支持），本案 AI-rebust 评级如下：

- **form uniqueness**：cellgen 包内任何 (Pkg.Path, Name) 形态匹配 `cellgenErrConstructorBlacklist` 即 fail；function-valued var 路径单独 fail。集合外 (Pkg.Path, Name) 落在合法 operation（os/template/pathsafe 等）或 errcode 白名单，正确放行——**无误抦无漏抦**
- **archtest fail-on-deviation**：任何偏离立即 CI 红
- **诚实声明**：编译期不可阻止（Go 允许任何包定义任何 callable），enforcement 完全依赖 archtest + depguard 联合。这是 Go 语言中 "error 构造点 funnel" 在 OSS 业界实测调查中可达的**最高评级**
- **funnel 双向锁评级**（§Funnel 双向锁评级）：
  - **上游 Hard**：cellgen 包内任何 error construct callsite 必须**不落入** `cellgenErrConstructorBlacklist`（stdlib 已知构造函数 + 通过 depguard ban 关闭第三方 lib import 边界）。**评级依据章程 §"typed function call as Hard funnel for unbounded operations"**：「form uniqueness + archtest fail-on-deviation 是 Go 语言中此类规则形态可达最高级，不要求编译期阻止」（与 PANIC-REGISTERED-01 同源认定）。
  - **下游 Hard**：`pkg/errcode` funnel 集合本身由 `ERRCODE-KIND-LITERAL-01` + `MESSAGE-CONST-LITERAL-01` + `DETAILS-SLOG-ATTR-01` 锁定（三档 archtest 形态锁，与 ADR `202605051730-adr-errcode-message-pii-safety.md` 一致）
  - **defense-in-depth**：`.golangci.yml` `cellgen-error-libs` depguard 规则在 import 边界 ban 五个常见第三方 error lib（pkg/errors / xerrors / multierr / hashicorp/go-multierror / cockroachdb/errors）——第三方构造函数路径不需要进入 archtest 黑名单
  - **闭环**：上游 (stdlib 黑名单 + 第三方 import ban + var 通道拒绝) + 下游 funnel 内容锁 — 集合外不能进 + 集合内必须经过 funnel 双向 Hard

**显式盲区登记**：详 §D5 盲区清单。复合字面量构造 (`&myErr{}`) 是当前 Hard 上限以下唯一可达 escape；trigger-based mitigation 路径详 §D7 Path C-full 升级。

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
- ~~(c'') 原稿 STEP 2 设计（"callee 返回含 error" 触发判定）~~ — **修正非否决**：实施期发现 STEP 2 过宽（误抦 `os.RemoveAll` / `template.Execute` 等 operation），**已修正为 D5 Path A 黑名单形式**（保留 (c'') 形态唯一性 + 对偶 PANIC-REGISTERED-01 的设计目标，仅改 STEP 2 触发判定从"返回 error"到"已知构造函数黑名单匹配"）。OSS 业界（K8s/Kratos/go-zero）调研证明 type-system Hard 全包性 enforcement 不可达；Path A 是实测上限。注意：D4 否决的 (c) 是另一方案——"仅扩 fmt.Errorf 单点到 type-aware 升级"，与 (c'') 完全不同
- Path C-lite（cellgen funcs return *errcode.Error 但不私有化字段）— 被 errcode.Error exported 字段卡住，与 Path A 同 Hard 等级但额外 5-8h 成本，无收益
- Path C-full — 见 §D7，超出 P5.1 范围，backlog 登记

## Escalation

- **新增 cellgen 包内 error escape route**（如引入第三方 lib 需绕过 funnel）：必须 RETRACT 本 ADR 或扩 `cellgenErrConstructorBlacklist` + `.golangci.yml` `cellgen-error-libs` 双侧；同 PR 修改 archtest + depguard + 反向自检 fixture + ADR 三处
- **复合字面量 `&errcode.Error{...}` 在 cellgen 包内出现首例事故**：trigger §D7 Path C-full 升级（字段私有化）
- **章程 §AI-rebust 三档分级 升级路径**：若 §D7 Path C-full 落地，本 ADR 转 Superseded
