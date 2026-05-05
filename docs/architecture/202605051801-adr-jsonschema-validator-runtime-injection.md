# ADR: Generated HTTP handlers delegate request schema validation to santhosh-tekuri/jsonschema/v6

**Date**: 2026-05-05
**Status**: Proposed (pending C5 batch implementation)
**PR**: #376 (decision recorded); implementation deferred to C5 per `202605041700-adr-contractgen-errors-defer-to-c5.md`

---

## Context

`handler.tmpl` 当前在 `handle()` 函数体中内联生成 `minLength`/`maxLength`/`minimum`/`maximum`
校验代码（见 `handler.tmpl:116-139`）：

```go
if len(req.Password) < 8 {
    httputil.WriteError(r.Context(), w,
        errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "password: value too short"))
    return
}
```

该模式引入了以下问题：

1. **Password length oracle**：`minLength: 8` 等具体长度常量被内联进生成代码，
   攻击者可通过阅读开源生成模板反推密码策略，规避短密码枚举防御。

2. **错误码漂移**：内联逻辑使用裸字符串消息（`"password: value too short"`），
   与 `ERR_VALIDATION_*` 枚举不一致。随着字段数量增加，维护成本线性增长。

3. **schema 双真理源**：`minLength` 同时存在于 `request.schema.json`（结构约束）
   和 `handler_gen.go`（运行时检查）。两者可能漂移（codegen 与手工修改不同步）。

4. **模板复杂度**：`handler.tmpl` 包含大量条件块处理校验分支，认知复杂度接近限制，
   阻碍后续扩展（如 `enum`、`pattern`、`$ref` 约束）。

## Decision

**删除 handler.tmpl 中的内联校验逻辑。生成的 handler 将 JSON body 校验委托给
`santhosh-tekuri/jsonschema/v6` 的预编译 validator。**

具体实现规则：

1. `BuildContractSpec` 在 build 阶段通过 `jsonschema.Compiler` 预编译 `request.schema.json`
   并将 `*jsonschema.Schema` 嵌入 `ContractGenSpec.RequestSchema`。

2. `handler_gen.go` 在 `DecodeJSONStrict` 之后调用预编译 validator：
   ```go
   if err := h.validator.Validate(req); err != nil {
       httputil.WriteSchemaError(r.Context(), w, err) // maps ValidationError → ERR_VALIDATION_*
       return
   }
   ```

3. `httputil.WriteSchemaError` 将 `jsonschema.ValidationError` 统一映射为
   `{"error": {"code": "ERR_VALIDATION_FAILED", "message": "...", "details": {...}}}`，
   不暴露 schema 内部约束（`minLength`/`minItems` 等）的具体数值。

4. path param 和 query param 的范围校验（`minLength`/`maxLength`/`minimum`/`maximum`）
   保留内联，因为这些是路由层职责，schema 文件不覆盖它们；但消息文本统一使用
   `errcode.ErrValidationFailed` 枚举，不裸字符串。

5. `NewHandler` 接受 `*jsonschema.Schema` 参数（或通过 `WithValidator(schema)` Option），
   在 composition root 注入，cell 代码不直接依赖 `jsonschema` 包。

## Consequences

**正面**：
- `request.schema.json` 是唯一的请求校验真理源，schema 变更自动覆盖到运行时。
- `handler_gen.go` 不包含具体的长度/范围约束数值，消除 password length oracle。
- 错误码统一走 `ERR_VALIDATION_FAILED`，`WriteDomainError` 不需要逐字段 case。
- `handler.tmpl` 认知复杂度下降，后续扩展（`enum`、`pattern`）无需修改模板。

**负面**：
- `NewHandler` 签名变更，已生成的 `handler_gen.go` 需要 re-generate；
  composition root 需要注入 `*jsonschema.Schema`。
- `santhosh-tekuri/jsonschema/v6` 的 `ValidationError` 结构可能携带 schema 细节，
  `WriteSchemaError` 必须做 redaction，避免泄漏 `minLength` 等约束到 response body。
- 校验性能从 O(1) 内联比较变为 validator tree walk；对于简单字段（`minLength: 1`）
  增加微量开销。该开销在 I/O 主导的 HTTP handler 中可忽略（benchmark 确认后更新此条）。

## Alternatives Considered

### 方案 A：kin-openapi/openapi3 validator

`getkin/kin-openapi` 提供 OpenAPI 3.x request 校验，可从 OAS spec 生成 validator。

**被拒原因**：GoCell 不维护 OpenAPI spec 文件，contract.yaml 是权威源。
kin-openapi 需要额外的 OAS 生成步骤，引入 N+1 真理源。且 kin-openapi 依赖链较重，
与 kernel/ 无外部依赖原则冲突（validator 在 runtime 层注入，不在 kernel）。

### 方案 B：手写 validator 补全（继续内联）

继续在 handler.tmpl 中扩展内联逻辑，覆盖 `enum`、`pattern`、`$ref`。

**被拒原因**：每个新关键字都需要模板条件分支，复杂度增长不可持续。
`password: value too short` 类消息是安全隐患，无法通过模板内联彻底解决。

### 方案 C：oapi-codegen + kin-openapi 联合

从 contract.yaml 自动生成 OAS，再用 oapi-codegen 驱动 handler 代码生成。

**被拒原因**：gocell 的 contractgen 已解决 OAS codegen 的核心问题（HTTP handler、DTO、
spec）；将权威源移到 OAS 文件会破坏 kernel/metadata 的单源模型，增加工具链层数。

## Implementation Note (deferred to C5)

当前 `handler.tmpl` 内联逻辑暂时保留（见 `202605041700-adr-contractgen-errors-defer-to-c5.md`）。
C5 实施要点：
- 删除 `handler.tmpl:116-139` 的 body field 校验块
- `ContractGenSpec` 增加 `RequestSchema *jsonschema.Schema` 字段
- `httputil` 增加 `WriteSchemaError(ctx, w, *jsonschema.ValidationError)`
- archtest 增加 `HANDLER-NO-INLINE-BODY-VALIDATION-01` 门：禁止 `handler_gen.go` 中出现裸 `len(req.X) <` 模式

## References

- `santhosh-tekuri/jsonschema/v6`: https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6
- oapi-codegen + kin-openapi: https://github.com/oapi-codegen/oapi-codegen
- `kernel/metadata/schemas/contract_schema_test.go` — schema compilation test 用同一 compiler
- `docs/architecture/202605041700-adr-contractgen-errors-defer-to-c5.md` — 延迟决策记录
- `handler.tmpl:80-160` — 当前内联校验实现
