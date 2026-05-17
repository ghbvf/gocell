# ADR: Credential / Session 协议（typed Protocol primitive 决议）

**Date**: 2026-05-10
**Status**: Accepted
**Related plan**: `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` S1
**Related ADRs**:

- `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`（typed-Go-heavy 范式锚点）
- `docs/architecture/202605101400-adr-admin-invariant.md`（admin 不变量；同 PR）

---

## 0. Amendments（S4b 落地后修订；2026-05-14 — S4d 重写；2026-05-15；S4e mutation funnel landed PR #494；2026-05-15；PR #501 RC-A/B/C/D/E 闭环（RoleRevoked 死代码 + §A10 Medium 天花板锁定 + schema_guard CHECK 注册 + login error path 归一化 + Reconstitute params + storetest const）；2026-05-16；Wave 5 P1-1 authzmutator.ApplyInTx 单入口 Hard funnel；2026-05-17；2026-05-17 — §A11 重写：session-revoke 出 funnel + 上游 4 勿一 Hard 化 + 新增 §A13 wire-uniformity 防枚举载体（PR #542 P1-A/P1-B/P2-A/P2-B/P2-C 闭环）；2026-05-17 Wave 4 — value-capture detector 升级到 typed-parent-check（form uniqueness）+ scope 扩到 cells/+cmd/+runtime/ + TYPESUTIL-IMPLEMENTS-FUNNEL-01 合规 + Sonar TestAssert 复杂度 20→1 + checkOK godoc）

S4b PR 落地后实际实现与 §2/§3 描述出现漂移。**S4d (PR S4d) 之后实际行为以本节 +
§A8 / §D1 / §D2 / §D4.2 同 PR 重写后的描述为准。** 与 amendment 矛盾的原文段落
已在 S4d 同 PR 内重写（不再保留为"历史脉络"——见 ai-collab.md §"ADR amendment
重跑威胁矩阵"规则）。

### A1 RETRACTED — 2026-05-15（PR S4d）

**原 amendment（撤回）**：声称 `sessions.authz_epoch_at_issue` 是 JWT claim 的
冗余镜像，行内 pin 提供"零额外防御"，故 migration 025 DROP 该列，
`schema_guard.forbiddenColumns` 守卫禁止该列再次出现。

**为什么撤回**：A1 仅在 access JWT 单独存在的口径下成立，跨到 refresh 链路与并发
login 串行化场景就不成立。具体盲点（PR #490 review 六席审查发现）：

1. **refresh row 无 epoch_at_issue**：`sessionrefresh` 用 live `users.authz_epoch`
   重铸 access claim，stale refresh grant 一次刷新即升级到当前 epoch（P1-#2）。
2. **删除列时未评估 refresh 维度**：原 amendment 把"删 session 行 pin"论证为
   "claim 的镜像"，但 refresh 是 opaque token + DB row，无 claim 可作 SoR。整个
   refresh 维度没纳入 alternative analysis。
3. **删除列同时也删了"login vs role-revoke"的 server-side 串行化路径**：原 §D2
   SQL `INSERT INTO sessions (..., authz_epoch_at_issue=<read>)` 让 login tx 与
   role-revoke tx 通过 user 行天然串行化；A1 删列后没有等价补偿，导致并发窗口
   重新打开（P1-#3）。
4. **ADR 自身不一致**：A1 删了列，但 §3 威胁矩阵未重跑（仍把 "Role downgrade 后
   旧 token 仍持高权" 标 ✅），§D4.2 仍引用已删除的列描述。

**S4d 决议**：恢复 row-level credential provenance（详见 §A8）；migration 026
ADD COLUMN 恢复 `sessions.authz_epoch_at_issue`；migration 027 ADD COLUMN
`refresh_tokens.authz_epoch_at_issue`；schema_guard 切换为 requiredColumns 守
（保证列存在）。原 §D2 SQL 形态恢复（FOR UPDATE 行锁 + INSERT epoch snapshot）。

### A2 sessionvalidate 按 `sid` 查 session，不按 `jti`（替代 §D1 SQL）

- §D1 示例 `SELECT ... WHERE jti = $1` 描述的是早期设计。实际 sessionvalidate 用 `claims.SessionID` (sid) 查 session 行（`session.Store.Get(ctx, sid)`），与 OIDC Back-Channel Logout sid stability 一致。
- session.JTI 列仍存 RFC 9068 §2.2.4 要求的 per-token jti（由 sessionmint.MintAccess 生成 UUID 写入 IssueOptions.JTI，登录时持久化）。
- **但 validate 不比对 jti**：refresh 流程会保持 sid 不变 + 旋转 access token 的 jti，validate 期比对 jti 会把所有刷新后的 token 拒掉（错的）。jti 的真实用途固定为：(a) RFC 9068 合规 per-token uniqueness，(b) 日志/排障 token-level correlation 标识。
- 未来若引入 explicit jti revoke list（如 logout-on-current-jti 精细控制），再行扩 validate 路径；本 ADR 范围下不做。

### A3 Epoch 比对使用 `!=`，不是 `<`（替代 §D1 / §D2）

- §D1 SQL "`if claim.epoch < user.authz_epoch → reject`" 是宽松等价语义；实际 sessionvalidate.enforceSessionState 用 `!=` 严格不等性比较（`if user.AuthzEpoch != claims.AuthzEpoch → reject`）。
- 严格 `!=` 拒绝任何不匹配，包含 "claim.epoch > user.epoch"（未来 epoch token，必是篡改/重放/时钟漂移）的 fail-closed 边界。`<` 在该方向上是 false-pass。
- archtest `SESSIONVALIDATE-EPOCH-COMPARE-01` 静态守卫 `!=` 形态。

### A4 PG `users` SELECT 必带 `authz_epoch` 列

- `adapters/postgres/user_repo.go` `selectUserByIDSQL` / `selectUserByUsernameSQL` / `scanUser` 必须包含 `authz_epoch`，否则 D2 整个 epoch invalidation 链路在生产路径上 silently 失效（PR #490 review Finding #1，已修）。
- PG integration test `TestPGUserRepo_BumpAuthzEpoch_ReadbackVisible` 把这条约束锁定。

### A6 role.assigned 不 bump authz_epoch（替代 §D2 / §D3）

- §D2 "bump 触发"列表把 "role assigned / revoked" 并列；实际 HIGH-3 决策：assigned 是加权操作（additive），不构成 credential-security 事件，**不 bump epoch**。只有 revoke / downgrade / permission-set 缩减触发 funnel。
- §D3 表中 `CredentialEventRoleRevoke` 描述 "role assignment 删除 / role 重新分配" 不准确：reassign 路径独立（如有），不由该事件触发；该事件只覆盖 revoke / downgrade 的 narrowing-scope 语义。
- 实际实现：`rbacassign.Service.Assign` → `persistChange(callFunnel=false)`（仅写 outbox role.assigned.v1 事件，不调 invalidator）；`rbacassign.Service.Revoke` → `persistChange(callFunnel=true)`（同 tx 内 invalidator.Apply bump epoch + revoke sessions + revoke refresh）。
- sessionlogout consumer 接到 role.assigned 事件后 Ack but no-op（注释 "no credential invalidation needed"）；接到 role.revoked 同样 Ack（funnel 已在 rbacassign tx 内运行，避免双 bump）。
- 对标 OAuth2 业界：scope 扩大不要求重新认证；scope 缩小才必须 revoke 已发 token（OAuth Security BCP §4.13.2）。

### A5 JWT 验证错误分类（补充 §D2）

D2 epoch 比对是 401 路径（token claim 与 server state 不符）。但 JWT 验证还有两类错误：

| 错误源 | 例子 | HTTP 状态 |
|---|---|---|
| Token-side | expired / unknown kid / wrong alg / malformed | 401 ErrAuthInvalidToken（伪枚举防御统一文案） |
| Verifier-side infra | JWKS fetch / KMS unreachable | 503 ErrAuthServiceUnavailable（KindUnavailable，wire 投影为 ERR_SERVICE_UNAVAILABLE） |

`runtime/auth/jwt.go::hasExplicitInfraSignal` 显式区分两类：只有底层错误链上有 `*errcode.Error` 且 `Kind=Unavailable` 或 `Category=Infra` 才升级 503。**绝不**用 fail-closed predicate（如 `errcode.IsInfraError`），那会把 jwt 库的 plain error 全升级。

### A6 Refresh reuse 单一 funnel 入口（补充 §D5）

`sessionrefresh.handleReuseDetected` 是 Peek/Rotate 检测 reuse 后的**唯一**入口，触发 `credentialinvalidate.Invalidator.Apply`：

- `refresh.Store` 接口契约现在要求 ErrReused 返 `(*Token{SubjectID, SessionID, ...}, ErrReused)`，让 service 拿到 cascade 所需 metadata。空 SubjectID 命中 = 上游违约，service 走 `panicregister.Approved` panic（Recovery middleware 转 500 + audit）。
- 接 `ctxutil.WithDetachedTimeout(outerCtx, 5s)`：cascade 不受外层 cancel 影响 + 不会因 DB 卡住泄漏 goroutine。
- `runtime/auth/refresh/storetest` 的 T20 / T23 conformance 子测试断言任何新 Store impl 必返非空 token，否则编译之外的 CI 失败。

S4d 扩展（已被 S4e PR #494 修正）：`refreshInTx` 在 `fetchUserForRefresh` 后比对
`presented.AuthzEpochAtIssue != user.AuthzEpoch`；不匹配路由进 `rejectIfStaleEpoch`，
后者调 `cascadeRevoke("stale-epoch")`（session-scoped revoke，不触发 user-wide
Invalidator.Apply）。stale-epoch 与 reuse-attack 拆分为不同路径，
archtest `SESSIONREFRESH-STALE-EPOCH-REJECT-01` prong 4 NEGATIVE 静态守卫二者不混用。

### A8 Row-level credential provenance（S4d；替代 A1）

- session / refresh 行都携带 `authz_epoch_at_issue BIGINT NOT NULL DEFAULT 0`。
  DEFAULT 是 DDL-only 兼容性（rules/go-standards.md "新字段必须有默认值或允许 NULL"）；
  应用层 `Store.Create` / `Store.Issue` 拒 zero（`ErrValidationFailed`），由
  storetest conformance T-S4D-1 / T-S4D-2 守。
- migration 026/027 同 PR 落地；schema_guard `requiredColumns` 加入两列、
  `forbiddenColumns` 删除 session 列项（A1 的反操作）。
- **schema_guard CHECK 约束注册**（PR #501 RC-B）：`expectedChecks` 显式注册
  migration 028 的三条 `authz_epoch{,_at_issue} > 0` CHECK 约束
  （`users_authz_epoch_positive` / `sessions_authz_epoch_at_issue_positive` /
  `refresh_tokens_authz_epoch_at_issue_positive`），`VerifyExpectedShape` 启动期
  检测任一约束被删则 fail-fast——schema_guard 现在同时 assert column type / NOT NULL
  / CHECK 三层（之前仅前两层）。
- sessionlogin 用 `userRepo.GetByUsernameForUpdate`（PG `SELECT ... FOR UPDATE`；
  mem store-wide Lock）在 RunInTx 闭包顶部读 user → 同 tx 写 session 行的
  `AuthzEpochAtIssue = user.AuthzEpoch` → 同 tx 写 refresh chain 根行的
  `authz_epoch_at_issue = user.AuthzEpoch`。Login tx 与 `Invalidator.Apply` 的
  `BumpAuthzEpoch`（也持 user 行写锁）天然串行化（PG read-committed + row lock
  原生语义）。
- refresh chain 内 epoch 稳定：Rotate 创建 child 行时复制 parent
  `authz_epoch_at_issue`，符合 OAuth2 §1.5 "refresh 是同一份 grant 的延续"
  + ADR §D4.1。Stale-grant 检测在 service 层（A6 cascade 入口），不在 store 层。
- archtest `SESSIONVALIDATE-EPOCH-SOURCE-01` 锁 sessionvalidate epoch 比对的
  右值必须是 `view.AuthzEpochAtIssue`（而不是 deleted `claims.AuthzEpoch`）；
  `SESSIONREFRESH-STALE-EPOCH-REJECT-01` 锁 sessionrefresh row != user 比对 +
  `"stale-epoch"` cascade 入口；`CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01`
  (Medium) 锁 `Invalidator.Apply` 的 caller allowlist。

### A9 identitymanage RequirePasswordReset 接入 funnel（S4d，P1-#1）

- `identitymanage.Service.applyUserUpdate` 现在在 status demotion **或**
  RequirePasswordReset false→true transition 时都通过 `authzmutate.Mutator.Apply`
  路由到 `credentialinvalidate.Invalidator.Apply`。authzmutate 的 sealed Mutation
  `LockUser` / `SuspendUser` 携带 `CredentialEventLock`，`RequirePasswordReset`
  携带 `CredentialEventPasswordReset`（与 changePasswordInTx 走同一事件，因二者
  在 credential-weakening 语义上等价，无需新增独立事件）。
- 之前的实现只在 status demotion 时走 funnel，导致 PATCH
  `requirePasswordReset=true` 不 bump epoch，强制改密门禁延迟到 token 自然 exp
  才生效（PR #490 review P1-#1）。

### A10 后续治理（S4e）— funnel 闭合（LANDED PR #494；2026-05-15）+ Medium 天花板锁定（PR #494 residual RC-A）

**Canonical upstream caller allowlist**（`CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01` archtest）：

```
credentialinvalidate/  (funnel package itself)
authzmutate/           (sealed Mutation funnel for live-aggregate authz mutations)
identitymanage/        (Delete + changePasswordInTx co-tx atomicity, see below)
sessionrefresh/        (reuse + stale-epoch cascade entry)
rbacassign/            (role-revoke co-tx atomicity, see below)
```

这 5 个 caller 是**最终 allowlist**——不是"待收窄到 2 个"的过渡形态。Co-tx atomicity
约束证明这是 Go 类型系统在 tx-bound side-effect funnel 上的天花板（下文逐一论证）。

**AS-BUILT 实现（PR #494 落地；RoleRevoked 死代码删除 RC-A）**

#### 字段私有化 + setter 收口（Hard Rule a）

`domain.User` 的 `status` / `passwordResetRequired` / `authzEpoch` 三个字段全部小写
私有化。包外无法直接写入。唯二的 mutation 入口：

- `user.SetStatus(s UserStatus, now time.Time)` — 仅 authzmutate 包调用
- `user.SetPasswordResetRequired(v bool, now time.Time)` — 仅 authzmutate 包调用

`ReconstituteUser(id, username, email, passwordHash string, passwordVersion int64,
passwordResetRequired bool, status UserStatus, source UserSource, authzEpoch int64,
createdAt, updatedAt time.Time) (*User, error)` 是 DDD rehydration 构造函数（repository
层调用），持久化以外的业务层仍须走 authzmutate 的 `Mutator.Apply`。

archtest `DOMAIN-AUTHZ-FIELD-PRIVATE-01` 静态守卫：production AST 内 `SetStatus` /
`SetPasswordResetRequired` 的调用方身份必须在 allowlist 内。

#### authzmutate sealed Mutation interface — 5 个 variant 目录（Hard Rule a）

`cells/accesscore/internal/authzmutate` 包：

- sealed `Mutation` interface（含 unexported `mutationOK()` method，包外不可表达实现）
- **5 个** Mutation variants（RC-A 删除死代码 `RoleRevoked`，见下）：
  `LockUser` / `SuspendUser` / `ActivateUser` / `RequirePasswordReset` / `ClearPasswordReset`
- `Mutator.ApplyInTx(ctx, txCtx, userID, m Mutation, now)` 唯一入口（Wave 5 P1-1 改造，原
  `Apply` 已删除，见 §A12）：caller 提供 `txCtx`（outer RunInTx context）→
  `GetByIDForUpdate(txCtx)` → `m.apply(user, now)` → `repo.Update(txCtx)` →
  （若 `m.Invalidates()`）`inv.Apply(txCtx)`
- `ActivateUser.Invalidates() == false`（additive，per OAuth Security BCP §4.13.2）
- `ClearPasswordReset.Invalidates() == false`（clearing flag；实际密码变更由 changePasswordInTx 完成）

**RoleRevoked 删除（RC-A）**：`RoleRevoked` 变体从未被任何生产路径实例化。
`rbacassign.Revoke` 调用 `persistChange(ctx, writeFn, evt, dto.TopicRoleRevoked, callFunnel=true)`，
后者直接调 `invalidator.Apply`（co-tx 语义，见下文），从未通过 `authzmutate.Mutator.Apply`
传入 `RoleRevoked{}`。该变体是"伪统一"设计阶段的残骸：原设计希望把所有 authz 事件
都走 authzmutate funnel，但 rbacassign 的 role-row write + epoch-bump 必须同 tx，
无法被 authzmutate 的独立 tx 覆盖。死代码删除，sealed Mutation interface 收窄到 5 个真实 variant。

archtest `AUTHZ-MUTATION-APPLY-FUNNEL-01` 静态守卫：production AST 内调用
`Invalidator.Apply` 的 caller 前缀必须在 allowlist 内。

#### Co-tx atomicity 约束：为何上游 caller-set 不可再收窄

`{authzmutate, sessionrefresh}` 曾被作为理想最小 funnel 集合提出（S4d 设计阶段），
但 Go 的聚合-tx 语义证明该目标**不可实现**——最终 canonical 5-set 是天花板，不是
妥协。逐一论证：

**identitymanage.Delete（对象生命周期 ≠ authz 变更）**

`identitymanage.Service.Delete` 必须在同 tx 内原子完成：
(a) user 行 hard delete / soft delete 标记；(b) `invalidator.Apply` 触发 epoch-bump +
session/refresh revoke。若路由经 authzmutate.Mutator.Apply，后者开启独立 tx，
Delete tx 提交后 Apply tx 异步执行 — 窗口期内已删 user 的会话仍有效。
user-row-delete 是对象生命周期事件，不是 authz-state 变更，authzmutate 的聚合
语义（GetByIDForUpdate → SetStatus → Update）与之错配。**必须直调 invalidator。**

**identitymanage.changePasswordInTx（凭据载体写 + 撤销原子对）**

`changePasswordInTx` 在同 tx 内：(a) 更新 `users.password_hash` + `password_version`；
(b) 调 `invalidator.Apply`（CredentialEventPasswordReset）。两操作必须原子：
密码写入成功但 revoke 失败 = 旧会话在新凭据下仍有效（P1 级）。
若路由经 authzmutate，Apply 独立 tx，原子性破坏。**必须直调 invalidator。**

**rbacassign.Revoke（role 是独立聚合根，user 行不参与）**

`rbacassign.Revoke` 在同 tx 内：(a) `roleRepo.RemoveFromUserIfNotLast`（role_assignments
行写锁 + count-check TOCTOU 消除）；(b) `invalidator.Apply`（epoch-bump 持 user 行写锁）。
两操作必须同 tx 以阻断并发 login（§D2 串行化机制）。
role 是独立聚合根（与 user 是不同 aggregate），authzmutate.Mutator.Apply 以 user 为
操作对象——routing rbacassign 的 role-row write 经 authzmutate 的 user-aggregate tx
是**聚合语义错误**：role_assignments write 不属于 user aggregate 边界。
**必须直调 invalidator，不可经 authzmutate。**

**identitymanage.Update 的 tx1/tx2 拆分 TOCTOU（RESOLVED — Wave 5 P1-1）**

~~`applyUserUpdate` 拆为两个独立 tx：tx1（非 authz 字段更新：email/username 等）和
tx2（authz demotion + RequirePasswordReset false→true → `invalidator.Apply`）。~~

**已由 Wave 5 P1-1 修复**（§A12）：`applyUserUpdate` 改为单 RunInTx，非 authz 字段
更新、`ApplyInTx`（credential mutation + invalidation）、event publish 三者共用同一
事务 context（`txCtx`），TOCTOU 窗口消除。tx1/tx2 拆分不再存在。

#### 上游 caller-set 为何不可再收窄——Go 类型天花板

Go 在"side-effect 必须在调用方 tx 内"约束上无法通过类型系统强制 funnel：

**对标 ent（entgo.io）**：`tx.Client()` 是 capability object，任何持有 `tx.Client()`
的代码都可以绕过任意 repository funnel 直接操作 DB。ent 不提供"此 tx 内只能调 X"
的类型约束。ent 的最高保证 = **Medium**（caller 能绕过 = Medium，不是 Hard）。
`ref: ent/ent tx.Client`

**对标 go-kratos**：context-tx 把 tx 注入 context，任何持有 ctx 的 handler 都可
提取 tx 直接操作。kratos 没有"context-tx 只能被 Y 消费"的编译期约束。最高保证
= **Low/Medium（纯 convention）**。`ref: go-kratos context-tx`

**结论**：Go 在"tx-bound side-effect funnel"约束的类型天花板是：
**下游 Hard（字段私有化 + sealed Mutation interface = 包外不可绕过）+
上游 Medium-by-necessity（archtest caller allowlist，但 allowlist 内的 caller
可以不调 funnel 而直调 invalidator = 漏调 regression 不被静态防）**。
任何在 Go 中宣称"上游也 Hard"的 TxHandle/marker 方案均是伪 Hard：
仍需调用方主动使用 marker，marker 的使用本身不能被强制。

实际 allowlist（`CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01`，archtest 锁定）：

```
credentialinvalidate/, authzmutate/, identitymanage/, sessionrefresh/, rbacassign/
```

write-side Hard 保证来自 **Rule (a)**（字段私有化 + sealed interface）：包外无法
在 authzmutate.Mutator.Apply 之外触及 authz-affecting 字段。Rule (b)（caller
allowlist）因 co-tx atomicity 约束无法收窄，**Medium-by-necessity 是该约束在 Go
类型系统下的天花板**，不是设计妥协——业界对标（ent/kratos）证实同类 tx-bound
side-effect funnel 在 Go 也只到 Medium。双向锁评级：下游 Hard / 上游 Medium。

### A12 Wave 5 P1-1 authzmutator.ApplyInTx 单入口 Hard funnel（2026-05-17）

**改造背景**：PR #525 deep review 发现 `authzmutate.Mutator.Apply` 内置 `RunInTx`
边界，导致 Lock/Unlock/applyUserUpdate 的 caller 在 authzmutate tx 之外另起一个
publish tx，event publish 和 domain mutation 分属两个独立事务（split-tx）——L2
OutboxFact 保证实际上未生效。

**改造内容**：

- `authzmutate.Mutator.Apply` 删除。原 `Apply` 内 `RunInTx` 闭包的**内容**提取为
  `ApplyInTx(ctx, txCtx context.Context, userID string, m Mutation, now time.Time) error`。
  `txCtx` 是调用方 outer `RunInTx` 提供的事务 context；funnel 不再持有 tx 边界。
- `Mutator.txMgr` 字段删除；`New` / `MustNew` 不再接受 `txMgr` 参数。
  `Mutator` 仅持有 `inv *credentialinvalidate.Invalidator` + `repo ports.UserRepository`。
- identitymanage 三处 caller 全部迁移：
  - `lockUserAndRevokeSessions`：guard tx 保留，apply+publish 合并为单 RunInTx（tx2）。
  - `Unlock`：apply+publish 合并为单 RunInTx。
  - `applyUserUpdate`：非 authz 字段更新 + credMut.ApplyInTx + publish 合并为单 RunInTx，
    消除原 §A10 §A10"accepted-by-design tx1/tx2 拆分"中所述的 TOCTOU 窗口。

**威胁矩阵重评**（仅影响行）：

| 场景 | 修改前 | 修改后 |
|------|-------|-------|
| Lock domain mutation + event publish 原子性 | ⚠ split-tx（mutation tx 单独提交，publish tx 另起） | ✅ 单 RunInTx co-commit（L2 OutboxFact） |
| Unlock domain mutation + event publish 原子性 | ⚠ split-tx | ✅ 单 RunInTx co-commit |
| applyUserUpdate credMut + event publish 原子性 | ⚠ split-tx（tx1 publish，tx2 credMut 独立） | ✅ 单 RunInTx co-commit |

§A10 "identitymanage.Update 的 tx1/tx2 拆分 TOCTOU（accepted-by-design）"段落不再适用：
合并后为单 tx，accepted TOCTOU 窗口已消除。

**backlog**：`IDENTITYMANAGE-LOCKUNLOCK-CO-TX-UPGRADE-01` ✅ closed（Wave 5 PR #525）。

### A11 读侧 credential-authority funnel（S-next 落地；2026-05-17 重写：session-revoke 出 funnel）

**问题**：token issue（sessionlogin / sessionrefresh）和 token validate（sessionvalidate）
路径均包含"是否允许该用户凭据"的判断逻辑，但实现散落在各 slice 内，无单一 Hard 收口：

- `sessionlogin` 检查 `user.CanAuthenticate()` + password hash
- `sessionvalidate` 检查 `user.CanAuthenticate()` + epoch 比对
- `sessionrefresh` 检查 epoch 比对但不检查 `CanAuthenticate()`（P1.1/P1.3 class）

任何新增 issue/validate 路径若遗漏其中任一检查，均构成 P1.x 级 regression。

#### A11.1 § 重写（2026-05-17，PR #542 reviewer P1-A）

S-next 首版把 `session.{Session,ValidateView}.RevokedAt` 也塞进 `credentialauthority.Check`
接口（`WithSessionNotRevoked`），让 `Assert(user, sessionRevoked)` 同时收口 user-bound
凭证检查 + session-state 检查。形态错位的铁证：`WithSessionNotRevoked.apply(_ *User)`
用 underscore 丢弃 user 参数 — Go 类型系统已经在告知 "这个 Check 根本不需要 user"。

错位的运行时代价（PR #542 reviewer P1-A）：因为 `Assert(user, ...)` 强制要求 user
已 fetched，session-revoked 检查被迫**后置于** `userRepo.GetByID`。结果：

- **revoked + suspended user → 403 ErrAuthUserNotActive**（被 user-not-active 路径
  截胡，wire envelope 漂移）
- **revoked + userRepo 503 → 503 ErrAuthServiceUnavailable**（infra outage 漏出
  side-channel：revoked 即攻击者可观测，与 user 状态正交）

§A11 重写后，**funnel 适用域收窄为 user-bound credential checks only**：

```go
// credentialauthority.Assert(user *domain.User, checks ...Check) error
//
// baseline check (user.CanAuthenticate()) 始终在 Assert body 内联跑；
// 调用方加入 Check 表达 user-bound 附加要求：
//   - SnapshotPasswordVersion(*domain.User) → Check （issue 路径，
//     sessionlogin 用）：funnel 包内部读 User.PasswordVersion；slice 代码
//     永不直接读字段。concrete 类型 withPasswordVersionPin 是 unexported,
//     包外不可零值构造（上游 Hard sealed-by-name）。
//
// session-state 检查（RevokedAt）**不**走本 funnel。它是 session 实体属性，
// 不需要 user 上下文，并由独立 archtest SESSION-REVOKED-FIELD-ACCESS-01 守护
// owner-package 字段访问 allowlist（cells/accesscore/slices/sessionvalidate/、
// cells/accesscore/slices/sessionrefresh/、runtime/auth/session/）。三个
// slice 在 sessionStore.Get 后**立即**判 RevokedAt → uniform 401，不再
// 等到 user lookup。
//
// 失败统一返回 KindPermissionDenied + ErrAuthUserNotActive，具体 reason
// 仅经 WithInternal 写入 slog；slice 必须按 "调用入口" 转换 wire-level
// 错误，不可按 err 内容分支（防枚举 + "callers must not branch on err"）。
//
// 无 ctx 参数：funnel 不做 I/O，无 tracing 表面，加 ctx 等于为假设未来需求
// 设计。
```

#### A11.2 Hard funnel 实现（archtest 四勿一）

`tools/archtest/credential_authority_assert_funnel_test.go`，
INVARIANT: `CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01`：

- **下游 Hard**（caller allowlist）：`typeseval.ResolvePackageRef` 解析
  `credentialauthority.Assert` 的 `*types.Func` 身份；caller 限定
  `cells/accesscore/{internal/credentialauthority/, slices/sessionlogin/,
   slices/sessionrefresh/, slices/sessionvalidate/}`。
- **上游 Hard direct**（mandatory funnel）：扫 3 slice 目录 production 文件，
  禁止 `domain.(*User).CanAuthenticate` typed method call、
  `domain.User.PasswordVersion` typed field selector 在 funnel 外出现；通过
  `ResolveMethodCall` + `*types.Info.Selections` 双 prong。**RevokedAt 不再
  在此 funnel 守护范围内**（由 SESSION-REVOKED-FIELD-ACCESS-01 接管）。
- **上游 Hard sealed-by-name**（P1-B）：扫 credentialauthority 包内每个 named
  struct，凡实现 `Check` interface 必须 unexported（首字母小写）。阻止包外
  零值构造 concrete Check 绕过工厂函数。
- **上游 Hard typed callee reference**（P2-B，Wave 4 升级）：扫 `cells/` +
  `cmd/` + `runtime/` 全 production scope（与 downstream caller allowlist 同
  scope），对每个 SelectorExpr typed-resolve 到 `credentialauthority.Assert`
  或 `domain.(*User).CanAuthenticate`，**只要不在 CallExpr.Fun 位置就是违规**
  （typed-parent-check 单点判定）。**没有 syntactic context 枚举**：
  AssignStmt RHS / ValueSpec / CallExpr arg / **ReturnStmt** / SendStmt /
  IndexExpr / CompositeLit element 等所有 Go expression 位置均自动覆盖，
  符合 ai-collab.md §"Hard 范本：typed function call as Hard funnel" 的
  form uniqueness 要求。Wave 4 之前 Soft 形态（按 3 类 syntactic context
  枚举 + scope 限 3 slice）已废弃。`types.Implements` 调用走
  `tools/typesutil.ImplementsInterface` funnel（`TYPESUTIL-IMPLEMENTS-FUNNEL-01`
  守卫，archtest 内禁止裸 `types.Implements`）。
- **Blind-spot 反向自检** 4 件套：method-value 赋值（EachInSubtree 覆盖链式
  形态 `obj.GetUser().CanAuthenticate`）、`reflect.MethodByName`、
  `reflect.FieldByName(PasswordVersion)`、`unsafe` 导入。
- **3 个 RED fixture**（per-detector 分桶 ≥1 验证 detector live）：
  `testdata/outside_caller_red`、`testdata/direct_canauth_skip_red`（per-bucket
  CanAuthenticate + PasswordVersion 各 ≥ 1）、`testdata/value_capture_red`
  （per-bucket AssignStmt + ValueSpec + CallArg 各 ≥ 1）。

#### A11.3 已知 caveat（archtest pkg-doc 显式列出，不构成 Hard 等级下调）

- 跨包 helper 间接：若 slice 引入外部包 wrapper（如 `pkg/authcheck.X(user)`
  内部读 CanAuthenticate），AST scope 是 slice 前缀，外包不可见。本仓库 slice
  自含 service.go + handler.go + 包内 helpers，不引入外部 authz wrapper。
- interface 间接：若 slice 通过 interface 读 *domain.User 等价物，`*types.Info`
  解析到 interface method 而非 concrete *types.Func。当前 slice 直接持
  *domain.User，不构成实际盲区。

#### A11.4 评级与正交关系

下游 Hard + 上游 Hard 四勿一，闭环 funnel；与 write-side
`AUTHZ-MUTATION-APPLY-FUNNEL-01`（Rule a Hard 下游 + Medium-by-necessity 上游）
对称达成 read-side / write-side 双向闭合。本 funnel 无 co-tx atomicity 约束，
upstream Hard 无需 Medium ceiling。

session-state 由独立 funnel `SESSION-REVOKED-FIELD-ACCESS-01`（Hard upstream
allowlist；详见 §A13）。两条 funnel 各自单语义、各自 Hard，AI 不可绕过的形态
数从 §A11 重写前的 1 个 funnel 翻倍为 2 个独立 funnel。

**与 §A8 / §A9 / §A10 正交**：funnel 范围限定 (a) baseline / (b)
password-pin；epoch 比对（§A8 row provenance）/ password-reset transition
（§A9）/ co-tx atomicity（§A10）/ subject-binding 保持原位，**不**进 funnel。

#### A11.5 wire-uniformity 防枚举（ADR §A13 载体）

S-next 首版把单 envelope 防枚举的语义责任压在 funnel 上（"三种失败统一返回
同一 errcode"），但 funnel 后置导致 revoked 路径产生 wire 漂移。重写后**单
envelope 防枚举语义改由 wire 层承担**：三 slice 对 revoked / user-not-found /
userRepo-error / inactive 全部返回同一 `errcode`（sessionvalidate:
`ErrAuthInvalidToken` 401；sessionrefresh: `ErrAuthRefreshFailed` 401）+ 同
slog 字段集合（subject / sid / level）。Funnel 只是 "user-bound 检查的语法
收口"，不是 "单 envelope 的载体"。详见 §A13。

**威胁矩阵更新**：本节重写后，§3 表 "issue/validate authority predicate
scatter" 一行保持 ✅ Hard，但说明改为"双 funnel 闭合"（user-bound funnel + 独立
session-revoked allowlist），并新增一行 "revoked-session 后置导致 wire 漂移"
✅ Hard（由 §A11 重写 + §A13 wire-uniformity 共同闭合）。

**backlog**：`CREDENTIAL-AUTHORITY-READSIDE-FUNNEL-01` ✅ closed by S-next PR；
`CREDENTIAL-AUTHORITY-FUNNEL-SCOPE-AUTO-DERIVE-01`（触发型：accesscore 新增
第 4 slice 时升级 funnel scope 自动派生）+
`SESSION-AUTHORITY-FUNNEL-CONDITIONAL-UPGRADE-01`（触发型：session 状态字段
≥3 时升级 SessionAuthority 平行 funnel）+
`WIRE-UNIFORM-RESPONSE-ARCHTEST-01`（Medium → Hard：wire 层 single-envelope
当前由 service_test 守护，未来升级为 archtest typed comparison）。

### A13 wire-uniformity 防枚举载体（2026-05-17）

**问题**：§A11 首版把"四态合 single envelope"（revoked / user-not-found /
userRepo-error / inactive）的责任压给 funnel；funnel 后置后 wire 漂移
（revoked + inactive → 403、revoked + repoErr → 503）。

**决策**：单 envelope 防枚举语义由 wire 层显式承担，funnel 只锁字段访问。
三个 slice 必须对下列失败路径返回**同一 errcode envelope** + **同一 slog 字段集合**：

| Slice | 失败路径 | wire errcode | wire status | slog msg |
|---|---|---|---|---|
| sessionvalidate | revoked / not-found-user / repoErr / inactive / epoch-mismatch / sid-subject-mismatch | `KindUnauthenticated` + `ErrAuthInvalidToken` | 401 | `errMsgAuthFailed`（"invalid or expired authentication token"） |
| sessionrefresh | revoked / subject-mismatch / user-not-active / stale-epoch / reuse | `KindUnauthenticated` + `ErrAuthRefreshFailed` | 401 | `errMsgInvalidRefreshToken`（"invalid refresh token"） |
| sessionrefresh | infra outage（session store / refresh store / user store） | `KindUnavailable` + `ErrAuthRefreshUnavailable` | 503 | distinct（infra 故障不属于"防枚举"范围） |
| sessionlogin | wrong-password / not-found-user / inactive | `KindUnauthenticated` + `ErrAuthLoginFailed` | 401 | `errMsgInvalidCredentials` |

**关键不变量**：一旦 session 被发现 revoked，**user 状态 / userRepo 可用性
都不能改变 wire envelope**。revoked + inactive → 401（不漂 403）；revoked +
repoErr → 401（不漂 503）。

**enforcement 现状**：

- 当前 wire 层一致性由 per-slice **service_test.go** 断言（结构化字段 +
  组合用例 revoked+inactive、revoked+repoErr 等）守护 — Medium 评级。
- backlog `WIRE-UNIFORM-RESPONSE-ARCHTEST-01` 登记 Medium → Hard 升级路径：
  扫三 slice 入口（VerifyIntent / Refresh / Login）的 error return path，
  按入口归类、断言每类入口对应的 errcode 集合是 enumerable allowlist。

**实施载体**（Wave 2 GREEN）：

- sessionvalidate/service.go `enforceSessionState`：sessionStore.Get →
  RevokedAt inline check → sid/subject 匹配 → userRepo.GetByID →
  `credentialauthority.Assert(user)` → authz_epoch。revoke 命中直接 uniform 401。
- sessionrefresh/service.go `refreshInTx`：Peek → verifySession → RevokedAt
  inline check + cascadeRevoke → subject-match → fetchUserForRefresh →
  rejectIfUserNotActive → rejectIfStaleEpoch → mint+rotate。
- sessionlogin/service.go: 删除 `bcrypt_ok` 字段（P2-C 安全清理；inactive
  路径 slog Internal 不再泄漏 "密码匹配真值" 给账号枚举攻击者）。

**§3 威胁矩阵新行**：

| 威胁场景 | 防御 |
|---|---|
| revoked session 后置导致 wire 漂移（P1-A）| ✅ Hard：§A11 重写 + revoke inline check 前置；wire 层 single-envelope 由 service_test 守护（Medium，升 Hard 路径见 backlog `WIRE-UNIFORM-RESPONSE-ARCHTEST-01`）。回归测试：`TestService_Refresh_RevokedSession_RevokeBeforeUserLookup`、`TestService_Refresh_RevokedSession_UserRepoUnavailable_StillReturns401`、`TestEnforce_RevokedSession_UserRepoUnavailable_Returns401_Uniform`。 |

---

## 1. Context

### 1.1 触发因素

PR#417 在 `accesscore` PG 接入中暴露 5 个 P0/P1 协议缺口（详见 `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md` §3-§7）。问题不是 PG 引入新风险，而是 **PG 持久化把 mem 模式下隐式的协议决策一次性翻出**：

| 缺口 | mem 模式现状 | PG 持久化后 |
|---|---|---|
| **P1-① 明文 token 落库** | session 表 `AccessToken: string` 字段（`cells/accesscore/internal/domain/session.go:17-25`） | DB 备份/replica 让 token 明文长期持久；攻击者拿 DB 即拿到可重放 token |
| **P1-② JWT role claim 失效** | 无 server-side state；revoke 不影响 JWT 签名校验 | 多实例/reload 让旧 token 仍然合法；缺少强制失效机制 |
| **P1-③ login vs role-revoke 排序** | mem mutex 隐含同步 | 并发 login 可能在 revoke sweep 之后落入新 session 仍含旧 role claim |
| **P1-④ credential event 旧凭据失效协议** | password reset / lock / delete / role revoke 各 endpoint 单独实现，无统一接口 | PG store 必须有显式 revoke 语句；散落实现易漏 |
| **P1-⑤ admin 业务不变量** | 应用层 fast-path 检查 + mutex | PG unique constraint 是隐式拍板（不应由 schema 决定产品语义） |

P1-⑤ 由配套 ADR（`202605101400-adr-admin-invariant.md`）单独决议；本 ADR 决议 P1-① 至 P1-④ 加 2 个 PR#417 review §12 中点出的边界（refresh-vs-session 关系 / 事务边界），共 6 项。

### 1.2 在 typed-Go-heavy 范式中的位置

ADR-Typed (`202605101200-...`) 锁定了 GoCell 协议决策范式：sealed interface + Option + composition-root 显式构造。本 ADR 是该范式在 session 协议上的**首个落地**：

- 协议决策落 typed Go 词汇表（`runtime/auth/session/protocol.go`，S1 PR 实施）
- mem / PG store 共享 storetest conformance（S2 / S3+S5 PR 实施）
- composition root 显式 `MustNewProtocol(...)`（S4 PR 实施）

S1 PR 仅落 ADR + Go 头文件骨架（含完整 Option 实现，不留 panic stub）；后续 PR 落 Store / mem / storetest / cell 接入。

### 1.3 概念澄清（避免歧义）

- **JWT** = token 的编码格式（base64 + signature）
- **jti** = JWT 内的标准 claim 字段（"JWT ID"，[RFC 9068 §2.2.4](https://datatracker.ietf.org/doc/html/rfc9068#section-2.2.4) **强制**要求 access token 含 jti）
- **fingerprint** = server 端存放的 token 派生物（HMAC / hash），用于"DB 不存明文但能 validate"
- **authz_epoch** = user 表上的整数列，每次 role 变更 bump，JWT claim 内含 epoch，validate 时比对

"jti-only" = **继续用 JWT**，JWT 含 jti claim（合规 RFC 9068），session 表只存 jti 引用，**不存** token 明文也**不存** HMAC fingerprint。

---

## 2. Decision

### D1 Token 状态模型 = jti-only（解 P1-①）

JWT access token 必含 `jti` claim（合规 RFC 9068 §2.2.4）。session 表存 jti 引用 + epoch 烧入值，**不存** token 明文，**也不存** HMAC fingerprint。

**当前形态**（`cells/accesscore/internal/domain/session.go:17-25`）：

```
session row: { id, user_id, AccessToken: string ← 明文, expires_at, revoked_at, ... }
```

**目标形态**（S3+S5 落 PG migration）：

```
session row: {
    id,
    user_id,
    jti string NOT NULL,                  -- JWT jti claim，validate 时 lookup
    authz_epoch_at_issue bigint NOT NULL, -- 签发时刻 user.authz_epoch 的快照
    expires_at,
    revoked_at,
    ...
}
-- index: (jti) UNIQUE, (user_id, revoked_at)
```

JWT validate 路径（S4d 形态：§A8 row SoR + access JWT 不含 authz_epoch claim — S4d 决议；§A7 段在 FU-1 删除，row SoR 已是唯一真值，无平行 §A7 原文）：

```
parse JWT → verify signature
SELECT id, subject_id, revoked_at, authz_epoch_at_issue FROM sessions WHERE id = sid
if !found OR revoked_at IS NOT NULL → reject (uniform 401)
if view.subject_id != claims.sub → reject (defense-in-depth)
SELECT authz_epoch FROM users WHERE id = claims.sub
if user.authz_epoch != view.authz_epoch_at_issue → reject (D2 stale token)
```

注：sessionvalidate 按 `sid` (claims.SessionID) 查 session（A2），不按 jti。
epoch 比对源是 `view.AuthzEpochAtIssue`（row），不是 `claims.AuthzEpoch`
（已删，A7）。archtest `SESSIONVALIDATE-EPOCH-SOURCE-01` 锁定该形态。

**Alternatives Considered**：

| 方案 | session 表 | DB 泄露后 | JWT 改造 | 业界共识 |
|---|---|---|---|---|
| jti-only ✅ | `jti: string` | 不可重放 | + jti claim（RFC 9068 强制） | RFC 9068 / OWASP / OAuth Security BCP / Vault accessor / dex storage 同源 |
| HMAC fingerprint | `fingerprint: HMAC(token,key)` | 不可重放 | 无 | 适用 opaque token；JWT 已自签 = 双重签名；增加 key 管理面 |
| 明文（现状） | `AccessToken: string` | 直接重放 | 无 | 反 OWASP，PR#417 P1-① 报点 |

`runtime/auth/session/protocol.go` sealed `FingerprintMode` 仅含 `FingerprintJTIRef`。未来 opaque-token 部署若需，新增 sealed sibling（如 `FingerprintHMACSha256`），不破当前 API。

### D2 Login vs Role-Revoke 排序 = AuthzEpoch（解 P1-②、P1-③）

`users` 表加单调递增整数列 `authz_epoch BIGINT NOT NULL`。**bump 触发**：

- role assigned / revoked
- password reset
- account lock / unlock
- account delete

login 时（S4d 形态，行锁 + epoch 快照）：

```
BEGIN tx
  SELECT authz_epoch FROM users WHERE id = $1 FOR UPDATE   -- row lock blocks Invalidator.Apply
  INSERT INTO sessions (..., jti=$jti, authz_epoch_at_issue=<read>) ...
  INSERT INTO refresh_tokens (..., authz_epoch_at_issue=<read>) ...
  -- access JWT payload (S4d 删 epoch claim):  { sub, jti, sid, exp, ... }
COMMIT
```

role revoke 时（同 tx 完成 D5，Invalidator.Apply 三操作 atomic）：

```
BEGIN tx
  DELETE FROM role_assignments WHERE ...
  -- Invalidator.Apply trifecta (downstream Hard funnel; archtest 守):
  UPDATE users SET authz_epoch = authz_epoch + 1 WHERE id = $1   -- 持 user 行写锁 → 阻塞并发 login
  UPDATE sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL
  UPDATE refresh_tokens SET revoked_at = NOW() WHERE subject_id = $1 AND revoked_at IS NULL
  INSERT INTO outbox (event=role.revoked, ...)
COMMIT
```

validate 路径（S4d 形态）：

```
view, err := sessionStore.Get(ctx, sid)                            -- read session row (carries AuthzEpochAtIssue)
user, err := userRepo.GetByID(ctx, claims.Subject)                 -- read live user.AuthzEpoch
if user.AuthzEpoch != view.AuthzEpochAtIssue → 401 ErrAuthInvalidToken
```

**效果**：旧 session 行 `authz_epoch_at_issue=5`，role revoke 后
`user.authz_epoch=6`，下一次 validate 立即拒。refresh 同理：refresh 行
`authz_epoch_at_issue=5` 与 user.AuthzEpoch=6 不匹配，sessionrefresh 路由进
`rejectIfStaleEpoch`，后者调 `cascadeRevoke("stale-epoch")`（session-scoped revoke；S4e 修正，见 §A6）。无需 sweep 已发 token——它们自动失效。

**P1-③（并发 login vs revoke）的串行化机制**：S4d 用 PG row-level lock
（SELECT ... FOR UPDATE on users）让 login tx 与 revoke tx 通过 user 行天然串行化。
login 持 user 行写锁的窗口覆盖 session/refresh INSERT；revoke 期 BumpAuthzEpoch
也持同一行写锁。无 advisory lock、无 application-side CAS。

**Alternatives Considered**：

| 方案 | 性能 | 已签发 token | 业界共识 |
|---|---|---|---|
| AuthzEpoch ✅ | 无锁；validate +1 user.epoch lookup | 立即失效 | OAuth Security BCP §4.13.1；Auth0/Okta/Keycloak 同源 |
| AdvisoryLock | login 串行化 | 不解决（旧 token 仍持权） | 解决"同时性"，不解决"已签发"；非主流 |
| RowVersion | session 行加 version | 部分（session-level 不对 user-level RBAC） | 粒度错位 |
| Pure sweep（现状） | 简单 | sweep 与并发 login 之间无序 | 即 P1-③ 报点 |

`runtime/auth/session/protocol.go` sealed `OrderingModel` 仅含 `OrderingAuthzEpoch`。

### D3 Credential event 撤销范围 = 4 事件 fail-closed（解 P1-④）

定义 typed enum `CredentialEvent`，4 个常量。每个事件触发 access + refresh 全部撤销：

| Event | 触发条件 | 撤销范围 |
|---|---|---|
| `CredentialEventPasswordReset` | password reset 流程完成 / change password 流程完成 | 该 user 所有活跃 session + 该 user 所有 refresh chain |
| `CredentialEventLock` | account lock（手动 / 失败累计阈值，详见 ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 backlog） | 同上 |
| `CredentialEventDelete` | user delete | 同上（之后清理 session/refresh 行） |
| `CredentialEventRoleRevoke` | role assignment 删除 / role 重新分配 | 同上 |

`permission remove` 走 `CredentialEventRoleRevoke` 路径（permission 是 role 的属性，permission 调整 = role 行为变化 = epoch bump）。

**对照 OWASP / NIST**：

- OWASP ASVS V3 (Session Management) §3.3.1 / §3.3.2：credential 状态变化必须立即失效活跃 session
- NIST SP 800-63B §5.1.4.2：credential lock / delete / re-issue 必须同时失效 outstanding tokens
- 不采纳 ory/kratos 的 AAL（Authenticator Assurance Level）软失效路线

**Alternatives Considered**：仅撤销 access（不撤销 refresh）。否决：refresh 设计是续 access，access 撤销但 refresh 保留 = 用户能立即换出新 access，等同没撤。

### D4 Refresh-vs-Session 关系 = 共生命周期

session revoke ⇒ 该 session 下所有 refresh chain 同 tx 标记 revoked。

当前 `cells/accesscore/slices/sessionlogout/service.go:109-142` 已是此语义（`persistRevoke` 同 tx 撤 session + cascade revoke refresh chain）。本 ADR 锁定该约定为协议级要求，禁止未来"refresh 独立生命周期"分支演化。

`runtime/auth/refresh/` 已是 selector+verifier opaque-token 范式（与 session 的 jti-ref 概念一致）；session.Store 的 `RevokeForSubject(ctx, subjectID, event)` 与 refresh.Store 的 batch revoke 在 cell 层组合，runtime 不引入跨包 dependency。

#### D4.1 Refresh 不轮换 session 行（session.ID stable across refresh）

`sessionrefresh.Service.Refresh` **不创建 / 不撤销 / 不更新** session 行。
session.ID 从 login 时刻设定后保持稳定，直到 logout / `RevokeForSubject` /
`expires_at` 自然过期；access JWT 在每次 refresh 时携带同一个 `sid` claim，
仅 `jti` 与 `exp` 随每次 mint 推进。

本条对齐三层业界标准：

- **OAuth2 RFC 6749 §1.5 / §6**：refresh token 代表 "the authorization granted"，
  refresh 是同一份 grant 的持续延伸，**不产生新 authorization**。
- **OIDC Back-Channel Logout 1.0**：`sid` claim 标识一个用户登录会话，
  OP 用它通知 RP 撤销同一会话；隐含语义是 `sid` 跨 refresh 稳定，否则
  logout 通知无法定位 RP session。
- **业界实现**：
  - ory/fosite `handler/oauth2/flow_refresh.go`：refresh 时 `request.SetID(originalRequest.GetID())` + `session.Clone()`，aggregate ID 不变
  - zitadel/zitadel `internal/command/oidc_session.go`：refresh 在同一 OIDCSession aggregate 上 append `OIDCSessionRefreshTokenRenewedEvent`
  - keycloak `TokenManager.java::refreshAccessToken`：`findOfflineUserSession(realm, oldToken.getSessionState())` 复用 sid

**反模式（不允许）**：每次 refresh 都 Revoke 旧 session ID + Create 新 UUID。
该模式与 refresh chain 一致性域冲突（child refresh row 仍继承旧 session_id，
二次 refresh 失败），且使 OIDC `sid` 不再 stable。曾在 commit fd954cb8 引入，
在 PR #482 review 撤回。`SESSIONREFRESH-NO-SESSION-CREATE-01` archtest 静态拦截
任何重新引入该模式的尝试（`cells/accesscore/slices/sessionrefresh/` 包内禁止
调用 `session.Store.Create / Revoke / RevokeForSubject`）。

**AuthzEpoch / role snapshot 推进路径**（S4d 形态）：refresh 时通过
`presented.AuthzEpochAtIssue`（从 refresh 行读，A8 引入）与 live
`users.authz_epoch` 比对；不匹配走 `rejectIfStaleEpoch` → `cascadeRevoke("stale-epoch")`（S4e 修正，§A6；非 user-wide invalidation）。
sessionvalidate 同源比对 `view.AuthzEpochAtIssue`（session 行，A8）与
`users.authz_epoch`。Session UUID 不轮换（OAuth2 §1.5 + ADR §D4.1）；refresh
child 继承 chain 的 `authz_epoch_at_issue`，refresh chain 内 epoch 稳定。
访问令牌 claims 在 refresh 时按 user state 重新签发（password reset flag /
role membership），但 access JWT 不再携带 `authz_epoch` claim（A7），不写回
session/refresh 行。

#### D4.2 Session 行 retention

session 行只在以下路径状态变化：

| 路径 | 操作 |
|---|---|
| login | INSERT 新行，`revoked_at = NULL` |
| logout / `RevokeForSubject` | UPDATE `revoked_at = NOW()` |
| `expires_at` 自然过期 | 行保留，应用层基于 `expires_at` 判失效 |

session 行不进行 in-place rotation。冷数据清理由运维侧 cron 处理（与
`audit_entries` 同模式），不在 cells/accesscore 内引入 session_gc worker
——避免 refresh_gc 的 lifecycle 复杂度被复制；refresh_gc 之所以存在是因为
refresh_tokens 的 sliding window 模型必须实时 GC，session 行没有同等压力。

### D5 事务边界 = credential event 与 session revoke 同 tx + outbox

任何 credential event 触发的 session revoke **必须**与 credential 状态变更同 tx：

```
BEGIN tx
  -- credential 状态变更（例 role revoke）
  DELETE FROM role_assignments WHERE ...
  UPDATE users SET authz_epoch = authz_epoch + 1 WHERE id = $1
  -- 同 tx 撤销 session + cascade refresh
  UPDATE sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL
  UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL
  -- L2 OutboxFact
  INSERT INTO outbox (event=role.revoked.v1, payload=..., ...)
COMMIT
```

consumer 端（cross-cell fan-out / authz cache invalidation）走 outbox L3 eventual。本地 revoke 必须 strong consistency；远程 fan-out 是 best effort。

**Alternatives Considered**：异步 sweep（credential change 后发事件，consumer 异步 revoke session）。否决：留漏窗口（窗口内攻击者持旧 token 仍合法），反 fail-closed。

### D6 Fingerprint 备选

sealed `FingerprintMode` 当前仅含 `FingerprintJTIRef` 单实现。未来 opaque-token 部署若需 HMAC fingerprint，新增 sealed sibling `FingerprintHMACSha256`（不破现有 API；composition root 显式选）。

不引入 `FingerprintNone`：dev-only 类型 + phase0 拦的设计是反 fail-closed（开发期偷懒选 None，phase0 漏拦就上线 → 明文 token 持久化）。开发期使用 mem store + JTIRef，无需 None。

---

## 3. Threat Model 覆盖矩阵

> S4d 重跑（2026-05-15）：A1 RETRACTED + A8 row provenance + A9 RequirePasswordReset
> funnel + access JWT 不再携带 authz_epoch claim（S4d 决议；§A7 段已在 FU-1 删除）。
> S4e 重跑（PR #494，2026-05-15）：
> authzmutate Hard funnel 闭合 + P2.b stale-epoch 路径修正。RC-A 重跑（2026-05-15）：
> RoleRevoked 死代码删除 + §A10 co-tx atomicity Medium 天花板显式锁定。每行重新评估（按
> ai-collab.md §"ADR amendment 重跑威胁矩阵" 规则）。`Row SoR` 列代替原 `AuthzEpoch`
> 列以反映实际 SoR 位置；`Funnel 上游` 列新增反映 P1-#1 修复。

| 威胁场景 | jti-only | Row SoR (AuthzEpochAtIssue) | Fail-closed events | Funnel 上游 (S4e/RC-A) | 同 tx |
|---|---|---|---|---|---|
| DB 泄露 → access token 直接重放 | ✅ DB 无明文/HMAC | — | — | — | — |
| Role downgrade 后旧 access 仍持高权 | — | ✅ user.epoch != session.epoch_at_issue → validate reject | ✅ Invalidator.Apply 同 tx 撤 session | ✅ rbacassign.Revoke 直调 inv.Apply（co-tx 必需；§A10 co-tx atomicity 约束）| ✅ 失效原子 |
| Role downgrade 后旧 refresh 升级到新 epoch (P1-#2) | — | ✅ user.epoch != refresh.epoch_at_issue → sessionrefresh cascade（A6 stale-epoch 入口） | ✅ Invalidator.Apply 撤所有 refresh chain | ✅ 同上 | ✅ 失效原子 |
| Device theft → user lock | ✅ session lookup 拒 | ✅ row.epoch 同步 stale → validate 双层防 | ✅ Lock event 同 tx 撤 session+refresh | ✅ identitymanage.Update authz demotion 走 authzmutate funnel（tx2 path；tx1/tx2 TOCTOU accepted-by-design，见 §A10） | ✅ 失效原子 |
| Password reset → 旧 access/refresh 仍可用 | — | ✅ epoch bump 让 row stale | ✅ ChangePassword event 同 tx 撤 session+refresh | ✅ changePasswordInTx 直调 inv.Apply（co-tx 必需，见 §A10） | ✅ 失效原子 |
| PATCH RequirePasswordReset=true 不立即生效 (P1-#1) | — | ✅ epoch bump → row stale | ✅ 复用 `CredentialEventPasswordReset` event（false→true transition 等价于 credential-weakening，无需新增独立事件） | ✅ identitymanage false→true transition 走 authzmutate.Mutator.Apply → inv.Apply（co-tx tx2 path；见 §A10） | ✅ 失效原子 |
| Account delete → 残留 session 攻击面 | — | — | ✅ Delete event 同 tx 撤所有 | ✅ identitymanage.Delete 直调 inv.Apply（co-tx 必需，见 §A10） | ✅ 失效原子 |
| 并发 login 与 role revoke (P1-#3) | — | ✅ login 持 user 行 FOR UPDATE 写锁，revoke 期 BumpAuthzEpoch 也持同行写锁 → PG read-committed + row lock 天然串行化 | — | — | ✅ 失效原子 |
| stale refresh + epoch 不匹配（P2.b，S4e 修正）| — | ✅ row.epoch != user.epoch → `rejectIfStaleEpoch` → `cascadeRevoke("stale-epoch")`（session-scoped，非 user-wide） | ✅ session 失效原子（cascade revoke） | ✅ sessionrefresh 走 stale-epoch 路径（非 user-wide Invalidator.Apply） | ✅ 失效原子 |
| 新增 user authz-affecting 字段漏调 invalidator（S4d → S4e 闭合）| — | — | — | ✅ S4e PR #494：domain.User authz 字段私有化（SetStatus/SetPasswordResetRequired caller-set ⊆ authzmutate）+ archtest `AUTHZ-MUTATION-APPLY-FUNNEL-01` Hard 闭合。RC-A：RoleRevoked 死代码删除，Mutation 目录精确到 5 个。`CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01` Medium-by-necessity（co-tx atomicity，§A10 天花板证明） | — |
| issue/validate authority predicate scatter（P1.1/P1.3 class）| — | — | — | ✅ Hard：read-side funnel `credentialauthority.Assert` 已落地（§A11），下游 caller allowlist + 上游 sealed-by-name + 上游 mandatory direct + 上游 value-capture 四勿一（Hard archtest `CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01`）；slice 通过 unexported concrete + 工厂函数（SnapshotPasswordVersion）永不直接读 `CanAuthenticate` / `PasswordVersion`。session-state（RevokedAt）由独立 Hard funnel `SESSION-REVOKED-FIELD-ACCESS-01` 接管 owner-package 字段访问 allowlist（§A11.2 + §A13）| — |
| revoked session 后置导致 wire 漂移（PR #542 P1-A）| — | — | — | ✅ Hard：§A11 重写 — `SessionNotRevoked` 从 user-bound funnel 移出，session-state 检查 inline 跑在 `sessionStore.Get` 之后、`userRepo.GetByID` 之前；revoked + inactive 不再漂 403、revoked + repoErr 不再漂 503。wire 层 single-envelope 由 service_test 守护（Medium，升 Hard 路径 backlog `WIRE-UNIFORM-RESPONSE-ARCHTEST-01`）| — |
| inactive/locked 账号密码匹配真值泄漏（PR #542 P2-C）| — | — | — | ✅ Hard：`sessionlogin/service.go` 删除 `bcrypt_ok=%v` slog Internal 字段。inactive 路径 slog 仅含 user_id + status，不再泄漏 "密码匹配" 真值，关闭账号枚举增强通道 | — |
| 账号枚举（公开 login 端点回响是否存在）（PR #501 RC-C1）| — | — | — | ✅ sessionlogin missing-user / wrong-password / inactive 三态均返 401 `ErrAuthLoginFailed` + 同一 `errMsgInvalidCredentials`；wire shape 不可区分用户态 | — |
| timing 旁路（公开 login 用响应耗时枚举用户）（PR #501 RC-C1）| — | — | — | ✅ missing-user 路径运行 `dummyBcryptHash` 校验（cost=12），耗时与真实 wrong-password / inactive 路径同阶；inactive 路径同样跑真 bcrypt 再返 401 | — |
| JWT 签名密钥泄露 | ❌（jti-only 不解此场景） | — | — | — | — |
| key rotation | ❌（不在本 ADR 范围；JWT issuer key rotation 是独立机制） | — | — | — |

JWT 签名密钥泄露 / key rotation 由 `runtime/auth/jwt.go` issuer key management 独立处理，不在 session protocol 范畴。

---

## 4. Consequences

### 4.1 正面

- **明文 token 落库消除**：DB 泄露不再等于 token 重放（D1）
- **role 变更立即生效**：epoch bump 让旧 token 自动失效，无需 sweep 已签发 token（D2）
- **统一 credential 失效协议**：4 事件 + fail-closed by default，无散落实现（D3）
- **协议决策类型化**：sealed FingerprintMode / OrderingModel / typed CredentialEvent → 编译期检查；mem/PG conform 同一 typed Protocol（D6）
- **archtest 减负**：`PG-REPO-CONSTRUCTOR-FAIL-FAST-01` 由 typed signature + `NewProtocol` 末尾 sentinel 校验直接覆盖；`PG-REPO-INVARIANT-LIST` 索引由 storetest 派生（参见 ADR-Typed §4.5）

### 4.2 负面

- **JWT payload 增大**：含 jti（UUID 36 字符）+ epoch（int64 < 20 字符）；预估 +60 字节 / token，影响可忽略
- **validate +1 DB lookup**：jti / sid lookup 与 user.epoch lookup 可合并到一次 join，但 mem 模式下也多了一次 store 调用（影响 < 100ns，可忽略）
- **migration 不向后兼容**：当前 mem 模式无 jti / epoch；切换时老 session 启动期 invalidate（CLAUDE.md "不考虑向后兼容"原则下接受）
- **role revoke 路径需要 update users.authz_epoch + update sessions**：写放大（每 role revoke 至少 4 次 update + 1 次 outbox insert），但 role revoke 是低频操作（< 1 op/s 量级）

### 4.3 与现有约束的关系

- **K-04**（cells 留 framework 仓）：cell 仍是协议本体（消费 typed Protocol + 业务编排）；runtime/auth/session 是协议词汇表，无矛盾
- **runtime-api.md Option 范式分层**：`WithFingerprint` / `WithOrdering` 是强依赖 wiring（一次声明一个不可替代依赖）；`WithRevokeOn` 是累加式 builder（events 可拆多次声明）
- **error-handling.md**：本 ADR 不引入新 errcode；`runtime/auth/session/protocol.go` 用 `errcode.New(errcode.ErrValidationFailed, ...)`（const literal message）保持 PII safety
- **observability.md**：session protocol 不直接产 metric / log；消费者 cell 在 logout / revoke 路径产 slog（已有约定）

---

## 5. Migration

| 阶段 | 工作 | PR |
|---|---|---|
| **S1（本 PR）** | ADR 落地；`runtime/auth/session/{protocol.go, protocol_test.go, doc.go}` typed Protocol 骨架（含完整 Option 实现） | refactor/547 |
| **S2** | `runtime/auth/session/{store.go, mem_store.go, storetest/suite.go}`；mem store conform Protocol-driven storetest | TBD |
| **S3+S5** | `adapters/postgres/session_store.go` PG conform；migration 加 `jti`、`authz_epoch_at_issue` 列；users 表加 `authz_epoch` 列；migration 删除 `sessions.access_token` 列 | TBD |
| **S4** | accesscore composition root 显式 `MustNewProtocol(...)`；cell 注入 `*session.Protocol` + `session.Store`；4 个 CredentialEvent 撤销路径在各 slice 接入；存量 session 启动期 `revoked_at = NOW()` invalidate | TBD |

### 5.1 Deployment Playbook

CLAUDE.md "Review 和重构时不考虑向后兼容" 原则适用于代码层；运维操作层仍需护栏避免发布事故。

**部署顺序（S3+S5 → S4 PR 落地时）**：

1. **预部署校验**：staging 环境跑完整 e2e（覆盖 fresh state + 升级路径），smoke test PASS
2. **Migration（独立步骤）**：执行 SQL migration（添加 `users.authz_epoch` 列 + `sessions.jti` / `sessions.authz_epoch_at_issue` 列；删除 `sessions.access_token` 列）。Migration 必须 `BEGIN; ... ; COMMIT;` 单事务幂等
3. **Binary swap**：替换为新版本 binary。新 binary 启动期 phase0 校验：(a) `users.authz_epoch` 列存在；(b) `sessions.jti` 列存在；(c) `sessions.access_token` 列**不存在**（防止 partial migration）。任一缺失 → fail-fast，不启动
4. **Forced re-login（启动期）**：新 binary 在 lifecycle phase 内执行 `UPDATE sessions SET revoked_at = NOW() WHERE revoked_at IS NULL`。所有用户被强制 re-login（B 路线决议；预期一次性运维成本）

**回滚触发条件**（任一）：

- Migration 步骤失败：保留 migration（已部分应用则 SQL 自动 rollback by `BEGIN ... COMMIT;`），不替换 binary，问题排查后重新走步骤 2
- Binary phase0 fail-fast：保留旧 binary 运行（migration 已成功，老 binary 无法读 `sessions.jti` 列但 `sessions.access_token` 已删 → 服务降级；此时**不能仅 revert binary**，必须 revert migration + revert binary 双步）
- 启动后 smoke test fail / production error spike（5 分钟窗口）：执行 revert 双步（reapply 历史 schema + downgrade binary）。Forced re-login 不可逆，但用户体感等同密码改动后再登录

**窗口期校验项**（部署完成后 24h 内）：

- jti lookup 命中率：`session validate` 路径的 jti SELECT 命中率应稳定 ≥ 99%（缺失通常是过期/revoke，不是 store 错位）
- authz_epoch reject rate：role revoke 后旧 token validate reject 比例应 > 0（证明 epoch 机制有效），且与 role-change 频率成比例
- error rate baseline：`ERR_AUTH_TOKEN_INVALID` 计数有一次性激增（forced re-login 副作用），24h 后应回归基线

**与 e2e regression 的关系**：S2-S5 PR 内已落 storetest conformance（mem + PG 共享套件，Protocol-driven case 派生）。S4 PR 必须新增 e2e：覆盖 (a) fresh deployment 路径；(b) `sessions.access_token` 列存在的"未升级"DB 启动 phase0 fail-fast 验证；(c) forced re-login 后所有用户 401 → re-login 后正常的完整 user journey。

**不向后兼容**（CLAUDE.md `## 工作方式` "Review 和重构时不考虑向后兼容"）：

- S3+S5 migration 直接删除 `sessions.access_token` 列；任何依赖该列的代码必须同 PR 删除
- S4 启动期把所有 `revoked_at IS NULL` 的存量 session 标记 `revoked_at = NOW()`，强制全员 re-login（B 路线决议；预期一次性运维成本）
- 任何 fixture / e2e test 依赖 "AccessToken 字段存明文" 必须在 S3+S5 / S4 同 PR 更新，不留兼容路径

---

## 6. Alternatives Considered（汇总）

详见各 D1-D6 节内的 "Alternatives Considered" 子表。整体路线的替代方案（"library-style runtime + archtest"）已在 ADR-Typed §5.3 撤回。

---

## 7. References

### 标准与 RFC

- [RFC 9068 - JWT Profile for OAuth 2.0 Access Tokens](https://datatracker.ietf.org/doc/html/rfc9068) — §2.2.4 强制 jti claim
- [RFC 7009 - OAuth 2.0 Token Revocation](https://datatracker.ietf.org/doc/html/rfc7009) — opaque token 撤销协议（refresh chain 路径）
- [OAuth 2.0 Security Best Current Practice](https://datatracker.ietf.org/doc/html/draft-ietf-oauth-security-topics) §4.13.1 — token revocation at credential change
- [OWASP ASVS V3 - Session Management](https://github.com/OWASP/ASVS/blob/master/4.0/en/0x12-V3-Session-management.md) — fail-closed at credential change
- [OWASP Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
- NIST SP 800-63B §5.1.4.2 — credential 失效事件触发

### 开源对标

- ref: `hashicorp/vault vault/token_store.go@main` — accessor / id 分离 + revokeTree cascade
- ref: `dexidp/dex storage/storage.go@master` — protocol-storage 解耦 + ObsoleteToken rotation
- ref: `ory/kratos session/session.go@master` — Token / LogoutToken dual-token split（GoCell 不采纳 AAL 软失效路线，参见 D3）
- ref: `uber-go/fx app.go` — sealed Option（unexported apply method），已被 GoCell PR262 / runtime-api.md 采纳

### GoCell 内部前置

- `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md` §4 session protocol 原型 — 本 ADR 是其在 session 域的实例化
- `docs/architecture/202605101400-adr-admin-invariant.md` — admin 不变量配套决议（同 PR）
- `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md` §3-§7、§12 — 5 个 P0/P1 缺口分析 + 决策点
- `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` §4 S1 — PR 范围与产物形态
- `docs/plans/202605082130-pg-corecell-open-issues.md` — 关联 backlog（B2-C-02、ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01、P3-TD-10、PR280-FU1 等）
- `.claude/rules/gocell/runtime-api.md` § Option 范式分层 — wiring vs builder option 判定
- PR262 typed AuthPlan / PR-MODE-1 typed-nil reject / PR-MODE-6 error-first constructor — typed-Go-heavy 演化前例
