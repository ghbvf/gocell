# Research: Epic #1423 探索结论（ship 阶段 1，2026-06-13）

三路并行探索：开源对标 / 仓内现状 / 边界・安全・测试。本文是任务分解（tasks.md）的证据底座。

## 1. 仓内现状（关键事实）

### 已完成（epic 写于 2026-06-02，一周内已滚动）

| 项 | 状态 | 证据 |
|---|---|---|
| 金丝雀 `ModuleExports.BootstrapLedgerStore` | **已删除**（Wave-1） | PR #1467；`runtime/composition/cell_module.go:36` 注释「ModuleExports 已删（Wave-1 #1423）」；替代 = `event.auth.bootstrap-failed.v1` 事件（accesscore emit → auditcore 消费） |
| ModuleResult 单源（#1420） | 已落地 | `ModuleResult{Cell, Opts, Resources}` + `MODULE-PROVIDE-NO-VALUE-HANDOFF-01` 签名冻结 archtest |
| per-cell migration namespace（#1089 M8） | 已落地 | PR #1572；`pkg/migration/namespace.go` sealed `Namespace`，tracking 表 `schema_migrations_<ns>`，`Builder.WithMigrations(ns, fsys)` |
| 方向 ADR | **缺失** | `docs/architecture/` 无 #1423 专属 ADR |

### Seam 缺口（任务分解依据）

| 缺口 | 现有最近锚点 | 量级 |
|---|---|---|
| topology 声明（remote endpoint 映射） | `assemblies/corebundle/assembly.yaml`（现仅 `id/cells/owner/build`）+ `kernel/assembly/assembly.go`（模型/解析）| L |
| 拓扑校验 | `runtime/composition/builder.go` validateClosedSet 模板；`runtime/bootstrap/topology.go`（现仅 adapterMode/storageBackend） | M |
| EventBus broker 注入 seam | `runtime/composition/shared_deps.go:92` **硬编码 `*eventbus.InMemoryEventBus`（非接口）**；`adapters/rabbitmq` Publisher 已存在但无 composition 接线 → **由 #1940 承载** | M |
| sync 调用端 | **完全空白**：只有服务端（internal listener + `RequireCallerCell` + service token + NonceStore）；无 cellID→endpoint 发现、无内部 HTTP client 抽象 | L |
| principal 跨进程传播 | 只有 outbox 异步通道（`kernel/outbox/principal.go` PrincipalMetadata + RestoreToContext）；**无 HTTP 头规范** | M |
| per-cell DB/broker 凭据 | `runtime/capability/capability.go` 单一全局 PG 池，所有 cell 共享 TxManager/OutboxWriter | M |
| errcode | 无 upstream-cell-unavailable 专属码（仅通用 ERR_SERVICE_UNAVAILABLE） | S |
| archtest 盲区 | gRPC cross-cell（#1752 引入后未覆盖）；sync 直拨无 funnel 规则 | M |

### 安全栈可复用度（拆分后跨进程 cell→cell）

- service token 4 段格式 `ts:nonce:callerCell:mac`（`runtime/auth/authenticator.go`）+ HMAC keyring + `RequiresDistributedReplay()`（`runtime/bootstrap/topology.go`，多实例强制分布式 NonceStore）——**可直接复用为远程客户端出站签名**。
- 缺口：所有 cell 进程共享同一 HMAC keyring（无 per-cell 身份颁发）；无 mTLS 对等认证。
- fail-fast 先例：`WithPrimaryAuthorizer` nil → phase0 拒绝；`validateNilDependencySentinels()`（`runtime/bootstrap/phases_assembly.go`）。

## 2. 开源对标结论

### Service Weaver（primary，与 epic 目标同构）

- 采纳：**local/remote 二态 stub**（同一接口、组合根按拓扑注入）；**拓扑判定启动期 WriteOnce**（不在请求路径反复 evaluate）；**colocate 声明在配置非代码**（weaver.toml → 印证 assembly.yaml 扩展方向）。
- 偏离：method-level 字节序列化 RPC stub（GoCell 保持 contract-level HTTP/JSON，绕过 contract 治理不可接受）；`Ref[T]` 隐式 embedding 注册（GoCell 用 cell.yaml/contractUsages 显式单源）；运行时动态 routing（GoCell 拓扑静态）。
- **停更教训（2024-12 维护模式）**：① 强制「逻辑/物理完全分离」是错误前提——物理拓扑是领域约束；② 隐藏 RPC 边界 → 错误处理/超时/幂等无处声明（GoCell contract 边界显式，恰好规避）；③「透明」不得变成「不可诊断」——**跨进程调用必须在 trace/metrics 区分 in-process vs remote**。
- ref: ServiceWeaver/weaver internal/weaver/remoteweavelet.go（component local/remote dispatch）；runtime/codegen/stub.go；runtime/protos/config.proto（colocate）。

### Dapr

- 采纳：appId=cellID 的逻辑寻址（调用方永不接触 IP）；pubsub 后端 manifest 切换 ↔ EventBus in-mem/broker 切换同构。
- 偏离：sidecar 进程（GoCell 是 library）、运行时 placement 协商（GoCell 静态拓扑）。

### go-micro

- 三层抽象（Registry/Selector/Client）对 GoCell 过重；最小形态 = `Resolver`（cellID→endpoint）+ `CellTransport`（contract-level Do）。无需 Watcher/Mark/Reset（健康已由 readyz/ConsumerBase 覆盖）。
- ref: micro/go-micro registry/registry.go；selector/default.go。

### 接口形态建议（供 US1 ADR 裁决起点）

```go
// runtime 层（包名待 ADR 定）
type CellTransport interface {
    DoContract(ctx context.Context, contractID string, req *http.Request) (*http.Response, error)
}
type Resolver interface {
    Resolve(ctx context.Context, cellID string) (string, error) // "host:port"
}
```

InProcess 实现：bootstrap router build 后持有 `http.Handler` 内存 dispatch（celltest mux 机制先例）；
Remote 实现：`Resolver` + `http.Client` + service token 签名 + principal 传播头。

## 3. 边界・失败模式・测试

- **broker-mandatory 双闸**：静态（gocell validate 遍历 contractUsages 判 pub/sub 跨进程）+ 运行时（bootstrap phase0：split 拓扑 ∧ in-memory bus → 拒绝）。topology.validate() 既有「postgres requires real adapter」耦合规则是同形模板。
- **缺失依赖 fail-fast**：consumer 声明的 contract 提供方 ∉ 本进程 ∪ remote 映射 → phase 校验拒绝。
- **错误语义**：新增 `errcode.Code` `ERR_UPSTREAM_CELL_UNAVAILABLE`（用既有 `KindUnavailable` 构造，非新 Kind；`pkg/errcode/status.go` 已有该 Kind），区分「本地依赖缺失」vs「远端暂不可达」；超时/拒绝/5xx 的具体原因进**服务端通道（log/trace/internal details/metrics）**，不进 5xx wire details（5xx 强制 strip + `KindUnavailable.PublicCode()` 折叠为 `ERR_SERVICE_UNAVAILABLE`，客户端 wire 区分需 US5 有意重评投影）。
- **测试基建**：journeys 现仅单进程。双拓扑验收 = 同一 journey 参数化跑 (a) 单进程 assembly (b) compose 拆分 + broker。CI 锚点 `.github/workflows/_build-lint.yml` integration-test job（testcontainers）。
- **archtest red case 范式**：synthetic 违例文件 + anti-vacuity（删除用例须转绿）；最近模板 `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`、`CTXKEYS-PRINCIPAL-WRITE-CALLER-01`。
- **覆盖率**：新增 runtime 包 ≥80%；若进 kernel ≥90%。

## 4. 与兄弟 epic 的接口

- **#1940**（OPEN, area-eventing）：composition root 无 real-broker publisher 路径——即本 epic「event 维度 broker funnel」，作为 US3 的 blocked-by，不重复建单。
- **#1081**：composition.With 子集组合已 ship（#1085 簇）；本 epic 接「分开部署」。
- **#303**：discovery/transport 与本 epic 共享接口形态；#303 本体 gated 在后。
- **#1337**：per-cell 隔离与多租户对齐（US6 涉及）。
