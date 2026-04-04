# 错误处理规范

## 错误响应格式

```json
{"error": {"code": "ERR_DEVICE_NOT_FOUND", "message": "device not found", "details": {}}}
```

## 错误码分组

| 前缀 | 模块 | 示例 |
|------|------|------|
| `ERR_AUTH_*` | 认证 | `ERR_AUTH_INVALID_TOKEN` |
| `ERR_VALIDATION_*` | 通用校验 | `ERR_VALIDATION_REQUIRED_FIELD` |

> 按项目实际模块扩展错误码前缀。

## HTTP 状态码映射

200 GET/PUT/PATCH | 201 POST | 202 异步 | 204 DELETE | 400 参数错误 | 401 未认证 | 403 无权限 | 404 不存在 | 409 冲突 | 413 过大 | 429 限流 | 500 内部错误

## 编码规则

1. 禁止 `errors.New` 对外暴露，使用 `errcode` 包
2. 错误必须包装上下文：`fmt.Errorf("enrollment: %w", err)`
3. 禁止 `_ = someFunc()` 忽略错误，必须显式处理或记录
4. handler 层统一转换领域错误为 HTTP 状态码，domain 层禁止返回 HTTP 状态码
5. 500 不暴露内部细节，写 `slog`；客户端看到的错误信息必须对用户有意义
