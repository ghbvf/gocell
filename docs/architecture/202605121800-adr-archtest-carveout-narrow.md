# ADR: archtest 豁免（carve-out）收窄到 function-level 与 ADR 登记

> Status: Accepted
> Date: 2026-05-12
> Implementation: PR #503 / commit 3fd2cbaf6（PR-A2；原规划 037 §1.2）
> ref: docs/plans/archive/202605121750-037-wave4-advance-plan.md (已归档);
>      docs/backlog/cap-14-tooling.md (ARCHTEST-CARVEOUT-NARROW-FUNCLEVEL, B2-K-08-CARVEOUT-NARROW)

## Context

`ERRCODE-KIND-LITERAL-01`（位于 `tools/archtest/errcode_invariants_test.go`）当前对整个文件
`pkg/ctxcancel/ctxcancel.go` 与 `pkg/httputil/response.go` 做 file-level 豁免。

File-level 豁免 = soft over-permission：这两个文件中的任何其他函数若使用 `errcode.Error{}` 结构体字面量，也会同样绕过检查，不被 archtest 守卫。

真正需要豁免的仅有 2 个具名函数：

- `ctxcancel.WrapOrInfra`（`ctxcancel.go:147`）：bridge helper，调用方提供的 `fallbackMsg` 必须通过结构体字面量拼入 `errcode.Error.Message`。若改为 `errcode.New(...)` 调用，则每个调用点都会触犯 `MESSAGE-CONST-LITERAL-01`（该规则要求 message 参数为 const literal）。
- `httputil.WritePublic`（`response.go:64`）：HTTP 序列化边界，框架选定的 message 通过结构体字面量构造响应 error；调用点保持在 const-literal 约束之内。

注意：`MESSAGE-CONST-LITERAL-01` 是独立的 gated-callee 调用点检查器，对 ctxcancel/httputil 无 carve-out，不受本 ADR 影响。

随着项目发展，若第 3 处 file-level carve-out 出现并延续整包豁免模式，守卫将持续退化，直至失去实际效果。

## Decisions

### D1. 将 file-level 豁免收窄为 function-level

`errcodeKindLiteralCarveOuts` 的 allowlist 键从 `packagePath`（字符串）改为
`{relPath, funcName}` 二元组（`carveOut` struct）。

豁免粒度变为：仅对具名函数体内的结构体字面量不报错；同一文件中其他函数若使用同类结构体字面量，仍会被 archtest 拦截。

### D2. 本 ADR registry 表是 carve-out 集合的唯一真值来源

carve-out 的完整清单、各条理由和升级路径统一维护在下方 `## Carve-out registry` 表中。
代码侧的 `errcodeKindLiteralCarveOuts` 映射必须与该表严格等价，任何一侧单独变更均视为漂移。

### D3. 新增 archtest `ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` 双向一致性校验

新 archtest 解析本 ADR 的 registry 表（以 `<!-- CARVEOUT-REGISTRY:BEGIN -->` /
`<!-- CARVEOUT-REGISTRY:END -->` HTML 注释为锚点，读取 `File` 与 `Function` 列），
断言其 `(File, Function)` 集合与代码侧 `errcodeKindLiteralCarveOuts` 的键集合**严格相等**：

- ADR 表有、代码无 → CI 红
- 代码有、ADR 表无 → CI 红
- 两侧一致 → GREEN

**AI-rebust 评级：Hard（archtest fail-on-deviation，非编译期）**。

违反是可表达的（例如编辑代码映射但忘记更新 ADR），但任何偏离均导致 CI 变红，没有灰色地带，没有注释豁免路径。按照 `.claude/rules/gocell/ai-collab.md` §Hard 范本（PANIC-REGISTERED-01 注解）的定义，这是 Go 语言中此类规则所能达到的最高评级；诚实声明：编译期不可阻止（Go 允许直接编辑 map 字面量），enforcement 依赖 archtest。

**Funnel 双向锁评级**（依照 `.claude/rules/gocell/ai-collab.md` §Funnel 双向锁评级要求）：下游 Hard — ADR 表有记录但代码映射无对应条目时 CI 立即报红（archtest 严格等价断言）；上游 Hard — 代码映射有条目但 ADR 表无对应记录时同样 CI 报红；两侧均为 archtest-Hard，构成闭环 funnel。诚实声明：两侧均非编译期阻止，这是 Go 语言中此类 ADR↔code 等价规则可达到的最高评级。

## Carve-out registry

以下表格是 `ERRCODE-KIND-LITERAL-01` 规则全部 carve-out 的权威清单。
archtest `ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` 解析此表（`File` 与 `Function` 列）
并与代码侧映射做严格等价断言。**禁止在表格标记行之间手动插入注释或空行。**

<!-- CARVEOUT-REGISTRY:BEGIN -->
| Rule | File | Function | Reason |
|---|---|---|---|
| ERRCODE-KIND-LITERAL-01 | pkg/ctxcancel/ctxcancel.go | WrapOrInfra | bridge helper: caller-supplied fallbackMsg must be spliced into Message via struct literal, else every call site violates MESSAGE-CONST-LITERAL-01 |
| ERRCODE-KIND-LITERAL-01 | pkg/httputil/response.go | WritePublic | HTTP serialization boundary: framework-selected message constructs the response error via struct literal; call sites stay under the const-literal constraint |
<!-- CARVEOUT-REGISTRY:END -->

## Escalation

新增或删除任何 carve-out 条目，必须在**同一 PR 内**同时完成：

1. 修改本 ADR 的 registry 表（增删对应行）
2. 修改 `tools/archtest/errcode_invariants_test.go` 中的 `errcodeKindLiteralCarveOuts` 映射

任意一侧遗漏，`ERRCODE-CARVEOUT-ADR-CONSISTENCY-01` 将在 CI 中报红，阻止合并。

**禁止 file-level 或 package-level carve-out**——仅允许 function-level 粒度。如需对某个文件内多个函数豁免，必须逐函数在 registry 表中单独登记，并说明各自理由。

## Rejected alternatives

**(a) in-test golden snapshot**：在 archtest 内维护一份 carve-out 列表的 golden 文件。
弱点：ADR 与 golden 的同步仍依赖 convention 约束，reviewer 需手动对比；不如 ADR↔code 严格等价检查直接。

**(b) 注释式"新增须写 ADR"约定**：在 errcode_invariants_test.go 顶部加注释要求新增 carve-out 时写 ADR。
弱点：Soft 形态，reviewer 必须主动抓；AI 可在注释无效条件下绕过。已明确排除（按 `.claude/rules/gocell/ai-collab.md` §Soft 严禁立项原则）。

最终选择 Hard ADR↔code 严格等价交叉校验，由 archtest 在 CI 层机器执行。

## References

- 037 Wave 4 提前推进计划（已归档）：[`docs/plans/archive/202605121750-037-wave4-advance-plan.md`](../plans/archive/202605121750-037-wave4-advance-plan.md) §1.2 PR-A2；实现锚点 PR #503 / commit `3fd2cbaf6`
- cap-14 backlog：[`docs/backlog/cap-14-tooling.md`](../backlog/cap-14-tooling.md) 条目 `ARCHTEST-CARVEOUT-NARROW-FUNCLEVEL` + `B2-K-08-CARVEOUT-NARROW`
- AI 协作章程：[`.claude/rules/gocell/ai-collab.md`](../../.claude/rules/gocell/ai-collab.md) §AI-rebust 三档分级、§Hard 范本
