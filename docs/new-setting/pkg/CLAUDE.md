# pkg/ 层规则

pkg/ 是跨层共享工具包，被 kernel/、runtime/、cells/、adapters/ 所有层使用。

## 依赖约束

**只允许**：标准库
**严禁**：`kernel/`、`cells/`、`runtime/`、`adapters/`

引入任何非标准库依赖前，先在根 CLAUDE.md 和 `docs/reviews/202604061630-dependency-replacement-plan.md` 中评估。

## 模块职责

| 模块 | 职责 |
|------|------|
| `errcode` | 统一错误码定义和包装，禁止裸 `errors.New` 对外暴露 |
| `ctxkeys` | context key 类型定义，防止不同包 key 冲突 |
| `httputil` | HTTP 请求/响应工具函数 |
| `query` | 列表查询参数解析（分页、过滤、排序） |
| `idutil` | ID 格式校验工具（跨 Cell 共享，L4 级别） |
| `securecookie` | 安全 cookie 签名/验证工具 |
| `aeadutil` | AEAD 加密工具 |
| `testutil` | 测试辅助工具（仅 `_test.go` 文件使用） |
| `contracttest` | Contract 测试断言工具 |

## errcode 使用规范

```go
// 正确：使用 errcode 包
return errcode.New(errcode.ErrDeviceNotFound, "device not found")

// 正确：包装上下文
return fmt.Errorf("enrollment: %w", err)

// 禁止：裸 errors.New 对外暴露
return errors.New("device not found")

// 禁止：忽略错误
_ = someFunc()
```

## 新增工具包原则

新增 pkg/ 工具包前，先确认：
1. 确实无法在某一层内部解决
2. 需要被至少两个不同的层引用
3. 不引入新的外部依赖
