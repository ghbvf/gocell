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

## 核心裁决：拆开两件被 conflate 的事（canonical anchor）

> 注：本节的「reconcile」指消解宪法 vs ADR 的概念矛盾，与 `kernel/reconcile`（L4 desired-state
> 收敛控制环）无关，勿混。

**裁定：框架 MUST 提供 location-transparent contract transport seam。** 拆开两件被
conflate 的事，前者保留、后者纠正纳入框架职责：

1. **「框架不 prescribe 部署拓扑」——保留。** 框架不替客户决定哪个 cell 跑在哪、几个进程、
   什么编排器；N=每个客户不同（k8s pod / lambda / cf worker / 裸金属）是事实。
2. **「框架不提供 relocatability 的 seam」——纠正。** 框架 MUST 提供一套
   location-transparent 的 contract transport，使**同一份 cell 代码（零改动）仅改
   composition/topology wiring** 即可在 (a) 单二进制 modular monolith、(b) 任意子集组合、
   (c) 多进程拆分（cell 间 event 经 broker / sync 经发现+客户端）三种形态间切换。seam 是
   能力，拓扑是客户的 wiring 选择——这正是 N 异所**要求**的，而非排除的。

**【关键自洽点】seam 经 ADR `202605041430` §3.2 时间维度模型自己的通道交付，不违反其权限模型。**
分两步精确对齐（注意区分 §3.2 的「启动期」行与「运行时」行）：

1. **注入发生在启动期。** §3.2 把「启动期」（corebundle 二进制启动）定义为框架的 `fail-fast 权限`
   时间窗——`CellTransport` 正是由 composition root 在**启动期**按拓扑声明注入（宿主的 wiring 选择，
   与既有 DI / `bootstrap.With*` option 注入同构）。
2. **注入后框架不在运行时夺取宿主权限。** §3.2「运行时」行限定框架为「**只读 + 经接口注入**」，
   §3.3 强调「GoCell 运行时没有权限改宿主应用（它是被嵌入的库）」——seam 注入完成后仅以只读接口
   形态被调用，不引入 sidecar、不引入控制面、不在运行时改宿主。

两步合起来证明 seam 与 §3.2/§3.3 权限模型 **不仅正交、而是一致**——`202605041430` 自己的「启动期经
接口注入」模型本来就 license 了「框架提供 transport 接口、宿主在启动期注入拓扑」。**conflation 不是
`202605041430` 模型的缺陷，而是下游（review line 37 / K-04）从 §3.1 真命题误推的结论。** 因此
reconcile 不需推翻 `202605041430` 的形态约束，只需补全其 §3.1 被省略的正确推论（见「Amendments to
prior decisions」）。

**本 ADR = 该 reconcile 的当前真相单源（canonical anchor）。** 「cell = 可重定位单元」这条原本
只活在宪法层的**隐含**承诺，自此显式钉死在一处；并由闭环交叉引用（`202605041430` ↔ 本 ADR ↔
`030-review-0504` ↔ epic #1423）保证任一载体都指回此处 → 概念矛盾**review 可追溯**（人/再审循引用即达，
非机器强制——本 ADR 不自带 guard），不在各处再生。

seam 不破坏 contract-only（Article III）：sync 调用仍走 contract（本 seam 覆盖 `http` kind；`grpc`
独立 seam 见 D2），只是 transport **可注入**（co-located 注入进程内短路 ↔ split 注入远程客户端）；不破坏数据主权
（Article V）：seam 只搬运 contract 请求，不开跨 cell JOIN/FK 后门。

## Decision snapshot

| # | 决策 | 裁定 | 落地（下游） |
|---|------|------|--------------|
| **D1** | topology 声明载体 | **assembly.yaml 扩展**（非独立 topology 文件） | US2 #1962 |
| **D2** | sync transport 接口形态 | **contract-level HTTP** `CellTransport.DoContract`（in-proc/remote 二态注入，**明确偏离** Service Weaver method-level RPC stub）| US4 #1963 / US5 #1966 |
| **D3** | discovery 接口 | **`Resolver`**（cellID→endpoint），静态配置起步，**接口形态与 #303 共享** | US5 #1966 / #303 |
| **D4** | in-process 调用语义 | 进程内短路**不 bypass** listener auth chain / contract 校验；trace/metrics **必须区分 in-proc vs remote**；L0 cell 显式豁免 | US4 #1963 / US8 #1961 |

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
远程客户端。**「零改动」的精确边界**：指 cell **业务代码**（handler / service / domain）零改动；
generated contract client 的注入方式会经 codegen 更新（走 `CellTransport` 而非裸 client），但该变更
由工具自动完成、不需 cell 作者手写——**cell 代码界面零变动，generated 产物由 codegen 驱动变动**
（codegen funnel + golden）。**裁定接口在 contract 层、载体为 HTTP**——故本 seam **仅覆盖 `http`
kind 的 sync contract**：

```go
// runtime 层（最小形态；确切包名/命名在 US4 实现时定，shape 在此锁定）
type CellTransport interface {
    DoContract(ctx context.Context, contractID string, req *http.Request) (*http.Response, error)
}
```

**`grpc` cross-cell relocatability 是独立 transport seam，本 ADR/epic 暂不覆盖**（`*http.Request`/
`*http.Response` 签名只表达 HTTP）——显式留为后续 scope boundary，与 #1752（gRPC 引入）对齐，届时另出
gRPC seam 接口/验收，不在本 seam 上硬塞。这是拒绝 Service Weaver method-level stub 后的有意收口：
contract-level HTTP 先落地，gRPC 走独立路径而非伪装成同一 `DoContract`。

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
- **接口 owner = 本 epic 实施方（US5 #1966，预期落 `runtime/transport` 包）**；#303 若需改 `Resolver`
  形态须经 ADR amendment 重评（与 contract-fanout 规则一致），不在 #303 单方静默改。

### D4 — in-process 调用语义：不 bypass auth、可观测区分 in-proc/remote、L0 豁免

进程内短路是**性能优化**，不是**安全/治理豁免**：

- **不得因 bypass TCP 而 bypass listener auth chain / contract 校验。** in-proc 短路分发目标 = 该
  contract **被服务的 handler 及其声明 listener 的 auth plan**（短路替换网络栈，不替换治理栈）。
  cell↔cell sync 走的是 internal contract（`/internal/v1/*`，挂 `InternalListener`，service token +
  `RequireCallerCell`）——故 **in-proc 路径也必须合成 callerCell 身份并经 `RequireCallerCell`**，
  与 remote 路径对称；不因 co-located 就降级为无鉴权直拨。
- **trace/metrics 必须区分 in-process vs remote 调用**（Service Weaver 2024-12 第三条教训：
  「透明」不得变成「不可诊断」）。裁定区分维度 = **二值 `transport_mode ∈ {in_proc, remote}`**，
  trace 侧作 span attribute、metrics 侧作 **frozen typed-enum label**——二值天然低基数，**不违反**
  observability 规范「label 值集必须冻结 / 经 typed enum 入口」（对标 OTel RPC metrics 用受控基数
  attribute 过滤）。故 metrics **可**按 transport_mode 过滤（非 trace-only），既满足「trace/metrics
  区分」又不引入高基数；`cell` label 仍来自 closed set。确切常量名与 enum 入口在 US4 #1963 锁定并
  更新 observability 规范。**#1966 review P2.6 补第二个 frozen typed-enum label `outcome`**
  （`cell_transport_requests_total{transport_mode, outcome}`，sealed `transport.TransportOutcome`
  闭值集 `{success, dial_error, timeout, canceled, resolver_error, rewrite_error}`）：**每次**分发
  都 Record（成功 + 每条失败出口），不只成功路径——否则远端不可达/超时/取消的失败率被低报。失败 kind
  与 errcode Kind 同源分类（见下 §review amendment P2.8）；超出有界 kind 的 `error.type` 细节留 span，
  不进 label（≤12 series，仍低基数）。
- **L0 cell 显式豁免 transport 判定。** L0（纯计算分区）按宪法 Article I「可被同 Assembly 内兄弟 Cell
  直接导入，但 **MUST 在 `cell.l0Dependencies` 显式声明**」——故 transport 规则（含下游 funnel archtest）
  的 L0 carve-out **仅豁免已在 `cell.l0Dependencies` 声明的合法 L0 import**；未声明的 L0 direct import
  不在豁免内，仍被拦截（不放宽宪法 Article I 现有约束）。

## Governance / enforcement 档位规划（deferred — 设目标，不在本 PR 落地）

**本 ADR 是决策记录（非 enforcement 机制），按 AI-robust 章程不自带 archtest/guard**——澄清：章程
禁的是把 Soft 当**新增 enforcement 载体**，ADR 作决策记录本就不是 enforcement 载体，故不冲突。它对
「未来 AI 重新 conflate」的抗性来自两件事：(a) 上文闭环交叉引用使矛盾**review 可追溯**（人/再审循
引用即达，非机器强制；若要机器发现需补一个下游 cross-ref guard governance 任务，本 ADR 不立此 task）；
(b) 下游 seam 落地时的 enforcement。按 AI-robust 章程「新机制最低 Medium；Hard 可达则定 Hard，不以
Medium 为天花板」，本 ADR **设下游档位目标**（实现属对应 issue，本 PR 不写 archtest/guard）：

- **sync 直拨 funnel → 目标 Hard（上下游双侧）。** 按 AI-robust「funnel 须分别说明上游和下游强度，
  只锁 callsite 不是闭环」：
  - **上游（Hard）**：`CellTransport` 实现为 sealed type（unexported 字段 + 单一 sanctioned
    constructor，预期 `runtime/transport` 包），包外不可结构字面量构造或伪造，AI 无法旁路造一个
    transport 绕过 funnel。
  - **下游（Hard + Medium backstop）**：generated contract client **只接收 sealed `CellTransport`**
    作为唯一可表达的兄弟-cell 调用路径（裸 `http.Client` 直拨 / 直接 import 兄弟 cell 在类型层不可
    表达）；Medium archtest typed scan 作 backstop，捕获绕过 generated client 的裸调用。
  - → US4 #1963 / US8 #1961。
- **broker-mandatory 双闸 fail-fast → Medium（启动期 guard，永久档位）。** 「拓扑 × bus 类型」组合
  type system **不可表达**（拓扑是启动期数据、bus 是注入实例），故 Hard 不可达、Medium 是合理永久
  天花板（非待升级 Medium）。**split 定义**：只要存在至少一条**跨进程** pub/sub contractUsage（生产
  cell 在本进程、消费 cell 在 remote，或反之）即为 split。双闸 = 静态（`gocell validate` 遍历
  contractUsages 判跨进程 pub/sub）+ 启动期（bootstrap phase0，同形于 `runtime/bootstrap/topology.go`
  既有「postgres requires real adapter」耦合规则）拒绝。→ US3 #1965（blocked-by #1940）。

  > **Amendment 2026-06-15（#2211，US3 加固）**：phase0 启动期闸 `validateSplitTopologyBroker` 的
  > 「是否真实 broker」判定，由 #1965 的 `StorageBackend()=="postgres" ∧ non-nil pub/sub` 代理升级为
  > **sealed `bootstrap.EventTransportKind` 事实**：`cellmodules/eventtransport.Resolve` 在构造 RabbitMQ
  > transport 的同一分支 mint `RealBrokerEventTransport()`，composition root 经 `WithEventTransportKind`
  > 透传 `Transport.Kind`，闸改查 `IsRealBroker()`。威胁矩阵重评：#1965 闸 godoc 记录的残留洞
  > 「`StorageBackend==postgres` + 手注入 non-nil in-memory 实例（non-nil ≠ real broker）」对 honest
  > wiring（经 Resolve）**已关闭**——Resolve 只在真实 broker 分支 mint real-broker kind；dishonest
  > forge（直调 `RealBrokerEventTransport()` 配 in-mem 总线）由 caller-funnel archtest
  > `EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01` 守，生产侧另由既有 `COREBUNDLE-EVENTBUS-FUNNEL-01` depguard
  > 使 in-mem 总线在生产 root **import 层不可表达**（Hard 端到端）。分层评级随之细化：sealed 构造 Hard、
  > minter-单调用方结构性 Medium（跨模块 `framework`↔`cellmodules`，`internal/` 不可桥接 → 无低成本 Hard
  > 路径，不立升级 issue）、闸运行时比较仍 Medium。「拓扑 × bus 类型 type system 不可表达」在 bus-realness
  > 维度被 sealed-kind 收紧；「split 是否真有跨进程 *event*」（vs sync-only-remote 粗代理）仍属 US7 #1967。
- **进程内跨 cell Go 直传 = 0 + gRPC 盲区收口 → Medium archtest。** 金丝雀
  `ModuleExports.BootstrapLedgerStore` 已删（PR #1467），archtest 收口直传=0 + 覆盖 #1752 引入的
  gRPC cross-cell 盲区。typed AST scan 即足，无低成本 Hard 化路径。→ US8 #1961。

remote 出站安全直接复用既有栈，无需新造：service token 4 段 `ts:nonce:callerCell:mac`
（`runtime/auth/authenticator.go`）+ HMAC keyring + `RequiresDistributedReplay()`
（`runtime/bootstrap/topology.go`，多实例强制分布式 NonceStore）+ 服务端
`RequireCallerCell`（`runtime/auth/authz.go`）。

## 安全模型与已知缺口（security gap matrix）

split 拓扑把若干 cell↔cell 调用从进程内移到网络上，安全边界随之变化。按 AI-robust 章程「ADR
amendment 落地时必须同步重评安全模型」，此处显式列出威胁、当前补偿与归属（实现/收口属下游 issue，
本 ADR 只锁安全约束方向）：

| 缺口 / 威胁 | split 下风险 | 当前补偿 / 约束 | 归属 |
|---|---|---|---|
| **业务 principal 跨进程传播伪造** | caller 伪造他人 actor/subject/session → 越权 | **现有栈不足，是真缺口**：service token MAC（`runtime/auth/servicetoken.go`）只覆盖 method/path/query/timestamp/nonce/`callerCell`/`X-Tenant-ID`，且 `authenticator.go` 只构造 `PrincipalService{CallerCellID}`——**只认证调用方 cell 身份，不传播也不还原原始业务 principal（actor/subject/session）**。故 split 下传播业务 principal **MUST 用 tamper-evident 的 signed/sealed envelope**（或把 actor/subject/session/tenant 全纳入 MAC material）+ 专用 callee middleware 重建——不能靠「现有 auth middleware 已足够」。| US5 #1966 → **已闭合**（折进 MAC + sealed funnel，见 §#1966 Amendment；残留 keyring 隔离归 #2153）|
| **共享 HMAC keyring（无 per-cell 身份颁发）** | 单 cell 进程泄露 keyring → 可签发任意 `callerCell` | **#1964 评估并登记此缺口**：`runtime/auth/servicetoken.go` 的 4 段 MAC（`ts:nonce:callerCell:mac`）确实覆盖了 `callerCell` 字段，但所有 cell 使用**同一** `ring.Current()` 密钥签名——这只能证明「某个 keyring 持有者」发出了请求，无法证明「哪个 cell」发出。任何持有 keyring 的 cell 进程均可伪造任意 `callerCell`。推荐方向：**通过以 cellID 为 HKDF 派生上下文的 per-cell 子密钥**（`HKDF(masterKey, cellID)` → per-cell signing key），使单 cell 泄露无法伪造其它 cell 的 `callerCell`。当前补偿控制 = 服务端 `RequireCallerCell` allowlist（防止跳入预期以外的 internal endpoint）+ 可信网络/同进程假设——对 monolith/同址部署足够，**跨信任边界拆分不足**。**per-cell keyring 子密钥派生在本 PR（#1964）中不实现**，追踪在 **#2153**。 | US6 #1964（登记）→ **#2153 已实现**：per-cell provisioning（cell 持子密钥、**master 缺席**）+ HKDF 子密钥，**split 下 CLOSED**；monolith 不变（单信任域，非 per-cell-Hard，可接受）。见 §#2153 Amendment（含对上文「per-cell HKDF」措辞的修正）|
| **无 mTLS 对等认证** | 中间人 / 端点伪造 | service token MAC 提供消息完整性，但无传输层对等认证——此缺口已登记，**#1964/#2153 均不实现 mTLS** | US6 → **defer 至 #2263**（与 token 层 per-cell 身份正交，见 §#2153 Amendment §残留）|
| **token replay（多实例）** | 重放已签 token | `RequiresDistributedReplay()` 多实例强制分布式 NonceStore（**已有，US5 复用**）| 已覆盖 |
| **`upstream-cell-unavailable` 错误语义** | 远端不可达与本地依赖缺失混淆 → 误诊 | 新增的是 **`errcode.Code`（`ERR_UPSTREAM_CELL_UNAVAILABLE`），用既有 `KindUnavailable` 构造**（`pkg/errcode/status.go` 已有该 Kind，**非新增 Kind**），Code 经 `ERRCODE-PREFIX-OWNERSHIP-01` 注册 + golden。**wire 可见性警示**：`KindUnavailable.PublicCode()` 现折叠为 `ERR_SERVICE_UNAVAILABLE` 且 5xx details 强制 strip——故该专属码默认只作**服务端**诊断（log/trace/internal）；若要客户端 wire 可区分，须 US5 **有意重评 5xx public-code 投影策略** + redaction（非默认）。| US5 #1966 → **已落地**（见 §#1966 Amendment）|

**安全约束裁定（方向，下游遵循）**：(1) 业务 principal/tenant 跨进程传播 **MUST 经 tamper-evident
signed/sealed envelope**（经单一 sealed propagation funnel 注入 + 专用 callee middleware 重建），业务
handler 不得自构 principal header——**不得假定现有 service-token 栈已覆盖业务 principal**（它只认证
callerCell）；(2) in-proc 与 remote 同样经 `RequireCallerCell`（D4）；(3) 缺口非本 ADR 解，但 MUST
在下游 issue 落地前不被静默放过——上表即其 backlog 账。

### #1966 Amendment — US5 sync 远程实现落地 + 业务 principal 传播闭合（2026-06-16）

US5（#1966）落地 sync 跨进程：`transport.Resolver`（cellID→endpoint，与 #303 共享形态）+ `transport.RemoteHTTPTransport`
（实现 `CellTransport`，service token 出站签名 + 真实 TCP）+ 业务 principal 跨进程传播 + `ERR_UPSTREAM_CELL_UNAVAILABLE`
+ 解除 interim 门。**按 AI-robust 章程逐项重评上表威胁矩阵**：

- **「业务 principal 跨进程传播伪造」行 → 现已闭合（CLOSED）**。机制：actor/subject/session 经 `outbox.PrincipalMetadata`
  序列化（TenantID 清空——tenant 单源仍走 `X-Tenant-ID`）→ base64url(compact-JSON) → 新签名头 `X-Gocell-Principal`，
  **无条件折进 service-token MAC material**（`buildServiceTokenMessage` 末段 ` x-gocell-principal=<v>`，与 `x-tenant-id`
  同机制）。故篡改/注入/剥离该头 → MAC 不匹配 → 401，**结构性不可伪造**（Hard，密码学 fail-closed）。注入收口于**单一
  sealed funnel** `auth.SignInternalRequest`（裸 `GenerateServiceToken` 经 `SVCTOKEN-CALLER-CELL-REQUIRED-01` 收口，
  函数级 carve-out 仅放行 funnel 自身的转发）；callee 验签后经 `runtime/auth/principal_propagation.go` 重建（先清 4 键
  再 restore，业务 principal WIN 过 service 派生 actor=CallerCellID），该写入点纳入 `CTXKEYS-PRINCIPAL-WRITE-CALLER-01`
  allowlist。**in-proc 与 remote 同构闭合**（configclient 无论拓扑都经 funnel 签）——位置透明覆盖业务 principal，非仅 tenant。
- **残留缺口不变**：本闭合**假定 keyring 在信任边界内可信**——「共享 HMAC keyring」（任何持 keyring 的 cell 可伪造任意
  `callerCell` 及其 principal 头）仍开，per-cell HKDF 子密钥追踪在 **#2153**；「无 mTLS」仍开（**#2153**）。即 US5 把业务
  principal 提升到与 `callerCell` 同等的 MAC 完整性等级，但**未**提升 keyring 的 per-cell 隔离强度——二者正交，后者归 #2153。
- **「token replay」「upstream-cell-unavailable」行 → 已落地**（前者复用既有分布式 NonceStore；后者 `ERR_UPSTREAM_CELL_UNAVAILABLE`
  仅 transport「连不上/超时/ctx deadline」用，5xx 仍返回 response 由调用方区分，resolver miss → `KindInternal`）。

**新增 enforcement（同 PR 三件套）**：`REMOTE-TRANSPORT-SEALED-01`（reflect 字段 freeze，Hard）；`CELLTRANSPORT-SELECT-FUNNEL-01`
（wiring 层裸构造 `transport.NewRemoteHTTP` ban，调用级 AST 扫描，Medium，仿 `REPLAYDEPS-INMEM-FUNNEL-01`）——transport 选型
经 `cellmodules/celltransport.Resolve`（topology-gated，`eventtransport`/`replaydeps`/`sagaprojectiondeps` 的第 4 sibling）。
**interim 门移除**：TOPO-12（governance）+ `metadata.CheckRemotePlacementSupported`（codegen）已删，`topology.remote` 现过
`gocell validate` + codegen；TOPO-13（split + in-memory bus → reject）随之 production-reachable，成为真门。**集成测试**为单进程
真实 TCP loopback（覆盖 sign→TCP→verify→handler→response + connection refused/timeout/5xx/401/403/resolver-miss 全分支）；
真双进程端到端属 US7 journey 验收，非本 issue。

### #1966 review Amendment — 门删后威胁矩阵重评 + 远程边界语义/可观测收敛（2026-06-16）

`/pr-review` #2228 R1/R2 提出：本 PR **删除 interim 门**（TOPO-12 + `CheckRemotePlacementSupported`）后，
`topology.remote` 成 production-reachable，故「共享 HMAC keyring + 自报 `callerCell` + 明文 HTTP」不再是
理论缺口，而是真实生产路径——质疑 split topology 在 per-cell 身份 / mTLS 落地前是否可合并。按 AI-robust 章程
「ADR amendment 落地必须同步重评威胁矩阵」，逐项裁定：

- **per-cell keyring 隔离（矩阵行「共享 HMAC keyring」）+ mTLS（行「无 mTLS」）→ 维持 #2153 分阶段 defer
  （决策 A）**。理由：二者是 epic 级密码学/身份工作流（HKDF 子密钥派生 / 密钥分发 / 轮换 / SPIFFE-SVID 或
  mTLS 证书），已有 OPEN tracking issue **#2153**（US6 实施）+ 上表登记，**不**塞进本 transport-shape PR。
  门删**不引入新缺口**——它使**既有登记缺口可达**，故本 amendment 的职责是把**操作边界写明、写响**，而非
  静默放行。
- **操作约束（文档化、fail-closed-by-deployment）**：在 #2153 的 TLS/per-cell-key 落地前，`topology.remote`
  **MUST 仅部署于可信/私有网络**，且 ① 所有 cell 进程共享同一 `GOCELL_SERVICE_SECRET`（跨进程 service token
  验签前提）；② internal listener 绑定 pod/网络可达地址并由 NetworkPolicy/VPC 限制 ingress 至授权 caller cell；
  ③ 服务端 `RequireCallerCell` allowlist 仍是当前补偿控制（防跳入预期外 internal endpoint）。该 checklist 落
  `docs/guides/deployment-topology.md`（#1966 review P2.10），把「明文 + 共享 secret + 自报身份」的适用边界
  与残留风险对运维显式可见——区别于「悄悄能跑」。残留威胁画像：明文 = wire 无机密性（私网部署补偿，mTLS 归
  #2153）；shared keyring = 已被攻陷且持 secret 的 cell 可伪造他 cell 身份（per-cell HKDF 归 #2153）。MAC 仍
  保证 `callerCell` + principal **完整性**（Hard）不变。**`netutil.go` 对 TLS/loopback enforcement 的 US6
  归属注记保持不动**（不在本 PR 反转该 scope）。
- **远程错误语义对齐既有 contract（P2.8）**：`RemoteHTTPTransport` 对 `client.Do` 失败按因分类——caller-ctx
  cancel → `KindClientClosed`(499)；deadline-exceeded / net timeout → `KindDeadlineExceeded`(504，复用既有
  `pkg/errcode/status.go` 映射)；其余 dial（refused/reset/DNS）→ `KindUnavailable`(503)。errcode Code 恒为
  `ERR_UPSTREAM_CELL_UNAVAILABLE`（Kind 驱动 HTTP status，Code 作服务端诊断）——不再把 timeout 误折成 503。
- **失败也进 transport metrics（P2.6）**：`cell_transport_requests_total` 加 sealed `outcome` 第二 label，
  **每条出口**（成功 + dial/timeout/cancel/resolver/rewrite 失败）Record，失败率不再低报。详见上 §D4 + observability 规范。
- **端点 path/query/fragment fail-closed（P2.9）**：`netutil.IsValidNetworkAddress`（配置期校验）+
  `rewriteToAbsolute`（请求期纵深）拒绝携 path/query/fragment 的 endpoint（仅容忍裸根 `/`），不再静默截断。
- **生产 remote metrics 接线（P1.3）**：composition `SharedDeps.TransportMetrics` 暴露 Build 单例 metrics，
  accesscore `celltransport.Resolve` 传入（非 nil），split topology remote 调用现发 `transport_mode=remote`
  指标（关闭 ADR D4 指标缺口）。**span tracer 半残留**：cross-cell span tracer 在 bootstrap phase5
  late-bind（`InProcessTransport.Bind`），module-Provide 期不可得 → remote span tracing 追踪在独立 follow-up
  issue（结构性 late-bind blocker，非静默缺口）。**→ #2251 已闭合**（见 §#2251 Amendment）。
- **AI-robust 护栏补全（P1.4 / P1.5）**：`SVCTOKEN-CALLER-CELL-REQUIRED-01` 增「auth 包外生产代码禁直调
  `GenerateServiceToken`（须走 `SignInternalRequest` funnel）」arm + red fixture——把 godoc 已宣称但未 enforce
  的禁令落为机器可判定（Medium）；`CELLTRANSPORT-SELECT-FUNNEL-01` 扫描根加 `corecells` + red fixture，封死
  core cell 直构 `transport.NewRemoteHTTP` 绕 topology gate 的未来路径。

### #2153 Amendment — per-cell service-token 身份隔离落地（2026-06-16）

US6（#2153）落地 token 层 per-cell 密钥隔离，**使 split 部署可安全发生**（破除「无 split 故不建 / 不建故不敢 split」
的死锁）。按 AI-robust 章程逐项重评威胁矩阵：

- **「共享 HMAC keyring」行 → split 下 CLOSED**。机制：service-token 的 HMAC keying 从「单一 master 直接签」改为
  **per-cell HKDF 子密钥**——签发用 `HKDF(parent, info=cellID)`（`deriveCellSecret`，RFC 5869，HMAC-SHA256，
  `crypto/hkdf` stdlib，wire 格式不变：仍 `ts:nonce:callerCell:mac`、MAC 仍 32B，故无版本目录），验签按 token 自报
  `callerCell` 取对应子密钥。kernel `auth.ServiceKeyring`（`SigningSecrets(ownCell)`/`VerifySecrets(callerCell)`/
  `Validate()`，取代旧 `HMACKeyring{Current/Secrets}`）有两个实现：① `HMACKeyRing`（monolith，持 master，按需派生）；
  ② `ProvisionedKeyring`（split，**master 缺席**，只持自身签名子密钥 + 其声明 caller 的验签子密钥）。
- **关键修正（对上文威胁矩阵「per-cell HKDF」措辞）**：上文与 #1966 review amendment 把「per-cell HKDF」当作该威胁的
  修复方向，**措辞有缺陷** —— HKDF over **仍共享的 master** 不修复它：持 master 者可 `HKDF(master, 任意 cellID)`
  派生任意 cell 子密钥、照样伪造。**Hard 属性的唯一判据是 master 在 cell 进程缺席**（`ProvisionedKeyring`）。故：
  - **split（`ProvisionedKeyring`）→ Hard**：被攻陷 cell 既无 master、也无他 cell 子密钥（验签集按声明 caller
    least-privilege 收窄），跨 cell `callerCell` 伪造在密码学上 fail-closed（签发侧：cell 只能签自身；验签侧：未声明
    caller 无子密钥 → 拒）。
  - **monolith（`HMACKeyRing`，master 在进程内）→ 明确非 per-cell-Hard，可接受**：monolith 是单一信任域，
    进程内派生无隔离收益；行为等价旧栈（只是 MAC key 变成派生子密钥）。**这正是「仅最小 HKDF over 共享 master」
    方案被否的原因**（详见实施计划 `2153-*.md` 自审）。
- **密钥分发（library-form，无 key service）**：`gocell derive-service-keys --cell <id>` 从 master 派生该 cell 的
  签名子密钥 + 其声明 caller 的验签子密钥，输出部署 env 块（`GOCELL_SERVICE_CELL` / `GOCELL_SERVICE_SIGNING_KEY` /
  `GOCELL_SERVICE_VERIFY_KEYS` [+ `_PREVIOUS`]）。operator 在每个 split cell 部署时跑一次，进程只拿子密钥、不拿 master。
  派生单源（`auth.DeriveProvisionedKeys` 复用 `deriveCellSecret`），故 CLI 产物与 monolith 运行时派生字节一致、跨模式可互验。
- **env 层互斥 fail-closed 守卫（Medium）**：composition root `buildInternalServiceKeyring` 二选一——master
  （`GOCELL_SERVICE_SECRET`）**XOR** provisioned（`GOCELL_SERVICE_SIGNING_KEY`...）；两者皆设（provisioned cell 又持
  master）或皆缺 → 启动 fail-closed。
- **轮换**：复用既有 current/previous 双密钥（验签 try current 后 previous）；master rotation → 重跑 CLI 重新分发 →
  重叠窗口平滑切换，**零 wire 改动**。

**残留（显式 backlog，不 silent）**：
- **mTLS / SPIFFE-SVID 对等认证** → **#2263**。与 token 层 per-cell 身份**正交**且**不构成死锁**：token 层
  落地后 split 已密码学 fail-closed 可上（私网部署补偿明文），mTLS 是叠加的传输层防御（端点对等认证 + wire 机密性），
  自带独立大工作流（证书/SVID 签发/分发/轮换）—— 真正可排下一环，非循环借口。
- **「remote placement 强制 provisioned」enforcement** → **#2265**（blocked-by US7 真实 process-splitting）。今
  `Topology` 无 remote/split 字段、`topology.remote` 仍单进程 loopback，无真实分进程信号；该 PR 将用本单的
  `ProvisionedKeyring` 能力 + 加该 enforcement。非本单 Hard 属性所必需（Hard 来自 cell 跑 `ProvisionedKeyring`、
  master 缺席本身）。
- **per-cell 独立轮换**（不动 master、靠 token 版本段区分）→ **#2264**；需 wire 加版本段，非本单 Hard 所必需。

**新增 enforcement**：per-cell 伪造防护 = 密码学 fail-closed（split，Hard）；env 层 master XOR provisioned 互斥 =
bootstrap guard（Medium）；CLI↔运行时派生一致 = 单测锁定（同源 `deriveCellSecret`）。**不新增 archtest 扫描**——派生
收口于既有 `SVCTOKEN-CALLER-CELL-REQUIRED-01` funnel 内的 sign/verify，以行为测试（跨 cell 伪造拒绝 + least-privilege
验签 + 轮换 + 跨模式互验）作机器守卫。

### #2251 Amendment — remote 可观测/Ops 收尾（span 半闭合 + readiness 闭环）

承接 §#1966 Amendment 拆出的两条 follow-up，**关闭 D4 trace 半缺口**并补 remote peer 的 readiness 闭环。

- **span tracer 单源（P1.3，D4 span 半闭合）**：放弃「remote 须复制 in-proc phase5 late-bind」的方向
  ——remote transport 不依赖 phase5 才存在的 handler（只需 tracer），故构造期即可注入。tracer + metrics
  收敛进 sealed `transport.CrossCellObs`（unexported 字段）；`SharedDeps` 以输入 `Tracer` + 派生
  `TransportObs` **替换**松散 `TransportMetrics` 字段，Builder 同时 `bootstrap.WithTracer`
  （router/in-proc/event-router）+ 注入捆绑（remote），故 remote 与 in-proc span **同源**。
  `celltransport.Resolve` 收**一个**预建捆绑参——边界上无可省略/可 nil 的 tracer 参数，「接 metrics 忘
  tracer」**类型级不可表达**（Hard sealed-construction）。范围 seam-only：`SharedDeps.Tracer` 可 nil 或
  typed-nil（经 `validation.IsNilInterface` 归一降级 NoopTracer，#2251 review F2），生产暂未接真 otel（见 EPIC）。
  - **评级分层（AI-robust「funnel 双向锁」，#2251 review F1）**：「remote 与 in-proc span 同源」依赖
    「唯一 minter = `composition.Builder`」，须分上下游评级。**下游 = Hard**：`CrossCellObs` 字段 unexported，
    包外不可 struct-literal 伪造，唯一 mint 路径是构造器。**上游「只 composition mint」= Medium**：由
    caller-funnel `CROSSCELLOBS-MINTER-FUNNEL-01`（type-aware AST scan + RED fixture）守。**原 amendment 把
    整句标 Hard 是 overclaim**——`NewCrossCellObs` 是 exported 构造器；bundle 类型在 `framework/runtime/transport`、
    minter 在 `framework/runtime/composition`、consumer 在 `cellmodules/celltransport`，三包跨两模块、字段所有权
    属 transport，sealed mint token 会成跨模块 import cycle，故 Go 可见性**不可**表达「只 composition mint」。
    这是与 `EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01` / `COMMAND-ASYNC-EMIT-CALLER-01` / RowScopeAll minter
    同族的文档化永久 Go/module 天花板，不开 fake Hard-upgrade issue。
- **remote peer readiness（P2.7）**：`celltransport.Resolve` 的 remote 分支经 `ModuleResult.Resources`
  注册 `<cell>_remote_ready`（typed `healthz.RemoteCellReadyProbeName`）。probe 只对 resolved endpoint 做
  **TCP dial**（`transport.EndpointDialTarget` 解析，与 `rewriteToAbsolute` 同源），**不**打远端 `/readyz`
  ——cascade-safe（避免 A↔B 互探 readiness 死锁）。peer 不可达 → 本 cell `/readyz` 降级（运维摘流量），
  只进 readiness aggregator、**不** kill liveness。**威胁矩阵补全**：D4 此前认为「健康检查已由 readyz/
  ConsumerBase 覆盖」对 remote peer 不成立（peer down 时本 cell /readyz 仍 200）——本条把 remote 依赖纳入
  本 cell readiness 闭环，纠正该盲区。
- **范围切割（EPIC）**：本 PR 只做 TCP-dial 基线。更丰富的 peer health（HTTP `/readyz` 深度探测 + 远端
  health-listener 地址发现、多副本 endpoint 替换 `StaticResolver`、liveness/readiness 编排、依赖环检测、
  对标 k8s controller-runtime/readiness gate）归独立 EPIC「类 k8s cell 运行时管理」。
- **golden 顺带修复**：`SHAREDDEPS-FIELDSET-FROZEN-01` golden 此前漏更 #1966 加的 `TransportMetrics`
  字段（drift，base 已红）；本 PR 移除该字段 + 加 `Tracer`/`TransportObs`，golden 同步归绿。

### #1964 Amendment — per-cell 基础设施 seam 落地记录

**#1964（US6）实际落地内容**：

**per-cell DB 凭据 / 连接注入 seam**（`cellmodules/percellpg`）已在本 PR 落地：composition root
可为每个 cell 注入独立的 `GOCELL_<CELLID>_DATABASE_URL`，seam 以 DSN 去重——同一 DSN 的 cell
共享连接池（monolith 常见形态），不同 DSN 的 cell 持有独立连接池（split / per-cell DB 形态）。
若某 cellID 缺少对应的 `DATABASE_URL` 配置，启动期 fail-closed（非静默降级回全局 pool）。注意：
**split 拓扑下 per-cell outbox relay 扇出** 尚未实现（每条 outbox entry 需由所属 cell 的连接池读取并
relay 到 broker），此部分追踪在 **#2152** 中；在该 issue 落地前，多个 DSN
（即真正 split 的 per-cell DB）的组合在启动期以「多于 1 个不同 DSN」作为 fail-closed 边界。

**broker 连接为 assembly 级（非 per-cell seam）的设计论据**：broker（RabbitMQ，`GOCELL_AMQP_URL`）
是跨 cell 事件总线的传输介质——其天然语义是「跨 cell 共享」，而非「per-cell 独立」。若为每个 cell
引入独立 broker 连接甚至独立 broker 实例，跨 cell 的 publish/subscribe 链路将被切断（cell A 发布到自己
的 broker，cell B 订阅自己的 broker，事件永远无法跨越）。因此 **per-cell 独立 broker 连接不是一个有意义
的 per-cell-DB 式 seam**——正确的单元是 per-进程（assembly 级）连接，即现有的 `GOCELL_AMQP_URL`。
进程内 per-cell publisher/subscriber 扇出（即在同一 broker 连接上为每个 cell 派生独立的 exchange /
routing-key 命名空间）属于 per-cell relay 扇出范畴，与 per-cell DB relay 一起追踪在 **#2152** 中。

### #1967 Amendment — 「彻底拆分」能力落地：groups/role 子集挂载 + 双拓扑验收（2026-06-17）

US7（#1967）把 SC-001 验收信号「可执行化」时暴露一个**模型缺口**（非疏忽）：所有拆分 seam（US2-US6/US8）
均已 ship，但**没有任何端到端「以子集运行 + 把对端当 remote」的接线**——生产 `corebundle`
`generatedCellModules()` 无条件挂载**全部** `asm.Cells`，`generatedDeploymentTopology()` 恒空。根因：epic
按 seam 拆分并假定「subset 部署 = 写一个子集 composition root」（`composition.With`，#1081），从未把
「assembly 声明拓扑 → 进程只挂 colocated 子集 → 其余当 remote」这条桥接接上。本 amendment 裁定**把该能力
做进生产**（用户裁定「彻底拆分」，非另造测试 harness——后者是平行结构），并细化 D1/D4。

**开源对标深化（13 框架，写入对标库索引）**：「一份代码 + 配置驱动子集部署 + 位置透明调用」的共识模式 =
**枚举全部组件 → 本进程只实例化 colocated 子集 → 其余给位置透明 client**。代表实现：Service Weaver
`component.local` `WriteOnce[bool]`（`internal/weaver/remoteweavelet.go`，deployer 启动期决定）；**Akka
ClusterSharding** `init()` 在所有节点调用、role 匹配建真 `ShardRegion`/不匹配建 proxy（`.withRole()`）；
kube-controller-manager `NewControllerDescriptors()` 枚举全部 + `--controllers=` 子集实例化；Orleans
`[SiloRoleBasedPlacement]` + silo metadata role；Helm `{{ if .Values.x.enabled }}`；Spring
`@ConditionalOnProperty`。事件传输 swap（in-mem↔broker）+ in-mem 不跨进程 fail-fast：Spring Modulith
`@Externalized` / MassTransit `UsingInMemory` vs `UsingRabbitMq`（均已对应 #1965）。静态边界校验：Spring
Modulith `ApplicationModules.verify()`（对应缺失依赖闸）。多拓扑测试 gold standard = Service Weaver
`weavertest.Local`/`.Multi` 同 test 双 runner（对应 US7 双拓扑 journey）。**核心借鉴 = Akka 节点角色模型**：
一份制品、静态 role 配置选 role；与 ADR D1「拓扑静态声明在 assembly.yaml」一致（role 选择是启动期 env，
仍 WriteOnce、非 Dapr 式运行期协商）。
ref: ServiceWeaver/weaver `internal/weaver/remoteweavelet.go`；akka/akka cluster-sharding `withRole`；
kubernetes `cmd/kube-controller-manager` ControllerDescriptor；spring-projects/spring-modulith
`ApplicationModules.verify`。

**D1 amendment — topology 载体增 `groups`（全图），取代单进程 `colocated/remote` authoring**：
- assembly.yaml `topology.groups: [{role, cells, endpoint}]` 声明**完整部署分区图**（每组的 cell 集 + 对端可达
  endpoint）；`ValidateTopologyStructure` 重写为 groups 的**穷尽 + 互斥分区**校验（每 cell 恰在一组、role 唯一、
  endpoint 合法）。**删除**现有 `TopologyMeta.{Colocated,Remote}` **authoring** 字段 + 单进程视图校验 +
  `ClassifyCell` 单视图分支（#1962 引入，生产零 assembly 使用，pre-GA 窗口允许原地删除，无 shim/双 schema）。
- 启动期 role 选择器 `GOCELL_CELL_ROLE`（WriteOnce）由 groups 派生本进程 `bootstrap.DeploymentTopologySpec`
  （colocated = 本组 cells，remote = 其余组 cells × 各自 endpoint）——**运行期 `DeploymentTopologySpec{Colocated,
  Remote}` 作派生形态保留**（仍被 `celltransport.Resolve` 消费，#1966 接线不变，喂入值变真）。未设 role + 多 group
  → fail-fast；未设 + 单/无 group → 全 colocated（零迁移默认不变）；role ∉ 声明集 → fail-fast。
- codegen：`generatedDeploymentTopology()`（恒空单 spec）替换为 `generatedTopologyGroups()`（全图）+ golden。
- 备选 (i) 多 assembly（每 tier 一 assembly + 各自二进制）与 (ii') 纯运行期 env 拓扑均拒（见 Rejected alternatives）。

**D4 amendment — M12a 闭集语义精化 + 新增 `MOUNTED-EQUALS-COLOCATED` 守卫**：
- M12a（`validateClosedSet`，#1093）守的是 `New==With` 双射，**不是** `mounted==colocated`——若把未过滤全量模块喂
  `New(allCells).With(allMods)`，M12a 通过但拆分被破坏（remote cell 仍被本地挂载，静默双挂）。故闭集**语义**由
  「assembly 声明的全部 cell」精化为「本进程 host 的 colocated cell」（对标 controller-runtime ControllerDescriptor
  「枚举全部、实例化 enabled 子集」/ Akka role-match-or-proxy）；`validateClosedSet` 代码不变（仍校验 New==With，
  在 role 过滤后 New/With 同为 colocated，双射天然成立）。
- **新增** bootstrap-phase 守卫 `MOUNTED-EQUALS-COLOCATED`：本进程已挂 cell 集 == active topology colocated 集
  ∧ **remote cell 一律不得被挂载**，违反 fail-fast。配单一 sealed 入口 `composition.NewForRole(topo, role,
  allModules...)`（内部 filter+New+With+封 spec，happy-path 下「挂 remote cell」经 funnel 不可表达）作 downstream
  收紧。**AI-robust 评级 Medium**（运行期 bijection 守卫；cellID 是运行期字符串，Hard 不可达——同 M12a /
  broker-mandatory闸 的诚实天花板）；funnel 双向 = `NewForRole` 单入口（downstream，包内 sealed）+ phase guard
  （upstream backstop，捕获绕过入口的直挂）。red/green fixture + anti-vacuity。

**缺失依赖启动期闸（SC-002 sync 维补全）**：消费 contract 的 provider cell ∉ colocated ∪ remote map → fail-fast
（区分「本地依赖缺失」vs「拓扑漏声明」）+ `gocell validate` 静态 arm。**注**：该静态检查原属 US2 T011（计划但未落地）
+ US3 T030（event 维，已由 #1965 broker 闸覆盖）；本 amendment 把 sync 维补全并加运行期闸。对标 Spring Modulith
`ApplicationModules.verify()` 的 allowed-dependencies。sync 维（本闸）+ event 维（#1965）合起来 = 无静默死路由。

**威胁矩阵重评（AI-robust 章程：amendment 必须同步重评）**：本 amendment 使 split **真实端到端可发生**（此前
seam 齐全但无接线）。关键前置已落地——「共享 HMAC keyring」缺口经 **#2153（per-cell HKDF 子密钥 + master 缺席）
在 split 下 CLOSED**（见 §#2153 Amendment）；「无 mTLS」defer **#2263**（与 token 层身份正交）；remote 操作约束
（私网部署 + 共享 `GOCELL_SERVICE_SECRET` + `RequireCallerCell` allowlist）见 §#1966 review Amendment，groups 多
进程部署沿用之。本 amendment **不新增**安全缺口——它把既已可达的 remote 路径从「单进程内不触发」变为「双拓扑验收
触发」，威胁画像不变（MAC 完整性 Hard + per-cell 身份 #2153 + 私网/mTLS-#2263）。

**PR 分解（mini-epic 收口，逐个独立 review，TDD RED→GREEN 在各 feature PR 内；能力 = US9 #2278，验收 = US7 #1967）**：
- **PR-0（本 PR）**：本 amendment + `specs/069 tasks.md` US7 细化 + sibling issues。**docs-only、全绿**（不携 RED stub——
  独立可合并 PR 不挂失败测试；TDD RED→GREEN 落各 feature PR 内）。
- **PR-1**：`topology.groups` schema（取代 colocated/remote authoring）+ 重写 `ValidateTopologyStructure` + codegen
  golden（Hard golden + Medium validate）。
- **PR-2**：role 选择器 + `NewForRole` 子集挂载 + `MOUNTED-EQUALS-COLOCATED` 守卫（Medium）。
- **PR-3**：缺失依赖启动期闸 + `gocell validate` 静态 arm + archtest red/green（Medium）。
- **PR-4（= #1967 验收）**：`corebundle` assembly.yaml `topology.groups`（accesstier/configtier）+ `tests/e2e/` split
  compose（同 image 2 进程 + broker + PG）+ 双拓扑参数化 journey 一致性断言 + CI 接入（对标 weavertest Local/Multi）。

AI-robust 新机制评级：groups codegen golden = **Hard**；groups 校验 / role fail-fast / `MOUNTED-EQUALS-COLOCATED`
/ 缺失依赖闸 = **Medium**（拓扑/role/cellID 均运行期数据，Hard 不可达，诚实天花板，无 Soft）。

**范围切割（显式 backlog，不静默）**：外部 cell（ssobff 等）双拓扑覆盖（epic 验收第二条）→ 新 backlog issue；
per-cell relay 扇出 → 已 #2152；mTLS → 已 #2263。

## Rejected alternatives

| 方案 | 拒因 |
|---|---|
| method-level 字节序列化 RPC stub（Service Weaver `runtime/codegen/stub.go`）| 绕过 contract 治理；隐藏 RPC 边界使错误/超时/幂等无处声明（2024-12 停更教训）|
| 独立 topology 文件 | assembly 已是物理打包单源，拆出独立文件徒增第二真相源 |
| 运行时动态 routing（Dapr placement）| GoCell 拓扑静态；动态协商引入控制面，越出「嵌入式库」形态 |
| sidecar 进程（Dapr）| GoCell 是 library-form，无独立进程；与 §3.2「经接口注入」模型冲突 |
| go-micro 三层 Registry/Selector/Client | 对 GoCell 过重；最小 Resolver+CellTransport 即足 |
| 子集挂载方案 (i)：每 tier 一 assembly + 各自二进制（#1967 amendment）| N tier = N 个 cmd 二进制 + 各 assembly.yaml 重复列全 cell 分区；与「单 image 部署期可拆分」诉求不符，不如 groups/role 单制品优雅（对标 Akka 单 jar 多 role）|
| 子集挂载方案 (ii')：纯运行期 env 拓扑（无 assembly.yaml 声明，#1967 amendment）| 拓扑变运行期输入、绕过 `gocell validate` 静态门，削弱 SC-002「100% 静态/启动期拒绝非法组合」；违 D1「拓扑静态声明」。groups（静态可验证）+ role 选择器（启动期 WriteOnce）是 ADR-faithful 折中 |

## Boundaries with sibling epics & trigger gates

- **#1081（独立仓库*开发* + 组合部署）**：`composition.With` 子集组合已 ship（#1085 簇）；本 epic
  接「**分开部署**」。本 ADR 的 D1/D2 reshape #1081 的 M5(assembly=拓扑声明)/M6/M11 形态。
- **#303（外部*运行时*注册）**：discovery/transport 与本 epic 共享接口形态（D3）；#303 本体
  gated 在后，不在本 epic 实现。
- **#1337（多租户）**：per-cell 隔离与拆分场景数据隔离对齐（US6 #1964 涉及）。
- **#1940（event broker publisher funnel）**：composition root 的 real-broker publisher 路径——
  即「event 维度 broker funnel」，作为 US3 #1965 的 blocked-by，不重复建单。

下游 wave 顺序（epic #1423 DAG）：本 ADR（Wave-1 US1）合入后 US2-US7 形态锁定。**US8 #1961
（直传=0 + gRPC 盲区 archtest，no-regret）无前置依赖，与 US1 并行开工（Wave-1）**；W2 先 US2 #1962
（拓扑判定是 #1963/#1964 的消费源），#1963/#1964 随后并行；W3 #1965/#1966；W4 #1967。

**分段可执行验收 checkpoint**（避免端到端正确性拖到 W4 才可验）：W2 = US4 单进程 transport 闭环 +
contract 测试全过；W3 = US5 远程调用 + principal/tenant 传播集成测试；W4 = US7 #1967 双拓扑同一
journey（单进程 ∧ 拆分+broker）全绿（epic 验收信号，spec SC-001）。

## Amendments to prior decisions（本 ADR 同 PR 驱动）

按 AI-robust 章程「ADR amendment 落地时必须同步重评原 ADR 的威胁矩阵/安全模型；冲突段落在同一
改动中重写」，本 PR 同步：

1. **ADR `202605041430` §3.1**：就地**补全被省略的推论**（「部署多样性 N=每个客户不同」⟹ 框架
   MUST 提供 location-transparent seam，部署形态成为 wiring 选择，经 §3.2 启动期注入交付）+ 紧邻
   Amendment 段记录 conflation 纠正链 + **逐项重评 §3.2（启动期注入 + 运行时只读）/ §3.3（运行时无权
   改宿主）/ §3.4（双层 harvest 域正交、边界清晰）与 seam 一致**。§3.1 原行是真命题（保留），仅锁定
   其唯一被许可的下游推论。
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
- Epic：#1423；子任务 US2=#1962 / US3=#1965 / US4=#1963 / US5=#1966 / US6=#1964 / US7=#1967 /
  US8=#1961；兄弟 epic #1081 / #303 / #1337 / #1940 / #1752。
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
