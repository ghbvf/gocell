# ADR: Credential / Session 协议（typed Protocol primitive 决议）

**Date**: 2026-05-10
**Status**: Proposed
**Related plan**: `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` S1
**Related ADRs**:

- `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`（typed-Go-heavy 范式锚点）
- `docs/architecture/202605101400-adr-admin-invariant.md`（admin 不变量；同 PR）

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

JWT validate 路径：

```
parse JWT → verify signature
SELECT id, revoked_at, authz_epoch_at_issue FROM sessions WHERE jti = $1
if !found OR revoked_at IS NOT NULL OR expires_at < now → reject
SELECT authz_epoch FROM users WHERE id = $1
if claim.epoch < user.authz_epoch → reject (D2 stale token)
```

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

login 时：

```
BEGIN tx
  SELECT authz_epoch FROM users WHERE id = $1
  INSERT INTO sessions (..., jti=$jti, authz_epoch_at_issue=<read>) ...
  -- JWT payload:  { sub, jti, epoch:<read>, exp, ... }
COMMIT
```

role revoke 时（同 tx 完成 D5）：

```
BEGIN tx
  DELETE FROM role_assignments WHERE ...
  UPDATE users SET authz_epoch = authz_epoch + 1 WHERE id = $1
  UPDATE sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL
  INSERT INTO outbox (event=role.revoked, ...)
COMMIT
```

validate 路径已在 D1 描述（`if claim.epoch < user.authz_epoch → reject`）。**效果**：旧 token 含 `claim.epoch=5`，role revoke 后 `user.authz_epoch=6`，下一次 validate 立即拒。无需 sweep 已发 token——它们自动失效。

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

**AuthzEpoch / role snapshot 推进路径**：refresh 时通过 `users.authz_epoch` +
`sessions.authz_epoch_at_issue` 比对实现 fail-closed（D2，由 S4b 闭环），不通过
session UUID 轮换。访问令牌 claims 在 refresh 时按 user state 重新签发（password
reset flag / role membership），但不写回 session 行。

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

| 威胁场景 | jti-only | AuthzEpoch | Fail-closed events | 同 tx |
|---|---|---|---|---|
| DB 泄露 → token 直接重放 | ✅ 不可重放（DB 无 token 明文也无 HMAC） | — | — | — |
| Role downgrade 后旧 token 仍持高权 | — | ✅ epoch bump → 旧 token 自动 reject | ✅ session 同 tx 撤销 | ✅ 失效原子 |
| Device theft（设备被偷） → user lock | ✅ session lookup 拒 | — | ✅ Lock event 触发全撤 | ✅ 失效原子 |
| Password reset → 旧 access/refresh 仍可用 | — | — | ✅ PasswordReset event 触发全撤 | ✅ 失效原子 |
| Account delete → 残留 session 攻击面 | — | — | ✅ Delete event 触发全撤 | ✅ 失效原子 |
| 并发 login 与 role revoke | — | ✅ epoch 让顺序变成"已签发 vs current"判断 | ✅ session 同 tx 撤销 | ✅ 失效原子 |
| JWT 签名密钥泄露 | ❌（jti-only 不解此场景） | — | — | — |
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
