# ADR: #2020 — HTTP 路由鉴权默认 ABAC，非 ABAC 必须显式 opt-out

- Status: Accepted
- Date: 2026-06-19
- Issue: #2020
- Scope: HTTP 契约 AuthZ mode 强制声明机制。AuthN 分层、opt-out reason、迁移 ledger、
  FMT-42 扩展、PR-10a 威胁矩阵重评。不含现有未声明契约（37 个，含 examples）的迁移（归 #2355/#2358）。

## Context

GoCell 已经在 corecells 全面推进 `auth.RequirePermission(authz.Perm*)` + PDP 路径
（PR-10a～PR-10d #1348），并经 #2205 引入 `endpoints.http.permission` overlay → cellgen 派生
`<cell>HTTPResolver` → `auth.RequirePermissionForContract` 的 contract-derived HTTP 鉴权链路。

**现存缺口**：`endpoints.http.permission` overlay 当前是 sparse optional（FMT-42 仅做
present-only 合法性校验）。框架层没有一个**强制**的「每条业务路由必须声明 AuthZ mode」机制：
新 Cell 作者可在 `contract.yaml` 留空（handler 手写 gate），或隐式选 public/serviceOwned，而
框架无法区分「有意例外」与「忘记声明」。

平台 `contracts/` 49 个 HTTP 契约的当前分布：

| 类别 | 数量 | 形态 |
|------|------|------|
| contract-derived ABAC | 16 | `endpoints.http.permission: <action>` |
| 显式 opt-out | 5 | `public` / `bootstrap` / `serviceOwned` |
| **未声明（modeless）** | **28** | 无 mode；handler 手写 `auth.RequirePermission` |

强制机制按全 project 口径生效（含 `examples/`）：实现的迁移 ledger 共 **37** 个 modeless 契约
（32 平台 corecells + 5 examples `http.order.*`），另有 **10** 个已声明 opt-out 契约需回填 reason
（平台 5 + examples 5）。未声明契约的 handler 手写 gate 正确性由
`HTTP-PERMISSION-GATE-WIRING-FUNNEL-01`（Medium，archtest 扫描）隐式保证，但框架无法在构建期
区分「合理的手写 gate」与「遗漏声明」，削弱了 #1337 SC-005（role-literal 授权分支归零）与
多租户/行列级数据权限的治理闭环。

**目标**：把 HTTP 路由 AuthZ mode 声明从 sparse-optional 升为 build-time 强制，ABAC 为默认，
非 ABAC 必须显式 opt-out + reason；框架与 gRPC（#2008 `validateGrpcMethodOverlayAgainstProto`）
完整性预检同构。

## Decisions

### D1 — AuthN / AuthZ 分层（独立正交轴）

**认证（AuthN）** 是 per-listener 认证链，由 `bootstrap.WithAuthPlan` / listener config 决定：

- `PrimaryListener`：JWT（Bearer + OIDC）/ mTLS（设备证书）。
- `InternalListener`：service token（加 caller-cell allowlist）。
- `AdminListener`：operator HTTP Basic 凭据。
- `HealthListener`：无认证（`AuthNone{}`）。

**鉴权（AuthZ）** 是业务路由级 PDP 决策，与 listener 认证链正交：同一 `PrimaryListener` 上
的端点可以是 ABAC（`permission`）、Public、ServiceOwned，取决于**路由级声明**，而非 listener 认证层。

这意味着 `auth.Mount(mux, auth.Route{...})` 的 `Policy` 字段（AuthZ）与 listener auth chain
（AuthN）是两个独立的 pass：listener 先做 AuthN（JWT 验签/mTLS/nonce），路由随后做 AuthZ（PDP
决策/public-bypass/ownership 校验）。两者之间没有隐含优先级——错误的 Policy 不被合法的 JWT 纠正，
错误的 listener 也不被正确的 Permission 补救。

> 本 ADR 的主题是 AuthZ 层（D2 起）；AuthN 分层记录于此作为概念锚点，不引入新机制。

### D2 — 默认 ABAC + 强制 mode 声明

每个满足以下条件的 HTTP 契约**必须**声明恰好一个 AuthZ mode：

- `lifecycle: active`，且
- `codegen: true`，且
- `kind: http`，且
- 有 `endpoints.http` 块。

缺失（modeless）= 构建期失败（`gocell generate` 报错，CI build 红），除非 ID ∈ 冻结迁移 ledger
（见 D5）。ABAC 是**默认**——即：声明了 `endpoints.http.permission` 的契约被认为是 ABAC 路由，
无需额外 flag。其余 4 种 opt-out mode 必须显式声明对应 flag，且必须附带 `endpoints.http.auth.reason`。

这与 gRPC 的 `Completeness (#2008)` 预检同构（`validateGrpcMethodOverlayAgainstProto`：每条
proto RPC 必须是 public 或 permissioned，否则 generate 失败）。

### D3 — AuthZ mode 分类法（5 mode + 1 修饰符）

**不新增 schema flag**，复用 `HTTPTransportMeta` / `HTTPAuthMeta` 现有的 5 个 bool 字段
（`Public / PasswordResetExempt / Bootstrap / ClientsOnly / ServiceOwned`）与 `Permission string`：

| Mode | 声明方式 | 语义 |
|------|----------|------|
| **ABAC（默认）** | `endpoints.http.permission: <action>` | PDP 决策；`RequirePermissionForContract` 单一路径 |
| **NoAuth** | `auth.public: true` | 无 AuthZ；端点公开，仅 rate-limit 防护 |
| **ServiceOnly** | `auth.clientsOnly: true` + `clients:` caller list | 仅允许声明的 service-token caller |
| **OperatorOnly** | `auth.bootstrap: true`（+ AdminListener operator 凭据） | operator 引导端点；FMT-28 锁定路径形状 |
| **serviceOwned** | `auth.serviceOwned: true` | listener JWT 认证通过后，service 自校验 ownership；路由级无 PDP policy（设计本意，见 D4） |
| `passwordResetExempt` | `auth.passwordResetExempt: true` | **修饰符**，不是独立 mode；可与 ABAC（permission）或 serviceOwned 共存，与 public/bootstrap/clientsOnly 互斥 |

**未设立的 mode（YAGNI）**：

- `AuthenticatedOnly`（仅验 JWT，不做 ABAC 决策）：当前 corecells 无 route-level 消费者；已被
  PERMISSION-BASED-AUTHZ-01 归零的 role-literal gate 不构成此 mode 的消费者（legacy role 判定
  走 PDP policy 条件）。按 YAGNI 不落 schema；需要时在此 ADR 追加 Amendment。
- `RBAC`（路由级 role-literal gate）：role-literal route gate 已被 PERMISSION-BASED-AUTHZ-01
  禁止；RBAC 检查走 PDP policy 条件（action-scoped + role-conditioned baseline），不在 contract
  声明独立 RBAC mode。

合法 auth combo 由 `AuthComboLegal`（`metadata/auth_combo.go`）+ `LegalAuthComboNames` 双形式
冻结（`AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01`），本 PR 不新增 combo；修饰符 `passwordResetExempt`
与 `serviceOwned` 的合法共存已在矩阵中（`"p-R-S-b-c"`）。

### D4 — Hard 主载体：codegen generate-time 完整性预检（comprehensive + 共享 oracle）

判定收口为**单一共享 oracle** `metadata.ClassifyHTTPAuthMode(c)`（active+codegen+http 范围内分类）：

- 有 `permission` → ABAC（OK）。
- 有 opt-out flag（`public/bootstrap/clientsOnly/serviceOwned`）+ 非空 `auth.reason` → OK；缺 reason → `OptOutMissingReason`。
- 无 permission 且无 opt-out（含仅 passwordResetExempt）且不在冻结 ledger → `Modeless`。
- 非 opt-out 却带 `auth.reason` → `ReasonWithoutOptOut`（forbidden）。
- ID ∈ 冻结 ledger → OK（豁免，直到 #2355/#2358 迁移）。

**comprehensive 主控制点 = 共享 preflight** `metadata.ValidateProjectHTTPAuthModes(p)`，对 `p.Contracts`
**每个** active codegen http 契约跑 classifier，并由**所有** codegen/verify 入口复用——`gocell generate`
（`runCodegenGenerate`）、`gocell verify codegen-*`（`runCodegenVerifyInPlace`/`Sandbox`）、`gocell verify
generated`（generatedverify/`RenderContractArtifacts` 派生）。这既补上 cellgen serve-scan 漏的「非 served
契约」（F2），也堵 contractgen / RenderContractArtifacts 路径（F1），且 forbidden 分支（reason-without-opt-out）
在 codegen 期拦（F3）。`cellgen/builder.go` 的 serve-scan（`validateHTTPAuthModeCompleteness`）与治理 FMT-42
复用**同一** classifier，分别作 cell 构建期 defense 与 validate 期 Medium 层——一处判定、多处复用。

> 覆盖洞由外部再审（Codex）两轮发现并在本 PR 修正：先把判定抽为共享 classifier，再把项目级 preflight 收口为
> `metadata.ValidateProjectHTTPAuthModes` 并接入**每个** generate/verify 入口（含 verify-codegen 与
> generatedverify 的 RenderContractArtifacts 旁路），对标 k8s apiextensions（校验声明对象、非单一消费者命令外层）。

**违反不可表达于 generated artifacts**：modeless 契约让 `gocell generate`（任一 kind）**报错**，即使 handler
手写了正确 gate 也产不出 generated code。与 gRPC `Completeness (#2008)` 的 "dead 403 不可静默上线" 同构。

**AI-robust 评级（Hard）**：上游 = generate 编排单源读 `contract.yaml` + 共享 classifier；下游 = generated
artifacts 不可表达 modeless 路由（generate 失败）。

### D5 — runtime `auth.Mount` 不作为载体（设计裁决）

`auth.Mount(mux, auth.Route{...})` 的 `Policy` 字段**不承载**「mode 必须声明」的检查，原因：

1. **serviceOwned ≡ nil-policy 是设计本意**：serviceOwned 端点的 AuthZ 由 service 在 handler
   内部自校验 ownership，`auth.Mount` 的 Policy 字段合法地为 nil（无路由级 PDP 调用）。naive
   「nil-policy → error」会误杀所有 serviceOwned 路由。
2. **operator 路由鉴权来自 listener**：`projection_rebuild` 等挂在 `AdminListener` 的端点，
   其 operator 凭据鉴权由 listener auth chain 完成，`Mount` 的 Policy 参数在这些路由上也合法为
   nil（operator listener 不走 PDP）。
3. **`ContractSpec` 不携带 auth-mode**：`kernel/contractspec.ContractSpec` 是框架级接口，
   不含 `auth.public / bootstrap / serviceOwned` 等 authoring-schema 字段；`auth.Mount` 在
   runtime 侧看不到契约的 mode 声明。
4. **与 gRPC 同构**：gRPC 也只在 codegen 做完整性（`validateGrpcMethodOverlayAgainstProto`），
   runtime interceptor 不做 「permission 是否声明」的交叉校验。

**已知残留盲区**：手写 `auth.Mount` 给声明了 `permission` 的契约传 nil-policy（deliberate
mis-wiring），本机制覆盖不到。该场景属于有意的错误接线，narrowly scoped；`HTTP-PERMISSION-GATE-WIRING-FUNNEL-01`
（Medium archtest）从 resolver 源侧检测此类偏离（D1 resolver 单源守卫），提供纵深防御。

### D6 — opt-out 必须附带 reason（Hard via codegen + Medium via FMT-42）

`HTTPTransportMeta.Auth` 增加 `Reason string`（YAML 路径：`endpoints.http.auth.reason`）：

- 四个 opt-out flag（`public / bootstrap / clientsOnly / serviceOwned`）任一置位时，
  `reason` 字段**必填非空**；缺失 → codegen 完整性预检报错（generate 失败）/ `gocell validate`
  报告违规。
- ABAC（`permission`）/ 标准路由：`reason` 禁止设置（forbidden when not opt-out）。
  ABAC 是默认且自证明，无需 reason。
- `passwordResetExempt` 修饰符本身不触发 reason 要求（它不是 opt-out mode）；当
  `passwordResetExempt: true` + 无 permission 时为 modeless，由 D2 mandatory-mode 处理。

单字段 `auth.reason`，不为每个 flag 各自开 reason 字段（避免多字段 drift）。

**载体分层（重要）**：JSON Schema **只**钉死字段形状（`type: string` + `minLength: 1`，防空串）；
**不**用 `if/then` 表达 required-when-opt-out 耦合——否则会与既有 `TestContractSchemaAuthBoolMatrix`
（2^5 auth-bool 矩阵）耦合（矩阵构造 `{public:true}` 无 reason 即被判非法）。required/forbidden 耦合
统一落在 **codegen 完整性预检（Hard，generate 失败）** + **FMT-42（Medium，validate 早报）**。

**AI-robust 评级**：reason 的 required/forbidden 强制 = **Hard via codegen**（与 modeless 同一预检出口，
generate 失败）+ **Medium via FMT-42**（authoring UX）。schema 仅 Hard-pin 字段形状（非空字符串）。

**现有 10 个 opt-out 契约须回填 reason**（本 PR 范围内；含 examples）：

- 平台：`contracts/http/auth/{login,refresh,setup/status}/v1`（public）、
  `contracts/http/auth/setup/admin/v1`（bootstrap）、`contracts/http/auth/session/delete/v1`（serviceOwned）
- examples：`examples/demo/.../demo/hello/v1`（public）、`examples/iotdevice/.../device/register/v1`（public）、
  `examples/iotdevice/.../internal/devicecommands/list/v1`（clientsOnly）、
  `examples/orderfulfillment/.../orderfulfillment/{orderstatus,placeorder}/v1`（public）

### D7 — 迁移 ledger（Medium + frozen + no-stale）

37 个当前未声明 mode 的 active codegen HTTP 契约 ID 纳入冻结集（平台 corecells
accesscore/devicecore/registrycore/auditcore + examples 的 `http.order.*`），单源存放于
`framework/kernel/metadata/authz_mode.go`（`httpAuthModeMigrationLedger` map + `IsHTTPAuthModeLedgered`
/ `HTTPAuthModeLedgerIDs` 访问器，与分类 helper `HTTPAuthModeDeclared` / `HTTPAuthModeIsOptOut` 同包，
供 cellgen + governance + archtest 共享）。

**ledger 约束**（两道守卫，均 Medium）：

- **frozen-subset**（`TestHTTPAuthModeLedger_FrozenSubset`，metadata 包）：live ledger 必须是 #2020-landing
  的不可变 37-ID 集 `frozenInitialLedgerIDs` 的**子集**——既挡「新增 modeless 并入 ledger」（grow），也挡
  「删一个旧 ID + 换入一个新 modeless ID」（swap，size-only 守卫漏判）。迁移只从 ledger map 删条目，frozen 集不动。
  （外部再审 C2/F4 发现 size-only 守卫挡不住 swap，本 PR 升级为 subset。）
- **no-stale**（`TestHTTPAuthModeLedger_MatchesProjectModeless`，archtest）：ledger 条目若已迁移/删除 → stale → 红；
  新 modeless 路由未入 ledger 也红（双向），驱动集合单调收敛到空。
- **endgame**：ledger 清零后删除豁免逻辑分支 + frozen 集；清零工作归 #2355（accesscore/auditcore/configcore）
  /#2358（devicecore/registrycore）专项 wave，examples（`http.order.*`）随附。

**不引入 `ABAC_DISABLED=true` 全局开关**：PDP 缺失继续 fail-closed deny（框架既有
`enforcePermission` fail-closed 语义，`WithPrimaryAuthorizer` nil guard + `ResolveAuthorizer`
启动期 fail-fast 保证）。

**AI-robust 评级（Medium）**：frozen-subset + no-stale 双向反查（需运行测试，非编译期）；生成失败（D4）是 Hard。

### D8 — FMT-42 扩展作为 authoring UX 纵深（Medium）

**扩展既有 FMT-42**（`framework/kernel/governance/rules_fmt.go`，**不新增 FMT-43 rule code**，省 rule
registry / golden churn）：在 `gocell validate` 阶段即报 modeless / opt-out 缺 reason / reason 无 opt-out，
不等到 `gocell generate`。复用 D7 的分类 helper + ledger 单源。

**明确为纵深**：Hard 主控制点在 codegen（D4），FMT-42 是 authoring 时的早期提示，非唯一控制点。

## AI-robust 载体评级表

（章程要求显式说明上游/下游强度。）

| 措施 | 评级 | 上游 | 下游 |
|------|------|------|------|
| codegen 完整性预检（generate 编排层 comprehensive，modeless/缺-reason/reason-forbidden → generate 失败，**Hard 主控制点**） | **Hard** | generate 编排单源读 `contract.yaml` + 共享 `ClassifyHTTPAuthMode` oracle | generated artifacts 不可表达 modeless 路由（任一 kind generate 直接失败；覆盖每个 active codegen http 契约） |
| schema `auth.reason` 字段形状（`type:string` + `minLength:1`） | **Hard**（schema） | schema 钉死非空字符串（不表达 required/forbidden 耦合，避免与 2^5 auth-bool 矩阵耦合） | 空串 reason → schema 校验失败；required/forbidden 耦合由 codegen（Hard）+ FMT-42（Medium）承载 |
| 迁移 ledger frozen-subset + no-stale | **Medium** | frozen 37-ID 不可变集（ledger 须为子集，挡 grow+swap） | no-stale 双向反查驱动收敛（需运行测试，非编译期） |
| FMT-42 扩展（不新增 FMT-43，authoring UX 纵深） | **Medium** | `gocell validate` | 早期提示，非唯一控制点 |
| `HTTP-PERMISSION-GATE-WIRING-FUNNEL-01`（resolver 源单一性，既有） | **Medium** | archtest typed scan | D1 resolver 单源守卫，捕获 mis-wiring 偏离（Mount nil-policy 残留盲区的纵深防御） |

> runtime `auth.Mount` 经评估**不作为载体**（D5），避免误杀 serviceOwned / operator 合法路由，与
> gRPC `Completeness (#2008)` 同构。

## 威胁矩阵

| 威胁 | 攻击向量 | 防御 | 残留风险 |
|------|----------|------|----------|
| **忘记声明 mode** | Cell 作者在 `contract.yaml` 遗漏 `permission` 和所有 opt-out flag | codegen 完整性预检报错（Hard），`gocell validate` 早期报告（Medium）| ledger 内的 37 个历史契约豁免期间仍为 handler 手写 gate，存在手写 gate 错误的风险（同 HTTP-PERMISSION-GATE-WIRING-FUNNEL-01 现状）|
| **opt-out 无理由** | opt-out flag 置位但无 `reason`，掩盖未经审查的特例 | codegen 完整性预检（Hard）+ FMT-42（Medium）双道拦截 | 需回填现有 10 个 opt-out 契约（本 PR 范围内执行，含 examples）|
| **PDP 缺失/不可用** | Authorizer 未注入或 policy store 不可达 | fail-closed deny（`enforcePermission`）；启动期 `ResolveAuthorizer` fail-fast；`WithPrimaryAuthorizer` nil guard | policy store 短暂不可达时自 access 走 503（PDP fail-closed 是既有设计，非本 PR 新增风险）|
| **跨租户/数据访问** | 路由 ABAC 放行后尝试跨租户读取数据 | `RowScope` 独立治理（身份派生，policy 改不动，D3-independence）；PostgreSQL FORCE RLS tenant 边界（写端点）| 路由门禁是 coarse allow/deny，不是唯一控制点；RowScope 是真实数据边界（既有，PR-10a D3）|
| **手写 Mount mis-wiring** | 手写 `auth.Mount` 给 permission 契约传 nil-policy | `HTTP-PERMISSION-GATE-WIRING-FUNNEL-01`（Medium archtest）从 resolver 源侧检测偏离 | 已知盲区（deliberate mis-wiring，narrow scope）；启动期 `ResolveAuthorizer` 对 permission-gated 路由做 fail-fast（HTTP 侧，实现中确认与 gRPC 同构）|
| **新 opt-out flag 绕过 combo matrix** | 引入新 flag 但不更新 `AuthComboLegal` / `LegalAuthComboNames` | `TestHTTPAuthMetaFieldCount`（reflect 守卫）+ `TestAuthComboLegal_AgainstWhitelist`（双形式一致性）→ CI 红 | 守卫已存在（`AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01`），本 PR 不新增 flag，威胁不适用 |
| **ledger stale（已迁移契约未从 ledger 移除）** | 契约已迁移但 ledger 未收缩，误以为尚未迁移 | no-stale 反查测试（Medium）→ CI 红 | ledger 管理需人工执行清理 PR；收敛目标由 #2355/#2358 负责 |

## Consequences

### 正面

- 外部 Cell 作者与平台 Cell 新增端点无法隐式漏声明 AuthZ mode（codegen Hard 门禁）。
- ABAC 是默认而非可选：声明了 `permission` 的契约自动纳入 PDP 路径，无需额外配置。
- opt-out reason 使特例决策可追溯、可审查，消除「静默公开端点」风险。
- 与 gRPC `Completeness (#2008)` 同构，降低 Cell 作者在 HTTP / gRPC 两种 transport 之间的
  认知切换成本。
- ledger 的 no-stale 机制形成单调收敛压力，持续推动未迁移契约向 contract-derived ABAC 靠拢。

### 负面

- **37 个契约的 mode 迁移仍待 #2355/#2358（+ examples 随之）**：本 PR 以 ledger 豁免，不阻塞当前
  构建，但迁移债务显式化（ledger stale 检测在迁移 PR 落地后自动红）。
- **10 个现有 opt-out 契约须在本 PR 内回填 `auth.reason`**：属于已知范围内的小成本改动（含 examples）。
- **schema 增字段需同步 codegen + governance golden**：`auth.reason` 是 authoring schema 字段，
  不进 wire；codegen 消费侧已回归确认 golden 零 diff（16 ABAC + 现有 opt-out 形状不变）。
- **既有 `TestHTTPAuthMetaFieldCount` reflect 守卫期望值 +1**（6→7，新增非 bool 字段 `Reason`，bool 数仍 5）。

## 扇出矩阵（本 PR 范围）

| 变更 | contract.yaml | generated | cell/slice metadata | tests | docs |
|------|:-------------:|:---------:|:-------------------:|:-----:|:----:|
| `auth.reason` 字段增加 | schema 字段形状 | codegen 消费（golden 零 diff） | 无 | metadata + schema unit test | ADR |
| 10 opt-out 契约回填 reason | 10 文件 | golden 零 diff | 无 | governance test | ADR |
| cellgen 完整性预检 | 无 | builder.go 逻辑 | 无 | 红/绿/ledger-hit 用例 | ADR |
| metadata authz_mode + ledger | 无 | 无 | 无 | no-stale + frozen archtest | ADR |
| FMT-42 扩展（不新增 FMT-43） | 无 | 无 | 无 | governance 测试 | ADR |

> 本 PR **不改 wire contract**（`auth.reason` 是 authoring schema，非 wire；37 个契约不迁移），
> 扇出边界为 authoring schema + codegen + governance + 10 opt-out 回填 + 测试 + ADR。

## Amendment 占位（供后续追加）

本节保留供 #2355 / #2358 ledger 清零后追加 Amendment，记录最终迁移完成情况与 ledger 删除决策。

---

## PR-10a 威胁矩阵重评

以下对 PR-10a ADR
（`docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md`）的威胁矩阵逐项重评，
说明 #2020 mandatory 化后哪些行收紧、是否有冲突段落需重写。

### 重评维度

**#2020 对 PR-10a 的核心影响**：PR-10a 建立了 `RequirePermission` / PDP 的运行时语义（D1～D6）；
#2020 在**声明层**增加了「每个 HTTP 契约必须声明 mode」的 build-time 强制，是对 PR-10a 体系的
**纵深强化**，不是替代或重写。两者在控制平面上正交：PR-10a 保证「已声明 permission 的路由
正确接线 PDP」，#2020 保证「所有 active HTTP 路由不可静默漏声明 AuthZ mode」。

### 逐项重评

**D1（Composition root → PDP wiring）**：不受影响。#2020 的主控制点在 codegen，不触碰
`WithPrimaryAuthorizer` 注入路径。对于 ledger 豁免的 37 个未迁移契约，其 handler 手写 gate
仍经 PDP 路径（`auth.RequirePermission`）；`HTTP-PERMISSION-GATE-WIRING-FUNNEL-01` 继续守卫
resolver 源单一性。

**D2（Action-aware evaluator）**：不受影响。PDP evaluator 语义（`abac.Rule.Action`）与
「是否声明 mode」正交。

**D3（Built-in baseline vs FR-011 fail-closed）**：#2020 **收紧**了此处的一个隐含假设。
PR-10a D3 描述「业务端点已经接线 PDP 或有 role-literal gate」；#2020 强制「所有 active HTTP
路由必须声明 mode，漏声明在 generate 时拒绝」，消除了「既无 PDP 又无 role-literal gate 的
隐式公开路由」的可能性（这类路由在当前代码中因 baseline + `PERMISSION-BASED-AUTHZ-01` 联合
防御已不存在，但框架层没有 build-time 保证）。**#2020 后**：framework 在 generate-time 即
拒绝产生此类路由的 generated code，与 FR-011 fail-closed 形成纵深一致。

**D4（Sealed `authz.Permission`）**：不受影响。sealed type 约束是 runtime 层的 Hard 机制，
#2020 在 codegen/schema 层新增约束，两者不冲突。

**D5（Delete dead `RequireRole` scaffolding）**：不受影响。

**D6（`PERMISSION-BASED-AUTHZ-01` Medium → Hard 化路径）**：#2020 **推进了 Hard 化**。
PR-10a D6 指出 Hard endgame 是「`contract.yaml` 声明权限语义 → cellgen 派生 gate + golden」；
#2020 实现了这个 Hard endgame 的上半部分——`contract.yaml` 的 mode 声明变为强制（modeless
在 generate-time 拒绝），为 Hard endgame 下半部分（cellgen 派生 RequirePermission gate，彻底
替代手写 gate）奠定基础。`PERMISSION-BASED-AUTHZ-01` 本身的评级（Medium）和扫描机制不变，
但其收敛目标与 #2020 ledger（37 个未迁契约清零）一致，二者在 #2355/#2358 收敛。

**PR-10b Amendment（configcore super-admin 放宽的威胁矩阵重评）**：不受影响。configcore 已
全量迁移至 contract-derived ABAC（`endpoints.http.permission`），#2020 预检对其有效且无需调整。

**PR-10c Amendment（self/ownership → PDP）**：不受影响。`RequirePermissionForResource` 是
运行时层 gate，#2020 在声明层强制 permission 字段存在，两者配合：声明了 `permission` 的
owner-scoped 契约仍由 `HTTP-PERMISSION-GATE-WIRING-FUNNEL-01` 守卫其 resolver 接线。

**PR-10d Amendment（examples 迁移）**：examples 的 HTTP 契约若为 `codegen: true`，同样受
#2020 预检约束。examples 现有 HTTP 契约分布需在 #2020 实施时检查（实现中确认，按需纳入 ledger
或补 permission 声明）。

**冲突段落判定**：PR-10a ADR 无需重写段落。#2020 是其治理体系的**加层强化**，不改变
PR-10a 的运行时语义、API wire shape、合规模型或 RowScope-independence 保证（D3）。
PR-10a 威胁矩阵中「role-literal regression」/ 「sealed Permission 伪造」/ 「跨租户数据访问」
等行的防御不变；#2020 在「modeless route 存在」威胁行上**新增**了 build-time Hard 防御，
补全了 PR-10a 所称的「Hard endgame（contract.yaml → cellgen + golden）」。

## References

- 实施计划：`~/.claude/plans/2020-sprightly-hedgehog.md`（本 ADR 的决策来源）
- PR-10a ADR：`docs/architecture/202606121400-1348-adr-pr10a-authz-wiring.md`
- FMT-42 现有实现：`framework/kernel/governance/rules_fmt.go:1147-1207`
- AuthCombo oracle：`framework/kernel/metadata/auth_combo.go`（`AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01`）
- gRPC 完整性预检对标：`tools/codegen/cellgen/builder.go:1300`（`Completeness (#2008)`）
- Archtest 守卫：`tools/archtest/http_permission_gate_wiring_funnel_test.go`（`HTTP-PERMISSION-GATE-WIRING-FUNNEL-01`）
- 迁移 wave：GitHub Issue #2355（accesscore/configcore）、#2358（devicecore/registrycore）
- AI-robust 章程：`.claude/rules/gocell/ai-robust.md`
- 多租户/ABAC 规则：`.claude/rules/gocell/tenancy.md` §ABAC authz 接线
