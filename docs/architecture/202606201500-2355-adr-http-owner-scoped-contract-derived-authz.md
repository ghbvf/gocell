# ADR: HTTP owner-scoped / self-scoped 授权 contract-derived 化（#2355）

- 状态：Accepted
- 日期：2026-06-20
- 关联：#2355（GAP-1 / PR-13）；续 #2205（PR #2350，transport-neutral `MethodPolicyResolver` + `endpoints.http.permission`）；对标 #2008/#2207（gRPC `endpoints.grpc.methods[].resource`）
- 修订：本 ADR 扩展 HTTP AuthZ mode 模型（ADR `202606190847-2020-adr-authz-default-abac.md`）与 tenancy.md §Resource ownership 的「owner-scoped gate 来源」语义；二者威胁矩阵在本 ADR §威胁矩阵重评 同步重写。

## 背景

#2205 把 **coarse** HTTP route gate（`auth.RequirePermission`）迁到 contract-derived：`endpoints.http.permission` → cellgen `cellHTTPResolver`（`contractID→action`）→ 生成 handler 经 `auth.RequirePermissionForContract(contractSpec.ID, resolver)` 解析进 PDP。但 **owner-scoped**（`auth.RequirePermissionForResource(pathParam, perm)`，按 path-param 标识资源做 ownership 判定）和 **self-scoped**（`auth.RequirePermissionForSelf(perm)`，以调用者自身 subject 为资源）两类 gate 仍**手写**在 slice handler 里——因为 `MethodPolicyResolver` 只返回 `(Permission, bool)`，没有 pathParam / self 通道。

这形成临时双机制：configcore（全 coarse）已 contract-derived，accesscore（含 6 个 owner-scoped + 1 个 self-scoped）仍手写。本 ADR 收敛 owner/self-scoped 这一维。

## 决策

新增两个 HTTP 契约字段，对标 gRPC 的 `endpoints.grpc.methods[].resource`：

- `endpoints.http.resource: <pathParam>` —— owner-scoped，命名标识资源的 path 参数（如 `id`）。
- `endpoints.http.selfScoped: true` —— self-scoped，资源即调用者自身 subject（无 path 参数）。

**单一 funnel**：`MethodPolicyResolver` 接口**不变**（仍 transport-neutral，只返回 permission，与 gRPC 共享）。resource / selfScoped 作为 HTTP-specific 字段进 `contractspec.ContractSpec`（与 `Method/Path/Clients` 同处），由生成 handler 的单一 `auth.RequirePermissionForContract(contractSpec, resolver)` 消费——内部三分支：

| contractSpec | gate | PDP resource 参数 |
|---|---|---|
| `Resource != ""` | `RequirePermissionForResource(Resource, perm)` | canonical path-param 值 |
| `SelfScoped` | `RequirePermissionForSelf(perm)` | 调用者自身 canonical subject |
| 二者皆空 | `RequirePermission(perm)` | `r.URL.Path`（coarse） |

permission 仍走 resolver（按 `contractSpec.ID`）；resource/selfScoped 走 `contractSpec` 字面量（golden 锁，与 Method/Path 同源）。**无第二 helper、无模板 if/else、调用处不烤重复 resource 字面量**。

互斥（schema Hard 主关 + FMT-42 Medium 纵深 + `ContractSpec.Validate` 三重）：`resource ⇒ permission`、`selfScoped ⇒ permission`、`resource ⊕ selfScoped`、各 `⊕ opt-out`（后者经 `resource/selfScoped⇒permission` + 既有 `permission⊕opt-out` **传递**满足，不单列规则）。

## 关键决策：不照搬 gRPC FMT-41「owner-scoped permission ⇒ resource 必填」

gRPC FMT-41 强制「owner-scoped permission 必须声明 resource」。HTTP **故意不照搬**，因为同一 owner-scoped action 在 HTTP 上同时用于：

- **owner 路由**（带 resource）：`PUT /api/v1/access/users/{id}` → `user:write` + `resource: id`，主体只能改自己。
- **admin 路由**（不带 resource）：`POST /api/v1/access/users` → `user:write`（coarse），admin 创建任意用户。

若照搬「owner-scoped permission ⇒ resource 必填」，会**误拒** create/delete/lock/unlock 这些合法 admin 路由。故 HTTP 上 `resource` 是 **per-route 授权选择**，**不可**从 `permission.IsOwnerScoped()` 派生。该差异在 `metadata.HTTPTransportMeta.Resource` godoc、FMT-42 `validateFMT42ResourceShape` godoc、反退化测试 `TestFMT42_OwnerScopedPermissionWithoutResource_OK` 三处显式记录。

## 威胁矩阵重评

| 威胁 | 控制 | 评级 |
|---|---|---|
| owner-scoped gate 漂移成 coarse（丢 resource）→ 越权读他人资源 | gate 行为 100% 由 `contractSpec.{Resource,SelfScoped}` 经单 funnel 决定，生成字面量 golden 锁；改既有 owner 路由丢 resource → handler golden + OWNER-SCOPED-GATE-EXACT-SET-01 metadata 派生集变化即红 | **Hard**（codegen funnel + golden）/ 既有路由回归 Medium archtest |
| resource/selfScoped 误配（缺 permission、二者并存）| schema.json if/then parse 拒绝（不可表达）+ FMT-42 validate + ContractSpec.Validate | **Hard**（schema）+ Medium 纵深 |
| **新**路由作者忘写 `resource`（本应 owner-scoped 却成 coarse）→ 越权 | **无机器 enforcement**：owner vs admin 同 permission，意图不可从 permission 派生（见上）。已知路由由 exact-set 冻结覆盖回归；新路由靠 contract 403 描述 + ownership serve 测试 + review | **盲区（irreducible）**：诚实登记在 OWNER-SCOPED godoc，不冒充 Hard |
| self-scoped 漂移（decide 变 coarse 允许查他人决策）| 同 owner-scoped funnel + golden；`access:decide` baseline 仍由 `BASELINE-OWNER-RULE-TENANT-FREEZE-01` 锁 `{owner, admin}` | **Hard** |
| resolver 缺映射 / nil（接线 bug）| `RequirePermissionForContract` 构造期 fail-fast panic（沿用 #2205） | **Hard** |

新路由意图盲区是本设计**有意接受**的 irreducible 残留：消除它需要「owner-scoped permission ⇒ resource」规则，而该规则已被 `user:write` 双形态证伪（会误拒 admin 路由）。两害相权取其轻——宁可保留盲区，也不引入会破坏合法 admin 路由的错误强制。

## AI-Hard 评级

| 机制 | 载体 | 评级 |
|---|---|---|
| owner/self-scoped gate = `contractSpec.{Resource,SelfScoped}` via 单 funnel + 字节 golden | codegen funnel + golden | **Hard** |
| `resource/selfScoped⇒permission`、`resource⊕selfScoped` | contract.schema.json if/then（parse 拒绝） | **Hard** |
| FMT-42 resource-shape 守卫 | governance type-aware（与 schema defense-in-depth） | **Medium** |
| OWNER-SCOPED-GATE-EXACT-SET-01 反向校验 | archtest metadata 派生 + golden 对照 | **Medium**（底层 enforcement 已 Hard）|
| owner-vs-admin 新路由意图 | serve 测试 + review | **盲区**（irreducible，已登记）|

无 Soft 新增机制。

## 后续

owner/self-scoped 基座落地后，其余 served-HTTP cell 续波迁移：examples（devicecell/ordercell）见 **#2486**（续波 PR-A，**已落地**：13 个 examples HTTP 契约全量迁 contract-derived，ledger 删 examples 条目、owner-scoped gate 从 scan 臂迁入 contract-derived 臂；examples 现为干净的 contract-derived 范本）、auditcore-list query-param 见 **#2487**（续波 PR-B）。全部迁完后：ledger 清零 + FMT-42 收紧为强制 + 裸 `RequirePermission*` 收口为「仅生成 Contract helper 可调」（funnel 终态闭环）。迁后 ledger 仅剩 accesscore 续波幸存者（admin/audit/role.assign,revoke/config.internal）+ 框架归属 `http.devicestate.v1`（#2351，composition-root 单独 wave）。

续波 cell 迁移时，review checklist 须人工核对每个 owner 路由确实声明了 `resource`（owner-vs-admin 新路由意图是 irreducible 盲区，靠 review 兜底，见 §威胁矩阵）。
