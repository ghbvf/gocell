---
paths:
  - "cells/**/*.go"
  - "examples/**/*.go"
  - "contracts/**"
---

# Cell 实现模式

## 序列化边界

Handler 响应和事件 payload 统一用 typed struct + converter。

```go
// handler DTO
type OrderResponse struct {
    ID   string `json:"id"`
    Item string `json:"item"`
}
func toOrderResponse(o *domain.Order) OrderResponse { ... }

// 事件 DTO
type OrderCreatedEvent struct {
    ID   string `json:"id"`
    Item string `json:"item"`
}
payload, _ := json.Marshal(OrderCreatedEvent{ID: o.ID, Item: o.Item})
```

DTO 作用域：单 slice → handler.go 同包；同 cell 多 slice 共享 → `internal/dto/`；跨 cell → 禁止。

JSON 字段命名：HTTP DTO 和事件 payload 统一 camelCase。已有 snake_case 事件（session.created）在 v1.0 后迁移。

## Init() fail-fast

依赖缺失在 Init() 报错，不降级运行。`DiscardPublisher{}` 是 demo 信号。

```go
if c.publisher == nil && c.outboxWriter == nil {
    return errcode.New(errcode.ErrCellMissingOutbox,
        "requires publisher or outbox writer; use WithPublisher(outbox.DiscardPublisher{}) for demo mode")
}
```

## Contract test

HTTP 和事件都通过真实 handler 产出再验证 schema。请求路径必须使用 contract.yaml 声明的完整路径（`c.HTTP.Method` + `c.HTTP.Path`）。

**预检规则**：新增或修改 `contracts/` 下的文件后，必须运行 `go run ./cmd/gocell validate` 确认元数据一致性（TOPO-07 actor 级别、FMT 格式等）通过后再提交。

```go
h, pub := newContractHandler()
req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, body)
h.ServeHTTP(rec, req)
c.ValidateHTTPResponseRecorder(t, rec)
c.ValidatePayload(t, pub.calls[0].payload)
```
