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

1. `fmt.Errorf("...")` — 黑名单原型
2. `fmtx "fmt"` alias import + `fmtx.Errorf("...")` — alias 绕过
3. `errors.New("...")` — stdlib 另一 escape route
4. cellgen 包内自建 wrapper `func newErr(msg string) error { return errors.New(msg) }` 调用点 — AI 创新 escape

每个 fixture 应证明 archtest fail，从而验证白名单覆盖未知形态而非仅已知 escape。

### D6. AI-rebust 评级 = Hard

按章程 §"typed function call as Hard funnel for unbounded operations"：

- **form uniqueness**：cellgen 包内任何 error constructor callee 必须 ∈ `pkg/errcode`，集合外形态 archtest 全部 fail — 无灰色地带
- **archtest fail-on-deviation**：任何偏离立即 CI 红
- **诚实声明**：编译期不可阻止（Go 允许任何包定义任何 callable），enforcement 完全依赖 archtest。这是 Go 语言中 "error 构造点 funnel" 此类规则形态可达到的最高评级
- **funnel 双向锁评级**（§Funnel 双向锁评级）：上游 Hard（cellgen 包内任何 error construct callsite 必须落入白名单）+ 下游 Hard（pkg/errcode funnel 集合本身已由 ERRCODE-KIND-LITERAL-01 + MESSAGE-CONST-LITERAL-01 + DETAILS-SLOG-ATTR-01 锁定）— 闭环 funnel

## Rejected alternatives

- (a) depguard method-level rule — D1
- (a') depguard package-level 禁 fmt import — D2
- (b) typed Error return wrapper — D3
- (c) 黑名单单点禁 + type-aware 升级 — D4

## Escalation

- **新增 cellgen 包内 error escape route**（如引入第三方 lib 需绕过 funnel）：必须 RETRACT 本 ADR 或扩 D5 (3) 白名单集合，同 PR 修改 archtest + ADR 双侧
- **name pattern 误伤 / 漏报**：P5.1 实施期跑全仓 dry-run 确认；如有误伤合法 callee（如 cellgen 内合理使用某 stdlib `XxxError` helper），同 PR 调整 pattern 并补反向自检 fixture
- **章程 §AI-rebust 三档分级 升级路径**：若未来 Go 1.X 支持 method-level visibility（如 export 控制扩展到方法），评估升级到 compile-time Hard，本 ADR 转 Superseded
