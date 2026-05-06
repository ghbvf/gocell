# API 版本策略

所有端点使用 `/api/v1/` 前缀，内部 API 使用 `/internal/v1/`。

## 何时升级版本

- 新增端点/字段/可选参数 → 不需要，直接在 v1 下添加
- 删除/重命名字段、修改字段类型、修改必填参数 → 需要 v2

## 向后兼容规则

1. v1 响应只增不删
   - response / event payload schema 禁止 `additionalProperties: false`（含 nested），允许 v1 持续加 optional 字段
   - request schema 必须 `additionalProperties: false`（拒未知字段，对应 K8s `StrictSerializer`），由 FMT-20 守护
   - 共享 error envelope（`contracts/shared/errors/error-response-v1.schema.json`）例外：保持 strict
   - cell event consumer 不得调用 `json.Decoder.DisallowUnknownFields()`
   - 详见 ADR `docs/architecture/202605031600-adr-v1-schema-evolution.md`
   - typed response struct（如 `Get200JSONResponse`）是 codegen 派生产物，字段演化规则同 `Response` DTO；struct 名称变更等效于 status 声明变化，由 CH-06 governance 拦截，不触发 v2 升级。新增声明的 status code 不需要 v2 升级（client 不应假定 status 集合封闭）。
2. 新增请求参数必须有默认值
3. Deprecation 至少保留 2 个 Sprint（4 周）
4. 统一列表响应格式：`{"data": [...], "nextCursor": "...", "hasMore": bool}`
5. 单资源响应格式：`{"data": {...}}`

## 内部 API

- 版本变更不需要 deprecation 告期，但必须同步更新所有消费方
