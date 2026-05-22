# API 版本策略

所有端点使用 `/api/v1/` 前缀，内部 API 使用 `/internal/v1/`。

## 何时升级版本

v1.0 GA 之前 wire 契约直接演化（无需 v2 升级、无 deprecation 期）；详见 ADR `docs/architecture/202605211200-adr-pre-v1.0-direct-v1-evolution.md`。v1.0 GA 后恢复"新增可在 v1 下加，删除/重命名/类型变更需 v2"政策。

## 向后兼容规则

1. v1 响应只增不删
   - response / event payload schema 禁止 `additionalProperties: false`（含 nested），允许 v1 持续加 optional 字段
   - request schema 必须 `additionalProperties: false`（拒未知字段，对应 K8s `StrictSerializer`），由 FMT-20 守护
   - 共享 error envelope（`contracts/shared/errors/error-response-v1.schema.json`）例外：保持 strict
   - cell event consumer 不得调用 `json.Decoder.DisallowUnknownFields()`
   - 详见 ADR `docs/architecture/202605031600-adr-v1-schema-evolution.md`
   - typed response struct（如 `Get200JSONResponse`）是 codegen 派生产物，字段演化规则同 `Response` DTO；struct 名称变更等效于 status 声明变化，由 CH-06 governance 拦截，不触发 v2 升级。新增声明的 status code 不需要 v2 升级（client 不应假定 status 集合封闭）。
2. 新增请求参数必须有默认值
3. 统一列表响应格式：`{"data": [...], "nextCursor": "...", "hasMore": bool}`
4. 单资源响应格式：`{"data": {...}}`

## 内部 API

- 版本变更不需要 deprecation 告期，但必须同步更新所有消费方
