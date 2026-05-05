# ADR — Bootstrap Admin Security Boundary

- **Date**: 2026-05-06（Supersedes 2026-05-06 v1）
- **Revised**: 2026-05-06 16:00 (postmortem-driven from v1 00:30)
- **Status**: Accepted (revision per postmortem 202605060030)
- **Closes**: B2-C-02 (P0) SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT
- **Roadmap**: PR-V1-SEC-SETUP-CLOSURE
- **Supersedes**: 2026-05-06 v1（含 bootstrap admin provision mode；postmortem 触发的设计回归）
- **Postmortem**: [docs/reviews/202605060030-392-bootstrap-mode-design-postmortem.md](../reviews/202605060030-392-bootstrap-mode-design-postmortem.md)

## Context

`POST /api/v1/access/setup/admin` 在 first-run 窗口内以 `auth.public:true` 暴露于公网，攻击者可在运维操作前抢注 admin。此外，旧 credfile 路径（39 文件随机密码生成 + cleaner worker）容器化下传递不安全且运维复杂。

设计选择：删除匿名暴露，改用 operator Basic Auth 保护持久 endpoint。运维通过 HTTP 引导式发起 admin 注册（`POST /api/v1/access/setup/admin`），DB 唯一约束保证 multi-pod 幂等。

## Decision

### D1 — 删除匿名 public 暴露 + 闭合契约

`contracts/http/auth/setup/admin/v1/contract.yaml` 的 `auth.public:true` 替换为 `auth.bootstrap:true`。

`kernel/metadata/HTTPAuthMeta` 新增 `Bootstrap bool` 字段。`kernel/metadata/schemas/contract.schema.json` 在 `auth` 对象新增 `bootstrap` 字段，并以 `allOf` 三方两两互斥约束 `public` / `bootstrap` / `passwordResetExempt`。

`runtime/auth.Route` 不再包含 `Bootstrap bool` flag，改为单一字段 `BootstrapAuth func(http.Handler) http.Handler`。BootstrapAuth 非 nil 唯一表达 bootstrap 路由：listener JWT bypass + 装载该 middleware 替代认证。`validateBypassCompatibility` 在 BootstrapAuth 非 nil / Public / PasswordResetExempt 之间做三方互斥（与 FMT-27 治理规则对应）。

codegen 对 `auth.bootstrap: true` 的 contract 生成的 `NewHandler` 必须把 bootstrapAuth 作为必填首参；构造期 nil 校验 panic。这把「声明受保护」与「实际受保护」收敛到同一个不变量：contract 元数据 → codegen 输出 → composition root 注入函数 → Mount 装载 middleware，没有可选 wiring 路径。

archtest 守卫：CELLS-NO-ROUTEMUX-WRAPPER-01 / AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01 / SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01。

CI verify-codegen 三步（`go generate` + `git diff --quiet` + codegen drift check）从 build-test matrix 拆出为独立 job（无 `needs` 依赖），build-test 失败不再掩盖 codegen drift。`tools/archtest/TestVerifyCodegenJobIsIndependent` 静态守卫 job 拓扑不回退。

### D2 — Operator Basic Auth credential via env

启动期凭据通过两个环境变量注入：

- `GOCELL_BOOTSTRAP_ADMIN_USERNAME`：HTTP Basic Auth 的运维身份（authenticator）
- `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`：≥8 byte，TrimSpace 处理 K8s secret 末尾换行，含控制字符则 fail-fast

`cmd/corebundle/access_module.go::loadBootstrapCredentials` 启动时校验两个变量：TrimSpace + 控制字符检测 + 最小长度，任一不满足则启动 fail-fast。

**env 凭据是持久 operator authenticator，不是业务 admin password 来源**——不允许物化到 user table。轮换走滚动替换 K8s Secret + restart。

ref: nginx Basic Auth standard (RFC 7617) — 持久部署凭据的标准模式；Keycloak `KC_BOOTSTRAP_ADMIN_USERNAME` / `ApplianceBootstrap` 对比参考（一次性 seed + 独立恢复 CLI 的反例路径）。

### D3 — 删除 credfile 路径 + 删除 bootstrap admin provision mode

`cells/accesscore/initialadmin/` 整包（12 .go 文件，~843 LOC）+ `cell_initialadmin_test.go` + `cell_initialadmin_unsupported_test.go` 删除。

`cmd/corebundle/access_module.go` 删除 `SetupModeEnv` (`GOCELL_SETUP_MODE`) + `adminProvisionMode` 类型 + `resolveAdminProvisionMode` + `isMultiPod` + `GOCELL_REPLICA_COUNT`。

setup endpoint 单一路径：运维通过 `POST /api/v1/access/setup/admin` 手工发起请求，HTTP Basic Auth 保护：

```
Authorization: Basic base64(<GOCELL_BOOTSTRAP_ADMIN_USERNAME>:<GOCELL_BOOTSTRAP_ADMIN_PASSWORD>)
```

请求 body（`username` / `email` / `password`）决定真正 admin user，env 凭据只是 authenticator。

### D4 — Plane 分离 + governance 单一谓词

env = operator identity (authenticator)；body = admin identity (subject)。

- **FMT-27**：`auth.public` / `auth.bootstrap` / `auth.passwordResetExempt` 三方两两互斥
- **FMT-28**：`auth.bootstrap:true` 仅允许在路径匹配 **`metadata.IsBootstrapPath`**（精确 segment 匹配 `^/api/v\d+/[^/]+/setup/admin$`）的 contract 上声明
- 单一谓词锁定：`tools/archtest/bootstrap_path_predicate_test.go` (`BOOTSTRAP-PATH-PREDICATE-SOLE-01`) 静态扫描禁止任何位置出现 `strings.Contains(.+, "setup/admin")`，避免规则在多处近似复制
- contract.schema.json `auth.responses []int` 字段 + CH-04 双源校验：`declared = responses ∪ auth.responses`，handler AST 不需发出 listener middleware 注入的码（401/429）
- archtest SETUP-ADMIN-NOT-PUBLIC-01 / AUTH-BOOTSTRAP-PATH-RESTRICTED-01

### D5 — Setup endpoint 幂等性

**当前状态（in-memory mode）**：accesscore 仅注册 `mem.UserRepository`；多 pod 部署不属当前支持范围（无共享存储）。单进程幂等由 `cells/accesscore/internal/adminprovision/Provisioner` 的 `sync.Mutex` + admin role 是否已存在的检查保证。

**目标状态（PG adapter 落地后）**：accesscore PG repository + migration 引入 `UNIQUE (role=admin)` 约束，multi-pod 同时收到 setup POST 时第一个 INSERT 胜出，后续 409；admin 已存在时返回 410 Gone（保持 endpoint 受 Basic Auth 保护，避免 410 oracle）。该升级由 backlog `ACCESSCORE-PG-USERS-MIGRATION-01` 跟踪，非本 PR 范围。

不引入拓扑约束：删除原方案中 `GOCELL_REPLICA_COUNT` + `isMultiPod` 检查（拓扑是 hint 不是约束——postmortem P1#4 的修正）。

brute-force 防护：per-IP token-bucket limiter（5 req/min sustained, burst 10，对照 nginx `limit_req`）+ slog `onAuthFail` observer。

> setup HTTP 同步路径不再产生 pending row；crash 后用户重试得 409 + 运维 SQL 清理（不走自动 orphan recovery）。多 pod 跨进程幂等的最终承诺由 PG users(role=admin) UNIQUE 约束兑现（见 backlog ACCESSCORE-PG-USERS-MIGRATION-01）。

## 替代方案

**A — 保留 bootstrap admin provision mode（headless 路径）**：通过 credfile 物化一次性密码，容器化下传递不安全，cleaner worker 路径复杂度高。Postmortem 分析：crash 后 pending row 形成 orphan，自动 orphan recovery 需要维护 ProvisionState 状态机，复杂度超过收益。已废弃。

**B — 单独 CLI seed 命令**：适合 Keycloak / Vault 模式，但要求 operator 有容器 exec 或 job 权限，与 GoCell 「HTTP-first, zero-sidecar」设计矛盾。已排除。

**C — 放弃幂等，不做 409 处理**：username 冲突直接 500，依赖运维重命名。运维 UX 不可接受，对 multi-pod race 无守护。已排除。

## Consequences

- 单一引导式 admin 注册路径，运维每次部署后多发一次 curl POST
- bootstrap admin provision mode 概念删除，模式数 2→1，ADR D1–D10 → D1–D5
- D4 plane 分离唯一路径成立
- 测试矩阵单一路径完整覆盖

## Out of Scope / backlog 登记

- **BOOTSTRAP-AUDIT-CHAIN-WIRING-01**：`access_module.go` 当前传 `nil` OnAuthFail hook；触发条件：accesscore audit chain 跨 cell 注入路径打通后跟进；hook 接口已就位，扩展零成本
- **BOOTSTRAP-RATELIMIT-DISTRIBUTED-01**：per-replica in-memory bucket；multi-pod 拓扑约束已删除，分布式 rate limiter 仍是合理后续
- K8s etcd 加密 / Secret RBAC 最小化（运维责任域，不涉及 GoCell 代码）

## References

- [postmortem 202605060030](../reviews/202605060030-392-bootstrap-mode-design-postmortem.md)
- PR #392 PR-V1-SEC-SETUP-CLOSURE-V2
- nginx Basic Auth / RFC 7617: https://datatracker.ietf.org/doc/html/rfc7617
- Go stdlib `crypto/subtle.ConstantTimeCompare` — time-safe HTTP Basic Auth comparison
- Kratos `transport/http/server.go` + `middleware/selector/selector.go` — bypass 与替代认证由同一 middleware 抽象决策
