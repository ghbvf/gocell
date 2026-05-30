# Feature Specification: Multi-tenancy + ABAC + Row/Column-Level Data Permission Control

**Feature Branch**: `1220-tenancy-abac-dataperm`
**Created**: 2026-05-31
**Status**: Draft
**Input**: 多租户隔离 + ABAC policy-as-code 决策引擎 + 行列级数据权限控制（精确到用户级 / 设备级）。尚未实现，纯设计准备。

## 设计基线（探索结论摘要）

三者**不是一个机制**，业界主流分四层处理；GoCell 规避独立 PDP 进程（OPA/SpiceDB/OpenFGA），全部内嵌：

> **层标号用 D1–D4**（Data-perm 架构分层），与 GoCell 一致性等级 **L0–L4 物理无关**——`authorizationdecide` 的 consistencyLevel 实际是 **L0**，不要把 D2 误读为 L2。

| 层 | 关注点 | GoCell 落点 | 默认 fail 行为 |
|----|--------|------------|---------------|
| D1 租户边界 | 隔离域物理边界（强制不变式） | `runtime/auth` middleware → `ctxkeys.TenantID` | fail-closed（无 tenant 拒绝请求） |
| D2 ABAC 决策 | 「principal 能否对 resource 执行 action」 | `cells/accesscore/slices/authorizationdecide`（内嵌 policy 引擎，consistencyLevel L0） | fail-closed（deny on error/missing attr） |
| D3 行级 enforcement | 数据行可见范围 | repo `tenant.TenantID` 强制 typed param（粗）+ policy 派生 `RowScope`（细，user/device） + PG RLS 兜底 | fail-closed（无谓词→0 行） |
| D4 列级 masking | 字段可见性 | `pkg/projection.ResourceProjection` sealed type ← policy `FieldMask` obligation | fail-closed（无 opt-out，敏感列默认 mask） |

**核心安全不变式**：租户隔离 ≠ ABAC policy 的一条规则。租户谓词是 repo 接口的**强制 typed 位置参**，ABAC 决策是叠加在其上的**收窄层**。policy 漏洞绝不能放大成跨租户越权。

> **Hard 评级精确边界**：「repo 方法**漏传** `tenant.TenantID`」= 编译器 type-system Hard（签名缺参不可编译）；「传**空值** `TenantID("")`」= 合法 Go 表达式，**不是** type-system Hard，由构造器/`Validate()` 运行时 fail-fast + archtest（禁跳过 Validate）守，评级 **Medium**。两者合起来才是完整 fail-closed。

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 租户隔离地基：所有数据访问按租户强制收口 (Priority: P1) 🎯 MVP

平台承载多个租户的数据；任一租户的请求绝不能读到/写入其他租户的行。租户身份从认证边界（JWT tenant claim / service-token）解析，沿请求链路传播到数据访问层，repo 查询强制带租户谓词。

**Why this priority**：这是整个特性的地基。没有可信的租户边界，ABAC 与行列级控制都建在沙地上——policy 评估用到的 resource 属性本身就可能被跨租户污染（P0）。

**Independent Test**：构造 tenant-A 的请求查询 tenant-B 拥有的资源 ID，必须返回空/拒绝；删除/忘记注入租户谓词的代码路径无法编译通过（typed param）或被 archtest 拦截。

**Acceptance Scenarios**：
1. **Given** JWT 携带 `tenant=acme`，**When** 调用任一 list/get 端点，**Then** 仅返回 `tenant_id=acme` 的行。
2. **Given** 请求 ctx 无 TenantID（claim 缺失），**When** 进入需租户隔离的端点，**Then** fail-closed 返回 401/403，不执行查询。
3. **Given** 开发者新写一个 repo 方法漏传 `tenant.TenantID`，**When** 编译，**Then** 编译失败（typed 位置参缺失）。
4. **Given** 绕过应用层的裸 SQL（误用），**When** 执行，**Then** PG RLS `FORCE ROW LEVEL SECURITY` 兜底返回 0 行。

---

### User Story 2 - 行级权限精确到用户 / 设备 (Priority: P1)

在租户内，非特权主体只能看到「属于自己」的行：用户只看 `owner=self`，设备只看 `device_id=self`；管理员可看租户全量。这把现有 auditquery 的 actor-scoping（非 admin 强制 `actorId=self`）泛化成框架级能力，subject 可以是 **user 或 device**。

**Why this priority**：用户/设备级行隔离是 MDM（设备只能拉自己的命令/证书）与零信任（最小可见面）的直接需求。与 US1 同属 P1，因为单 tenant 内的越权同样是数据泄漏。

**Independent Test**：tenant 内非 admin 用户 U1 查列表，只返回 U1 拥有的行；设备 D1 的 service-token 查命令队列，只返回 `device_id=D1` 的行；admin 查返回租户全量。device 主体在本 epic 内用 `principalKind=device` 的 **test principal stub** 构造（不依赖 #1051 真实 device-token），保证 US2 独立可测。

**Acceptance Scenarios**：
1. **Given** 非 admin 用户 U1，**When** 查 audit/order 列表，**Then** 仅返回 `owner_id=U1` 的行（`RowScope=self`）。
2. **Given** 设备主体 D1（`principalKind=device`），**When** 查命令队列，**Then** 仅返回 `device_id=D1` 的行（`RowScope=device`）。
3. **Given** admin 用户，**When** 查列表，**Then** 返回 `tenant_id=caller_tenant` 全量（`RowScope=tenant`）。
4. **Given** 开发者新写 list repo 方法漏传 `RowScope`，**When** 编译，**Then** 编译失败（typed 位置参）。

---

### User Story 3 - ABAC policy-as-code 决策引擎（含设备 / 环境属性） (Priority: P2)

授权决策从「角色名字符串比较」升级为基于属性的策略：输入 subject 属性（user/device，含 JWT claims、device posture）、resource 属性、environment 属性（time / IP / clock 注入），输出 `Decision{Allow|Deny}` + obligations（`RowScope` / `FieldMask`）。policy 以数据持久化、可演化，为 MDM / 零信任的设备态势与持续评估预留扩展点。

**Why this priority**：P2，因为 US1/US2 的粗+细行隔离可先用「身份直绑谓词」交付 MVP；完整 policy 引擎是把决策逻辑从硬编码迁移到可配置策略，是能力升级而非地基。

**Independent Test**：写一条 policy「department=eng 且 resource.classification!=secret 才 allow，且 obligation: mask `ssn` 列」，构造命中/不命中两类请求，断言 Decision 与 obligations 正确；policy store 不可用时返回 deny（fail-closed）；引用的属性缺失时返回 deny。

**Acceptance Scenarios**：
1. **Given** policy 命中 permit 且无 forbid，**When** 评估，**Then** `Allow` + 附带 obligations。
2. **Given** policy store（DB）不可用，**When** 评估，**Then** 返回 `Deny` + `KindUnavailable`，绝不 allow-all。
3. **Given** policy 引用 `subject.department` 但 token 无该 claim，**When** 评估，**Then** `Deny`（missing attribute fail-closed）。
4. **Given** 同时匹配 permit 与 forbid，**When** 评估，**Then** `Deny`（forbid wins）。
5. **Given** `principalKind=device` 且 policy 引用 `device.compliant`，**When** 设备未合规，**Then** `Deny`（设备态势属性参与决策）。

---

### User Story 4 - authz 决策接线到路由 + permission-based 迁移 (Priority: P2)

当前 `authorizationdecide` slice 实现了 `auth.Authorizer` 但 composition root 从未注入任何路由（死代码）。本故事把它接线为真实的路由授权决策点，并把业务端点的 `RequireRole("admin")` 字符串比较迁移到 permission-based / policy-based authz（关闭既有 issue #914）。

**Why this priority**：P2，依赖 US3 的 policy 引擎就绪。

**Independent Test**：业务路由经 policy 决策放行/拒绝；代码中不再存在 role-name 字面量直比（archtest）；authorizationdecide 决策路径有真实请求流量（conformance）。

**Acceptance Scenarios**：
1. **Given** policy 决策 deny，**When** 请求业务端点，**Then** 403，且决策经 authorizationdecide contract（非本地 role 字符串）。
2. **Given** 代码库，**When** archtest 扫描，**Then** 业务 handler 无 `auth.RoleAdmin` 等 role-name 字面量授权分支。

---

### User Story 5 - 列级 masking 精确到用户 / 设备 (Priority: P2)

读端点返回的资源按主体权限 mask 敏感列：无权用户看到 `<REDACTED>`/省略字段；设备主体只看到设备相关列。masking 由 policy 的 `FieldMask` obligation 驱动，通过 `ResourceProjection` sealed type 在 type system 层强制——handler 拿不到 full view，无 opt-out（对齐 errcode `PublicDetail` / span redaction fail-closed 范式）。

**Why this priority**：P2，依赖 US3 obligations。

> **MVP 无裂变**：MVP（US1/US2）阶段读端点尚无 masking。引入 `ResourceProjection` 时，**无 `FieldMask` obligation 的端点用 identity projection**（可见字段集不变），使 MVP→P2 不产生 response type 破坏式裂变（见 tasks.md T11.2/T12.3）。

**Independent Test**：同一资源，admin 看到全列、非 admin 看到 masked 列、设备看到设备列子集；handler 代码无法返回含敏感列的 full view 类型（编译期阻止）。

**Acceptance Scenarios**：
1. **Given** policy obligation `mask=[ssn,email]`，**When** 非 admin 读，**Then** 响应中 `ssn`/`email` 为 `<REDACTED>` 或省略。
2. **Given** 开发者试图让 handler 直接返回 full-column 类型，**When** 编译，**Then** 失败（response 类型仅 `ResourceProjection`）。
3. **Given** 新增敏感列未走 projection，**When** archtest 扫描，**Then** 红灯。

---

### User Story 6 - 治理闭环：archtest 不变式 + ADR + 扇出 (Priority: P3)

把上述 enforcement 机制按 AI-robust 三档固化：租户/RowScope typed param funnel（Hard）、ResourceProjection sealed（Hard）、ABAC fail-closed / resource-attr 租户一致性（Medium archtest）、policy attribute fail-closed（Medium）。补 ADR、契约扇出闭环（contract.yaml / migration / docs 同步）。

**Why this priority**：P3，随各层落地增量补；但每条 enforcement 机制的守卫与其实现**同 PR 落地**（不甩 backlog，遵循「引入新约束必须同 PR 闭环」）。本故事仅收口跨层 ADR 与汇总文档。

**Independent Test**：`make verify`（含 archtest）全绿；每条新 invariant 有反向自检测试。

---

### Edge Cases

- **async consumer 租户污染**：consumer bootstrap ctx 不得预设 TenantID；entry `principal.TenantID` 是唯一来源（`RestoreToContext` 还原）。否则 consumer 用错租户查询（P1）。
- **resource 属性跨租户污染**：ABAC 评估用到的 resource 属性查询必须与主查询共享同一 `tenant.TenantID`，不可从不同来源读不同 tenant（P0）。
- **service-token 的 callerCellID ≠ tenant**：内部 cell-to-cell 调用的 callerCellID（HMAC 保护）用于 caller allowlist，**不可**用于推断 tenant；tenant 必须从 token 的 subject/tenant claim 读取。
- **空 tenant_id 字符串**：RLS 用 `NULLIF(current_setting('app.tenant_id', true), '')::uuid`，空值→NULL→0 行可见（fail-closed）。
- **管理员跨租户运维**：平台级 super-admin 的跨租户访问是显式独立路径（RowScope=all + 审计），不是默认绕过。
- **连接池租户泄漏**：pgxpool 用 `SET LOCAL`（事务级，归还连接自动撤销），禁止裸 `SET`。
- **decision 不跨 async 边界**：事件消费侧重新做 ABAC 决策，不信任 producer 携带的「已授权」结论；跨边界只传 PrincipalMetadata identity。

---

## Requirements *(mandatory)*

### Functional Requirements

**租户边界（D1）**
- **FR-001**: 系统 MUST 从 JWT tenant claim 与 service-token 路径解析 tenant，经 `injectPrincipalCtxKeys` 写入 `ctxkeys.TenantID`（消除现状「no source on develop」）；该 setter 写入方须同 commit 加入 `CTXKEYS-PRINCIPAL-WRITE-CALLER-01` allowlist。
- **FR-002**: 系统 MUST 提供 `tenant.TenantID` sealed newtype，**空值由构造器/`Validate()` 运行时 fail-fast**（非 type-system Hard——空值是合法 Go 表达式），对齐 `idutil.SafeID` 范式。
- **FR-003**: 需租户隔离的 repo 接口方法 MUST 接收 `tenant.TenantID` 强制 typed 位置参；**漏传为编译错误**（type-system Hard）。
- **FR-004**: 系统 MUST 提供 PG 层 `FORCE ROW LEVEL SECURITY` 兜底，TxRunner 在 `RunInTx` 起始注入 `SET LOCAL app.tenant_id`；空 tenant fail-closed。

**行级（D3，user/device 粒度）**
- **FR-005**: 系统 MUST 提供 `RowScope` typed obligation，取值至少 `{self, device, tenant, all}`；list/get repo 方法接收 `RowScope` 强制 typed 位置参。
- **FR-006**: 非特权 user 主体 MUST 被收窄为 `RowScope=self`（`owner_id=subject`）；device 主体（`principalKind=device`）收窄为 `RowScope=device`（`device_id=subject`）；admin 为 `RowScope=tenant`。
- **FR-007**: 平台 super-admin 的 `RowScope=all`（跨租户）MUST 是显式独立决策路径并强制审计。

**ABAC 决策（D2，consistencyLevel L0）**
- **FR-008**: 系统 MUST 提供 policy 持久化模型（Policy / Rule / Condition）与存储接口（mem + PG），policy 可演化不需停机。
- **FR-009**: 决策引擎 MUST 接受 subject（user/device）、resource、environment 三类属性；environment 时间属性经注入的 `clock.Clock`（禁 `time.Now()`）。
- **FR-010**: 决策语义 MUST 为 default-deny + forbid-wins，输出 `Decision{Allow|Deny}` + obligations（`RowScope` / `FieldMask`）。
- **FR-011**: policy 加载失败 / 引用属性缺失 MUST fail-closed（deny），绝不降级 allow-all。
- **FR-012**: subject 属性 MUST 来自可信通道（JWT claims，由 `CTXKEYS-PRINCIPAL-WRITE-CALLER-01` 锁定写入方）；resource 属性查询 MUST 与主查询共享同一 `tenant.TenantID`。
- **FR-013**: `principalKind` MUST 区分 `{user, device, service}`，device 为一等主体（喂 MDM / 零信任设备态势属性）。

**接线 + 迁移（#914）**
- **FR-014**: composition root MUST 把 `accesscore.Authorizer()`/policy 决策接线到业务路由（消除 authorizationdecide 死代码）。
- **FR-015**: 业务端点授权 MUST 迁移到 permission/policy-based；移除 role-name 字面量直比（关闭 #914）。

**列级（D4）**
- **FR-016**: 系统 MUST 提供 `pkg/projection.ResourceProjection` sealed type（全字段 unexported + 唯一构造器 `NewProjection(mask, data)`，包外不可字面量伪造 full view）；读端点 response 类型仅含 projection，handler 无法返回含敏感列的 full view（编译期阻止）。无 obligation 时 projection = identity（避免 MVP→P2 裂变）。
- **FR-017**: 列 masking MUST 由 policy `FieldMask` obligation 驱动，per user/device；无 caller opt-out（对齐 redaction fail-closed），复用 `pkg/redaction`。

**治理（贯穿）**
- **FR-018**: 每条 enforcement 机制 MUST 与守卫（archtest / type marker / governance rule）同 PR 落地，按 Hard/Medium 评级，附反向自检测试与 ADR。
- **FR-019**: 决策结论 MUST NOT 跨 async 边界携带；跨边界只传 PrincipalMetadata identity，消费侧重决策。

### Key Entities

- **TenantID**：sealed newtype，隔离域物理边界标识；空值不可表达有效查询。
- **Principal（扩展）**：actor / subject / tenant / session + 新增 `principalKind ∈ {user, device, service}`。
- **RowScope**：typed obligation enum `{self, device, tenant, all}`，决定行可见范围；repo 强制消费。
- **Policy / Rule / Condition**：ABAC 策略持久化模型；Condition 引用 subject/resource/env 属性。
- **Decision**：`{effect: Allow|Deny, obligations: {rowScope, fieldMask}}`。
- **FieldMask**：列级 obligation，列名集合 → mask；驱动 ResourceProjection。
- **ResourceProjection**（`pkg/projection`）：sealed type（全字段 unexported + 唯一构造器 `NewProjection(FieldMask, data)`），按 mask 后的可下发视图；full view 不可作 HTTP response，无 obligation 时为 identity。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 跨租户读/写尝试 100% 返回空或拒绝（应用层 + RLS 双层），0 泄漏。
- **SC-002**: 任一 repo 方法**漏传** `tenant.TenantID` 或 `RowScope` 时**编译失败**（type-system Hard）；传**空 TenantID** 由 `Validate()` 运行时拒绝（Medium）。反向自检 fixture（`testdata/.../violates/` 收 string 的假 repo）验证 archtest 能识别绕过。
- **SC-003**: 非 admin user 列表只见 own 行、device 只见 own-device 行、admin 见 tenant 全量——三类主体 e2e 通过。
- **SC-004**: policy store 不可用 / 属性缺失时决策为 deny——fail-closed 路径有测试覆盖。
- **SC-005**: 业务 handler 中 role-name 字面量授权分支数降至 0（#914 关闭）。
- **SC-006**: 敏感列对无权主体 100% masked；handler 返回 full view 编译失败。
- **SC-007**: `make verify`（含 archtest）全绿；每条新 invariant 有反向自检 + AI-robust 评级登记于其 archtest godoc。
- **SC-008**: 单次 ABAC 决策（内嵌，无外部 PDP）p95 < 1ms（无网络往返）——由 `BenchmarkEvaluator_*` 在固定基准数据集（如 100 policy × 10 subject 属性，CI `-benchtime=5s`）守卫；非阻塞 gate，作指导性目标 + 回归哨兵。

## Assumptions

- shared-schema + `tenant_id` 列 + RLS 隔离模式（非 database-per-tenant / schema-per-tenant），与现有 pgxpool + TxRunner 最契合。
- 不引入外部 PDP 进程（OPA/SpiceDB/OpenFGA）；policy 引擎内嵌 accesscore。
- 不做 policy→SQL partial-eval 编译；policy 只发有界 obligation（RowScope / FieldMask），repo 用 typed 参数消费。
- 不向后兼容：repo 签名、Authorize 签名直接破坏式演化，无 deprecation shim（CLAUDE.md）。
- JWT issuer 已（或将）发 tenant claim 与 device claim；OIDC tenant/device mapping 的 wire 约定在 US1/US3 内确定。
- device 主体经 service-token 或专用 device-token 通道认证；具体 device 认证协议细节由下游 MDM(#1051) epic 承接，本 epic 只保证 `principalKind=device` 的主体模型与行/列 enforcement 就位。
- super-admin 跨租户运维是显式低频路径，不优化其性能。
