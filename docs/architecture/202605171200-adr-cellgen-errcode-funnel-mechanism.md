# ADR: cellgen errcode funnel Hard 升级机制选型

> Status: Accepted
> Date: 2026-05-17
> Implementation: 待 P5.1 PR（重写 `tools/archtest/cellgen_errcode_funnel_test.go` 到白名单 form uniqueness）
> ref: docs/plans/202605162000-037r2-wave4-advance-round2.md §R2-P5;
>      docs/backlog/cap-14-tooling.md L44 `CELLGEN-ERRCODE-FUNNEL-HARDEN`;
>      .claude/rules/gocell/ai-collab.md §"typed function call as Hard funnel for unbounded operations";
>      tools/archtest/panic_registered_test.go（对偶范本 PANIC-REGISTERED-01）

## Context

`tools/codegen/cellgen/` 包负责生成 cell/slice scaffold 与运行时元数据 literal。PR#557 完成 cellgen 全包 errcode 迁移：50+ 处 error 构造均走 `errcode.New / errcode.Wrap / errcode.Assertion`，0 处 `fmt.Errorf / errors.New / pkg/errors`。

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

5. **PANIC-REGISTERED-01 范本结构**（`tools/archtest/panic_registered_test.go`）：
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

重写 `tools/archtest/cellgen_errcode_funnel_test.go` 为白名单 form uniqueness archtest，与 PANIC-REGISTERED-01 严格对偶：

```
对 cellgen 包内任一 *ast.CallExpr，如果：
  (1) callee 的 Sel.Name 命中 error-constructor pattern：
      ^(New|Newf|Wrap|Wrapf|Errorf|Errf|MustNew|.*Error)$
      （覆盖业界惯例命名 + cellgen 实际使用集 + 常见第三方 lib）
  AND
  (2) callee 类型签名（via *types.Info）的 result tuple 至少一个 type
      assignable to `error` interface
      （过滤误伤：strings.NewReader / http.NewRequest 等不返 error 的 New）
THEN 强制：
  (3) callee 解析到的 *types.Func.Pkg().Path() 必须 ∈
      {"github.com/ghbvf/gocell/pkg/errcode"}
ELSE
  archtest fail("cellgen: error constructor outside errcode funnel: <pos>")
```

**反向自检 RED fixture 覆盖面**（章程 §AI-rebust 三档分级 ≥ Hard 必备）：

1. `fmt.Errorf("...")` — 黑名单原型，失败位置 = 调用点 callsite
2. `fmtx "fmt"` alias import + `fmtx.Errorf("...")` — alias 绕过，失败位置 = 调用点 callsite（types.Info 解析覆盖 alias）
3. `errors.New("...")` — stdlib 另一 escape route，失败位置 = 调用点 callsite
4. cellgen 包内自建 wrapper `func newErr(msg string) error { return errors.New(msg) }` — AI 创新 escape。**失败位置 = wrapper 定义体内的 `errors.New` 调用**，不是外层 `newErr(...)` 调用点（外层 callee `newErr` 命中 pattern 但 result type 检查通过；funnel 仍需要 wrapper 定义内的最终构造点命中——`errors.New` 同时满足 name pattern + 返回 error，被覆盖）

每个 fixture 应证明 archtest fail，从而验证白名单覆盖未知形态而非仅已知 escape。

**Hard 覆盖完备性论证**：cellgen 包内任何最终构造 error 的语义（不透传现有 error）必然落入两类：(A) 直接调用 `errcode.{New,Wrap,Assertion}` — 落白名单内 ✅；(B) 调用任何其他 callable 间接构造 — 该 callable 的定义体内必然有最终构造点，最终构造点 callee name 必然命中 pattern（业界惯例：构造 error 必经 `New|Errorf|Wrap|...`），被 archtest 在 callable 定义处拦截。除反射动态构造（盲区清单 §D5 末尾）外无第三类。

**盲区清单**（章程 §"工具选定后强制盲区自检"，Hard 评级前置举证）：

- **反射动态构造 error**：通过 `reflect.MakeFunc` 等动态生成返回 `error` 的 callable，archtest 无法在 AST 层识别其内部 escape。cellgen 包当前无此模式（grep 验证），接受为已知遗留风险；未来若 cellgen 引入反射 codegen，扩 archtest 加 reflect 包 import ban。
- **build-tag 隔离的非默认 file**：archtest scope 当前包含 `tools/codegen/cellgen/*.go`（excluding `*_test.go`），若新增 build-tag 文件需同步检查 scope 覆盖。已由 `ARCHTEST-VERIFY-COVERAGE-01` 守护 archtest 注册一致性。
- **wrapper 名不含 error-constructor pattern 但定义体内的最终构造仍被命中**：见 fixture 4 论证；这不是盲区，是 Hard 覆盖路径。明文澄清避免 P5.1 实施者把此场景误判为遗漏。

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
