# Feature Specification: 运行时契约注册中心（Runtime Contract Registry + Admin 审批）

**Feature Branch**: `070-runtime-contract-registry`（规格分解阶段未建分支，实施期按子 issue 各自走 worktree，对齐 069 先例）
**Created**: 2026-06-16
**Status**: Draft（epic #303 任务分解产物）
**Input**: Epic #303 — 框架上线后持续运行，外部 cell（独立进程/服务）经 API 随时提交契约注册，framework admin 审批通过后契约即上线生效，**无需重新编译/部署框架二进制**。

## 范围基线（2026-06-16 探索结论）

- **架构形态已定（进程外控制面）**：外部 cell 是独立进程；注册中心管理其**契约元数据 + 订阅意图 + 端点地址**。框架退化为「契约控制面 + 事件/HTTP 数据面转发」；in-tree 二进制保持完全静态可验证。范式对标 K8s CRD + API aggregation / Envoy xDS / Confluent Schema Registry。
- **dual-track（必须诚实对齐宪法）**：In-tree cells 现有编译期静态治理（cellgen / archtest 扇出闭环 / `gocell validate`）**全部保留不变**，仍是 trust root；Out-of-tree cells 编译期治理结构性不可达 → 由 **runtime governance gate 补偿**（注册时跑同一套 `kernel/governance.Validator` + 自动 conformance + admin 审批 + 审计落账）。这是 out-of-tree 契约在 Go 下的天花板，**AI-robust 评级 = Medium（runtime guard + 人审），不得伪装成 Hard**。
- **现状证据（已读源码）**：`kernel/registry.ContractRegistry` 当前**完全只读**（`NewContractRegistry(project)` 一次性构建 map + 读时 deepCopy，无 mutation API）；`runtime/eventrouter.Router` 是「声明 then `Run`-once」（`Run MUST be called at most once`，无运行时增删 handler）；`kernel/governance.Validator` 是纯 Go 校验器，今天只被 `gocell validate` CLI 调用。对标与采纳/偏离详见同目录 `research.md`。
- **状态机**（分支式，避免「rejected→active」误读）：主路径 `submitted → probing → conformant → pending-approval → approved → active → retired`；**终态分支** `rejected`（由 `probing` conformance 失败 或 `pending-approval` admin 拒绝进入，**无 →active 迁移**）。
- **边界划清**（非重复造轮子）：
  - **#1081 / #1090（M9）= Tier-1**（编译期外部 cell：源码编译进二进制）；本 epic = **Tier-2**（运行时注册，进程外）。#1090 明确不做中心运行时 registry → 归本 epic。两者互补、非二选一。
  - **#1046（D5/W5 Schema Registry runtime）= codegen-time schema hash/compat**，与本 epic 的 runtime **契约**注册不同层，非重复。
  - **#1423 / #1966（location-transparent remote transport）= 数据面转发的真实前置**：US14（端点注册 + 转发）复用其 `CellTransport` remote 实现与 Resolver，不另造。
  - **#659（CELL-CONSUMER-EXTRA-TOPICS-OPTION-01）= #303 原始 `WithExtraTopics` 诉求**，已被本 epic 取代，分解落地后 close as superseded。
  - **#1337（多租户/ABAC）= US18 彻底档 conformance 的硬依赖**。

## User Scenarios & Testing *(mandatory)*

> 每个 user story = 一个 GitHub backlog issue = 一个（或一簇）PR，净增删 ≤2000 行（US14/US18 标记特殊可超）。优先级 P1=关键路径地基，P2=主体能力，P3=深度 gated。

### User Story 1 - 方向 ADR：dual-track + 进程外控制面/数据面转发裁决（Priority: P1）

平台维护者需要一份方向 ADR，论证 dual-track reconciliation（in-tree Hard trust root 不动摇 / out-of-tree runtime governance gate = Medium）、进程外控制面 + 数据面转发设计、完整威胁矩阵，并显式声明现有扇出 archtest（`DEAD-CONTRACT-01` / `IMPL-DECL-COVER-01` / `EMIT-DECL-COVER-01` / `DEAD-CODE-01`）对 runtime 注册契约的 vacuous 边界（设计预期，非缺陷）。

**Why this priority**: cheap 决策、最高杠杆——裁决结果 reshape 下游所有 story 形态（状态机载体、governance gate 同步/异步、数据面加载一致性模型、conformance 副作用方案）。

**Independent Test**: ADR 文档合入即交付；下游 story 可直接引用其裁决条款开工。

**Acceptance Scenarios**:

1. **Given** 宪法「AI-robust = 违反不可表达」与 runtime 注册的结构性张力，**When** ADR 合入，**Then** dual-track 论证完整：in-tree 轨道列出保留不变的编译期机器，out-of-tree 轨道明确 governance gate 的 Medium 评级 + 人审补偿，**不伪装成 Hard**。
2. **Given** 威胁矩阵（恶意注册 / 命名空间冲突 / consistency 越权 / 数据面并发 / 审批旁路 / conformance SSRF / 合成租户副作用泄漏 / AI-robust 降级），**When** ADR 合入，**Then** 每行有对应补偿措施 + 落地 story 指针。
3. **Given** 与 #1081/#1090/#1046/#1423 的关系，**When** ADR 合入，**Then** 显式划清「两层模型 + 外部团队何时选 Tier-1 vs Tier-2」+ 数据面转发复用 #1423 remote transport 的边界，并与 #1081 方向 ADR 对齐。

---

### User Story 2 - Runtime ContractRegistry 可变化：只读 → 状态机（Priority: P1）

`kernel/registry.ContractRegistry` 从一次性只读升级为带 sealed 状态机的可注册中心：`register / approve / activate / retire` 迁移 + sealed `RegistrationState`（submitted…retired）+ append-only 事件溯源底模 + 投影出 in-mem 索引（保留现有 deepCopy serving 读路径）。

**Why this priority**: 所有运行时注册能力的内核地基；状态迁移不变式（只有 approved 才能 active）是审批旁路防护的根。

**Independent Test**: 单元测试（kernel ≥90%）覆盖每条合法/非法状态迁移；非法迁移 fail-closed；投影索引与 append-only 事件一致。

**Acceptance Scenarios**:

1. **Given** 一个 submitted 契约，**When** 未经 approved 直接请求 activate，**Then** 状态机拒绝（不变式：approved 才能 active），返回 typed 错误。
2. **Given** sealed `RegistrationState`，**When** 包外尝试构造任意状态值或跳变，**Then** 编译期不可表达（sealed 构造）；状态值集冻结。
3. **Given** append-only 迁移事件流，**When** 重建投影索引，**Then** 当前态与事件流一致，且现有 in-tree 只读读路径（`Get`/`ByKind`/`ByOwner`/`Provider`/`Consumers`）零回归。

---

### User Story 3 - 注册时 governance gate（复用 Validator + dry-run/submit 双入口）（Priority: P1）

把现有 `kernel/governance.Validator`（今天只 CLI 调）包成 K8s AdmissionResponse 式 `{allowed, result, warnings}`，提供 `:check`（dry-run，不落库）+ `submit`（落库前同步校验）两入口共用一套校验逻辑（对标 Confluent SR 的 `/compatibility/*` vs `/versions`）；`FailurePolicy=Fail` fail-closed（校验器不可达/缺租户/store 不可用 → deny）。

**Why this priority**: 「把 `gocell validate` 从 CI 搬到注册端点」是 dual-track 的核心补偿机制；最小改动最高杠杆（Validator 已存在）。

**Independent Test**: 合法契约经 `:check` 返回 allowed+warnings；非法契约连 submitted 都不进（静态门同步拒绝）；校验器不可达时 fail-closed。

**Acceptance Scenarios**:

1. **Given** 一个 wire 合规但有非阻断告警的契约，**When** 调 `:check`，**Then** 返回 `{allowed:true, warnings:[…]}`，不落库。
2. **Given** 一个违反 governance 规则的契约，**When** 调 `submit`，**Then** 落库前同步拒绝（无「submitted 但 invalid」中间态），错误带可机读 reason。
3. **Given** Validator/依赖不可达，**When** 注册请求到达，**Then** fail-closed（deny），绝不 fail-open。
4. **Given** 一个缺 publisher/subscriber/owner 的运行时契约（in-tree 由扇出 archtest 静态保证、runtime 不可达），**When** submit 经 gate，**Then** gate 跑 runtime 扇出完整性校验（publisher/subscriber/owner 齐全）并对缺失 fail-closed——这是 ADR「扇出 archtest 对 runtime 契约 vacuous」的等价 runtime 补偿。

---

### User Story 4 - registrycore cell 骨架 + submit/list 契约声明（Priority: P1）

新增平台 cell `registrycore`（与 accesscore/auditcore/configcore 并列，吃自己的狗粮）：`cell.yaml` + slices + `http.registry.contract.submit.v1` / `http.registry.contract.list.v1` 契约声明，经 cellgen 从 metadata 单源派生注册代码。list 契约 MUST 声明强制分页（`limit`≤500 + `data`/`nextCursor`/`hasMore` 响应 envelope，per `go-standards.md` 列表约定），不返回无界全集。

**Why this priority**: 注册中心的承载 cell；submit/list 是外部 cell 接入的最小可用面。

**Independent Test**: `gocell validate` 接受新 cell + 契约；cellgen 派生的 `cell_gen.go` 与 golden 一致；契约级测试覆盖正常响应 schema。

**Acceptance Scenarios**:

1. **Given** registrycore cell.yaml + slice.yaml（含 contractUsages），**When** `gocell validate` 与 cellgen 运行，**Then** 校验通过、派生代码与 golden 字节一致。
2. **Given** submit/list HTTP 契约，**When** 契约级测试运行，**Then** 正常响应 schema / 参数错误码 / 鉴权边界 / path 参数校验全覆盖。

---

### User Story 5 - contract_registrations 表 + PG store（Priority: P1）

`contract_registrations` 表 migration（id / submitter / kind / payload-schema / state / approver / timestamps）+ PG store + repository（`internal/ports` 接口 + `internal/mem` + PG 实现）；状态迁移走 L1 本地事务；append-only 迁移历史（对标 SR/Pact 不可变 verification record）。

**Why this priority**: 注册态的持久化真源；US2 状态机的落地存储面。

**Independent Test**: repository 事务完整性测试（L1）；迁移历史 append-only；RLS/owner 形态符合 schema guard。

**Acceptance Scenarios**:

1. **Given** 一次状态迁移，**When** 写入失败回滚，**Then** 不留半态（L1 事务原子性）。
2. **Given** 已提交 migration，**When** 新增字段，**Then** 有默认值或允许 NULL（只增不改，命名 `{序号}_{动词}_{对象}.sql`）。

---

### User Story 6 - submit/list handlers + service 接线 + 契约测试（Priority: P1）

submit/list handler（typed response envelope 表达业务状态码）+ application service（必填依赖 `gocell:"required"`）+ 接 US2 状态机 / US3 gate / US5 store，contract-level 测试闭环。

**Why this priority**: 把 US2-US5 串成「外部 cell 能 submit 一个契约并 list 看到 pending」的端到端最小闭环（epic MVP 的提交面）。

**Independent Test**: httptest 覆盖 submit（合法→pending / 非法→4xx）+ list（按 state 过滤 + 分页翻页）；contract 测试断言 error envelope 合规。

**Acceptance Scenarios**:

1. **Given** 合法契约 payload，**When** POST submit，**Then** 经 gate → 落库 → 返回 201 + registration id，state=submitted/probing。
2. **Given** 非法 payload，**When** POST submit，**Then** 返回声明的 4xx + shared error schema（`{error:{code,message,details,requestId}}`）。
3. **Given** 超过一页的注册集，**When** GET list（带 `limit`≤500，超限截断到 500），**Then** 返回 `data`/`nextCursor`/`hasMore` envelope，`nextCursor` 可稳定翻页（per `go-standards.md` 列表分页约定）。

---

### User Story 7 - Admin 审批工作流：approve/reject/retire + RBAC（Priority: P2）

`http.registry.contract.approve.v1` / `reject.v1` / `retire.v1` 契约 + handler + 路由门禁经契约 `endpoints.http.permission` overlay → `auth.RequirePermissionForContract` 接 accesscore admin 决策（permission-based ABAC/PDP，非硬编 role 字面量）+ 状态机不变式（approved 才能 active；reject 不可激活；retire 是 `active→retired` 终态迁移）。retire 与 approve/reject 同走 PDP admin（FR-001 闭合：submit/list/approve/reject/retire 五端点全覆盖）。

> **范围边界（#2238 实现澄清）**：本 US 只交付三端点 + 状态机迁移。`contract-retired` 跨 cell 事件 + 审批审计落账由 **US8（#2239）** 与其 auditcore 消费者一起落地（事件一发即有消费方，避免死事件）；数据面 remove 该契约的订阅/路由由 **US11/US12** 消费 `contract-retired` 事件触发。本 US 的 retire 仅做状态迁移（store 已写 in-store migration-history 事件），不向跨 cell 发事件。

**Why this priority**: 替代编译期 CI gate 的人工补偿控制；审批是产品硬需求。

**Independent Test**: 非 admin 调 approve → PDP deny（fail-closed）；approve 后状态机推进 pending-approval→approved；reject 后契约不可 activate。

**Acceptance Scenarios**:

1. **Given** 非 admin principal，**When** 调 approve，**Then** PDP deny（路由门禁），不触碰状态机。
2. **Given** admin 对 conformant 契约 approve，**When** 状态机推进，**Then** approved，可被 US11 数据面加载。
3. **Given** 一个 rejected 契约，**When** 请求 activate，**Then** 状态机拒绝。
4. **Given** admin 对一个 active 契约 retire，**When** `retire.v1` 调用，**Then** 状态机迁 `active→retired`（本 US 交付）；非 admin retire → PDP deny（fail-closed）。`contract-retired` 跨 cell 事件由 US8（#2239）发出、数据面 remove 该订阅/路由由 US11/US12 消费——非本 US 验收项。

---

### User Story 8 - 审批审计落账 + 激活/退役事件（Priority: P2）

submit/approve/reject/retire 每次经 auditcore hash chain 落账（审批可追溯硬需求，PII hash/redaction）+ `event.registry.contract-activated.v1` / `contract-retired.v1`（L2 OutboxFact，envelope 字段由 `outbox.NewEntry` 注入，不伪造 reserved key）。

**Why this priority**: 审批旁路防护的留痕面 + 数据面动态加载的事件源（US11 消费）。

**Independent Test**: L2 outbox 原子性 + consumer 幂等测试；审计链含 submit/approve/activate；replayable PII 已 hash。

**Acceptance Scenarios**:

1. **Given** 一次 approve，**When** 事务提交，**Then** 审计 hash chain 追加一条 + `contract-activated` 事件原子入 outbox（同事务）。
2. **Given** 审计 payload 含 submitter 标识，**When** 落账，**Then** replayable PII 经 hash/redaction，不明文留存。

---

### User Story 9 - EventRouter handlers 容器 slice→map（地基重构）（Priority: P1）

`eventrouter.Router` 的 `handlers []handlerConfig` 改为 `map[handlerKey]*runningHandler`（key=topic+consumerGroup），新增 `runningHandler` 结构（持 cfg + cancel + started + done channel）；`Run` 4 阶段 / `HandlerCount` / `AddContractHandler` 同步改 map；重名返回 error（不 panic）。**行为等价、零功能变更**。

**Why this priority**: US10 运行时增删的数据结构前提；先做无行为变更的地基重构降低 US10 风险（对标 watermill `map[string]*handler`）。

**Independent Test**: 现有 eventrouter 测试全过（行为等价）；重名 key 返回 error。

**Acceptance Scenarios**:

1. **Given** 多个声明式 handler（pre-Run），**When** `Run` 遍历 map 执行 4 阶段，**Then** 与改造前 slice 行为一致，现有测试零回归。
2. **Given** 同 key（topic+group）重复 add，**When** 第二次 add，**Then** 返回 error（不 panic，对齐 error-handling.md）。

---

### User Story 10 - 数据面运行时增删订阅：per-handler ctx + Add/Remove（Priority: P1）

`runRootCtx` 字段提升（现为 `Run` 局部变量）+ 新增 `AddRunningHandler(ctx, spec, handler, group, owner, opts)` / `RemoveHandler(topic, group)`：每个动态 handler 挂 `context.WithCancel(runRootCtx)` 子 ctx；Add 复刻 Setup→Subscribe→await Ready（fail-closed：任一步失败 hcancel + 不入 map + 不 Inc）；Remove 取消子 ctx + 等 drain + DecActive；与 `runGuard sync.Once` 兼容（Run 仍一次，动态增删只在 started 后合法）；`Close` 三阶段 drain 零改动复用。

**Why this priority**: P4 数据面最大改造、epic 验收信号「框架不重启即转发新 topic 事件」的核心；goroutine 生命周期 + 并发安全是难点（对标 watermill per-handler `stopFn` + controller-runtime ctx-per-source）。

**Independent Test**: Run 后 AddRunningHandler 成功消费新 topic；RemoveHandler 后该订阅 goroutine 退出、不影响其他 handler；Add 中途失败无半启动泄漏；Close 仍能停掉动态 handler。

**Acceptance Scenarios**:

1. **Given** Router 已 Run，**When** AddRunningHandler 注册一个新 topic，**Then** 经 Setup→Subscribe→Ready 后开始消费，IncSubscriptionActive，其他 handler 不受影响。
2. **Given** 一个运行中的动态 handler，**When** RemoveHandler，**Then** 仅该 handler 子 ctx 取消、drain 后从 map 删除并 DecActive。
3. **Given** AddRunningHandler 在 Ready 等待超时/失败，**When** 失败返回，**Then** hcancel 调用、不入 map、不 Inc（fail-closed，无半启动）。

---

### User Story 11 - Snapshot 版本化 diff Reconcile + 激活事件 debounce（Priority: P2）

`RegistrySnapshot` 加单调 `Version`；新增 `Reconcile(snapshot)`：与当前 map diff 算 toAdd/toRemove 分别调 Add/RemoveRunningHandler，版本回退 fail-closed（对标 go-control-plane 版本化 + xDS NACK 保留 last-good）；`event.registry.contract-activated` 消费侧 debounce（对标 istio `DebounceAfter`+`debounceMax`，窗口内多激活 merge 成一次 reconcile）；archtest 守卫（动态 handler 子 ctx 必须挂 runRootCtx、Add fail 必 hcancel）同 PR 落地。

**Why this priority**: 把 US10 的单点增删升级为「控制面事件 → 数据面收敛」的原子/批量加载；避免逐事件全 Router 重建；闭合 AI-robust 守卫（同 PR 自证）。

**Independent Test**: 多个 activated 事件经 debounce 合并为一次 diff reconcile；版本回退被拒；archtest synthetic red case（裸 ctx / 漏 hcancel）红、修复后绿。

**Acceptance Scenarios**:

1. **Given** 窗口内 N 个 contract-activated 事件，**When** debounce 触发，**Then** 一次 Reconcile 应用 N 条 diff（非 N 次全量重建）。
2. **Given** 一个版本号回退的 snapshot，**When** Reconcile，**Then** fail-closed 拒绝，保留 last-good。
3. **Given** 违反「子 ctx 挂 runRootCtx」的 synthetic 代码，**When** archtest 运行，**Then** 规则红；修复后绿（anti-vacuity）。

---

### User Story 12 - HTTP Router 运行时动态 route（Priority: P2）

HTTP 数据面从「启动期一次性 drain 注册」升级为运行时 add/remove route（`net/http.ServeMux` 不支持运行时改 → 引入可替换/包装的动态 mux），消费 contract-activated 给外部 cell 端点登记路由；与 FinalizeAuth / listener auth plan 兼容（动态 route 仍过最终 matcher，不绕过 auth）。

**Why this priority**: HTTP 维度的数据面动态加载，与 US10/US11 的 event 维度对称；进程外 HTTP 契约转发的前提。

**Independent Test**: Run 后动态注册一条 route 可被请求命中、过 auth chain；移除后 404；并发 add/remove 无竞争。

**Acceptance Scenarios**:

1. **Given** 框架已起，**When** 动态注册一条外部 cell 的 HTTP route，**Then** 请求命中且 listener auth plan 照常生效（不因动态注册绕过）。
2. **Given** 已注册的动态 route，**When** 移除，**Then** 后续请求 404，无在途请求被腰斩。

---

### User Story 13 - 外部 cell 注册客户端 / SDK（Priority: P2）

外部 cell（进程外）如何声明契约、上传 payload schema、调注册 API 的 client/SDK：契约声明 helper + schema 上传 + submit/poll-status client（对标 SR client RegisterSchemaRequest）。

**Why this priority**: 外部团队接入的开发者体验面；把 US6 的服务端 submit 面补成可用的客户端。

**Independent Test**: SDK 构造一个 event 契约声明 → submit → poll 到 state 变迁；错误响应可区分（no-mapping / invalid / unavailable）。

**Acceptance Scenarios**:

1. **Given** 外部 cell 用 SDK 声明一个 event 契约，**When** 调 submit，**Then** 拿到 registration id 并可 poll 到 submitted→probing→pending-approval。
2. **Given** 注册被 gate 拒，**When** SDK 收到响应，**Then** 错误机读可区分（typed reason），非英文文本解析。

---

### User Story 14 - 端点注册 + 数据面转发（依赖 #1423/#1966）（Priority: P2，特殊可超 2000 行）

进程外模型下的端点地址登记 + 框架把 topic 事件 / HTTP 请求转发到外部 cell 网络端点：复用 #1423/#1966 的 `CellTransport` remote 实现 + Resolver（cellID→endpoint）+ service token 出站签名 + principal/tenant 跨进程传播，不另造转发栈。**ExternalEndpoint 注册与数据面转发目标 MUST 经与 conformance 探测同一 endpoint egress allowlist admission（US16）**——框架带 service token + principal/tenant 转发前，目标端点须已 admitted，禁向非 allowlist / 内网 metadata 地址转发（SSRF/身份外泄边界）。

**Why this priority**: epic 验收信号「框架不重启即转发该 topic 事件给该外部端点」的转发落地面；因复用 #1423 故 gated 在其 remote transport 就位之后。

**Independent Test**: 双进程 fixture 下，approve 后框架把合成事件转发到外部 cell 端点，principal/tenant 正确重建；端点不可达映射为专属 errcode。

**Acceptance Scenarios**:

1. **Given** 一个已 active 的外部 event 契约 + 已 admitted 端点，**When** 框架收到该 topic 事件，**Then** 经 remote transport 转发到外部端点，带 service token + principal/tenant 传播头。
2. **Given** 外部端点不可达，**When** 转发，**Then** 返回 upstream-cell-unavailable 类 errcode（复用 #1423 FR-007 投影约束），可区分本地依赖缺失。
3. **Given** 登记/转发目标是非 allowlist 或内网 metadata 地址，**When** 注册端点或框架准备转发，**Then** endpoint egress admission（US16）fail-closed 拒绝，不携 token/租户身份外发。

---

### User Story 15 - 命名空间防冲突 + consistency ceiling（Priority: P2）

注册时唯一性校验 `(kind, domain-path, version, owner)` 四元组（对标 K8s CRD NamesAccepted condition）+ 外部契约强制命名空间前缀；consistency 越权防护：runtime cell 声明的一致性级别经 actors.yaml `maxConsistencyLevel` 语义的 runtime 形态裁决。

**Why this priority**: 威胁矩阵「topic/路由命名空间冲突」+「consistency 越权」的补偿；in-tree 与 out-of-tree 契约 ID/topic/path 撞车的硬防护。

**Independent Test**: 注册与 in-tree 契约 ID/topic 撞车 → 拒绝；声明超出 ceiling 的一致性级别 → 拒绝。

**Acceptance Scenarios**:

1. **Given** 一个与已有 in-tree 契约同 `(kind,domain,version,owner)` 的注册，**When** submit，**Then** 唯一性校验拒绝。
2. **Given** 外部 cell 声明超出其 actor ceiling 的 consistency level，**When** 注册，**Then** fail-closed 拒绝。

---

### User Story 16 - 恶意注册防护 + 限流 + endpoint egress allowlist（Priority: P2）

注册端点鉴权 + 限流（防重复/恶意注册）+ **统一 endpoint egress allowlist admission**：框架一切向「提交方声明的端点」的出站——conformance 探测（US17）**与** US14 数据面转发 **与** ExternalEndpoint 注册——前都过同一 egress allowlist + 鉴权 + 限流，禁裸打任意地址（威胁矩阵 SSRF/身份外泄/出站攻击面）。单一 admission seam，数据面只消费 admitted endpoint。

**Why this priority**: 进程外控制面对外发请求的攻击面收敛；zero-trust 边界硬需求；conformance 与数据面转发共用一条 endpoint admission 边界，避免「探测过门、转发裸打」的不一致缺口。

**Independent Test**: 超频注册被限流；conformance 探测**或** US14 转发目标不在 allowlist / 是内网 metadata 地址 → 拒绝；未授权 submit → 401/403。

**Acceptance Scenarios**:

1. **Given** 短时间大量注册请求，**When** 超过预算，**Then** 限流拒绝（429 / 稳定 errcode）。
2. **Given** conformance 探测目标 **或** US14 数据面转发目标端点不在 egress allowlist（或为内网 metadata 地址），**When** 框架准备出站（探测 / 转发），**Then** 同一 admission fail-closed 拒绝（不裸打任意地址、不携 token/租户身份外发）。

---

### User Story 17 - 注册 conformance 自动测试（首档：读/幂等面 + setup/teardown）（Priority: P2，flag-cond）

`probing` 态机器门：框架扮合成 consumer，按契约 request schema 造请求打活体外部 cell 端点（读/幂等面），断言 successStatus + 响应 schema + 声明 4xx/5xx 可达 + error envelope 合规（对标 Pact provider verification）；副作用隔离用 setup/teardown 配对（sealed 类型强制配对，缺 teardown 校验错，强于 Pact 运行时约定）；conformance 结果落 append-only record，admin 据此审批。

**Why this priority**: admin 人审前的机器门（验活体黑盒兑现契约）；首档不依赖多租户，随 P3 可早上。

**Independent Test**: 活体端点 conformance 握手通过 → 晋级 conformant；schema 不符 → 停在 probing；setup 必配 teardown（缺则校验/编译错）。

**Acceptance Scenarios**:

1. **Given** 一个声明 HTTP-provider 契约的活体外部 cell，**When** 框架在隔离窗口跑读/幂等面 conformance，**Then** 通过则晋级 conformant 并写 result record，失败则停 probing。
2. **Given** 一个声明了 setup 但缺 teardown 的 conformance 用例，**When** 校验，**Then** sealed 类型不可表达（编译/校验错）。

**Trigger**: blocked-by US7（Admin 审批，P2）+ **US16（endpoint egress allowlist）**——探测向活体端点出站前 egress admission 必须先就位，避免可部署 probe 裸打外部端点；首档不 hard-depend 多租户。

---

### User Story 18 - 注册 conformance 彻底档（合成租户写面 + egress-suppression，hard-depend #1337）（Priority: P3，flag-cond）

彻底档 conformance：用一次性**合成租户**跑真实写路径（复用 tenant 隔离边界，不发明 conformance 专用旁路），测完整租户清除 + event-consumer 写面深测；合成租户附 **egress-suppression** 标记（禁真实外发：事件/外部系统/邮件）；非多租户 cell 退回首档 (c)+admin 签字（混合形态）。

**Why this priority**: conformance 最彻底形态，但其安全保证完全建立在 tenant 隔离边界为真之上 → 结构性 block 在多租户 GA 之后。

**Independent Test**: 多租户 GA 后，合成租户跑写路径 conformance，测完整租户可清除、无数据泄漏进共享态；egress-suppression 下合成请求不触发真实外发。

**Acceptance Scenarios**:

1. **Given** 多租户 GA + 一个 tenant-aware 外部 cell，**When** 合成租户跑写面 conformance，**Then** 真实 handler/真落库执行、测完整租户清除、无跨租户泄漏。
2. **Given** 合成租户请求触发 handler 对外发事件，**When** egress-suppression 生效，**Then** 真实外发被抑制。

**Trigger**: hard-depend 多租户/ABAC epic #1337 GA；US10/US17 就位后。

---

### Edge Cases

- 注册端点收到空/畸形 payload → 同步 gate 拒绝，不进 submitted（无 invalid 中间态）。
- 同一契约重复 submit（幂等）→ 按 `(kind,domain,version,owner)` 去重，返回既有 registration 而非新建。
- approve 一个非 conformant（probing 未过）契约 → 状态机拒绝（必须 conformant 才能 pending-approval→approved）。
- 数据面动态 add/remove 与正在消费的 goroutine 竞争 → per-handler 子 ctx 隔离 + map 锁 + drain。
- 外部端点在 active 后下线 → 转发失败映射 errcode；不影响 in-tree 契约转发。
- in-tree 静态扇出 archtest 对 runtime 注册契约 vacuous → 设计预期，P0 ADR 显式声明 scope 边界，并为 out-of-tree 提供等价 runtime 扇出校验（注册时验 publisher/subscriber/owner 完整性）。
- conformance 探测**或** US14 数据面转发目标声明为内网/元数据地址（SSRF）→ 同一 endpoint egress allowlist admission 拒绝（探测与转发不得有「探测过门、转发裸打」的边界不一致）。

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: 框架 MUST 提供运行时契约注册 API（submit/list/approve/reject/retire），外部 cell 经 API 注册，**无需重编译/部署框架**。
- **FR-002**: 注册时 MUST 经 governance gate（复用 `governance.Validator`）同步校验，校验不过的契约不进 submitted；gate fail-closed（校验器/租户/store 不可用 → deny）。
- **FR-003**: 契约状态机 MUST 为 sealed，合法迁移：主路径 `submitted→probing→conformant→pending-approval→approved→active→retired`；终态分支 `probing→rejected`（conformance 失败）与 `pending-approval→rejected`（admin 拒绝）——`rejected` 为终态、**无 →active 迁移**。不变式「approved 才能 active」「reject 不可激活」由迁移表强制，包外不可伪造状态。
- **FR-004**: in-tree 编译期静态治理（cellgen / archtest 扇出闭环 / `gocell validate`）MUST 零回归；其对 runtime 注册契约 vacuous 是设计预期，由 P0 ADR 显式声明。
- **FR-005**: approve/reject/retire MUST 经 PDP（`auth.RequirePermission`）接 accesscore admin 决策，不硬编 role 字面量；缺 Authorizer fail-closed。retire 是 `active→retired` 终态迁移，触发数据面 remove。
- **FR-006**: submit/approve/reject/retire MUST 经 auditcore hash chain 落账（replayable PII hash/redaction）；激活/退役 MUST 发 L2 OutboxFact 事件。
- **FR-007**: EventRouter MUST 支持运行时 add/remove subscription（per-handler 子 ctx 生命周期，fail-closed 无半启动），消费 contract-activated 动态加载；HTTP Router MUST 支持运行时 add/remove route（不绕过 auth chain）。
- **FR-008**: 数据面加载 MUST 版本化 + 版本回退 fail-closed（保留 last-good）+ 激活事件 debounce 批量合并。
- **FR-009**: 注册 MUST 唯一性校验 `(kind,domain-path,version,owner)` 四元组 + 外部契约命名空间前缀；consistency 越权 fail-closed。
- **FR-010**: 注册端点 MUST 鉴权 + 限流；一切向提交方声明端点的出站——conformance 探测（US17）+ US14 数据面转发 + ExternalEndpoint 注册——MUST 过**同一 endpoint egress allowlist admission**（SSRF/身份外泄防护），禁裸打任意端点；数据面只消费 admitted endpoint。
- **FR-011**: `probing` conformance 首档 MUST 验活体读/幂等面（schema/status/disposition/error envelope）+ setup/teardown sealed 配对；彻底档（写面 + 合成租户 + egress-suppression）hard-depend 多租户 GA（#1337）。
- **FR-012**: 端点注册 + 数据面转发 MUST 复用 #1423/#1966 的 remote transport（Resolver + service token + principal/tenant 传播），不另造转发栈。
- **FR-013**: 本 epic 引入的 runtime governance gate AI-robust 评级 MUST 标注为 **Medium**（runtime guard + 人审），在 ADR 威胁矩阵显式记录，不伪装成 Hard。

### Key Entities

- **ContractRegistration（注册条目）**: id / submitter / kind / payload-schema / sealed state / approver / timestamps；状态机驱动；append-only 迁移历史。
- **RegistrationState（sealed 状态）**: submitted / probing / conformant / pending-approval / approved / rejected / active / retired；包外不可构造，值集冻结。
- **GovernanceGateResult**: `{allowed, result, warnings}`（AdmissionResponse 式）；`:check`/`submit` 双入口共用 Validator。
- **RegistrySnapshot（数据面快照）**: 单调 Version + 订阅/路由意图集；diff reconcile 的 desired 源。
- **ConformanceProbe**: 活体握手用例 + sealed setup/teardown 配对 + egress allowlist；result record append-only。
- **ExternalEndpoint**: cellID→endpoint 地址登记；注册与转发前经 endpoint egress allowlist admission（与 conformance 同一边界，US16）；转发经 #1423 Resolver/CellTransport remote，数据面只消费 admitted endpoint。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 外部 cell（进程外）submit 一个 event 契约 → 自动 conformance 通过 → admin approve → 框架**不重启**即开始转发该 topic 事件给外部端点（双进程 journey 验收）。
- **SC-002**: 未审批 / conformance 失败 / reject 的契约 100% 不可激活（状态机不变式 + 审计强制落账）。
- **SC-003**: 全程审计可追溯（auditcore hash chain 含 submit/approve/activate）；replayable PII 0 明文。
- **SC-004**: in-tree 静态治理（archtest 扇出闭环 + `gocell validate`）零回归。
- **SC-005**: 恶意注册 / 命名空间冲突 / consistency 越权 / 审批旁路 / conformance SSRF 各有补偿措施，威胁矩阵逐行有落地 story。
- **SC-006**: 每个 PR 净增删 ≤2000 行（US14/US18 特殊可超，PR 说明动机）。

## Assumptions

- 架构形态「进程外控制面」已定，本分解不再论证替代形态（in-process 由 #1081 Tier-1 承载）。
- `governance.Validator` / `ContractRegistry` / `eventrouter.Router` 现状如范围基线所述（已读源码确认）。
- 数据面转发（US14）复用 #1423/#1966 remote transport，故 gated 在其 remote 实现就位之后；本 epic 不另造跨进程同步栈。
- conformance 彻底档（US18）hard-depend 多租户/ABAC epic #1337 GA；首档（US17）不依赖多租户，随 P3 可上。
- 与 #1081/#1090/#1046 边界如范围基线所述（互补/不同层，非重复造中心 registry）。
- #659 是被本 epic 取代的原始诉求，分解落地后 close as superseded by #303。
