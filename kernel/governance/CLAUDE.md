# kernel/governance/ 层规则

governance/ 实现 GoCell 元数据治理规则。每条规则是一个 detect 方法
（`validate<RULEID>()` / `checkDEP*` / `checkCH*`，零参返回 `[]ValidationResult`），并在
`rules_registry.go` 的 `allRules` 注册表以一个 `Rule{}` 条目登记（`Code` + `Phase` + `Next` +
可选 `Metric` + 编译期检查的 `Detect` 方法表达式），由 `engine.go` 的 `Validator.run` 单循环执行
（ADR `202605041430` §M3-RULE-ENGINE）。

## ValidationResult 构建

findings 一律经类型化构造函数（locator 方法 / `newErrorAt` 包级函数），**禁止**手写 `ValidationResult{}` 字面量（locator.go 外）。选 error 语义 = 调用强制带 `fix`（remediation guidance）参数的 API；warning 无 `fix`。由 archtest `GOVERNANCE-RULE-ERROR-FIX-FIELD-01` 守卫（fix 非空 + 禁裸字面量）+ `...CODE-CONST-SINGLE-SOURCE-01`（code 必为 `rulecodes.go` 的 `RuleCode` const）。

```go
// error：fix 必填（最后一个位置参数）
v.newError(
    codeADV05,          // RuleCode const（rulecodes.go）
    IssueForbidden,     // required | invalid | referenceNotFound | mismatch | forbidden | duplicate
    contractFile(c),    // 文件路径
    "endpoints.subscribers", // 字段路径
    fmt.Sprintf("active event contract %q has no subscribers", c.ID), // 问题陈述
    "add subscribers to endpoints.subscribers or set lifecycle: deprecated", // 修复指导 → Fix 字段
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
| ADV | ADV-01 ~ ADV-06 | 建议警告（dead event、journey 覆盖等） |
| OUTGARD | OUTGARD-01 | Outbox 约束 |

## 完整规则示例（ADV-05）

detect 方法只检测 + 经 `locator` 构造器产出 finding（severity 由 `newError`/`newWarning` 决定）：

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
                contractFile(c), "endpoints.subscribers",
                fmt.Sprintf("active event contract %q has no subscribers (dead event)", c.ID),
                "add subscribers to endpoints.subscribers or set lifecycle: deprecated",
            ))
        }
    }
    return results
}
```

## 规则注册

新规则：写 detect 方法 → 在 `rules_registry.go` 的 `allRules` 加一个 `Rule{}` 条目 →
在 `rule_inventory_test.go` 的 `goldenRuleIDs()` 加它的 code（`TestAllRulesMatchGolden` 锁集合 + 唯一性）。
`Next` 取 `NextBlock`（error）或 `NextAdvisory`（warning）；`Metric` 仅在规则有真实距离时声明，否则省略（nil）。

```go
var allRules = []Rule{
    // ... 已有规则 ...
    {
        Code: codeADV05, Phase: PhaseBase, Next: NextAdvisory,
        Metric: (*Validator).adv05DeadEventCount, // 可选：有距离的规则才声明
        Detect: (*Validator).validateADV05,        // 方法表达式，编译期检查存在
    },
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
    requireWarning(t, results, "ADV-05", "endpoints.subscribers") // advisory, not error
}
```

`minimalProject(t)` 返回最小化 `*metadata.ProjectMeta`；`requireWarning`/`requireError` 过滤并断言规则编号 + 字段路径。
