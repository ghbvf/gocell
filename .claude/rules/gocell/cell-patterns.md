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

JSON 字段命名：HTTP DTO 和事件 payload 统一 camelCase。已有 snake_case 事件（session.created）在 v1.0 后迁移。

## DTO 作用域三档

按耦合范围放置，**严禁跨档跃迁**：

| 档 | 适用 | 路径 |
|---|---|---|
| A | 单 slice 自用 | `cells/{cell}/slices/{slice}/handler.go` 同包 |
| B | 同 cell 多 slice 共享（publisher + lifecycle / handler + projection 等） | `cells/{cell}/internal/dto/`（HTTP / event payload）或 `internal/domain/`（含不变量） |
| C | 跨 cell 共享 wire 类型 | **禁止手写共享包** |

C 档的禁止位置（任意均不允许）：
- `pkg/events/`（污染 pkg 边界，违反"pkg 不依赖 cells"）
- `cells/{cell}/events/`（非 internal 不等于合规；同样把跨 cell 耦合从 contract 拽回 Go 类型层）
- `contracts/event/.../payload.go`（污染权威源目录，schema 才是 SoR）
- `runtime/events/`（让 runtime 反向知道业务事件形状）

### 为什么禁止跨 cell 共享 Go 类型

CLAUDE.md "Cell 之间只通过 contract 通信"——**contract = `payload.schema.json`，不是 Go type**。
跨 cell 共享 Go 类型会：
- 把消费者 cell 的 release/build 顺序绑死到生产者 cell
- 让 Go 类型与 schema 出现两个真理源（必然漂移）
- 一旦开口，每个 cell 都会建 `events/` 包，contract 目录沦为装饰

### 跨 cell decode 重复属于预期成本

每个 subscribing cell 在自己的 `internal/dto/` 维护 typed view + decode/validate（如 `accesscore/configreceive` 与 `configcore/configsubscribe` 各 ~40 行同形态代码）是 cell 隔离架构的合理代价，**不是技术债**：

- SonarCloud / 重复率指标命中此模式时，PR review 标 "Architectural debt accepted"，**不修，也不加 sonar 豁免规则**（保持 metrics 可见）
- 不要为消除重复而引入跨 cell 共享包（A→C 的伪 DRY 重构）

### 升级到 codegen 的触发条件

如果未来 decode/validate 重复扩散到 **≥ 5 cell consumer**，启动 codegen 路线：

- 目标：`generated/contracts/event/{domain}/{name}/v{N}/payload.gen.go`
- 来源：`gocell generate event-payload` 从 `payload.schema.json` 派生（CLAUDE.md `generated/` 禁止手工编辑）
- consumer 改 import 生成产物，语义等价 protobuf `*.pb.go`——双方对齐到 schema，不对齐到对方类型

当前 `gocell` CLI 的 `scaffold` / `generate` 不含 schema → Go 能力，见 backlog 触发项 T6。

## Init() fail-fast

依赖缺失在 Init() 报错，不降级运行。Cells 持有 sealed marker 字段（`outbox.CellPublisher` / `outbox.CellWriter` / `persistence.CellTxManager`），demo 信号通过包装的 `outbox.DiscardPublisher{}` / `outbox.NoopWriter{}` 透传 `Noop()` 进入 fail-fast 检查。

```go
// cells/<x>/cell.go
type MyCell struct {
    cell.BaseCell
    pendingPublisher outbox.CellPublisher  // sealed marker，非 raw outbox.Publisher
    pendingWriter    outbox.CellWriter
    txMgr            persistence.CellTxManager
}

func (c *MyCell) Init(ctx context.Context, reg cell.Registry) error {
    // validation.IsNilInterface is fail-safe vs bare `== nil`: sealed wrappers
    // collapse typed-nil at WrapPublisherForCell (PR 441 F1), so `== nil`
    // suffices today, but using IsNilInterface here matches the kernel/runtime
    // single-source typed-nil convention and protects against future direct
    // interface assignment outside the wrap path.
    if validation.IsNilInterface(c.pendingPublisher) && validation.IsNilInterface(c.pendingWriter) {
        return errcode.New(errcode.ErrCellMissingOutbox,
            "requires publisher or outbox writer; from composition root, "+
                "wrap with outbox.WrapPublisherForCell(outbox.DiscardPublisher{}) for demo mode")
    }
    // ...
}
```

## Sealed Marker Wrap Pattern

**约束**：cells/* 公开 With* Option 不得直接接受 raw infra 类型；只接 sealed marker，由 composition root 调 wrap 函数转换。

| Raw infra（cells 不可见） | Sealed marker（cells 字段类型 + With\* 参数） | Wrapper（composition root 调用） |
|---|---|---|
| `persistence.TxRunner` | `persistence.CellTxManager` | `persistence.WrapForCell` |
| `outbox.Publisher` | `outbox.CellPublisher` | `outbox.WrapPublisherForCell` |
| `outbox.Writer` | `outbox.CellWriter` | `outbox.WrapWriterForCell` |

按 cell 真实能力声明 cell-specific Option：

- Platform cell L1/L2（`cells/*`）：`WithOutboxDeps(pub outbox.CellPublisher, writer outbox.CellWriter)` + `WithTxManager(tx persistence.CellTxManager)`
- Example ordercell L2（无 publisher 路径）：`WithOutboxWriter(w outbox.CellWriter)` + `WithTxManager(tx persistence.CellTxManager)`
- Example devicecell L4（无 writer，无 txRunner）：`WithDirectPublisher(p outbox.CellPublisher)`

Wrapper 函数**仅允许**在以下位置调用（archtest `CELL-RAW-INFRA-WRAPPER-LOCATION-01` 守卫）：

- `cmd/*` 任意文件（composition root）
- `examples/<demo>/main.go` / `examples/<demo>/app.go`（example composition root）
- `*_test.go` 任意路径（测试构造 fake）
- `kernel/persistence/cell_marker.go` / `kernel/outbox/cell_marker.go`（marker 定义本身）
- `kernel/cell/demo_tx_runner.go`（`DemoCellTxManager()` 工厂）

**Hard 防线（type system）**：cells/* 持 sealed 字段 + With\* 接 sealed 参数，raw infra 在 compile 期不可入 cell。`Wrap*ForCell` 用 `validation.IsNilInterface` 拒 typed-nil，避免 typed-nil 包成非 nil sealed 值绕过 `Init()` 与 `cell.CheckNotNoop`（PR 441 F1 修复）。

**Medium 双重防线（archtest type-aware）**：type system 单独不可达签名形态空间——`CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` 拦 inline interface embed (`func WithBad(p interface{ outbox.Publisher })`)；`CELL-RAW-INFRA-WRAPPER-LOCATION-01` 拦 dot-import wrap call (`import . "kernel/persistence"; WrapForCell(p)`)。

详见 ADR `docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md`（amends `202605101800` §D6；旧 `CELL-RAW-DEPS-01` archtest scanner 已删除，由 sealed marker 双重防线替代）。

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

## Path-param 校验（PR-A45 / CH-04 / CH-05）

UUID 类型的路径参数必须用 `httputil.ParseUUIDPathParam(w, r, name)` 在 handler
入口校验，非法值返回 400 + `ERR_VALIDATION_INVALID_UUID`，不降级到下游 404：

```go
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
    id, ok := httputil.ParseUUIDPathParam(w, r, "id")
    if !ok {
        return // 400 + ERR_VALIDATION_INVALID_UUID 已写出
    }
    user, err := h.svc.GetByID(r.Context(), id)
    ...
}
```

**权威源**：contract.yaml 中 `pathParams.{name}.format == "uuid"` 决定该参数是否
强制使用 helper。`gocell check contract-health` 的 CH-05 规则在 build 阶段静态
强制 — `format: uuid` 路径参数若未调用 `ParseUUIDPathParam` 则 fail。

**Service 边界约定**：handler 调用 helper 后传给 service 的 string 已是 canonical
lowercase UUID。Service 函数签名保持 `string` 类型，不做二次 `uuid.Parse`。
未来若 service 被 CLI/RPC 等非 HTTP 入口直接调用，新调用方负责自己的 UUID 校验。

**Contract 4xx 完整性（CH-04）**：handler 直接返回的每个 4xx/5xx 状态码必须在
`contract.yaml.responses[N]` 中声明，否则 `gocell check contract-health` fail。
errcode 间接路径（`WriteDomainError(errcode.New(...))`）也会被 AST 反查 status
覆盖。中间件/框架隐式发出的状态（401/403/429/5xx 等）由于 handler AST 不可见，
不会触发 missing 报错；contract 中的 over-declaration 也不报警告（避免噪音）。

**Listener middleware 注入的状态码**（bootstrap auth 401、rate limiter 429 等）
应声明在 `endpoints.http.auth.responses`（`[]int`）而非 `responses` map。CH-04
双源校验：`declared = responses ∪ auth.responses`，handler AST 不需发出
middleware-injected 码即可通过治理检查。示例（bootstrap 路由）：

```yaml
auth:
  bootstrap: true
  responses:
    - 401
    - 429
```

## serviceOwned ownership 声明与 owner-guard 规则

`serviceOwned: true` 的 HTTP contract **必须**在 `endpoints.http` 下声明 `ownership` 块，包含 `subjectPath` 与 `resourcePath` 两个字段，由 governance rule FMT-32 `OWNERSHIP-DECLARATION-REQUIRED-01` fail-fast 强制（schema if/then 双层：if `auth.serviceOwned == true` then `ownership` 必填；缺失即 `gocell validate` 报 error 阻断 CI）。

**路径 DSL**：

- `ctx.<seg>`：caller 主体字段，段名 camelCase（如 `ctx.subjectID` = JWT principal subject）。
- `path.<param>.<seg>`：先定位路径参数，再取资源 owner 字段，段名 camelCase（如 `path.id.userID` = 路径参数 `id` 指向的 session 记录的 `userID` 字段）。`<param>` 须在 `pathParams` 中已声明。

**示例**（`contracts/http/auth/session/delete/v1/contract.yaml`）：

```yaml
  http:
    auth:
      serviceOwned: true
    ownership:
      subjectPath: ctx.subjectID
      resourcePath: path.id.userID
```

**owner-guard 必须在 service 层，不可上移 handler**：

owner 信息（如 `sess.SubjectID`）只在 domain state（service 通过 DB 查询得到），handler 层结构上不可达。强行上移 handler 会引入双重 DB 读（Get-for-auth + Get-for-business = TOCTOU 窗口）并产生 403 泄漏（向攻击者确认资源存在）。正确形态：service 层比对 `sess.SubjectID != subjectID` 时返回 `errcode.KindNotFound`，与"资源不存在"合并为同一错误（= IDOR-safe 404 collapse，防跨用户枚举）。

archtest `SERVICEOWNED-HANDLER-OWNER-CHECK-01` type-aware 守该形态：凡 contract.yaml 声明 `serviceOwned: true` 的 endpoint 对应的 handler，若在 handler 函数体内出现 owner 对比或 `KindNotFound` 的 `ErrNotFound` 返回，则 fail（owner-guard 只能在 service 调用链内，不应在 handler 层显式出现）。删除 service 层 guard 或写成 403/非 KindNotFound Kind 亦红。

**升级路径**（触发型，见 `docs/backlog/cap-14-tooling.md` `SERVICEOWNED-HANDLER-OWNER-CHECK-01-HARD-UPGRADE`）：当前 Medium（跨函数 helper 封装形态存在理论逃逸空间）；serviceOwned endpoint ≥ 3 且形态收敛后，升级为 `auth.OwnerGuard[T]` typed funnel（Hard）。

## ADV-05 治理规则：active event 必须有 subscriber

`kernel/governance` ADV-05 在 `gocell validate` 阶段对每个 `kind: event` 的 contract 强制：
- 若 `lifecycle: active` 且 `endpoints.subscribers` 为空（nil 或 []），SeverityError 阻断 CI
- `lifecycle: draft` 给豁免（设计期允许"未连线"，转 active 时再要求订阅，对标 K8s API deprecation policy / Watermill router lifecycle）
- `lifecycle: deprecated` 给豁免（dead event 标记为 deprecated 即可）
- subscribers 可以是 cell ID 或 actor ID（actors.yaml 注册的 external 系统）

典型修复路径：
- 真死事件 → `lifecycle: deprecated`
- 设计中尚未连线 → `lifecycle: draft`（待真实 producer/consumer 落地后转 active）
- 内部 fan-out 但还没 consumer → 添加占位 cell consumer（注释说明意图）
- 对外 fan-out 给 SIEM/外部平台 → actors.yaml 注册 actor + subscribers 引用 actor ID

## typed response envelope adapter（codegen 合同）

当 contract.yaml 声明 `codegen: true` 时，`gocell generate contract` 为每个 HTTP endpoint
生成 `{HandlerMethod}ResponseObject` 接口 + 对应 typed struct（见
`tools/codegen/contractgen/doc.go`）。Cell adapter 须实现 generated `Service` 接口：

```go
// XxxAdapter 将 generated Service 接口桥接到领域 Service。
type XxxAdapter struct {
    S *Service
}

// GetItem 实现 generated pkg.Service 接口。
// 成功路径返回 typed struct，错误路径返回 (nil, err) 交由框架兜底。
func (a *XxxAdapter) GetItem(ctx context.Context, req *pkg.GetItemRequest) (pkg.GetItemResponseObject, error) {
    item, err := a.S.GetItem(ctx, req.ItemID)
    if err != nil {
        // 已声明的业务 4xx：返回 typed struct，状态码由 CH-06 静态守卫
        var notFound *domain.ErrNotFound
        if errors.As(err, &notFound) {
            return pkg.GetItem404ErrorResponse{Body: errcode.Error{...}}, nil
        }
        // 未声明的 framework 5xx：返回 (nil, err)，handler 走 httputil.WriteError 兜底
        return nil, err
    }
    return pkg.GetItem200JSONResponse{ID: item.ID, Name: item.Name}, nil
}
```

关键约定：
- 已声明的业务 4xx/5xx 必须返回对应 `Xxx{Status}ErrorResponse` typed struct，不得 `return nil, err`。
- `return nil, err` 仅保留给未在 contract.yaml 中声明的基础设施故障（panic recover、DB 断连等），由 generated handler 走 `httputil.WriteError` 兜底。
- `return nil, nil` 是合同违反，框架走 KindInternal 500 兜底并记 Error 日志（见 ADR §Runtime 行为）。

cross-ref: `tools/codegen/contractgen/doc.go` §iface_gen.go、
`docs/architecture/202605061500-adr-typed-response-envelope.md` §D1。
