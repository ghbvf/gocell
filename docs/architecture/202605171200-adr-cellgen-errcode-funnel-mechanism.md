# ADR: cellgen errcode funnel Hard 升级机制选型

> Status: Accepted
> Date: 2026-05-17
> Implementation: 待 P5.1 PR（重写 `tools/archtest/cellgen_errcode_funnel_test.go` 到白名单 form uniqueness）
> ref: docs/plans/202605162000-037r2-wave4-advance-round2.md §R2-P5;
>      docs/backlog/cap-14-tooling.md L44 `CELLGEN-ERRCODE-FUNNEL-HARDEN`;
>      .claude/rules/gocell/ai-collab.md §"typed function call as Hard funnel for unbounded operations";
>      tools/archtest/panic_invariants_test.go（对偶范本 PANIC-REGISTERED-01；INVARIANT ID 来自旧文件名 panic_registered_test.go，文件按章程 §archtest 文件命名「同主题规则 ≥ 3 → *_invariants_test.go」重命名）

## Context

`tools/codegen/cellgen/` 包负责生成 cell/slice scaffold 与运行时元数据 literal。PR #453（`refactor(cellgen): PR442 follow-up — housekeeping (cellgen errcode + CI + docs)`，merged 2026-05-11）完成 cellgen 全包 errcode 迁移：50+ 处 error 构造均走 `errcode.New / errcode.Wrap / errcode.Assertion`，0 处 `fmt.Errorf / errors.New / pkg/errors`。（备注：backlog `cap-14-tooling.md:44` 原文写「PR#557」是事实错误，本 ADR 以 GitHub merged PR 号为准）

当前 enforcement archtest `CELLGEN-SCAFFOLD-ERRCODE-FUNNEL-01`（`tools/archtest/cellgen_errcode_funnel_test.go` L57-71）用纯 AST identifier 匹配（`SelectorExpr.X.Name == "fmt" && Sel.Name == "Errorf"`）单点禁 `fmt.Errorf`：

- 未使用 `types.Info`；alias import (`fmtx "fmt"`)、dot import (`. "fmt"`)、re-export 均可绕过
- 不覆盖 `errors.New` / 第三方 errors lib / cellgen 包内自建 error wrapper（如 `func newErr(msg) error { return errors.New(msg) }`）等其他 escape route
- 自我评级 Medium（文件头 godoc L11-14 明示「Medium scanner AST + concrete-package allowlist」）

backlog `cap-14-tooling.md` L44 提出两个 Hard 升级候选：

> Hard 升级路径：加 `.golangci.yml` method-level depguard rule 或抽 typed Error return wrapper 让 `fmt.Errorf` 在编译期不可表达

本 ADR 为 R2-P5 spike 的决策产物，评估 backlog 原案 + 衍生方案并选定实施方向。

## Spike key facts

1. **cellgen 包 error 构造现状**（grep `tools/codegen/cellgen/*.go` 非测试代码）：
   - `errcode.New / errcode.Wrap / errcode.Assertion` = 50+ 处
   - `fmt.Errorf / errors.New / errors.Wrap / pkg/errors.*` = 0 处
   - 透传 `return err`（不构造新 error）= 30+ 处
   - **包已事实上是白名单 form uniqueness 形态**，Hard 升级 = 把该事实形态做成 archtest 强约束

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

### D5. 选 (c'') 白名单 form uniqueness（对偶 PANIC-REGISTERED-01）

重写 `tools/archtest/cellgen_errcode_funnel_test.go` 为 **types.Info-driven 白名单 form uniqueness archtest**，与 PANIC-REGISTERED-01 严格对偶。**不依赖 callee name pattern 匹配**——name pattern 设计在 v1 草案中曾被使用，被 review 发现漏 dot import / 裸 Ident / function-valued re-export 三类 escape，且容易漏白名单成员（如 `Assertion` 不含 `New/Wrap/Error` 子串），故彻底改为按 callee 解析对象 + result type 双重判定：

```
对 cellgen 包内任一 *ast.CallExpr c：

STEP 1 — callee 解析（覆盖所有 import 形态）：
  根据 c.Fun 形态用 *types.Info 解析到 callee object：
    a. SelectorExpr (pkg.Func / x.Method)：types.Info.Uses[sel.Sel] →
       *types.Func 或 *types.Var
    b. Ident（裸函数名 / dot import 后的裸函数名）：
       types.Info.Uses[ident] → *types.Func 或 *types.Var
    c. 其他形态（FuncLit、ParenExpr、IndexExpr 泛型实例化等）：递归
       拆解到底层 callee

STEP 2 — 仅看 error 构造点（result type 触发判定）：
  callee 的 Signature.Results() 含至少一个 type 满足
  types.Implements(t, errorInterface) — 即可赋值给 `error`
  若 callee 不返回 error → skip（非 error 构造，无需检查）

STEP 3 — 白名单 form uniqueness 强制：
  callee 必须是 *types.Func 且 callee.Pkg().Path() ∈
    {"github.com/ghbvf/gocell/pkg/errcode"}
  自动覆盖 errcode 包内任何当前/未来公开 callable（New / Wrap /
  Assertion / 未来新增），无需手动维护白名单成员
  否则 archtest fail("cellgen: error constructor outside errcode funnel:
    <pos>: callee=<resolved-name> from <pkg>")

STEP 4 — function-valued re-export 显式拒绝：
  STEP 1 解析到 *types.Var（如 `var ErrNew = errors.New` 后调用
  `ErrNew(msg)`，callee 是 var 而非 func）→ archtest fail
  ("cellgen: error constructor via function-valued variable forbidden:
  <pos>: var=<name>")
  rationale: 间接寻址通道是 AI 创新 escape 的主要面，cellgen 包内
  无此模式（grep 验证），显式 fail-closed
```

为什么放弃 name pattern：v1 草案 `^(New|Newf|Wrap|Wrapf|Errorf|Errf|MustNew|.*Error)$` 有三类硬错：

1. **漏白名单成员**：cellgen 现用 `errcode.Assertion`（`literal_printer.go` 4 处），`Assertion` 不含 `New/Wrap/Error/Errorf` 任何子串，pattern 不命中 → 自身白名单成员都被排除
2. **漏 dot import / 裸 Ident**：`import . "fmt"` 后 `Errorf(msg)` 是 `*ast.Ident` 不是 `*ast.SelectorExpr`，根本不进入 `Sel.Name` 检查路径
3. **漏 function-valued re-export**：`var ErrNew = errors.New; ErrNew(msg)` callee 是 Ident 解析到 var，绕过 name pattern + types.Info func 检查

types.Info-driven 设计同时解决三类问题：(1) 白名单按 pkg.Path() 检查，errcode 包内所有 callable 自动入白名单；(2) callee 解析覆盖 SelectorExpr/Ident/FuncLit 等所有 ast 形态；(3) STEP 4 显式禁 var 通道。

**反向自检 RED fixture 覆盖面**（章程 §AI-rebust 三档分级 ≥ Hard 必备）：

1. `fmt.Errorf("...")` — normal SelectorExpr import，失败位置 = 调用点
2. `fmtx "fmt"` alias import + `fmtx.Errorf("...")` — alias SelectorExpr，失败位置 = 调用点（types.Info 解析覆盖 alias）
3. `import . "fmt"` + `Errorf("...")` — dot import，callee 是裸 Ident，失败位置 = 调用点（STEP 1.b 覆盖）
4. `errors.New("...")` — stdlib 另一 escape route，失败位置 = 调用点
5. `import . "errors"` + `New("...")` — dot import 第二 lib，失败位置 = 调用点
6. `var ErrNew = errors.New` + 调用 `ErrNew("...")` — function-valued var re-export，失败位置 = 调用点（STEP 4 显式 fail）
7. cellgen 包内自建 wrapper `func newErr(msg string) error { return errors.New(msg) }` — AI 创新 escape。**失败位置 = wrapper 定义体内的 `errors.New` 调用**（errors.New result type 含 error，pkg.Path 不在白名单）；外层 `newErr(...)` 调用点 result type 含 error 但 callee.Pkg().Path() == "github.com/.../tools/codegen/cellgen"（也不在白名单）→ 外层调用点同样 fail；任一处先报都构成 Hard 覆盖

每个 fixture 应证明 archtest fail，从而验证白名单覆盖未知形态而非仅已知 escape。

**Hard 覆盖完备性论证**（types.Info-driven 设计）：cellgen 包内任何最终构造 error 的语义（不透传现有 error）必然产生 CallExpr 且 callee 返回类型含 `error`，被 STEP 2 触发。该 CallExpr 的 callee 经 STEP 1 解析后必然落入以下三类：
- (A) `*types.Func` 且 pkg.Path() == `pkg/errcode` — 白名单内 ✅
- (B) `*types.Func` 且 pkg.Path() ≠ `pkg/errcode`（含 cellgen 包内自建 helper）— STEP 3 fail
- (C) `*types.Var`（function-valued 变量）— STEP 4 fail

除反射动态构造（盲区清单见下）外无第四类。

**盲区清单**（章程 §"工具选定后强制盲区自检"，Hard 评级前置举证）：

- **反射动态构造 error**：通过 `reflect.MakeFunc` / `reflect.Value.Call` 等动态生成返回 `error` 的 callable，CallExpr 的 callee 类型在静态 AST 层不可解析为具名 `*types.Func`（多解析为 `*types.Var` 走 STEP 4 fail，但 `reflect.Value.Call(...).Interface().(error)` 路径无 CallExpr 直接产 error）。cellgen 包当前无此模式（grep 验证 `reflect.MakeFunc` / `reflect.Value.Call` 均不出现），接受为已知遗留风险；未来若 cellgen 引入反射 codegen，扩 archtest 加 reflect 包 import ban
- **build-tag 隔离的非默认 file**：archtest scope 当前包含 `tools/codegen/cellgen/*.go`（excluding `*_test.go`），若新增 build-tag 文件需同步检查 scope 覆盖。已由 `ARCHTEST-VERIFY-COVERAGE-01` 守护 archtest 注册一致性
- **interface method call 返回 error**：如 `var x SomeInterface; x.Method() error`。当前 cellgen 包内无此调用形态返回 error（grep 验证）；若未来出现，STEP 1.a 解析到 `*types.Func`（interface method），pkg.Path 不在白名单则 fail，与 (B) 同处理

### D6. AI-rebust 评级 = Hard

按章程 §"typed function call as Hard funnel for unbounded operations"：

- **form uniqueness**：cellgen 包内任何 error constructor callee 必须 ∈ `pkg/errcode`，集合外形态 archtest 全部 fail — 无灰色地带
- **archtest fail-on-deviation**：任何偏离立即 CI 红
- **诚实声明**：编译期不可阻止（Go 允许任何包定义任何 callable），enforcement 完全依赖 archtest。这是 Go 语言中 "error 构造点 funnel" 此类规则形态可达到的最高评级
- **funnel 双向锁评级**（§Funnel 双向锁评级）：
  - **上游 Hard**：cellgen 包内任何 error construct callsite 必须落入白名单。**评级依据章程 §"typed function call as Hard funnel for unbounded operations"**：「form uniqueness + archtest fail-on-deviation 是 Go 语言中此类规则形态可达最高级，不要求编译期阻止」（与 PANIC-REGISTERED-01 同源认定）。本案上游不采用「典型形态 = sealed interface + 字段私有化」（章程 §Funnel 双向锁评级第一项典型形态）——sealed interface 适用于「funnel API 接受任意载荷」（如 panic(any)），cellgen errcode 的 funnel API 是 `errcode.{New,Wrap,Assertion}` 三个 typed function，载荷已被类型签名锁定，无需 sealed interface
  - **下游 Hard**：`pkg/errcode` funnel 集合本身由 ERRCODE-KIND-LITERAL-01 + MESSAGE-CONST-LITERAL-01 + DETAILS-SLOG-ATTR-01 锁定（三档 archtest 形态锁，与 ADR `202605051730-adr-errcode-message-pii-safety.md` 一致）
  - **闭环**：上游白名单 + 下游 funnel 内容锁 — 集合外不能进 + 集合内必须经过 funnel 双向 Hard

## Rejected alternatives

- (a) depguard method-level rule — D1
- (a') depguard package-level 禁 fmt import — D2
- (b) typed Error return wrapper — D3
- (c) 黑名单单点禁 + type-aware 升级 — D4

## Escalation

- **新增 cellgen 包内 error escape route**（如引入第三方 lib 需绕过 funnel）：必须 RETRACT 本 ADR 或扩 D5 (3) 白名单集合，同 PR 修改 archtest + ADR 双侧
- **name pattern 误伤 / 漏报**：P5.1 实施期跑全仓 dry-run 确认；如有误伤合法 callee（如 cellgen 内合理使用某 stdlib `XxxError` helper），同 PR 调整 pattern 并补反向自检 fixture
- **章程 §AI-rebust 三档分级 升级路径**：若未来 Go 1.X 支持 method-level visibility（如 export 控制扩展到方法），评估升级到 compile-time Hard，本 ADR 转 Superseded
