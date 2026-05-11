# kernel/governance/ 层规则

governance/ 实现 GoCell 元数据治理规则，每条规则对应一个 `validate<RULEID>()` 方法。

## ValidationResult 构建

```go
v.newResult(
    "ADV-05",           // 规则编号
    SeverityError,      // SeverityError | SeverityWarning
    IssueForbidden,     // required | invalid | referenceNotFound | mismatch | forbidden | duplicate
    contractFile(c),    // 文件路径（或 Scope 虚拟域，二选一）
    "endpoints.subscribers", // 字段路径
    fmt.Sprintf("active event contract %q has no subscribers", c.ID),
)
```

## 规则编号体系

| 系列 | 范围 | 职责 |
|------|------|------|
| REF | REF-01 ~ REF-17 | 引用完整性（slice → cell/contract/journey） |
| TOPO | TOPO-01 ~ TOPO-08 | 拓扑合法性（assembly、journey 结构） |
| VERIFY | VERIFY-01 ~ VERIFY-06 | 验证闭包（verify.smoke/unit/contract 命令存在） |
| FMT | FMT-01 ~ FMT-31 | 格式合规（YAML 结构、HTTP 契约、路径参数） |
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
            results = append(results, v.newResult(
                "ADV-05", SeverityError, IssueForbidden,
                contractFile(c), "endpoints.subscribers",
                fmt.Sprintf("active event contract %q has no subscribers (dead event)", c.ID),
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
