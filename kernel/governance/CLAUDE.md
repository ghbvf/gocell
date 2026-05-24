# kernel/governance/ 层规则

governance/ 实现 GoCell 元数据治理规则，每条规则对应一个 `validate<RULEID>()` 方法。

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

// warning：无 fix
v.newWarning(codeADV01, IssueRefNotFound, journeyFile(j), "id",
    fmt.Sprintf("journey %q has no entry in status-board.yaml", j.ID))

// scope（跨文件，无单一位置）：newScopedError(code, typ, scope, field, msg, fix)
// content-scan 位置（调用方提供 Line/Column）：newErrorAt(code, typ, file, pos, field, msg, fix)
```

> `msg` 是问题陈述，`fix` 是「怎么改」。两者都可用 `fmt.Sprintf`（插值参数各自分配）。CLI 输出把 `fix` 渲染成独立的 `fix:` 行 / JSON `"fix"` 字段，不再拼进 message。详见 ADR `docs/architecture/202605241730-adr-governance-error-fix-field-funnel.md`。

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

```go
func (v *Validator) validateADV05() []ValidationResult {
    var results []ValidationResult
    for _, c := range v.project.Contracts {
        if c.Kind != "event" || c.Lifecycle != "active" {
            continue
        }
        if len(c.Endpoints.Subscribers) == 0 {
            results = append(results, v.newError(
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

新规则在 `rules()` 方法末尾追加闭包：

```go
func (v *Validator) rules() []func() []ValidationResult {
    return []func() []ValidationResult{
        // ... 已有规则 ...
        v.validateADV05,
        v.validateMyNewRule, // 追加在这里
    }
}
```

## 测试写法

```go
func TestADV05_NoSubscribers(t *testing.T) {
    project := minimalProject(t)
    project.Contracts["event.dead.v1"] = &metadata.ContractMeta{
        ID: "event.dead.v1", Kind: "event", Lifecycle: "active",
        Endpoints: metadata.EndpointsMeta{Subscribers: nil},
        File: "contracts/event/dead/v1/contract.yaml",
    }
    results := NewValidator(project, "").validateADV05()
    requireError(t, results, "ADV-05", "endpoints.subscribers")
}
```

`minimalProject(t)` 返回最小化 `*metadata.ProjectMeta`；`requireError` 过滤并断言规则编号 + 字段路径。
