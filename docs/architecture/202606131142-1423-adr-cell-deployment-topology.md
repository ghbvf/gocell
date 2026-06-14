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
  更新 observability 规范。
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
| **业务 principal 跨进程传播伪造** | caller 伪造他人 actor/subject/session → 越权 | **现有栈不足，是真缺口**：service token MAC（`runtime/auth/servicetoken.go`）只覆盖 method/path/query/timestamp/nonce/`callerCell`/`X-Tenant-ID`，且 `authenticator.go` 只构造 `PrincipalService{CallerCellID}`——**只认证调用方 cell 身份，不传播也不还原原始业务 principal（actor/subject/session）**。故 split 下传播业务 principal **MUST 用 tamper-evident 的 signed/sealed envelope**（或把 actor/subject/session/tenant 全纳入 MAC material）+ 专用 callee middleware 重建——不能靠「现有 auth middleware 已足够」。| US5 #1966（spec FR-006，安全 obligation）|
| **共享 HMAC keyring（无 per-cell 身份颁发）** | 单 cell 进程泄露 keyring → 可签发任意 `callerCell` | **#1964 评估并登记此缺口**：`runtime/auth/servicetoken.go` 的 4 段 MAC（`ts:nonce:callerCell:mac`）确实覆盖了 `callerCell` 字段，但所有 cell 使用**同一** `ring.Current()` 密钥签名——这只能证明「某个 keyring 持有者」发出了请求，无法证明「哪个 cell」发出。任何持有 keyring 的 cell 进程均可伪造任意 `callerCell`。推荐方向：**通过以 cellID 为 HKDF 派生上下文的 per-cell 子密钥**（`HKDF(masterKey, cellID)` → per-cell signing key），使单 cell 泄露无法伪造其它 cell 的 `callerCell`。当前补偿控制 = 服务端 `RequireCallerCell` allowlist（防止跳入预期以外的 internal endpoint）+ 可信网络/同进程假设——对 monolith/同址部署足够，**跨信任边界拆分不足**。**per-cell keyring 子密钥派生在本 PR（#1964）中不实现**，追踪在 **#2153**。 | US6 #1964（评估 + 登记；实现在 #2153）|
| **无 mTLS 对等认证** | 中间人 / 端点伪造 | service token MAC 提供消息完整性，但无传输层对等认证——此缺口已登记，**#1964 不实现 mTLS**，追踪在 **#2153** | US6 #1964（登记；实现在 #2153）|
| **token replay（多实例）** | 重放已签 token | `RequiresDistributedReplay()` 多实例强制分布式 NonceStore（**已有，US5 复用**）| 已覆盖 |
| **`upstream-cell-unavailable` 错误语义** | 远端不可达与本地依赖缺失混淆 → 误诊 | 新增的是 **`errcode.Code`（`ERR_UPSTREAM_CELL_UNAVAILABLE`），用既有 `KindUnavailable` 构造**（`pkg/errcode/status.go` 已有该 Kind，**非新增 Kind**），Code 经 `ERRCODE-PREFIX-OWNERSHIP-01` 注册 + golden。**wire 可见性警示**：`KindUnavailable.PublicCode()` 现折叠为 `ERR_SERVICE_UNAVAILABLE` 且 5xx details 强制 strip——故该专属码默认只作**服务端**诊断（log/trace/internal）；若要客户端 wire 可区分，须 US5 **有意重评 5xx public-code 投影策略** + redaction（非默认）。| US5 #1966（spec T043）|

**安全约束裁定（方向，下游遵循）**：(1) 业务 principal/tenant 跨进程传播 **MUST 经 tamper-evident
signed/sealed envelope**（经单一 sealed propagation funnel 注入 + 专用 callee middleware 重建），业务
handler 不得自构 principal header——**不得假定现有 service-token 栈已覆盖业务 principal**（它只认证
callerCell）；(2) in-proc 与 remote 同样经 `RequireCallerCell`（D4）；(3) 缺口非本 ADR 解，但 MUST
在下游 issue 落地前不被静默放过——上表即其 backlog 账。

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
