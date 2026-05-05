# ADR — Bootstrap Admin Security Boundary

- **Date**: 2026-05-06
- **Status**: Accepted
- **Closes**: B2-C-02 (P0) SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT
- **Roadmap**: PR-V1-SEC-SETUP-CLOSURE (Batch 0-3)

## Context

`POST /api/v1/access/setup/admin` 在 first-run 窗口内以 `auth.public:true` 暴露于公网。任何匿名请求者均可在 admin 未初始化时抢注第一个 admin 用户——即使 multi-pod 场景已通过 `adminprovision` 分布式锁 + 410 Gone 限制重复创建，"默认不安全"的设计仍是产品级风险：

1. 未初始化部署暴露于公网时，攻击者可在运维操作前抢先注册 admin。
2. 旧 bootstrap 模式（随机密码 + credfile）需要 39 个文件支撑 sweep/cleaner/credfile_io/generator/scheduler，运维复杂度高且不与 K8s Secret / Vault 集成。
3. credfile 路径无法在容器化环境中安全传递（emptyDir 重启即失，bind-mount 增加主机攻击面）。

B2-C-02 及多份归档审查一致确认根因：`auth.public:true` 是唯一允许匿名访问的机制，且 setup/admin endpoint 的生命周期与该属性绑定。

## Decision

### D1 — 删除匿名 public 暴露 + 闭合契约

`contracts/http/auth/setup/admin/v1/contract.yaml` 的 `auth.public:true` 替换为 `auth.bootstrap:true`。

`kernel/metadata/HTTPAuthMeta` 新增 `Bootstrap bool` 字段。`kernel/metadata/schemas/contract.schema.json` 在 `auth` 对象新增 `bootstrap` 字段，并以 `allOf` 三方两两互斥约束 `public` / `bootstrap` / `passwordResetExempt`。

`runtime/auth.Route` **不再**包含 `Bootstrap bool` flag；改为单一字段 `BootstrapAuth func(http.Handler) http.Handler`。BootstrapAuth 非 nil 唯一表达 bootstrap 路由：listener JWT bypass + 装载该 middleware 替代认证。`validateBypassCompatibility` 在 BootstrapAuth 非 nil / Public / PasswordResetExempt 之间做三方互斥（与 FMT-27 治理规则对应）。

codegen 对 `auth.bootstrap: true` 的 contract 生成的 `NewHandler` 必须把 bootstrapAuth 作为必填首参；构造期 nil 校验 panic。这把「声明受保护」与「实际受保护」收敛到同一个不变量：contract 元数据 → codegen 输出 → composition root 注入函数 → Mount 装载 middleware，没有可选 wiring 路径。

### D2 — Bootstrap credential via env（持久 startup credential 模型）

启动期凭据通过两个环境变量注入：

- `GOCELL_BOOTSTRAP_ADMIN_USERNAME`：bootstrap 或 interactive 模式下 HTTP Basic Auth 的运维身份
- `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`：≥8 byte，TrimSpace 处理 K8s secret 末尾换行，含控制字符则 fail-fast

`cmd/corebundle/access_module.go` 的 `loadBootstrapCredentials` 函数在启动时校验两个变量：TrimSpace + 控制字符检测 + 最小长度，任一不满足则启动 fail-fast。

**生命周期模型：持久 startup credential**。bootstrap 凭据是 setup endpoint 的常驻保护，不是「一次性 seed」——admin 已存在后 env 仍是必填，不允许在运行期清理；轮换走「滚动替换 K8s Secret + restart」。详见 §D9。

### D3 — 删除 credfile 路径

`cells/accesscore/initialadmin` 下的随机密码生成 + 文件写入 + cleaner worker 整体删除，共 39 文件（sweep / cleaner / credfile_io / generator / sweep / scheduler 等），减少约 1500 LOC。

`initialadmin.NewLifecycle` 把 `BootstrapCredentials` 提升为必填首参（而非 functional option），未注入则编译期不通过。与 K8s Secret / Vault external secrets / HashiCorp Vault transit 的集成由 Pod 注入 env 的运维栈负责，GoCell 不内建凭据传递机制。

### D4 — 两模式语义

`GOCELL_SETUP_MODE` 必填，**无默认值**；空值在启动期 fail-fast。两个合法值：`bootstrap` / `interactive`。

**bootstrap 模式**（`GOCELL_SETUP_MODE=bootstrap`）：

accesscore lifecycle 在启动时自动检测 admin role 是否有 user。若无，用 `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 作为 body 凭据创建初始 admin。lifecycle 完成后 setup/admin endpoint 返回 410 Gone，operationally inactive，但 endpoint 仍受 bootstrap Basic Auth 保护（未通过 Basic Auth 仍 401，避免 410 oracle）。

**interactive 模式**（`GOCELL_SETUP_MODE=interactive`）：

运维通过 `POST /api/v1/access/setup/admin` 手工发起请求，但 endpoint 以 HTTP Basic Auth 保护：`Authorization: Basic base64(<GOCELL_BOOTSTRAP_ADMIN_USERNAME>:<GOCELL_BOOTSTRAP_ADMIN_PASSWORD>)`。

请求的 body（`username` / `email` / `password`）决定真正 admin user（**运维身份与业务身份解耦**），对标 Keycloak temp-admin 退化版——env 凭据是操作员身份（认证谁可以触发这次操作），body 是业务身份（创建哪个 admin 账号）。

endpoint 不再 anonymously accessible，brute-force 防护由 per-IP token-bucket limiter + 401 oracle-safe envelope（`ERR_AUTH_BOOTSTRAP_FAILED`） + audit hook（接口已就位）提供。

### D5 — 三方 governance 规则

- **FMT-27**：`auth.public` / `auth.bootstrap` / `auth.passwordResetExempt` 三方两两互斥。contract.schema.json `allOf` + `runtime/auth` `validateBypassCompatibility` 双重守护。
- **FMT-28**：`auth.bootstrap:true` 仅允许在路径匹配 `/api/v1/*/setup/admin` 的 contract 上声明，禁止业务路由复用该标记。
- **archtest SETUP-ADMIN-NOT-PUBLIC-01**：静态断言 setup/admin contract 中 `auth.public` 必须为 false（或不存在）。
- **archtest AUTH-BOOTSTRAP-PATH-RESTRICTED-01**：静态断言所有含 `auth.bootstrap:true` 的 contract 路径符合 setup/admin 限定规则（FMT-28）。

### D6 — multi-pod interactive 拒绝

`GOCELL_REPLICA_COUNT > 1` 时，interactive 模式启动 fail-fast（不依赖分布式锁）。运维显式声明 replica count 优于从 pod 拓扑推断——false-positive 代价（运维忘设 env 但实际只有 1 pod）是 fail-fast 拒绝启动，运维修正后重启即可，不引入竞态。

### D7 — Body vs env 凭证关系

env（`GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`）是 HTTP Basic Auth 凭据，即操作员身份——验证"谁有权限触发这次 setup 请求"。body 中的 `username` / `email` / `password` 是要创建的 admin user 的业务凭据，即运维身份。

两层身份解耦对标 Keycloak `KC_BOOTSTRAP_ADMIN_USERNAME` temp-admin 模式：启动期 env 创建一个临时操作员权限，业务 admin 由操作员通过 API 正式注册。

### D8 — CI verify-codegen 独立 job

`.github/workflows/_build-lint.yml` 的 verify-codegen 三步（`go generate` + `git diff --quiet` + codegen drift check）从 build-test matrix 拆出为独立 job（无 `needs` 依赖）。

build-test 失败（如单测红）不再掩盖 codegen drift，两条流水线可并行发现问题。`tools/archtest/TestVerifyCodegenJobIsIndependent` 静态守卫 job 拓扑不回退。

### D9 — Credential lifetime model

GoCell 选择「持久 startup credential」模型，不是「一次性 seed + 后续删除」。三条理由：

1. **与 D1 闭合契约一致**：D1 让 setup/admin endpoint 永久受 bootstrap Basic Auth 保护（即便 admin 已创建后 endpoint 返回 410，401 也比 410 优先短路）。每次启动都需 creds 来注入 BootstrapAuth middleware；creds 不能在运行期消失。
2. **简化状态机**：不需要「系统是否已初始化」的运行时状态决定要不要还求 env；启动逻辑无分支。
3. **与 MinIO/Vault 同类**：MinIO root credentials、HashiCorp Vault unseal keys 都是「持续启动期身份」；GoCell 的 bootstrap creds 落入同类。

对照另一类「一次性 seed + 独立 reset CLI」模型（Keycloak / Grafana / Elasticsearch）：恢复路径走专门 CLI（`bin/kc.sh bootstrap-admin`、`elasticsearch-reset-password`），main entry point 不再消费 seed env。GoCell 选择前者是对齐 D1 的副产物，不是否定后者的合理性。

**轮换流程**：

```bash
# 1. 滚动替换 K8s Secret
kubectl create secret generic gocell-bootstrap \
  --from-literal=username=ops --from-literal=password=NewOpsPass456! \
  --dry-run=client -o yaml | kubectl apply -f -

# 2. 滚动重启
kubectl rollout restart deployment/gocell

# 3. 验证
kubectl rollout status deployment/gocell
```

**禁止行为**：admin 创建后删除 env / Secret；这等于关闭了 setup/admin endpoint 的常驻保护层（虽然该 endpoint 此时返回 410，但攻击者仍可通过 401 vs 410 探测系统是否已初始化）。

### D10 — Backwards-incompatible changes（PR #392）

本 PR 引入两处不向后兼容变更，运维侧需在升级前同步配置：

| # | 旧行为 | 新行为 | 落点 | 迁移指引 |
|---|--------|--------|------|---------|
| C1 | `setup/admin` 是匿名 public 入口（`auth.public: true`） | bootstrap 入口（`auth.bootstrap: true`），HTTP Basic Auth 必填 | `contracts/http/auth/setup/admin/v1/contract.yaml`、`generated/contracts/http/auth/setup/admin/v1/handler_gen.go` | 客户端在 `POST /api/v1/access/setup/admin` 必须携带 `Authorization: Basic base64(<GOCELL_BOOTSTRAP_ADMIN_USERNAME>:<GOCELL_BOOTSTRAP_ADMIN_PASSWORD>)`；旧 anonymous 调用方一次性更新 |
| C2 | `GOCELL_SETUP_MODE` 空值默认 `bootstrap` | 必填，空值 fail-fast | `cmd/corebundle/access_module.go` 的 `resolveAdminProvisionMode` | 部署清单显式注 `GOCELL_SETUP_MODE=bootstrap` 或 `interactive`；不允许依赖默认值 |

PR 同时删除以下 wiring 表面（zero-consumer，无需运维迁移）：

- `cells/accesscore/slices/setup` 的 `WithAdminMiddleware` option 与 `middlewareRouteMux` 类型
- `cells/accesscore/initialadmin` 的 `WithBootstrapCredentials` functional option（提升为 `NewLifecycle(creds, opts...)` 必填首参）
- `runtime/auth.BootstrapAllowAllLimiter` 类型与 `bootstrapRateLimiter` type alias
- `runtime/auth.Route.Bootstrap bool` 字段（合并到 `BootstrapAuth` 单一字段）
- `examples/ssobff/app.go` 的 `accesscore.WithInitialAdminBootstrap()` 调用（示例改为 interactive 演示流）

## Consequences

**正面**：

- bootstrap 模式下不再需要凭据文件、TTL cleanup（39 文件删除，~1500 LOC 减少）
- interactive 模式下 endpoint 不再 anonymously accessible，brute-force 防护由 token-bucket limiter + oracle-safe envelope + audit hook 提供
- env 注入与 K8s secret / Vault external secrets / HashiCorp Vault transit 运维栈天然集成
- CI build-test 红色不再掩盖 codegen drift

**已知风险**：

- `GOCELL_REPLICA_COUNT` false-positive：运维忘设此 env 但实际只有 1 pod，导致 interactive 模式 fail-fast 拒绝启动。代价：运维需显式声明 replica count，重启即可恢复。这是 fail-closed 的接受代价。
- env 凭据生命周期管理属于运维栈责任域；K8s Secret 未加密（etcd 明文）场景下仍有泄露风险，但该风险由 K8s etcd 加密 + Secret RBAC 最小化承担（out of scope）。

## Out of Scope / backlog 登记

- **BOOTSTRAP-AUDIT-CHAIN-WIRING-01**（新增）：`access_module.go` 当前传 `nil` OnAuthFail hook；触发条件：accesscore audit chain 跨 cell 注入路径打通后跟进；hook 接口已就位，扩展零成本。
- K8s etcd 加密 / Secret RBAC 最小化（运维责任域，不涉及 GoCell 代码）
- multi-region bootstrap 协同（多集群部署需求）

## 对标参考

| 参考 | 对标点 |
|------|--------|
| MinIO `cmd/common-main.go` / `internal/auth/credentials.go` | 持久 startup credential：root creds 每次启动重新解析，缺失即拒绝启动；启动期凭据长度 fail-fast |
| HashiCorp Vault unseal keys、`vault/seal/*` | 持久 startup credential：unseal keys 每次启动都需要 |
| Keycloak `KC_BOOTSTRAP_ADMIN_USERNAME` / `ApplianceBootstrap` | 反例参照：一次性 seed + 独立 `bin/kc.sh bootstrap-admin` 恢复 CLI；GoCell 选择持久模型而非一次性 seed |
| Grafana `defaults.ini` / `pkg/setting/setting.go` | 反例参照：admin_user/admin_password 一次性 seed |
| Kratos `transport/http/server.go` + `middleware/selector/selector.go` | bypass 与替代认证由同一 middleware 抽象决策；不靠外部 wiring 补保护 |
| go-zero `rest/server.go` + `handler/authhandler.go` | 鉴权是 route group 注册属性，bindRoute 时自动注入 Authorize |
| HashiCorp Vault `vault/router.go` LoginPaths | bypass 与替代认证在同一执行点决策；无「先 bypass、后注入认证」的中间状态 |
| Kubernetes `hack/verify-*.sh` | 独立 Prow presubmit，不依赖主流水线 |
| Go stdlib `crypto/subtle.ConstantTimeCompare` | HTTP Basic Auth 时间安全比较，防时序泄漏 |
