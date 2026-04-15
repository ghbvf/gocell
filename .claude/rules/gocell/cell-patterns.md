---
paths:
  - "cells/**/*.go"
  - "examples/**/*.go"
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

## Init() fail-fast

依赖缺失在 Init() 报错，不降级运行。`DiscardPublisher{}` 是 demo 信号。

```go
if c.publisher == nil && c.outboxWriter == nil {
    return errcode.New(errcode.ErrCellMissingOutbox,
        "requires publisher or outbox writer; use WithPublisher(outbox.DiscardPublisher{}) for demo mode")
}
```

## Contract test

HTTP 和事件都通过真实 handler 产出再验证 schema。

```go
h, pub := newContractHandler()
h.ServeHTTP(rec, req)
c.ValidateHTTPResponseRecorder(t, rec)
c.ValidatePayload(t, pub.calls[0].payload)
```
