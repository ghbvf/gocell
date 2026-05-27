# 批次 E：认证 / 会话 / 凭证 / Refresh / 安全默认 / 角色 / UserRepo

本批次覆盖 GoCell 安全与身份语义核心。源文件均位于 `tools/archtest/` 下，主题围绕
访问控制路径（session lifecycle、凭证权限、JWT claims、bcrypt 成本）、运行时安全默认
（TLS、WebSocket、监听器鉴权链）以及 UserRepo 接口完整性。

本文件 47 条 | Hard 17 / Medium 26 / Soft 0（含 1 条 RETIRED）| 通用 4 / 半通用 21 / 专属 22

---

### 批次 1：AuthPlan / 鉴权测试包边界（auth_plan_test.go, auth_authtest_boundary_test.go, auth_keystest_boundary_test.go, no_deleted_auth_symbols_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| AUTH-PLAN-01 | Medium (推断) | AST-pattern | 禁止旧 cell.Policy 字符串字面量（jwt/mtls/service-token）在生产代码出现 | runtime/auth, kernel/cell | 半通用 |
| AUTH-PLAN-02 | Medium (推断) | AST-pattern | 禁止已删除的 bootstrap.Policy{None,JWT,…} 选择器表达式 | runtime/bootstrap | 专属 |
| AUTH-PLAN-03 | Medium (推断) | AST-pattern | 禁止已删除的 cell.Policy 类型引用或字面量 | kernel/cell | 专属 |
| AUTH-PLAN-04 | Medium (推断) | callsite-allowlist | cells/ 代码禁止直接构造 AuthPlan 结构体（组合根职责） | kernel/cell, runtime/auth | 专属 |
| AUTH-AUTHTEST-BOUNDARY-01 | Medium | import-ban | authtest 子包仅限 _test.go 引入；auth.Authenticated() 已删除 | runtime/internal/authtest, kernel/auth/authtest | 半通用 |
| AUTH-KEYSTEST-IMPORT-BOUNDARY-01 | Medium | import-ban | keystest（Must* key helpers）禁止被非测试文件引入 | runtime/auth/keystest | 半通用 |
| NO-DELETED-AUTH-SYMBOLS-01 | Medium | AST-pattern | 已删除的 auth.RoleInternalAdmin 等符号不得在白名单外引用 | runtime/auth | 专属 |

---

### 批次 2：会话生命周期（caching_session, session_protocol, session_revoked, sessionrefresh）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CACHING-SESSION-REVOKE-DELEGATE-ONLY-01 | Hard | AST-pattern | CachingSessionStore.Revoke 体必须是单句纯委托，禁止本地 cache 操作 | adapters/redis（session 相关） | 半通用 |
| SESSION-PROTOCOL-COMPOSITION-ROOT-01 | Medium | callsite-allowlist | session.NewProtocol 只能从 cmd/（组合根）或 session 包本身调用 | runtime/auth/session | 专属 |
| SESSION-REVOKED-FIELD-ACCESS-01 | Hard | callsite-allowlist | session.Session.RevokedAt 字段读取限定允许列表包 | runtime/auth/session, cells/accesscore | 专属 |
| SESSIONREFRESH-NO-SESSION-CREATE-01 | Medium | callsite-allowlist | refresh 路径禁止调用 session.Store 的 Create/Revoke/RevokeForSubject | cells/accesscore/slices/sessionrefresh, runtime/auth/session | 专属 |

---

### 批次 3：Epoch 安全约束（sessionrefresh_stale_epoch, sessionvalidate_epoch）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SESSIONREFRESH-STALE-EPOCH-REJECT-01 | Medium | AST-pattern | rejectIfStaleEpoch 必须用 `==` 比较、调用 cascadeRevoke、不得混用 reuse-attack 路径 | cells/accesscore/slices/sessionrefresh | 专属 |
| SESSIONVALIDATE-EPOCH-COMPARE-01 | Medium | AST-pattern | enforceSessionState 必须含 AuthzEpoch 比较和 GetByID 调用，且用 `!=` 运算符 | cells/accesscore/slices/sessionvalidate | 专属 |
| SESSIONVALIDATE-EPOCH-SOURCE-01 | Hard | AST-pattern | enforceSessionState 的 epoch 比较必须引用行来源字段 AuthzEpochAtIssue（而非 JWT claim） | cells/accesscore/slices/sessionvalidate | 专属 |

---

### 批次 4：凭证权限 funnel（credential_authority, credential_invalidate, domain_authz_mutation, reconstitute_user, jwt_claims）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 | Hard↓/Hard↑ | callsite-allowlist | credentialauthority.Assert 只能从限定 slice 调用；直接读 CanAuthenticate/PasswordVersion 被拦截 | cells/accesscore/slices/{sessionlogin,sessionrefresh,sessionvalidate,credentialauthority} | 专属 |
| CREDENTIAL-INVALIDATE-FUNNEL-01 | Hard | callsite-allowlist | session.Store.RevokeForSubject 只能从 credentialinvalidate funnel 内调用 | runtime/auth/session, cells/accesscore | 专属 |
| USER-AUTHZ-EPOCH-BUMP-FUNNEL-01 | Hard | callsite-allowlist | ports.UserRepository.BumpAuthzEpoch 只能从 credentialinvalidate funnel 内调用 | cells/accesscore/internal/ports | 专属 |
| REFRESH-REVOKE-USER-FUNNEL-01 | Hard | callsite-allowlist | refresh.Store.RevokeUser 只能从 credentialinvalidate funnel 内调用 | runtime/auth/refresh | 专属 |
| CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 | Medium | callsite-allowlist | Invalidator.Apply 入口调用方限定（Medium：捕获错误调用点，不能阻止遗漏调用点） | cells/accesscore/slices/credentialinvalidate | 专属 |
| DOMAIN-AUTHZ-FIELD-PRIVATE-01 | Hard | type-system-seal | domain.User authz 字段（status/passwordResetRequired/authzEpoch）unexported，跨包写编译失败 | cells/accesscore/internal/domain | 专属 |
| AUTHZ-MUTATION-APPLY-FUNNEL-01 | Hard↓/Medium↑ | callsite-allowlist | SetStatus/SetPasswordResetRequired 调用方限定到文件级别 allowlist | cells/accesscore/internal/domain | 专属 |
| RECONSTITUTE-USER-CALLER-01 | Medium | callsite-allowlist | domain.ReconstituteUser 只能被 mem/、postgres adapter、domain 包本身调用 | cells/accesscore/internal/domain | 专属 |
| JWT-CLAIMS-NO-AUTHZ-EPOCH-01 | Hard | AST-pattern | JWT Claims 结构体禁止含 AuthzEpoch 字段或 JSON tag "authz_epoch" | kernel/auth, runtime/auth/jwt | 专属 |

---

### 批次 5：Refresh 事务约束（refresh_invariants_test.go）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| REFRESH-CROSS-STORE-TX-01 | Medium | callsite-allowlist | sessionrefresh.Refresh 必须把 Peek/Get/GetByID/Rotate/RevokeSession 包在单个 RunInTx 闭包内 | cells/accesscore/slices/sessionrefresh, runtime/auth/{refresh,session} | 半通用 |
| REFRESH-INVALID-INDEX-SINGLE-SOURCE-01 | Medium (推断) | AST-pattern | DetectInvalidIndexes 函数只能在 adapters/postgres/schema_guard.go 中声明一处 | adapters/postgres | 专属 |
| REFRESH-AMBIENT-TX-01 | Medium (推断) | AST-pattern | adapters/postgres/refresh_store.go 禁止直接调用 pool.Begin（必须委托 TxRunner） | adapters/postgres | 半通用 |

---

### 批次 6：安全默认（security_defaults_test.go，SEC-FAIL-CLOSED-01..10）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| SEC-FAIL-CLOSED-01 | —（RETIRED） | AST-pattern | addr 驱动的 WithListener if-gate 已退役（SEC-02 覆盖真正 fail-open 风险） | runtime/bootstrap | 专属 |
| SEC-FAIL-CLOSED-02 | Medium (推断) | AST-pattern | package main 中 bootstrap.WithListener 第三参数禁止裸 nil | runtime/bootstrap, kernel/cell | 半通用 |
| SEC-FAIL-CLOSED-03 | Medium (推断) | AST-pattern | redis/vault/s3 adapter 必须引入并调用 secutil.ValidateTLSEndpoint | adapters/{redis,vault,s3}, pkg/secutil | 半通用 |
| SEC-FAIL-CLOSED-04 | Medium (推断) | AST-pattern | adapters/websocket 禁止赋值 opts.InsecureSkipVerify = true | adapters/websocket | 半通用 |
| SEC-FAIL-CLOSED-05 | Medium (推断) | metadata/yaml-derive | examples docker-compose 密码字段必须用 `${VAR:?required}` 环境插值 | examples/ | 半通用 |
| SEC-FAIL-CLOSED-06 | Medium (推断) | AST-pattern | InternalListener 禁止搭配裸 AuthNone 链 | runtime/bootstrap, kernel/cell | 半通用 |
| SEC-FAIL-CLOSED-07 | Medium (推断) | AST-pattern | adapters/websocket UpgradeConfig 字面量必须包含 Authenticator 字段 | adapters/websocket | 半通用 |
| SEC-FAIL-CLOSED-08 | Medium (推断) | AST-pattern | 禁止调用已删除的 runtime/websocket.Hub.Broadcast（替换为 BroadcastFilter/BroadcastToSubject） | runtime/websocket | 专属 |
| SEC-FAIL-CLOSED-09 | Medium (推断) | AST-pattern | hub.go 中 conns 删除操作必须在 removeConnLocked/shutdown 内（防 subjectIdx 不同步） | runtime/websocket | 专属 |
| SEC-FAIL-CLOSED-10 | Medium | callsite-allowlist | 引用 PrimaryListener 的 package main 必须同时引用 HealthListener | kernel/cell, runtime/bootstrap | 半通用 |

---

### 批次 7：角色 / SvcToken / Bcrypt（role_admin_literal, seed_role_iface, svctoken_caller_cell, bcrypt_cost_funnel）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| ROLE-ADMIN-LITERAL-01 | Medium (推断) | AST-pattern | 禁止重复定义值为 "admin" 的 Admin 常量（单源 runtime/auth/roles.go） | runtime/auth | 通用 |
| ROLE-ADMIN-LITERAL-02 | Medium | AST-pattern | auth.AnyRole 等调用参数禁止裸字符串 "admin"，必须用 auth.RoleAdmin 常量 | runtime/auth | 半通用 |
| SEED-ROLE-IFACE-01 | Hard | AST-pattern | 生产代码禁止命名 `*mem.RoleRepository` 类型（测试专用 seed helper 不泄漏） | cells/accesscore/internal/{mem,ports} | 专属 |
| SVCTOKEN-CALLER-CELL-REQUIRED-01 | Medium | callsite-allowlist | auth.GenerateServiceToken 第二参数必须是合法已注册的 cell ID 字面量 | runtime/auth, kernel/metadata | 专属 |
| BCRYPT-COST-FUNNEL-01 | Hard↓/Medium↑ | callsite-allowlist | bcrypt.GenerateFromPassword 仅限 credential/hasher.go；NewTestHasher 仅限测试白名单 | cells/accesscore/internal/credential | 专属 |

---

### 批次 8：UserRepo 完整性（identitymanage_last_admin, user_repo_conformance, userrepo_method_set, userrepo_nonempty_cast）

| INVARIANT ID | 评级 | 机制类型 | 用途（≤30字中文） | 依赖 gocell 包 | 可移植性 |
|---|---|---|---|---|---|
| IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01 | Medium | callsite-allowlist | identitymanage.NewService 每个调用处必须在同文件内包含 WithLastAdminProtection 选项 | cells/accesscore/slices/identitymanage | 专属 |
| USERREPO-CONFORMANCE-ENROLLMENT-01 | Medium | conformance-test | 每个实现 ports.UserRepository 的具体类型必须在其包的测试文件中调用合规测试 | cells/accesscore/internal/ports/conformance | 半通用 |
| USERREPO-METHOD-SET-FROZEN-01 | Medium | reflect-field-freeze | ports.UserRepository 方法集合与每方法签名精确锁定，防止回退宽泛 Update 形态 | cells/accesscore/internal/ports | 半通用 |
| USERREPO-NONEMPTY-CAST-FUNNEL-01 | Medium↓/Medium↑ | callsite-allowlist | domain.NonEmpty(_) 显式类型转换只允许 allowlist 文件（防绕过 NewNonEmpty 验证 funnel） | cells/accesscore/internal/domain | 专属 |
