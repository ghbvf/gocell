# ADR: 运行时契约注册中心 — dual-track 治理 + 进程外控制面/数据面转发

- Status: Accepted
- Date: 2026-06-16
- Issue: #2230 (Epic #303, Wave-1 US1), Closes #2230
- Scope: **方向裁决（directional）**。本 ADR 裁定「框架是否、以何种治理模型支持**运行时**契约
  注册（外部进程外 cell 经 API 注册 + admin 审批上线，框架不重编译/部署）」，并锁定四项形态：
  ① dual-track 治理边界（in-tree 编译期 Hard vs out-of-tree runtime gate Medium）；② 进程外
  控制面/数据面转发的架构形态；③ 完整威胁矩阵 + 每行补偿 sub-issue 指针；④ 与既有扇出
  archtest 的 scope 边界。注册中心的**实现**——`registrycore` cell、`contract_registrations`
  表、状态机、governance gate adapter、数据面运行时增删、conformance 探测——一律属下游
  US2-US18（#2231 + #2233–#2248；#2232 为无关 CI issue、非本 epic），**不在本 ADR 落地**。证据底座：
  `specs/070-runtime-contract-registry/{spec,research,tasks}.md`。

## Context

### 缘起：#303 从 pri-p2 小问题升级为 Epic

#303 原始诉求是给消费型 cell 加 `WithExtraTopics` option（业务方发新 topic 自动被消费）。
该诉求**前提已过时**：auditcore 早已没有硬编码 `Topics` 数组，订阅由 cellgen 从 `slice.yaml`
`contractUsages[role=subscribe]` **单源派生**进 `cell_gen.go`，`cellID` 是 `Registrar.Subscribe`
第 4 个位置必填参（编译期 HARD 契约，ADR `202605111000`）。`WithExtraTopics` 方向与当下
codegen-funnel 收口哲学相反——它把已收口成静态派生的订阅重新打开成 runtime 可变 option。

产品决策由此升级：把「扩展订阅」从一次性编译期 option，提升为**持续运行的平台能力**——框架
上线后保持运行，外部 cell 经 API 随时提交契约注册，framework admin 审批通过后契约即上线生效，
**无需重新编译/部署框架二进制**。原始诉求 #659（`CELL-CONSUMER-EXTRA-TOPICS-OPTION-01`）已被
本 epic 取代并 closed as superseded。

### 核心张力：runtime 注册 vs 宪法第一性原理（AI-robust）

GoCell 第一性原理是 **AI-robust**：约束做到「违反不可表达」，靠 codegen funnel + 编译期
type system + 静态 archtest。整个契约扇出闭环（`DEAD-CONTRACT-01` / `IMPL-DECL-COVER-01` /
`EMIT-DECL-COVER-01` / `DEAD-CODE-01`）都依赖**契约在编译期可被静态枚举**。

运行时注册把契约的「权威源」从编译期 YAML 搬到 runtime registry，**对动态注册的那部分契约，
编译期静态治理结构上不可达**。这是真实张力，不能假装没有。本 ADR 的职责就是诚实地 reconcile
它，而非用「也算 Hard」的措辞掩盖降级。

### 代码现状（已读源码，分解前提）

- `framework/kernel/registry/contract.go`：`ContractRegistry` **完全只读**——`NewContractRegistry(project)`
  一次性构建 `contracts/byKind/byOwner` map，读时 `deepCopyContract`，**无 mutation API**；doc
  注明「populated once during assembly bootstrap and remain immutable afterward」。
- `framework/runtime/eventrouter/router.go`：「声明 then `Run`-once」——`runGuard sync.Once`
  保证 `Run` 只一次，`handlers []handlerConfig` 受 `mu sync.Mutex` 保护，**Run 后冻结、无运行时
  增删**；全局单 `runCtx`（cancel 即全停）。
- `framework/kernel/governance/validate.go`：`Validator` 是纯 Go 校验器，今天只被
  `gocell validate` CLI 调用。

**关键洞察**：`governance.Validator` 与 `ContractRegistry` 本就是纯 Go runtime 代码，今天只是
被 CLI 调用。把它们接到一个注册 API 上即可 runtime 化——**运行时注册校验 ≈ 把 `gocell validate`
从 CI 搬到注册端点**。admin 审批是替代编译期 CI gate 的人工补偿控制。

## 核心裁决 1：dual-track reconciliation（canonical anchor）

> 注：本节「reconcile」指消解「runtime 注册」与「AI-robust 宪法」的概念张力，与
> `kernel/reconcile`（L4 desired-state 收敛控制环）无关，勿混。

**裁定：框架 MUST 以 dual-track 治理模型支持运行时契约注册——in-tree 轨道编译期 Hard 治理
全保留不动摇，out-of-tree 轨道由 runtime governance gate（Medium）+ 自动 conformance + admin
人审 + 审计落账补偿。两轨并存、信任根不同、不互相降级。**

| 轨道 | 契约来源 | 治理方式 | 信任根 | AI-robust 评级 |
|------|---------|---------|--------|----------------|
| **In-tree cells**（框架自带 + 编译进二进制） | 编译期 YAML，可静态枚举 | 现有编译期静态治理（cellgen / archtest 扇出闭环 / `gocell validate`）**全部保留不变** | build-time，framework 自身 trust root | **Hard**（不动摇） |
| **Out-of-tree cells**（运行时注册，进程外） | runtime registry，编译期不可枚举 | 注册时跑同一套 `governance.Validator` + admin 审批 state machine + 自动 conformance + 审计落账 | register-time + admin 人审 | **Medium**（runtime guard + 人审） |

**【诚实评级，不伪装 Hard】** 本 epic 引入的 out-of-tree 治理是 **runtime governance gate
（Medium 形态：runtime invariant guard + 人审）**，不是 Hard 编译期收口。这是 out-of-tree
契约在 Go 下能达到的天花板：动态注册的契约编译期不存在，type system / golden / archtest
对它结构性不可达。本 ADR 在威胁矩阵中逐项显式记录这一点，**任何下游实现不得用「也算
Hard」措辞掩盖此降级**（AI-robust 章程：Soft 严禁、Medium 须显式登记 Hard 化路径或盲区）。

**为何 Medium 可接受**：① in-tree Hard trust root 不动摇——框架自身 + 编译进二进制的外部 cell
仍是完全静态可验证的；② out-of-tree 的 Medium 不是「降级 allow-all」，而是把 `gocell validate`
的同一套规则搬到注册端点 + 叠加人审 gate；③ 进程边界提供天然隔离（见核心裁决 2）。

## 核心裁决 2：进程外控制面 + 数据面转发（架构形态）

**裁定：注册中心采用进程外控制面形态。外部 cell 是独立进程/服务；注册中心管理其契约元数据 +
订阅意图 + 端点地址。框架运行时退化为「契约控制面 + 事件/HTTP 数据面转发」——in-tree 二进制
保持完全静态可验证，out-of-tree 只贡献元数据与网络端点。**

```
外部 cell ──submit──▶ registrycore ──governance gate──▶ probing(自动 conformance) ──▶ pending-approval
（独立进程）          (#2233-2237)      (#2234)            (#2247/#2248)                  │
                                                                          admin ──approve──▶ active
                                                                          (#2238/#2239)      │
                                                                                             ▼
                                                              EventRouter / HTTP Router 运行时加载订阅/路由
                                                              (#2240/#2241/#2242) ──转发──▶ 外部 cell 端点 (#2244)
```

状态机：`submitted → probing → (conformant → pending-approval → approved | rejected) → active → retired`。

**范式对标（research.md §2/§4 实拉源码）**：
- **K8s CRD + API aggregation layer**：CRD 运行时注册新资源类型而不重启 apiserver；
  `NamesAccepted`→`Established` condition 状态机与本状态机高度同构（→ #2245 唯一性校验、#2233
  状态机）；admission webhook = 同步 inline 门 + CRD controller = 异步 condition，直接对应
  GoCell 同步 governance gate（#2234）+ 异步 conformance/审批收敛。
- **Envoy xDS**：控制面声明 → 数据面无重启加载；NACK 保留 last-good（坏配置永不生效）→
  数据面版本化 diff + 版本回退 fail-closed（#2241）。
- **Confluent Schema Registry**：`/compatibility`(dry-run) + `/versions`(写入) 双端点共用校验
  逻辑 → `:check`/`submit` 双入口共用 `Validator`（#2234）；append-only log 作真源 → PG
  append-only 迁移历史（#2236）。

**为何这是正确形态**（不是引入服务网格）：进程边界与 GoCell「cell 间只经 contract 通信」「数据
主权」「`assemblies/` = 物理打包」一脉相承——进程外 cell 本就该经 contract + 网络端点交互。
注册中心**不**在运行时夺取宿主权限、**不**引入 sidecar、**不**做控制面下发配置改宿主；它只是
把「contract 声明 + 端点登记」从启动期一次性扩展为运行期可增删，数据面转发复用既有 contract
transport（见核心裁决 3）。

## 核心裁决 3：数据面转发复用 #1423/#1966，不另造跨进程栈

**裁定：进程外数据面转发（topic 事件 / HTTP 请求 → 外部 cell 网络端点）MUST 复用 #1423/#1966
的 location-transparent remote transport（`CellTransport` remote 实现 + `Resolver` cellID→endpoint
+ service token 出站签名 + principal/tenant 跨进程传播），不为 #303 另造一套跨进程同步栈。**

依据：#1423 ADR（`202606131142-1423`）已裁定框架提供 location-transparent contract transport
seam，其 §「discovery 接口形态与 #303 共享」已显式预留。#303 的端点注册（#2244）= 在该 seam 上
增加「运行时端点登记 + 转发」，故 **#2244 gated 在 #1966 remote transport 就位之后**。数据面的
运行时增删能力（EventRouter add/remove #2240/#2241、HTTP Router 动态 route #2242）是 #303 自有
（eventrouter Run-once → 运行时可变），转发的网络客户端层复用 #1423。

## 威胁矩阵（每行配补偿 sub-issue）

| # | 威胁 | 补偿措施 | 落地 sub-issue |
|---|------|---------|----------------|
| T1 | **恶意注册**：未授权方注册伪造契约 | 注册端点鉴权 + admin 审批 gate（approved 才能 active）+ RBAC + 限流 | #2234（gate）、#2238（审批/RBAC）、#2246（鉴权/限流） |
| T2 | **审批旁路**：active 态契约绕过审批直接生效 | 状态机不变式（sealed state，只有 approved→active）+ 审计强制落账 | #2233（sealed 状态机）、#2239（审计） |
| T3 | **命名空间冲突**：runtime 契约与 in-tree 契约 ID/topic/HTTP path 撞车 | 注册时 `(kind,domain-path,version,owner)` 四元组唯一性校验 + 外部契约强制命名空间前缀（对标 CRD NamesAccepted） | #2245 |
| T4 | **consistency 越权**：runtime cell 声明超出 actor ceiling 的一致性级别 | 复用 actors.yaml `maxConsistencyLevel` 语义的 runtime 形态，超限 fail-closed | #2245 |
| T5 | **数据面动态加载并发**：add/remove subscription 与正在消费的 goroutine 竞争 | per-handler 子 ctx 隔离 + map 锁 + Close 三阶段 drain；版本化 diff + 回退 fail-closed | #2240、#2241 |
| T6 | **conformance 探测 SSRF / 出站攻击面**：框架主动向「提交方声明的任意端点」发请求 | egress allowlist + 鉴权 + 限流，禁裸打任意地址 | #2246 |
| T7 | **合成租户副作用泄漏**（conformance 写面）：合成租户请求触发真实外发 / 数据漏进共享态 | egress-suppression 标记（禁真实外发）+ 整租户可清除；**hard-depend 多租户 GA #1337 的隔离边界为真** | #2248（gated #1337） |
| T8 | **gate fail-open**：校验器/租户/store 不可用时放行 | `FailurePolicy=Fail` fail-closed（对标 K8s admission 默认）——缺 Authorizer / 缺租户 / store 不可用 / 无适用 permit → deny | #2234、#2238 |
| T9 | **审计不可追溯**：审批/激活无留痕 | submit/approve/reject/retire → auditcore hash chain（replayable PII hash/redaction） | #2239 |
| T10 | **AI-robust 降级被掩盖**：runtime gate 是 Medium 却被当 Hard | 本 ADR 显式记录 Medium 评级 + in-tree Hard 轨道不动摇；下游 PR review 核对评级诚实性 | 本 ADR + 全下游 |

## 与扇出 archtest 的关系（scope 边界声明）

**裁定：`DEAD-CONTRACT-01` / `IMPL-DECL-COVER-01` / `EMIT-DECL-COVER-01` / `DEAD-CODE-01`
等扇出闭环 invariant 只覆盖 in-tree 静态契约，对 runtime 注册契约 vacuous——这是设计预期，
不是缺陷。**

理由：这些 archtest 在编译期静态枚举契约并验证 publisher/subscriber/owner 完整性；runtime
注册契约编译期不存在，故对它们 vacuous。这正是 dual-track 的结构性事实（核心裁决 1）。

**补偿方向**：为 out-of-tree 契约提供等价的 **runtime 扇出校验**——注册时（而非编译期）验证
publisher/subscriber/owner 完整性，复用 `governance.Validator` 规则的 runtime 形态。这是
governance gate（#2234）的一部分，并随命名空间/ceiling 校验（#2245）补全。下游实现 MUST 在
对应 archtest godoc 标注「runtime 注册契约 vacuous = 设计预期」以防误读为覆盖盲区。

## 与相邻工作的边界（不重复造轮子）

| 相邻项 | 关系 | 边界 |
|--------|------|------|
| **#1081 / #1090（M9）= Tier-1** | 互补、非二选一 | Tier-1 = 编译期外部 cell（源码编译进二进制，编译期静态治理，trust root）；本 epic = Tier-2（运行时注册，进程外，runtime gate Medium）。#1090 明确**不做**中心运行时 registry → 归本 epic。「一套共享平台 + 多个跨部署外部 cell 互相交互的运行时生态」恰是 #303 的价值主张。 |
| **#1046（D5/W5 Schema Registry runtime）** | 不同层，非重复 | #1046 = codegen-time schema **hash/compat** policy（wire 兼容性校验）；本 epic = runtime **契约**注册（元数据 + 订阅意图 + 端点）。两者可叠加：注册的契约其 schema 仍可经 #1046 compat 校验。 |
| **#1423 / #1966（remote transport）** | 数据面转发的真实前置 | 见核心裁决 3。#2244 复用其 `CellTransport` remote + Resolver。 |
| **#1337（多租户/ABAC）** | conformance 彻底档硬依赖 | #2248（合成租户写面 conformance）的安全保证建立在 tenant 隔离边界为真之上 → 结构性 block 在 #1337 GA 之后；首档（#2247，读/幂等面）不依赖多租户。 |

> 本 ADR 与 #1423 ADR 方向对齐：二者共享 discovery 接口形态，分别承载「运行时注册（控制面）」
> 与「location-transparent transport（数据面客户端）」两层。外部团队选型：编译期进二进制走
> Tier-1（#1081）；独立部署运行中服务走 Tier-2（#303）。

## Amendments to prior decisions

- **#659（`CELL-CONSUMER-EXTRA-TOPICS-OPTION-01`）**：原始 `WithExtraTopics` 诉求与 codegen-funnel
  收口哲学相反，已 closed as superseded by #303（本 ADR Context 已记）。
- **设计文档 `docs/plans/202605310313-047-runtime-contract-registry-epic.md`**：本 ADR 是其
  「P0 方向 ADR」产出，正式化其 §3 dual-track + §4 进程外控制面 + §8 威胁矩阵，并补全威胁矩阵的
  sub-issue 补偿指针（该设计文档撰写时 sub-issue 尚未登记）。
- 本 ADR 不推翻任何既有已 Accepted 的 ADR；与 `202606131142-1423` 互为引用、方向一致。

## 验收信号（epic 级，本 ADR 锁定，下游交付）

- 外部 cell（进程外）submit 一个 event 契约 → 自动 conformance 通过 → admin approve → 框架
  **不重启**即开始转发该 topic 事件给外部端点（双进程 journey）。
- 未审批 / conformance 失败 / reject 的契约 100% 不可激活（状态机不变式 + 审计强制落账）。
- 全程审计可追溯（auditcore hash chain 含 submit/approve/activate）；replayable PII 0 明文。
- in-tree 静态治理（archtest 扇出闭环 + `gocell validate`）**零回归**。
- 威胁矩阵逐行有补偿 sub-issue 且 AI-robust 评级诚实（Medium 不伪装 Hard）。

## 参考

- 设计文档：`docs/plans/202605310313-047-runtime-contract-registry-epic.md`
- 规格 / 对标 / 任务：`specs/070-runtime-contract-registry/{spec,research,tasks}.md`
- 相邻 ADR：`docs/architecture/202606131142-1423-adr-cell-deployment-topology.md`（remote transport）
- 对标源码（research.md §6 全清单）：confluentinc/schema-registry、kubernetes/api admission+apiextensions、
  pact-foundation/pact-js、envoyproxy/{envoy,go-control-plane}、kubernetes-sigs/controller-runtime、
  ThreeDotsLabs/watermill、istio/istio
- AI-robust 章程：`.claude/rules/gocell/ai-robust.md`；扇出闭环：`.claude/rules/gocell/contract-fanout.md`
