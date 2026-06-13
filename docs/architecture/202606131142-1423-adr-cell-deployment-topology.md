# ADR: Cell 部署拓扑 / location-transparent contract transport seam

- Status: Accepted
- Date: 2026-06-13
- Issue: #1960 (Epic #1423, Wave-1 US1), Closes #1960
- Scope: **方向裁决（directional）**。本 ADR 裁定「框架是否提供 location-transparent
  transport seam」并锁定 seam 的四项形态（拓扑声明载体 / sync transport 接口 / discovery /
  in-process 语义），同时 reconcile 宪法（`.specify/memory/constitution.md`）与 ADR
  `202605041430`。seam 的**实现**——assembly.yaml schema 扩展、`CellTransport`/`Resolver`
  代码、archtest funnel、broker 双闸、per-cell 凭据分区——一律属下游 US2-US8
  （#1961–#1967），**不在本 ADR 落地**。

## Context

宪法的 Cell-Native 模型本身就把「cell = 可重定位单元」设计了进去：Article I「Cell 之间
MUST NOT 直接 import 另一个 Cell 的 `internal/`」+ Article III「L1+ Cell 之间的所有交互
MUST 通过 Contract」（五 kind：`http`/`event`/`command`/`projection`/`grpc`）+ Article V
「每个 Cell 独占 DB schema，跨 Cell JOIN/FK/UPDATE FORBIDDEN」。数据主权 + contract-only +
「`assemblies/` = 物理打包」三者合起来是一条隐含承诺：**cell（内部平台 cell + 外部 cell）
既能分开部署、也能自定义组合部署，部署形态是 wiring 选择**。

这条承诺只活在宪法层，从未落进代码，并被逐步焊死成「co-located 库」的运行时假设：

| 时间 | 载体 | 偏离动作 |
|---|---|---|
| 2026-05-05 | `030-review-0504` won't-do line 37 | 「微服务化拆分 / 服务网格集成」打成 won't-do，理由「形态层冲突，N=每个客户不同部署形态」（据 ADR `202605041430` §3.1）|
| 2026-05-05 | 同 review K-04 | accesscore/auditcore/configcore「不迁移」，固化成 co-located 的「框架自带能力」 |
| 2026-05-20 → 06-02 | PR #603 → M4/#1385 | `BootstrapAuthFailObserver` 让 accesscore 绕 contract 用进程内 Go handle 直写 auditcore ledger，最终固化为 `ModuleExports.BootstrapLedgerStore` + 单进程 fx DI——co-location 从「未建的 seam」升级为「代码里硬编码的运行时假设」 |

代码考古结论（「原本支持吗」）：

- **异步（event）维度**：一直网络可达——`adapters/rabbitmq`(AMQP) + outbox 自项目早期即在。
  relocatability 的异步那一半**代码层从来都在**（只差 composition 接线，由 #1940 承载）。
- **同步 + 组合拓扑维度**：**从未支持**——composition root（`runtime/bootstrap` +
  `runtime/composition`）自诞生即单进程，只有服务端入站校验（internal listener +
  `RequireCallerCell` + service token + NonceStore），**无调用端**：无 cellID→endpoint 发现、
  无 location-transparent 内部 HTTP 客户端。

**偏离的本质（L3 概念 conflation）**：

> **「框架不 prescribe 部署拓扑」（正确，保留）** 被执行成了
> **「框架不提供 relocatability 的 seam」（错误，剥夺了产品能力）**。

「N=每个客户不同部署形态」恰恰是**支持做 seam 的论据，被当成了不做的理由**——正因形态因客户
而异，框架才该提供 location-transparent 的 contract transport，让部署形态成为 wiring 选择而非
改 cell 代码。这是宪法「cell-native = 可重定位单元」与 ADR `202605041430`「嵌入式框架 =
co-located 库」两个自我从未 reconcile 的 L3 根因；同一部署弹性缺口反复浮现（内部 cell + 外部
cell）根都在此。Wave-1 防债已完成（金丝雀 `ModuleExports.BootstrapLedgerStore` 已删并事件化
为 `event.auth.bootstrap-failed.v1` = PR #1467；per-cell migration namespace #1089 M8 =
PR #1572 / `pkg/migration/namespace.go`），但方向裁决缺位使下游 seam 任务形态未锁定。本 ADR
补这一裁决。证据底座：`specs/069-cell-deploy-topology/{research,spec,tasks}.md`。

## Reconcile thesis（核心裁决）

**裁定：框架 MUST 提供 location-transparent contract transport seam。** 拆开两件被
conflate 的事，前者保留、后者纠正纳入框架职责：

1. **「框架不 prescribe 部署拓扑」——保留。** 框架不替客户决定哪个 cell 跑在哪、几个进程、
   什么编排器；N=每个客户不同（k8s pod / lambda / cf worker / 裸金属）是事实。
2. **「框架不提供 relocatability 的 seam」——纠正。** 框架 MUST 提供一套
   location-transparent 的 contract transport，使**同一份 cell 代码（零改动）仅改
   composition/topology wiring** 即可在 (a) 单二进制 modular monolith、(b) 任意子集组合、
   (c) 多进程拆分（cell 间 event 经 broker / sync 经发现+客户端）三种形态间切换。seam 是
   能力，拓扑是客户的 wiring 选择——这正是 N 异所**要求**的，而非排除的。

**【关键自洽点】seam 经 ADR `202605041430` §3.2 自己的通道交付，不违反其权限模型。**
`202605041430` §3.2 把框架运行时权限定义为「**只读 + 经接口注入**」，§3.3 强调「GoCell 运行时
没有权限改宿主应用（它是被嵌入的库）」。location-transparent seam 完全落在这条通道内：
`CellTransport` 由 **composition root 在启动期注入**（宿主的 wiring 选择），框架只**提供接口**、
**不在运行时夺取宿主权限**、不引入 sidecar 或控制面。故 §3.3 的权限模型与 seam **不仅正交、
而是一致**——`202605041430` 自己的「经接口注入」模型本来就 license 了「框架提供 transport 接口、
宿主注入拓扑」。**conflation 不是 `202605041430` 模型的缺陷，而是下游（review line 37 / K-04）
从 §3.1 真命题误推的结论。** 因此 reconcile 不需推翻 `202605041430` 的形态约束，只需补全其
§3.1 被省略的正确推论（见「Amendments to prior decisions」）。

**本 ADR = 该 reconcile 的当前真相单源（canonical anchor）。** 「cell = 可重定位单元」这条原本
只活在宪法层的**隐含**承诺，自此显式钉死在一处；并由闭环交叉引用（`202605041430` ↔ 本 ADR ↔
`030-review-0504` ↔ epic #1423）保证任一载体都指回此处 → 概念矛盾**机器可发现**，不在各处再生。

seam 不破坏 contract-only（Article III）：sync 调用仍走 contract（`http`/`grpc` kind），只是
transport **可注入**（co-located 注入进程内短路 ↔ split 注入远程客户端）；不破坏数据主权
（Article V）：seam 只搬运 contract 请求，不开跨 cell JOIN/FK 后门。

## Decision snapshot

| # | 决策 | 裁定 | 落地（下游） |
|---|------|------|------|
| **D1** | topology 声明载体 | **assembly.yaml 扩展**（非独立 topology 文件） | US2 #1962 |
| **D2** | sync transport 接口形态 | **contract-level HTTP** `CellTransport.DoContract`（in-proc/remote 二态注入，**明确偏离** Service Weaver method-level RPC stub）| US4 #1963 / US5 #1966 |
| **D3** | discovery 接口 | **`Resolver`**（cellID→endpoint），静态配置起步，**接口形态与 #303 共享** | US5 #1966 / #303 |
| **D4** | in-process 调用语义 | 进程内短路**不 bypass** listener auth chain / contract 校验；trace/metrics **必须区分 in-proc vs remote**；L0 cell 显式豁免 | US4 #1963 |

## Decisions

### D1 — topology 声明载体 = assembly.yaml 扩展

拓扑声明是**一等 wiring**：声明「本进程装哪些 cell；不在本进程的 cell 在哪（远程 endpoint
映射）」。**裁定载体 = `assembly.yaml` 扩展**，不新建独立 topology 文件——assembly 已是
「物理打包配置」单源（`assemblies/corebundle/assembly.yaml` 现含 `id/cells/owner/build`），
拓扑是 assembly 语义的自然延伸；assembly 模型/解析锚点 `kernel/assembly/assembly.go`（+
`generator.go::GenerateBoundary` 派生 per-assembly `assemblies/<id>/generated/boundary.yaml`），
closed-set 校验模板 `runtime/composition/builder.go` validateClosedSet。

- 对标：Service Weaver `weaver.toml` 把 colocate 声明在**配置而非代码**——印证「声明在 assembly
  config」方向。
- 偏离 Dapr 运行时 placement 协商：GoCell 拓扑**静态**（启动期 WriteOnce evaluate，不在请求路径
  反复 resolve）。
- 实现（schema 字段、`gocell validate` 接受扩展、bootstrap 查询任一 cellID 的 co-located/remote
  归属）属 US2 #1962。

### D2 — sync transport 接口形态 = contract-level HTTP（`CellTransport.DoContract`）

所有 sync contract 调用 MUST 经统一 `CellTransport` seam；co-located 注入进程内短路、split 注入
远程客户端，**cell 代码与 generated contract client 接口零改动**。**裁定接口在 contract 层、
载体为 HTTP**（与现有 `http`/`grpc` contract kind 同构）：

```go
// runtime 层（最小形态；确切包名/命名在 US4 实现时定，shape 在此锁定）
type CellTransport interface {
    DoContract(ctx context.Context, contractID string, req *http.Request) (*http.Response, error)
}
```

- in-process 实现：bootstrap router build 后持有 `http.Handler`，内存 dispatch（celltest mux
  机制先例）。
- remote 实现：`Resolver` + `http.Client` + service token 签名 + principal 传播头（D3 / US5）。

**明确偏离 Service Weaver method-level 字节序列化 RPC stub**，理由是 Service Weaver 2024-12 进入
维护模式暴露的两条教训：

1. **隐藏 RPC 边界 → 错误处理 / 超时 / 幂等无处声明。** GoCell 的 contract 边界是**显式**的
   （contract.yaml 声明 idempotencyKey / deliverySemantics / 错误码），保持 contract-level 调用
   恰好规避此陷阱——绕过 contract 治理（method-level 透明 RPC）不可接受。
2. **「强制逻辑/物理完全分离」是错误前提**——物理拓扑是领域约束。GoCell 把拓扑当显式 wiring
   声明（D1），不假装它不存在。

亦偏离 go-micro 三层抽象（Registry/Selector/Client）——对 GoCell 过重，最小形态 = `CellTransport`
（contract-level Do）+ `Resolver`（D3）；健康检查已由 readyz/ConsumerBase 覆盖，无需 Watcher/Mark。

### D3 — discovery 接口 = `Resolver`（cellID→endpoint），形态与 #303 共享

cellID→endpoint 解析独立成接口，**静态配置起步**（拓扑声明里的 remote 映射），形态预留给 #303
（外部运行时注册 / xDS 式动态发现）共享：

```go
type Resolver interface {
    Resolve(ctx context.Context, cellID string) (string, error) // "host:port"
}
```

- 调用方经 cellID 逻辑寻址，**永不接触 IP**（对标 Dapr appId=cellID 逻辑寻址）。
- #303 本体（进程外控制面/数据面、动态注册）gated 在后（epic 跨 wave 顺序不变）；本 ADR 只锁
  **接口形态**，使 #1423 拆分需要的 sync 发现与 #303 是同一套机器，避免日后双造。

### D4 — in-process 调用语义：不 bypass auth、可观测区分 in-proc/remote、L0 豁免

进程内短路是**性能优化**，不是**安全/治理豁免**：

- **不得因 bypass TCP 而 bypass listener auth chain / contract 校验。** co-located 调用经短路
  送达 served handler 时，listener auth plan 与 contract 参数校验**照常生效**（短路替换的是
  网络栈，不是治理栈）。
- **trace/metrics 必须区分 in-process vs remote 调用**（Service Weaver 2024-12 第三条教训：
  「透明」不得变成「不可诊断」）。metrics `cell` label 仍来自 closed set（observability 规范）；
  in-proc 与 remote 是不同的 span/属性，运维可分辨调用穿没穿进程边界。
- **L0 cell 显式豁免 transport 判定。** L0（纯计算分区，Article I 允许同 Assembly 兄弟直接
  import、不参与契约）不经 contract、不经 transport seam——transport 规则（含下游 funnel
  archtest）MUST 显式 carve-out L0，避免把合法的 L0 import 误判为「绕过 seam 直拨」。

## Governance / enforcement 档位规划（deferred — 设目标，不在本 PR 落地）

**本 ADR 是 Soft 载体（决策记录），不自带 enforcement。** 它对「未来 AI 重新 conflate」的抗性
来自两件事：(a) 上文闭环交叉引用使矛盾**机器可发现**；(b) 下游 seam 落地时的 enforcement。
按 AI-robust 章程「新机制最低 Medium；Hard 可达则定 Hard，不以 Medium 为天花板」，本 ADR
**设下游档位目标**（实现属对应 issue，本 PR 不写 archtest/guard）：

- **sync 直拨 funnel → 目标 Hard。** generated contract client **只接收 sealed `CellTransport`**
  作为唯一可表达的兄弟-cell 调用路径——裸 `http.Client` 直拨兄弟 cell / 直接 import 兄弟 cell
  在类型层不可表达（typed marker funnel + sealed construction 范本）；Medium archtest typed scan
  作 backstop，捕获绕过 generated client 的裸调用。→ US4 #1963 / US8 #1961。
- **broker-mandatory 双闸 fail-fast → Medium（启动期 guard）。** type system 不可表达「拓扑 ×
  bus 类型」组合，故落 runtime guard：split 拓扑 ∧ in-memory bus → 静态（`gocell validate` 遍历
  contractUsages 判跨进程 pub/sub）+ 启动期（bootstrap phase0，同形于 `runtime/bootstrap/topology.go`
  既有「postgres requires real adapter」耦合规则）双闸拒绝。→ US3 #1965（blocked-by #1940）。
- **进程内跨 cell Go 直传 = 0 + gRPC 盲区收口 → Medium archtest。** 金丝雀
  `ModuleExports.BootstrapLedgerStore` 已删（PR #1467），archtest 收口直传=0 + 覆盖 #1752 引入的
  gRPC cross-cell 盲区。→ US8 #1961。

remote 出站安全直接复用既有栈，无需新造：service token 4 段 `ts:nonce:callerCell:mac`
（`runtime/auth/authenticator.go`）+ HMAC keyring + `RequiresDistributedReplay()`
（`runtime/bootstrap/topology.go`，多实例强制分布式 NonceStore）+ 服务端
`RequireCallerCell`（`runtime/auth/authz.go`）。已知缺口（per-cell 身份颁发 / mTLS 对等认证 /
`upstream-cell-unavailable` 专属错误码 KindUnavailable）记入 US5 #1966 / US6 #1964，不在本 ADR 解。

## Rejected alternatives

| 方案 | 拒因 |
|---|---|
| method-level 字节序列化 RPC stub（Service Weaver `runtime/codegen/stub.go`）| 绕过 contract 治理；隐藏 RPC 边界使错误/超时/幂等无处声明（2024-12 停更教训）|
| 独立 topology 文件 | assembly 已是物理打包单源，拆出独立文件徒增第二真相源 |
| 运行时动态 routing（Dapr placement）| GoCell 拓扑静态；动态协商引入控制面，越出「嵌入式库」形态 |
| sidecar 进程（Dapr）| GoCell 是 library-form，无独立进程；与 §3.2「经接口注入」模型冲突 |
| go-micro 三层 Registry/Selector/Client | 对 GoCell 过重；最小 Resolver+CellTransport 即足 |

## Boundaries with sibling epics & trigger gates

- **#1081（独立仓库*开发* + 组合部署）**：`composition.With` 子集组合已 ship（#1085 簇）；本 epic
  接「**分开部署**」。本 ADR 的 D1/D2 reshape #1081 的 M5(assembly=拓扑声明)/M6/M11 形态。
- **#303（外部*运行时*注册）**：discovery/transport 与本 epic 共享接口形态（D3）；#303 本体
  gated 在后，不在本 epic 实现。
- **#1337（多租户）**：per-cell 隔离与拆分场景数据隔离对齐（US6 #1964 涉及）。
- **#1940（event broker publisher funnel）**：composition root 的 real-broker publisher 路径——
  即「event 维度 broker funnel」，作为 US3 #1965 的 blocked-by，不重复建单。

下游 wave 顺序（epic #1423 DAG）：本 ADR（Wave-1 US1）合入后 US2-US7 形态锁定；W2 先 US2 #1962
（拓扑判定是 #1963/#1964 的消费源），#1963/#1964 随后并行；W3 #1965/#1966；W4 #1967。

## Amendments to prior decisions（本 ADR 同 PR 驱动）

按 AI-robust 章程「ADR amendment 落地时必须同步重评原 ADR 的威胁矩阵/安全模型；冲突段落在同一
改动中重写」，本 PR 同步：

1. **ADR `202605041430` §3.1**：就地**补全被省略的推论**（「部署多样性 N=每个客户不同」⟹ 框架
   MUST 提供 location-transparent seam，部署形态成为 wiring 选择，经 §3.2 启动期注入交付）+ 末尾
   Amendment 段记录 conflation 纠正链 + 声明 seam 与 §3.2/§3.3 权限模型**一致非违反**。§3.1 原行
   是真命题（保留），仅锁定其唯一被许可的下游推论。
2. **`docs/plans/archive/202605051600-030-review-0504-implementation.md`**：won't-do line 37
   （微服务化拆分 / 服务网格集成）+ K-04（corecells 不迁移）标 **❌ 已推翻**，指向本 ADR——拆开
   「不 prescribe 拓扑」（保留）与「提供 seam」（纳入框架职责），corecells 与外部 cell 同等享部署
   弹性。历史行保留（归档脉络），当前真相单源 = 本 ADR。

## References

- 宪法：`.specify/memory/constitution.md` Article I（Cell-Native 分层 + L0 例外）/ III（Contract
  边界纪律，五 kind）/ V（Cell 数据主权）/ VI（L0-L4 一致性）。
- 被 reconcile 的 ADR：`docs/architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md`
  §3.1/§3.2/§3.3（本 PR amendment）。
- 被推翻的 won't-do：`docs/plans/archive/202605051600-030-review-0504-implementation.md` line 37 / K-04。
- Epic：#1423；子任务 US2-US8 = #1962 / #1965 / #1963 / #1966 / #1964 / #1967 / #1961；兄弟 epic
  #1081 / #303 / #1337 / #1940 / #1752。
- 证据底座：`specs/069-cell-deploy-topology/{research,spec,tasks}.md`。
- 仓内锚点：`runtime/composition/cell_module.go`（`CellModule` / `ModuleResult{Cell,Opts,Resources}` /
  `MODULE-PROVIDE-NO-VALUE-HANDOFF-01`）、`runtime/composition/shared_deps.go`（硬编码
  `*eventbus.InMemoryEventBus`）、`runtime/composition/builder.go`（validateClosedSet）、
  `runtime/bootstrap/topology.go`（`RequiresDistributedReplay` / adapterMode）、
  `runtime/auth/{authenticator,authz}.go`（service token 4 段 / `RequireCallerCell`）、
  `assemblies/corebundle/assembly.yaml`、`kernel/assembly/{assembly,generator}.go`、`pkg/migration/namespace.go`（#1572）。
- ref: ServiceWeaver/weaver `runtime/protos/config.proto`（colocate）/ `internal/weaver/remoteweavelet.go`
  （local/remote dispatch）/ `runtime/codegen/stub.go`（被拒的 method-level stub）；go-micro
  `registry/registry.go` / `selector/default.go`。
