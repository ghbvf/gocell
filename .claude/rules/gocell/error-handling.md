# 错误处理规范

## Wire 格式

统一错误响应：

```json
{"error":{"code":"ERR_...","message":"...","details":[],"requestId":"..."}}
```

`requestId` 由框架注入。wire 字段 camelCase，日志字段 snake_case。

## errcode

- 对外错误使用 `errcode.New` / `errcode.Wrap`。
- exported package-scope `Err* = errors.New(...)` 禁止。
- domain 层不返回 HTTP 状态码。
- handler 层把领域错误映射为 contract 声明的状态码。
- codegen handler 用 typed response envelope 表达业务 4xx/5xx。

## Message 与 PII

`errcode.New` / `Wrap` 的 message 必须是 const literal，不能拼 runtime 数据。
runtime 数据进入两条 typed 通道：

- `WithDetails(PublicString/PublicInt/PublicBool/PublicDuration/PublicTime)`：
  4xx 可下发，5xx 强制 strip。
- `WithInternal(InternalAttr)`：只进服务端日志，永不进 wire。

相关守卫：`MESSAGE-CONST-LITERAL-01`、`DETAILS-SEALED-FIELD-FROZEN-01`。

## Panic

生产 panic 必须使用：

```go
panic(panicregister.Approved("reason", value))
```

`reason` 是 kebab-case literal。A/B 类 programmer error 使用 `errcode.Assertion`；
framework rethrow 保留原 recovered value。其它 panic 形态由 `PANIC-REGISTERED-01` 拦截。

## Carve-out

archtest carve-out 只能 function-level，不能 file-level 或 package-level。新增或删除
carve-out 必须同步修改 ADR registry 和测试内映射；任一侧漂移即 CI 红。

## 错误码前缀

新增 `ERR_<SEG>_` namespace 或 whole-code entry 必须：

1. 注册到 `pkg/errcode` 前缀所有权集合或外部 module init。
2. 更新 prefix golden。
3. 通过 `ERRCODE-PREFIX-OWNERSHIP-01`。

in-repo cell mint 新前缀时，平台 registry 也必须更新；单靠 cell init 不满足静态扫描。
