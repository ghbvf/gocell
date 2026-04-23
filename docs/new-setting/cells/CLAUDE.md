# cells/ 层规则

cells/ 包含各业务 Cell 的实现（accesscore / auditcore / configcore 等），每个 Cell 下含 `slices/`。

## 依赖约束

**允许**：`kernel/`、`runtime/`
**严禁**：直接依赖 `adapters/`（通过接口解耦）；直接 import 另一个 Cell 的 `internal/`

**例外**：L0 Cell（纯计算库）可被同一 assembly 内的兄弟 Cell 直接 import，无需 contract。

## DDD 分层

| 层 | 职责 | 禁止 |
|----|------|------|
| `handler` | 参数绑定 + 响应返回 | 业务判断 |
| `application` | 业务编排 | 领域规则 |
| `domain` | 聚合根 / 实体 / 值对象 / 领域服务 | 依赖框架 |
| `repository` | 数据持久化 | 业务逻辑 |

- Entity 禁止直接序列化为 API 响应，必须经 DTO 转换
- 实体含行为方法（充血模型），状态变更走方法
- 跨聚合通过 EventBus 解耦，禁止直接调 Repository
- 接口定义在 `domain/`，实现在 `repository/` 或 `infrastructure/`

## DTO 边界

| 作用域 | 位置 |
|--------|------|
| 单 slice | handler.go 同包 |
| 同 cell 多 slice 共享 | `internal/dto/` |
| 跨 cell | **禁止** |

JSON 字段命名：HTTP DTO 和事件 payload 统一 **camelCase**。

## Init() fail-fast

依赖缺失在 `Init()` 报错，不降级运行。`DiscardPublisher{}` 是 demo 信号，不得出现在生产配置中。

```go
if c.publisher == nil && c.outboxWriter == nil {
    return errcode.New(errcode.ErrCellMissingOutbox,
        "requires publisher or outbox writer; use WithPublisher(outbox.DiscardPublisher{}) for demo mode")
}
```

## Contract Test

HTTP 和事件都通过真实 handler 产出再验证 schema。路径必须使用 contract.yaml 声明的完整路径（`c.HTTP.Method` + `c.HTTP.Path`）。

```go
h, pub := newContractHandler()
req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, body)
h.ServeHTTP(rec, req)
c.ValidateHTTPResponseRecorder(t, rec)
c.ValidatePayload(t, pub.calls[0].payload)
```

## 各一致性级别测试要求

| 级别 | 必须测试 |
|------|---------|
| L0 | 纯单元测试（输入/输出验证） |
| L1 | 事务完整性测试（testcontainers + 真实 DB） |
| L2 | outbox 原子性测试 + consumer 幂等测试 |
| L3 | event replay 测试 + 投影重建测试 |
| L4 | 状态机转换测试 + 超时/重试测试 + 延迟到达测试 |

覆盖率要求：新增/修改代码 **≥ 80%**。

## 关键禁止项

- 禁止 L2 事件在写库后直接 `eventbus.Publish`（必须走 outbox）
- 禁止 consumer unmarshal 失败时直接 `return nil`（永久错误 → Reject）
- 禁止 `_ = someFunc()` 忽略错误

## Cell 间通信

只通过 contract 通信。`contractUsages.role` 按 contract kind 选取：

| kind | role |
|------|------|
| http | `serve`（提供方）/ `call`（调用方） |
| event | `publish`（发布方）/ `subscribe`（订阅方） |
| command | `handle`（处理方）/ `invoke`（调用方） |
| projection | `provide`（提供方）/ `read`（消费方） |

新增或修改 `contracts/` 文件后，必须运行 `go run ./cmd/gocell validate` 通过再提交。
