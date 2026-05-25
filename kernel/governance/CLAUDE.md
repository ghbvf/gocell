# kernel/governance/ 层规则

governance/ 实现 GoCell 元数据治理规则。每条规则是一个 detect 方法
（`validate<RULEID>()` / `checkDEP*` / `checkCH*`，零参返回 `[]ValidationResult`），并在
`rules_registry.go` 的 `allRules` 注册表以一个 `Rule{}` 条目登记（`Code` + `Phase` +
编译期检查的 `Detect` 方法表达式），由 `engine.go` 的 `Validator.run` 单循环执行
（ADR `202605041430` §M3-RULE-ENGINE）。

next-action（NextAction 类型）与 per-finding Metric 已从 M3 移除（推测性 M5-HARVEST
scaffolding，无消费方）。如 M5-HARVEST 落地，届时对真实消费方定义这两个字段。

## ValidationResult 构建

findings 一律经类型化构造函数（locator 方法 / `newErrorAt` 包级函数），**禁止**手写 `ValidationResult{}` 字面量（locator.go 外）。选 error 语义 = 调用强制带 `fix`（remediation guidance）参数的 API；warning 无 `fix`。由 archtest `GOVERNANCE-RULE-ERROR-FIX-FIELD-01` 守卫（fix 非空 + 禁裸字面量）+ `...CODE-CONST-SINGLE-SOURCE-01`（code 必为 `rulecodes.go` 的 `RuleCode` const）。

```go
// error：fix 必填（最后一个位置参数）
v.newError(
    codeREF01,          // RuleCode const（rulecodes.go）
    IssueForbidden,     // required | invalid | referenceNotFound | mismatch | forbidden | duplicate
    sliceFile(s),       // 文件路径
    "belongsToCell",    // 字段路径
    fmt.Sprintf("slice %q references unknown cell %q", s.ID, s.BelongsToCell), // 问题陈述
    "add the cell declaration or fix the belongsToCell value", // 修复指导 → Fix 字段
)

// warning：fix 同样必填（advisory，但"怎么改"也进 Fix，不留 Message）
v.newWarning(codeADV01, IssueRefNotFound, journeyFile(j), "id",
    fmt.Sprintf("journey %q has no entry in status-board.yaml", j.ID),
    "add an entry for this journey to journeys/status-board.yaml, or remove the journey if it is retired")

// scope（跨文件，无单一位置；Line/Column 恒为 0）
dc.newScopedError(codeDEP02, IssueForbidden, "project", "cells",
    fmt.Sprintf("circular dependency detected: %s", strings.Join(cycle, " → ")),
    "remove the dependency cycle by restructuring cell contracts")

// content-scan 位置（调用方提供 Line/Column；包级函数，无 receiver）
newErrorAt(codeDOCNAME01, IssueForbidden,
    file, metadata.Position{Line: lineNo, Column: col + 1}, "content",
    fmt.Sprintf(advHintDOCNAME01LegacyLiteral, repl.Literal, repl.Replacement),
    advHintDOCNAME01LegacyLiteralFix)
```

> `msg` 是问题陈述，`fix` 是「怎么改」。两者都可用 `fmt.Sprintf`（插值参数各自分配）。CLI 输出把 `fix` 渲染成独立的 `fix:` 行 / JSON `"fix"` 字段，不再拼进 message。typed-Fix 契约 **severity-agnostic**：四个构造函数（含 `newWarning`）全部强制 `fix`，INV-3 对全部检查 fix 非空。severity 只管 blocking(error) vs advisory(warning)，不管"指导是否结构化"——warning 的"怎么改"也进 Fix，不留 Message。详见 ADR `docs/architecture/202605241730-adr-governance-error-fix-field-funnel.md`。

## 规则编号体系

| 系列 | 范围 | 职责 |
|------|------|------|
| REF | REF-01 ~ REF-17 | 引用完整性（slice → cell/contract/journey） |
| TOPO | TOPO-01 ~ TOPO-09 | 拓扑合法性（assembly、journey 结构） |
| VERIFY | VERIFY-01 ~ VERIFY-06 | 验证闭包（verify.smoke/unit/contract 命令存在） |
| FMT | FMT-01 ~ FMT-34 | 格式合规（YAML 结构、HTTP 契约、路径参数；FMT-18 已退役） |
| ADV | ADV-01 ~ ADV-05 | 建议警告（dead event、journey 覆盖等；ADV-02/ADV-06 已退役） |
| OUTGUARD | OUTGUARD-01 | Outbox 约束 |

## 完整规则示例（ADV-05）

detect 方法只检测 + 经 `locator` 构造器产出 finding（severity 由 `newError`/`newWarning` 决定）。
ADV-05 使用 `"lifecycle"` 字段锚点（`endpoints.subscribers` 是 derived field，`yaml:"-"`，
用户不可编辑，指向它会误导用户）：

```go
func (v *Validator) validateADV05() []ValidationResult {
    var results []ValidationResult
    for _, c := range v.project.Contracts {
        if c.Kind != "event" || c.Lifecycle != "active" {
            continue
        }
        if len(c.Endpoints.Subscribers) == 0 {
            results = append(results, v.newWarning( // ADV-05 是 advisory（dead event 不阻断 CI）
                codeADV05, IssueForbidden,
                contractFile(c), "lifecycle",
                fmt.Sprintf(advHintADV05EmptySubscribers, c.ID),
                advHintADV05EmptySubscribersFix,
            ))
        }
    }
    return results
}
```

## 规则注册

新规则：写 detect 方法 → 在 `rules_registry.go` 的 `allRules` 加一个 `Rule{}` 条目 →
在 `rule_inventory_test.go` 的 `goldenRuleIDs()` 加它的 code（`TestAllRulesMatchGolden` 锁集合 + 唯一性）。

```go
var allRules = []Rule{
    // ... 已有规则 ...
    {Code: codeADV05, Phase: PhaseBase, Detect: (*Validator).validateADV05},
}
```

`Phase` 决定何时运行：`PhaseBase`（`gocell validate`）/ `PhaseStrict`（`--strict`）/
`PhaseDep`（依赖图）/ `PhaseHealth`（`gocell check`）。ctx-bound 规则（仅 VERIFY-06）的 `Detect`
是读 `v.runCtx` 的闭包。`GOVERNANCE-RULES-REGISTRATION-GUARD-01` 锁「detect 方法必在 allRules 登记」。

## 测试写法

```go
func TestADV05_NoSubscribers(t *testing.T) {
    project := minimalProject(t)
    project.Contracts["event.dead.v1"] = &metadata.ContractMeta{
        ID: "event.dead.v1", Kind: "event", Lifecycle: "active",
        Endpoints: metadata.EndpointsMeta{Subscribers: nil},
        File: "contracts/event/dead/v1/contract.yaml",
    }
    results := NewValidator(project, "", clock.Real()).validateADV05()
    requireWarning(t, results, "ADV-05", "lifecycle") // advisory, not error
}
```

`minimalProject(t)` 返回最小化 `*metadata.ProjectMeta`；`requireWarning`/`requireError` 过滤并断言规则编号 + 字段路径。
