# Feature Specification: Cell 部署拓扑 / 可重定位（location-transparent contract transport）

**Feature Branch**: `069-cell-deploy-topology`（规格分解阶段未建分支，实施期按子 issue 各自走 worktree）
**Created**: 2026-06-13
**Status**: Draft（epic #1423 任务分解产物）
**Input**: Epic #1423 — 内部+外部 cell 支持分开 & 组合部署；同一份 cell 代码零改动，仅改 composition/topology wiring 即可单进程 / 子集组合 / 多进程部署。

## 范围基线（2026-06-13 探索结论）

epic 原文五项「要补什么」中，**第 5 项（第一刀防债）已完成**：
`ModuleExports.BootstrapLedgerStore` 金丝雀已于 PR #1467（2026-06-02）整改为
`event.auth.bootstrap-failed.v1` 事件驱动并删除 `ModuleExports`；
`MODULE-PROVIDE-NO-VALUE-HANDOFF-01` archtest 已冻结 `ModuleResult` 签名。
per-cell migration namespace（#1089 M8）已落地（PR #1572）。
剩余范围 = 方向 ADR + 拓扑声明 + transport seam + fail-fast 校验 + per-cell 基建分区 + 双拓扑验收。
探索证据与对标详见同目录 `research.md`。

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 方向裁决 ADR：框架提供 relocatability seam（Priority: P1）

平台维护者需要一份 L3 方向 ADR，reconcile 宪法「cell-native = 可重定位单元」与
ADR `202605041430`「嵌入式框架 = co-located 库」两个从未对齐的自我：拆开
「不 prescribe 部署拓扑」（保留）与「提供 location-transparent transport seam」
（重新纳入框架职责），并重开 `030-review-0504` line 37 + K-04 两条 won't-do。

**Why this priority**: cheap 决策、最高杠杆——裁决结果 reshape 下游所有任务的形态
（topology 声明载体、sync transport 接口、与 #303 共享 discovery、#1081 M5/M6/M11）。

**Independent Test**: ADR 文档合入即交付；下游任务可直接引用其裁决条款开工。

**Acceptance Scenarios**:

1. **Given** 宪法与 ADR 202605041430 的冲突段落，**When** ADR 合入，**Then** 冲突段落在同一改动中重写（AI-robust 章程要求），并显式裁决：topology 声明载体（assembly.yaml 扩展 vs 独立文件）、sync transport 形态（contract-level HTTP，非 method-level RPC）、discovery 与 #303 共享。
2. **Given** `030-review-0504` won't-do line 37 + K-04，**When** ADR 合入，**Then** 两条 won't-do 被显式重开或改写，留痕指向本 ADR。

---

### User Story 2 - 拓扑声明作为一等 wiring（Priority: P1）

运维者在 assembly 层声明「本进程装哪些 cell；不在本进程的 cell 在哪（远程端点）」，
cell 代码零感知。

**Why this priority**: 所有 transport 选型（in-proc vs remote、in-mem vs broker）的判定单源。

**Independent Test**: `gocell validate` 接受扩展后的 assembly.yaml；bootstrap 启动期能从
Topology 查询任意 cellID 的 co-located / remote(endpoint) 归属。

**Acceptance Scenarios**:

1. **Given** assembly.yaml 声明 colocated 组 + remote 端点映射，**When** `gocell validate` 运行，**Then** schema 校验通过且 cell 归属判定（本地/远程/缺失）可静态导出。
2. **Given** 某 cell 既不在本进程也不在 remote 端点映射，但被 contractUsages 消费，**When** validate / bootstrap 运行，**Then** fail-fast 报错（不静默降级）。

---

### User Story 3 - Broker-mandatory 启动期 fail-fast（Priority: P1）

拓扑判定有 cell 被拆出本进程时，in-memory EventBus 即非法配置：静态（gocell validate）
与运行时（bootstrap phase0）双闸 fail-fast，杜绝「拆分拓扑 + 进程内总线」导致事件丢失。

**Why this priority**: 拆分部署正确性的硬前提；依赖 #1940（broker publisher funnel）落地。

**Independent Test**: 构造 split 拓扑 + in-memory bus 的 synthetic 配置，validate 与 bootstrap 均拒绝。

**Acceptance Scenarios**:

1. **Given** split 拓扑声明 + in-memory EventBus，**When** bootstrap 启动，**Then** phase0 fail-fast，错误信息指明缺 broker。
2. **Given** split 拓扑 + broker 配置齐全，**When** 启动，**Then** 事件经 broker 跨进程送达（与 #1940 funnel 对接）。

---

### User Story 4 - Sync transport seam + 进程内短路（Priority: P1）

cell 调用另一 cell 的 sync contract 时统一经 `CellTransport` 接口；co-located 时注入
进程内短路实现（内存 dispatch，bypass TCP），cell **业务代码**零改动（generated contract
client 的注入方式由 codegen/golden 驱动改造，非 cell 作者手写）。

**Why this priority**: epic 标记的核心缺口（同步维度从未支持）；接口形态是 US5 远程实现的前提。

**Independent Test**: 同一 generated contract client 在单进程 assembly 下经 InProcessTransport
完成调用，contract 测试（schema/错误码/鉴权边界）全过。

**Acceptance Scenarios**:

1. **Given** 两个 co-located cell 与一个 sync contract，**When** 调用方经 generated client 发起调用，**Then** 请求经进程内短路送达 served handler，auth chain / contract 校验照常生效。
2. **Given** cell 代码尝试绕过 CellTransport 直拨兄弟 cell（裸 http client / 直接 Go 调用），**Then** archtest funnel 规则拦截（Medium 起步）。

---

### User Story 5 - Sync transport 远程实现：发现 + 内部 HTTP 客户端（Priority: P1）

cell 被拆到另一进程后，同一 CellTransport 注入远程实现：Resolver（cellID→endpoint）+
内部 HTTP 客户端（service token cell 身份签名、超时/重试、principal/tenant 上下文传播）。

**Why this priority**: 与 US4 合并构成「同步维度 relocatability」；与 #303 共享 discovery 机器。

**Independent Test**: 拆分拓扑下同一 contract 调用经远程客户端完成，principal/tenant 在被调端
正确重建，远端不可达映射为专属 errcode。

**Acceptance Scenarios**:

1. **Given** remote 端点映射，**When** 调用方发起 sync contract 调用，**Then** Resolver 解析端点、请求带 service token（callerCell 身份）+ principal/tenant 传播头，被调端 RequireCallerCell 校验通过并重建 ctx。
2. **Given** 远端 cell 不可达，**When** 调用发生，**Then** 返回 `ERR_*` 专属 upstream-cell-unavailable 错误（KindUnavailable），与本地依赖缺失可区分。
3. **Given** 多实例部署，**When** service token 重放，**Then** 分布式 NonceStore 拒绝（复用 RequiresDistributedReplay 既有约束）。

---

### User Story 6 - Per-cell 基建分区（Priority: P2）

分开部署 = 每 cell-进程拥有独立 DB 凭据/连接、broker 连接与 config 来源；
migration namespace（已落地）之外补连接与凭据隔离的注入 seam。

**Why this priority**: 拆分部署的数据主权落地面；不阻塞 transport 主线，可在 ADR 后并行。

**Independent Test**: 单 cell assembly 以独立 PG 凭据启动，capability 层不再隐含共享全局池。

**Acceptance Scenarios**:

1. **Given** 单 cell 进程拓扑，**When** bootstrap 装配 capability，**Then** PG/broker 连接可按 cell 注入独立凭据，未配置时 fail-fast 而非静默共享。

---

### User Story 7 - 双拓扑 journey 验收基建（Priority: P2）

同一 journey（J-*.yaml）在 (a) 单进程 assembly 与 (b) 拆分拓扑（compose 多进程 + broker）
两种形态下跑通，作为 epic 验收信号的可执行化。

**Why this priority**: epic 级验收信号；依赖 US2-US5 全部就位，收口位。

**Independent Test**: CI integration job 中同一 journey 双形态各跑一遍全绿。

**Acceptance Scenarios**:

1. **Given** 既有 journey 与 fixture，**When** 以拆分拓扑 assembly 运行，**Then** 验收结果与单进程一致（事件经 broker、sync 经远程客户端）。

---

### User Story 8 - 跨 cell 直连禁制 archtest 收口（Priority: P2）

把「进程内跨 cell Go 直传 = 0」永久编码：补 gRPC（#1752 引入后未覆盖）与
sync 直拨的 archtest 盲区，含 synthetic red case。

**Why this priority**: no-regret，不依赖方向裁决，可立即开工（epic Wave 1 性质）。

**Independent Test**: synthetic red case（跨 cell 直接构造/调用）使规则失败；现网代码零违例。

**Acceptance Scenarios**:

1. **Given** synthetic 跨 cell gRPC/HTTP 直连用例，**When** archtest 运行，**Then** 规则红；删除用例后绿（anti-vacuity）。

---

### Edge Cases

- 空拓扑声明（无 colocated / remote 字段）→ 等价现状（全部 co-located），零迁移成本。
- cell 在 colocated 与 remote 中同时声明 → validate 拒绝（互斥）。
- 远程调用超时 vs 连接拒绝 vs 5xx → 统一映射 upstream-cell-unavailable（稳定 wire code）；**区分原因进服务端通道（log/trace/internal details/metrics），不进 5xx wire details**（5xx details 强制 strip，见 `error-handling.md`）。
- in-process 短路路径上的 auth chain：不得因 bypass TCP 而 bypass listener auth plan。
- L0 cell（纯计算库，允许兄弟直接 import）不参与 transport 判定，规则需显式豁免。
- 拆分拓扑下 trace/metrics 必须区分 in-process 与 remote 调用（Service Weaver「不可诊断」教训）。

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: 框架 MUST 提供拓扑声明：本进程 cell 集合 + 远程 cell→endpoint 映射（载体由 US1 ADR 裁决，默认方向 assembly.yaml 扩展）。
- **FR-002**: sync contract 调用 MUST 经统一 CellTransport seam（覆盖 `http` kind；`grpc` cross-cell 是独立 seam，见 ADR D2，本 epic 暂不覆盖）；co-located 注入进程内短路、split 注入远程客户端，cell **业务代码**零改动（generated client 注入方式由 codegen/golden 驱动改造）。
- **FR-003**: 拓扑判定有 cell 拆出时，in-memory EventBus MUST 被静态 + 运行时双闸拒绝（fail-fast，不静默降级）。
- **FR-004**: contractUsages 声明的消费若提供方既不在本进程也不在远程映射，bootstrap MUST fail-fast。
- **FR-005**: 远程 sync 调用 MUST 携带 service token（callerCell 身份）并通过被调端 RequireCallerCell + 分布式 nonce replay 防护。
- **FR-006**: 业务 principal/tenant 上下文 MUST 经 **tamper-evident signed/sealed envelope** 跨进程传播并在被调端重建（现有 service token 只认证 callerCell、不覆盖业务 principal；envelope 经单一 sealed propagation funnel，对称 outbox PrincipalMetadata 通道，禁伪造）。
- **FR-007**: 远端 cell 不可达 MUST 映射为专属 **errcode.Code**（`ERR_UPSTREAM_CELL_UNAVAILABLE`，用既有 `KindUnavailable` 构造，非新 Kind），区别于通用 `ERR_SERVICE_UNAVAILABLE`；wire 可见区分受 5xx public-code 投影约束（默认折叠为 503 + details strip，客户端可区分需 US5 有意重评投影）。
- **FR-008**: per-cell 进程 MUST 可注入独立 DB/broker 凭据；缺失时 fail-fast。
- **FR-009**: 跨 cell 进程内 Go 直传 / 直拨 MUST 为 0，由 archtest（含 gRPC 盲区）以 Medium+ 等级守卫。
- **FR-010**: 内部平台 cell（accesscore/auditcore/configcore）与外部 cell MUST 同等享有上述弹性。

### Key Entities

- **Topology（拓扑声明）**: assembly 级 wiring 元数据——colocated cell 组 + remote cellID→endpoint 映射；启动期一次判定、运行期只读（WriteOnce 语义）。
- **CellTransport**: contract-level sync 调用 seam；实现二态（InProcess / RemoteHTTP）。
- **Resolver**: cellID→endpoint 解析接口；静态配置起步，接口形态与 #303 共享。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 同一份 cell 代码零改动，仅改 topology wiring，可分别以 (a) 单二进制 (b) 任意子集 (c) 多进程 三形态启动且 contract 照常工作（双拓扑 journey 全绿）。
- **SC-002**: split 拓扑 + in-memory bus / 缺失依赖 / 缺 broker 的非法组合 100% 在启动期（或 validate 静态）被拒，0 个运行期才暴露。
- **SC-003**: 进程内跨 cell Go 直传 = 0（archtest 红线含 synthetic red case + anti-vacuity）。
- **SC-004**: in-tree 静态治理（archtest 扇出闭环 + `gocell validate`）零回归。

## Assumptions

- Wave-1 金丝雀整改（PR #1467）与 #1089 M8 migration namespace（PR #1572）已落地，不在本分解范围。
- event 维度的 broker publisher funnel 由既有 issue #1940 承载，本分解只建依赖不重复建单。
- 拓扑为启动期静态声明（assembly 驱动），运行时动态 placement / watch 明确 out of scope（归 #303）。
- 服务网格 / sidecar / 多语言 RPC 不引入（Service Weaver / Dapr 对标的偏离结论，见 research.md）。
- discovery 接口形态与 #303（外部运行时注册）共享，但 #303 本体 gated 在后（epic 跨 wave 顺序不变）。
