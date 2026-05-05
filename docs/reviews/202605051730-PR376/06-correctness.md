# PR #376 Review — 正确性维度

PR: feat(codegen): PR-V1-CODEGEN-FULL-MIGRATION (方案 D PR-4 末段)
HEAD: 446930ad / Base: origin/develop

## Summary

- 总体结论: 需修复（1 个 P1 语义缺陷，3 个 P2 建议）
- Findings: P0=0 P1=1 P2=3
- Cx: Cx1=3 Cx2=1 Cx3=0 Cx4=0

---

## Findings

### F-COR-001 [P1] [Cx2] tools/codegen/contractgen/builder.go:69-82（影响 generated/contracts/http/auth/role/list/v1/handler_gen.go 等）
**问题**: 可选整数 query param（`required: false`）带 `minimum` 约束时，**生成代码对未传值的情况不做校验**，`req.Limit` 保持零值 `0`，绕过 `minimum: 1`。服务层若不对 `Limit==0` 做 default fallback，会向 DB 传 `LIMIT 0` 返回空结果集。

**证据**:
```go
// generated/contracts/http/auth/role/list/v1/handler_gen.go:69-82
if raw := r.URL.Query().Get("limit"); raw != "" {  // 只在有值时校验
    v, err := strconv.ParseInt(raw, 10, 64)
    if err != nil { ... }
    if v < 1 { ...return }
    req.Limit = v
}
// 未传 limit → req.Limit = 0，但 0 违反 minimum(1)
```

template `handler.tmpl:214-234` 的 `else`（Required=false）分支完全跳过最小值校验。

受影响：`http.auth.role.list.v1`、`http.audit.list.v1`、`http.config.list.v1` 等约 7 处。`http.config.list.v1` 走 `httputil.ParsePageParams` 可能内置 default，但 `http.auth.role.list.v1` 走独立解析路径，确认存在问题。

**建议**: 在 `handler.tmpl` 可选整数分支中，当 minimum > 0 时对零值额外保护或显式注释提示 service 层 fallback 责任。需 grep `req.Limit` 在各服务层使用确认 default fallback 后决定修复粒度。

### F-COR-002 [P2] [Cx1] tools/codegen/contractgen/builder.go:380-405
**问题**: `buildPathParams` 中，路径模板含 `{foo}` 但 `contract.yaml pathParams` 未声明 schema 时，**静默跳过**该参数，handler 不做任何校验，原始字符串直接赋给 `req.Foo`。当前所有 contract 完整声明（FMT-13 governance 拦截），无功能 bug，但 codegen builder 自身不报错。

**证据**:
```go
for _, name := range names {
    schema, ok := http.PathParams[name]
    if !ok {
        continue  // ← 静默跳过
    }
    out = append(out, ParamSpec{...})
}
```

**建议**: 改为 `return nil, fmt.Errorf("contractgen: path param %q in path %q has no schema", name, http.Path)` fail-fast on generate。

### F-COR-003 [P2] [Cx1] tools/codegen/contractgen/builder.go:558-570 + tools/codegen/cellgen/builder.go:355-366
**问题**: `contractIDToPackagePath`（contractgen）与 `contractIDToImportPath`（cellgen）是逻辑等价的两份独立实现，均将 "internal" segment 改写为 "internalapi"。两处当前一致，但未来新增保留 segment 时存在漂移风险。

**建议**: 抽取到 `tools/codegen` 包共享函数，两处调用同一实现。

### F-COR-004 [P2] [Cx1] tools/codegen/contractgen/builder.go:174-177
**问题**: `HasBody` 不包含 `DELETE`（即使 DELETE 有 request body schema 也不生成 `DecodeJSONStrict`）。当前所有 DELETE 端点无 body，功能正确，但限制是隐性的：开发者声明 `schemaRefs.request` 不会得到警告，body 静默忽略。

**证据**:
```go
methodHasBody := http.Method == "POST" || http.Method == "PUT" || http.Method == "PATCH"
hasBody := methodHasBody && contract.SchemaRefs.Request != ""
```

**建议**: 在 `buildHTTPEndpointSpec` 中加 slog.Warn 防御。

---

## 已验证正确的关键点

1. **template 渲染所有形态正确**: Public/PasswordResetExempt/HasBody/NoContent/IsPagination 分支组合均生成可编译正确语义。已抽样 login/sessiondelete/change-password handler_gen.go。
2. **EventSpec 删除后 ContractSpec 字段完整**: Topic/Method/Path/Clients 字段语义完整，`validateEvent()`/`validateHTTP()` 覆盖零值场景。
3. **FMT-18 删除正确性**: 三个 archtest gate 取代 FMT-18，allowlist 均空，cells/ 下无残留字面量。
4. **internal→internalapi 路径一致性**: 生成文件 `generated/contracts/http/config/internalapi/get/v1/`、`generated/contracts/http/internalapi/devicecommands/list/v1/` 实际存在。
5. **kind=command 优雅 skip**: 返回 spec（Endpoint=nil、Event=nil），仅发出 types/iface 不崩溃，`TestBuildContractSpec_CommandKind_GracefulSkip` 覆盖。
6. **subscription 生成代码无并发问题**: Subscription 字段值类型，Mount 直接调 reg.Subscribe 无闭包捕获，consumerGroup/cellID 调用方显式传入。
7. **event.flag.changed.v1 删除完整性**: contract + schema 删除，generated/ 无对应目录，`flagwrite/service.go` 无 emitter 字段，`TestFlagWrite_NoOutboxEmit_AfterDowngrade` 守卫。
8. **cell_gen.go Init 调用链一致**: 4 个抽样 cell 的 RegisterRoutes 调用与 generated 包类型匹配。
9. **codegen:false contract 正确忽略**: BuildContractSpec 在 !contract.Codegen 时返回错误，selectContractIDs 只收集 Codegen=true。
10. **生成文件名一致**: `spec_gen.go`/`subscription_gen.go` 在 generator/archtest/render_test/render 四处完全一致。

---

## 复杂度汇总
Cx1: 3 / Cx2: 1 / Cx3: 0 / Cx4: 0

## 修复分流建议
- F-COR-001 (P1 Cx2) → 需 grep 各服务层 `req.Limit` 默认处理后派 developer agent
- F-COR-002/003/004 (P2 Cx1) → 直接派 developer agent

---

## Out of scope（其他维度）
1. **[安全]** `http.auth.login.v1` 声明 `clients: [edge-bff]`，但 public endpoint 应否声明 clients 需 gocell validate 校验
2. **[测试]** `render_test.go` golden 缺少 `PasswordResetExempt=true` 的 synth fixture，仅靠实际生成文件覆盖
3. **[可维护性]** `collectDTOs` array type 嵌套命名（`"Item"` 后缀）与 `schemaGoType` 的 `"[]*"` 逻辑分散
4. **[可维护性]** internal→internalapi 改写逻辑在 contractgen/cellgen 重复（已列 F-COR-003）
5. **[运维]** kind=command/projection 走 slog.Warn，CI 大量 warn 容易被忽略，建议专项告警或 dry-run 输出明确标记
