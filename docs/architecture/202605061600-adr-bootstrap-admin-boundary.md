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

### D1 — 删除匿名 public 暴露

`contracts/http/auth/setup/admin/v1/contract.yaml` 的 `auth.public:true` 替换为 `auth.bootstrap:true`。

`kernel/metadata/HTTPAuthMeta` 新增 `Bootstrap bool` 字段。`kernel/metadata/schemas/contract.schema.json` 在 `auth` 对象新增 `bootstrap` 字段，并以 `allOf` 三方两两互斥约束 `public` / `bootstrap` / `passwordResetExempt`。

`runtime/auth.Route` 新增 `Bootstrap bool` 字段。`validateBypassCompatibility` 函数强制三方互斥（与 FMT-27 治理规则对应）。

### D2 — Bootstrap credential via env

启动期凭据通过两个环境变量注入：

- `GOCELL_BOOTSTRAP_ADMIN_USERNAME`：bootstrap 或 interactive 模式下 HTTP Basic Auth 的运维身份
- `GOCELL_BOOTSTRAP_ADMIN_PASSWORD`：≥8 byte，TrimSpace 处理 K8s secret 末尾换行，含控制字符则 fail-fast

`cmd/corebundle/access_module.go` 的 `loadBootstrapCredentials` 函数在启动时校验两个变量：TrimSpace + 控制字符检测 + 最小长度，任一不满足则启动 fail-fast。

admin 已存在且 env 仍设时，打印 `slog.Warn`（提示运维删除 env，一次性使用原则），不阻止启动。

### D3 — 删除 credfile 路径

`cells/accesscore/initialadmin` 下的随机密码生成 + 文件写入 + cleaner worker 整体删除，共 39 文件（sweep / cleaner / credfile_io / generator / sweep / scheduler 等），减少约 1500 LOC。

只保留 `WithBootstrapCredentials` env-driven 路径。与 K8s Secret / Vault external secrets / HashiCorp Vault transit 的集成由 Pod 注入 env 的运维栈负责，GoCell 不内建凭据传递机制。

### D4 — 两模式语义

**bootstrap 模式**（`GOCELL_SETUP_MODE` 未设或为 `bootstrap`）：

accesscore lifecycle 在启动时自动检测 admin role 是否有 user。若无，用 `GOCELL_BOOTSTRAP_ADMIN_USERNAME` / `GOCELL_BOOTSTRAP_ADMIN_PASSWORD` 作为 body 凭据创建初始 admin。lifecycle 完成后 setup/admin endpoint 返回 410 Gone，operationally inactive。

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
| Keycloak `KC_BOOTSTRAP_ADMIN_USERNAME` | 一次性 env bootstrap，admin 已存在后 env 提示删除 |
| MinIO `internal/auth/credentials.go` | 启动期凭据长度 fail-fast |
| Grafana `pkg/setting/setting.go` | env override 模式 |
| Kubernetes `hack/verify-*.sh` | 独立 Prow presubmit，不依赖主流水线 |
| Go stdlib `crypto/subtle.ConstantTimeCompare` | HTTP Basic Auth 时间安全比较，防时序泄漏 |
