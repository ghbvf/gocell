# Oncall 反查链路 (`/correlate`)

> 端点由 `runtime/observability/correlate` 注册于 InternalListener，metric `cell=_runtime`（与 `/healthz`/`/metrics` 一致）。
> 设计决策见 ADR `docs/architecture/202606021400-1048-adr-observability-correlate-reverse-lookup.md`。

---

## 端点信息

| 属性 | 值 |
|------|---|
| URL | `GET /internal/v1/audit/correlate` |
| Listener | InternalListener（默认 `127.0.0.1:9090`，loopback only） |
| 认证 | service-token（HMAC + nonce 防重放，`Authorization: Bearer <token>`） |
| 授权 | 无 caller-cell allowlist——任意合法 service-token 均可调用（ops 面设计，见 ADR D4） |
| 模式 | `?traceId=<id>` XOR `?cell=<id>`，二选一；同时传或均不传 → 400 |

---

## 模式一：`?traceId=` — trace → audit 反查

```
GET /internal/v1/audit/correlate?traceId=<otel-trace-id>
Authorization: Bearer <service-token>
```

返回与该 trace_id 关联的 audit entries 列表（最多 500 条，单页无游标）。

### 响应字段（200）

```json
{
  "data": {
    "query": { "traceId": "4bf92f3577b34da6a3ce929d0e0e4736" },
    "auditEntries": [
      {
        "id":            "audit-entry-uuid",
        "eventId":       "outbox-event-uuid",
        "eventType":     "session.created",
        "actorId":       "user-uuid",
        "occurredAt":    "2026-06-02T14:30:00.000000000Z",
        "timestamp":     "2026-06-02T14:30:00.123456789Z",
        "correlationId": "correlation-uuid"
      }
    ],
    "hasMore": false,
    "returned": 1
  }
}
```

**保留字段**：`id / eventId / eventType / actorId / occurredAt / timestamp / correlationId`

**刻意排除**：`subjectId / tenantId / sessionId / payload / hash / prevHash`——minimal-PII fail-closed 设计。
`actorId`（触发主体）是 trace 关联所需的最小身份，明确保留；它可能与终端用户身份重合，
由 InternalListener loopback + service-token 双重边界缓解。`eventId` 是非 PII 的 UUID，
用于 forward-correlation（从 audit entry 回查 outbox / 日志）。
deeper identity（subject / tenant / session）及事件 payload 需走 JWT-authed auditquery endpoint
（`/api/v1/audit/entries`）。

| 字段 | 含义 |
|------|------|
| `id` | audit entry UUID（HMAC hash-chain key） |
| `eventId` | outbox 事件 UUID（非 PII，用于 forward-correlation 回查 outbox / 日志） |
| `eventType` | 事件类型（如 `session.created`） |
| `actorId` | 触发主体 ID（从 outbox entry 的 actor 字段提取；minimal-PII，可能与终端用户身份重合） |
| `occurredAt` | 域事件时间（RFC3339Nano，HMAC chain 输入之一，精度不可截断） |
| `timestamp` | audit store 落盘时间（RFC3339Nano） |
| `correlationId` | 跨 cell 关联 ID（从 W0 observability envelope 注入） |
| `hasMore` | `true` 表示该 trace_id 关联的 audit entries 超过 500 条（`traceQueryLimit`），结果已截断 |
| `returned` | 本次实际返回的条目数 |

### 分页说明

trace-mode 是 **单页 point-lookup**（上限 `traceQueryLimit=500`），**不支持游标分页**。
`hasMore=true` 说明共享该 trace_id 的 audit entries 超过 500 条，结果已截断。此时：

- 缩小时间窗口（在告警平台或 Jaeger 中定位更具体的 trace_id）再重查；
- 或改用 JWT-authed auditquery endpoint（`GET /api/v1/audit/entries`），该端点支持完整游标分页。

### 未命中

- 无匹配 audit entries → 404

---

## 模式二：`?cell=` — cell owner 反查

```
GET /internal/v1/audit/correlate?cell=<cell-id>
Authorization: Bearer <service-token>
```

返回该 cell 的 owner 信息与 metric/alert selector 指针。

### 响应示例（200）

```json
{
  "data": {
    "cellId":    "accesscore",
    "owner":     "platform-team",
    "metricSelector": "cell=\"accesscore\"",
    "alertHint": "filter alerts by cell=accesscore label"
  }
}
```

selector 是**派生字符串指针**（从 `cell.yaml` 的 `owner` 字段 codegen 派生）——非实时数据，
GoCell 不内嵌 TSDB，不查 Prometheus。

### 未命中

- `cell` 不在 assembly topology → 404

---

## 两步 Oncall 工作流（trace → cell → owner）

告警通常携带 `cell` label（Prometheus）或 trace_id（Jaeger/OTEL）。标准两步流程：

```
步骤 1  告警带 trace_id → 用 ?traceId= 定位 audit activity
步骤 2  从 audit 结果判断涉及 cell → 用 ?cell= 确认 owner / selector

或者：
步骤 1  告警直接带 cell label → 跳过 trace 步骤，直接 ?cell= 查 owner
```

**为何没有 trace → cell 直查**：audit entries 刻意不记录 source cell（补 source cell 需改
冻结 outbox wire envelope，与 D1 矛盾）。两步是接受的 UX trade-off，不是缺陷（见 ADR D3）。

---

## curl 示例

### 查 trace 关联的 audit entries

```bash
curl -s \
  -H "Authorization: Bearer $(gocell-token gen --cell ops-client)" \
  "http://127.0.0.1:9090/internal/v1/audit/correlate?traceId=4bf92f3577b34da6a3ce929d0e0e4736" \
  | jq '.data.auditEntries[] | {id, eventId, eventType, actorId, occurredAt}'
```

要同时查看分页状态：

```bash
curl -s \
  -H "Authorization: Bearer $(gocell-token gen --cell ops-client)" \
  "http://127.0.0.1:9090/internal/v1/audit/correlate?traceId=4bf92f3577b34da6a3ce929d0e0e4736" \
  | jq '{hasMore: .data.hasMore, returned: .data.returned, entries: .data.auditEntries}'
```

### 查 cell owner

```bash
curl -s \
  -H "Authorization: Bearer $(gocell-token gen --cell ops-client)" \
  "http://127.0.0.1:9090/internal/v1/audit/correlate?cell=accesscore" \
  | jq .
```

---

## 错误响应

| 状态 | 场景 |
|------|------|
| 400 | `traceId` 与 `cell` 同时传，或均未传 |
| 401 | service-token 缺失 / 无效 / 过期 / nonce 已消费（replay） |
| 404 | 未找到匹配的 audit entries 或 cell topology |
| 500 | 内部错误（查 slog `cell=_runtime` 日志） |

错误响应遵循标准 errcode envelope：

```json
{
  "error": {
    "code": "ERR_VALIDATION_REQUIRED_FIELD",
    "message": "traceId or cell query parameter required (mutually exclusive)",
    "requestId": "..."
  }
}
```

---

## 注意事项

- 该端点仅监听在 InternalListener loopback（`127.0.0.1:9090`）；k8s 上通过 exec
  进入 pod 或 port-forward 使用，**不暴露外网**。
- metric `cell=_runtime`：该端点归属 runtime 框架而非业务 cell（与 `/healthz`/`/metrics` 一致），
  SLO 告警过滤 `cell!="_runtime"` 时不覆盖该端点的错误率。
- deeper identity（subject / tenant / session）及 payload 走 JWT-authed auditquery：
  `GET /api/v1/audit/entries`（需业务 JWT，有 RBAC 检查）。

---

> 相关：`docs/ops/alerting-rules.md` HTTP `cell` label 说明中 `cell="_runtime"` 覆盖包含本端点；
> 告警 `{cell!="_runtime"}` 过滤后本端点不计入业务 SLO。
