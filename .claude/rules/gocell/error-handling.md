# 错误处理规范

## 错误响应格式

```json
{"error": {"code": "ERR_DEVICE_NOT_FOUND", "message": "device not found", "details": [{"key": "deviceId", "value": "abc-123"}]}}
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

1. 禁止 `errors.New` 对外暴露（exported package-scope `var Err* = errors.New(...)` 必须走 `errcode.New(code, message)`），由 archtest `EXPORTED-ERROR-NEW-01` 静态拦截。函数体内 `errors.New` 局部错误允许。
2. 错误必须包装上下文：`fmt.Errorf("enrollment: %w", err)`
3. 禁止 `_ = someFunc()` 忽略错误，必须显式处理或记录
4. handler 层统一转换领域错误为 HTTP 状态码，domain 层禁止返回 HTTP 状态码
5. 500 不暴露内部细节，写 `slog`；客户端看到的错误信息必须对用户有意义

## Message PII 静态字面量约束

`errcode.New` / `errcode.Wrap` 的 message 参数必须是 const literal（程序员写死的描述性文本）；runtime 产生的数据（用户输入、ID、计数等）禁止拼进 message。runtime 数据走两条通道：

- `WithDetails(slog.String("key", val))` — 公开字段，4xx 时随 `details` 数组下发给客户端，5xx 时框架 strip；
- `WithInternal(fmt.Sprintf(...))` — 仅服务端日志可见，任何状态码下均不下发客户端。

`WithInternal` 不受 const literal 约束。archtest `MESSAGE-CONST-LITERAL-01` 静态守卫，拦截任何在 `errcode.New/Wrap` 第三参数位置出现 `fmt.Sprintf` 或字符串拼接的调用点。

## Assertion vs panic

生产代码 panic 分三类处理：

- **A 类（状态机不可达分支）**：走 `errcode.Assertion("invalid state: %v", state)`，语义等同 `errcode.New(KindInternal, ErrInternal, ..., WithCategory(CategoryInfra))`，由 kernel recover 捕获后转 500 并记 Error 日志。
- **B 类（参数约定违反，编程错误）**：同 A 类，用 `errcode.Assertion`。
- **C 类（显式 re-throw，框架生命周期）**：保留 bare `panic`，共 6 处明确豁免：`lifecycle.go` 启动超时、`circuit_breaker` recover re-throw、`tx_manager` 嵌套事务 re-throw、`websocket handler` protocol error、`metrics` 注册冲突、`kernel/cell` bootstrap fatal。

新增 panic 必须在代码注释中声明属于哪一类，C 类需额外说明豁免理由。archtest `PANIC-REGISTERED-01` 拦截在非豁免列表文件里出现的 bare panic（recover 块内 re-throw 需在 `architecturalPanicWhitelist` 中显式注册）。

## Details 类型安全：slog.Attr

`WithDetails` 参数类型由 `map[string]any` 改为 `...slog.Attr`，调用方使用标准构造函数：

```go
errcode.New(ErrNotFound, "device not found",
    errcode.WithDetails(
        slog.String("deviceId", id),
        slog.Int("retryCount", n),
    ))
```

wire schema `error.details` 为 `array<{key: string, value: any}>`，由 `Error.MarshalJSON()` 从 `[]slog.Attr` 派生，不再是任意 JSON object。`error-response-v1.schema.json` 中 `details` 字段类型同步改为 `array`。archtest `DETAILS-SLOG-ATTR-01` 拦截直接传入 `map[string]any` 的旧式调用。移除旧版 `attrsToMap` helper（单源治理，不保留过渡桥接）。
