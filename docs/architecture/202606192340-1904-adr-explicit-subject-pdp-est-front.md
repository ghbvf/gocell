# ADR: explicit-subject PDP 授权路径 + 设备身份 EST 签发前端（PR-8b）

- **Status**: Accepted
- **Date**: 2026-06-19
- **Issue**: #1904（Epic #1895 PR-8b）
- **关联**: #1901（certsigning seam）/ #1902（softca）/ #2037（framework serving harness, PR-8a）/
  #1898（设备主体签发器）/ ADR `202606121400-1348-adr-pr10a-authz-wiring.md`（PR-10a authz 接线）/
  ADR `202606130635-1939-adr-framework-owned-contract.md`（框架归属契约）

## Context

Epic #1895 把设备证书/PKI 转为**框架能力**。PR-8b 交付 EST(RFC 7030) 风格的**同步签发前端**
（设备开箱 enroll/renew/取信任根），并把「Authorize 复用既有 PDP」做成**可被任意 cell（含外部）复用**
的干净桥。

阻抗问题：当前 `auth.Authorizer.Authorize(ctx, subject, resource, action)` 的 subject 属性**只从 ctx
`*Principal` 解析**，无 principal 即 fail-closed（`authorizationdecide/service.go` + `attributes.go`）。
这阻断了 cert-authz（只有 enrollment claim / 设备身份、无 HTTP principal）和 certlifecycle（system 身份、
背景 reconcile 无 ctx principal）复用同一 PDP。两条不可接受的旁路：(a) 各调用方各自伪造 `*Principal`
注入 ctx；(b) cert 路径自带一套平行授权逻辑，与 PDP policy 漂移。

开源共识：授权 API 接受**显式 subject 描述符、与 ambient caller 解耦**：

- **Cedar** `Request::new(principal: EntityUid, action, resource, context)` —— principal 是显式值，无 session。
- **kubernetes** `SubjectAccessReviewSpec`（User/Groups/UID/Extra 显式）+ `Authorizer.Authorize(ctx, Attributes)`。
- **cert-manager** webhook 注入 immutable `Username/Groups`，审批按 spec 字段决策。
- gocell 内部先例：`certsigning.Authorizer.AuthorizeEnroll(ctx, EnrollmentClaim)`（sealed 显式 subject）。

「subject 仅从 ctx principal 取」是 gocell 当前异常，非行业做法。

## Decision

### D1 — sealed `auth.SubjectDescriptor`（Hard：privileged 不可铸）

显式 subject 描述符（≈ k8s `user.Info` + Cedar `EntityUid`）：unexported 字段（kind/sub/tenant），
构造器注入式。**无 admin/super-admin 构造器**；privileged subject 经此路径**编译不出**（sealed
construction 范本）。本 PR 仅交付 `NewDeviceSubjectDescriptor(tenant, deviceID)`（kind=device，
fail-closed 校验 canonical tenant + 非空 sub）；`NewSystemSubjectDescriptor`（certlifecycle/background
reconcile 用）延至 PR-7 #1903，届时遵循同一 Hard 约束（无 admin/super-admin 构造器）。

### D2 — segregated `auth.SubjectAuthorizer.AuthorizeAs`（ISP）

`AuthorizeAs(ctx, subj SubjectDescriptor, resource, action)` 命名对标 k8s `Authorize(ctx, Attributes)` /
Cedar `is_authorized(Request)`。**独立于** `Authorizer` 接口（ISP：`Authorizer` 有 ~10 实现者，扩接口会
波及全部；只有 ABAC 引擎 `authorizationdecide.Service` 实现 `SubjectAuthorizer`）。tenant scope 从
**descriptor.tenant** 取（非 ctx，背景调用无 ctx tenant/principal）；fail-closed 语义不变。`Authorize` 与
`AuthorizeAs` 共用 `loadEvalInputs` + evaluate，subject 来源不同（principal-backed vs descriptor-backed
resolver），评估核一致。

### D3 — `AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01`（Medium，caller-allowlist）

`AuthorizeAs` 是显式-subject 旁路点：业务 HTTP handler 若可调，即可铸设备 subject 绕过
authenticated-principal 门禁。type-aware（`info.Uses` 解析到 `SubjectAuthorizer.AuthorizeAs` 接口方法，
import/调用-vs-取值形态无关）caller-allowlist 把生产调用方锁死为：

- `runtime/certsigning/pdpauthz`（PDP-backed `certsigning.Authorizer` adapter，唯一业务消费者）；
- `runtime/bootstrap`（lazyAuthorizer 委托：实现 `SubjectAuthorizer` 转发给 resolved PDP，framework 延迟
  解析 plumbing，转发的值由 adapter 构造、此处不铸 subject）。

业务代码必须走 principal-based `Authorize` / `RequirePermission`。anti-vacuity（allowlist 条目须 live）+
RED fixture（`internal/authorizeascallerfixture`，非授权调用必被抓）。评级 Medium：`AuthorizeAs` 是 exported
接口方法，Go 编译期不可阻断 → archtest-bound（与 `AUTHZ-DECISION-ALLOW-DENY-CALLER-01` 同 Go 天花板）。

### D4 — PDP-backed `certsigning.Authorizer` adapter（复用桥）

`runtime/certsigning/pdpauthz`：`AuthorizeEnroll(ctx, claim)` → `NewDeviceSubjectDescriptor(claim.tenant,
claim.device)` → `SubjectAuthorizer.AuthorizeAs(ctx, desc, resource=deviceID, action=device:enroll)` →
Allow→`SignConstraints(maxTTL, device-self SAN)`；deny/error/非零 obligation → 非 granted（fail-closed）。
这是 **EST + certlifecycle + 外部 cell 共用的复用桥**：阻抗（PDP 要 principal / cert 只有 claim）在
framework 解一次，不各自伪造。

### D5 — composition root lazy 桥接（Hard 下游 / Medium 上游）

corebundle 经 `bootstrap.AuthorizerFromCells(cells)` 取 lazy `auth.Authorizer`（duck-type
`authorizerProvider`，不 import corecells/）。lazyAuthorizer **同时实现** `SubjectAuthorizer`——`AuthorizeAs`
委托 resolved PDP（type-assert 到 `SubjectAuthorizer`，否则 fail-closed KindUnavailable）。**单一** lazy 实例
解析一次 PDP，同时服务 HTTP（Authorize）/ gRPC（Authorize）/ cert 路径（AuthorizeAs）。run.go 把
`authorizer.(auth.SubjectAuthorizer)` 注入 pdpauthz（断言失败 fail-fast）。

### D6 — EST 前端鉴权三态

- **enroll = enrollment-credential（应用层）**：route `auth.public:true`（跳 listener JWT）+ 包裹 middleware
  提取 `Authorization: Bearer` → `EnrollmentCredentialVerifier.Verify`（`TokenIntentEnrollment`，拒 access-intent
  token，token-confusion 防御）→ 身份入 ctx。**禁 setup-bootstrap 复用**（FMT-28 限 `auth.bootstrap` 于
  `/setup/admin`）。残差守卫 `EST-ENROLL-AUTH-BOUNDARY-01`（Medium）：deviceidentity 必引用 `Verify`（mandatory，
  anti-vacuity）+ 禁引用 `NewBootstrapMiddleware`/`BootstrapCredentials`（forbidden，RED fixture）。
- **renew = mTLS（传输层）**：挂独立 `cell.DeviceMTLSListener`（`kauth.AuthMTLS{}`）；身份从客户端证书
  SPIFFE URI SAN 恢复（`certsigning.ParseDeviceURISAN` → tenant+device，无 registry）。
- **cacerts = public**：RFC 7030 §4.1 信任根分发，鉴权前置悖论（设备取信任根时尚无任何凭据）。

### D7 — device-mTLS listener server cert：启动期临时自签（dev）

softca 无 server-cert 签发路径。renew listener 需无条件 server cert（active 契约耦合 serving）。决策：
**启动期生成 ephemeral ECDSA P-256 自签 server cert**（loopback SAN，每次启动重生成、永不持久化），
clientCAs = enroll signer 的 CA trust bundle（同一 CA 实例——设备 `/enroll` 拿到的证书正是 renew listener
mTLS 接受的）。与 dev softca「重启换锚」临时态一致；设备验证 server 端为 out-of-band（dev skip-verify）。
生产由 TLS-terminating 代理 / 真实 server cert 前置（`GOCELL_HTTP_DEVICE_MTLS_ADDR` 暴露 routable addr）。

### D8 — cacerts response projection carve-out

`http.deviceidentity.cacerts.v1`（GET，`{data:{trustBundle}}`）加入 `resourceReadProjectionCarveOut`
（`RESOURCE-PROJECTION-COVERAGE-01`）。理由（同 `http.admin.health.cells.v1` #1860 范式）：trustBundle 是
**public CA 材料**，unauthenticated、非 tenant-scoped、无 PII、无可掩码列轴——column-masking funnel
（responseProjection，FieldMask 来自 PDP 决策）对一个无 PDP 决策的公开端点是 dishonest 的恒等掩码。
sibling devicestate/status（device:read 鉴权、tenant-scoped）则正确标 `responseProjection: true`。

## 威胁矩阵

| 威胁 | 控制 | 评级 |
|---|---|---|
| 业务代码伪造设备 subject 绕过 principal 门禁 | `AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01` caller-allowlist + RED fixture | Medium（Go 天花板） |
| 经显式-subject 路径铸 admin/super-admin | `SubjectDescriptor` 无 privileged 构造器（sealed） | Hard |
| 包外 struct-literal 伪造 `SubjectDescriptor` | unexported 字段 | Hard |
| 未授权签发（PDP deny 仍签） | `certsigning.AuthorizedCertRequest` 须带 granted `SignConstraints`（`CERT-SIGN-FUNNEL-01`，继承） | Hard |
| enroll 复用 setup-bootstrap 凭据 | FMT-28（继承）+ `EST-ENROLL-AUTH-BOUNDARY-01` forbidden prong | Hard + Medium |
| enroll public 路由无应用层鉴权（dead public） | `EST-ENROLL-AUTH-BOUNDARY-01` mandatory prong（须 live 引用 `Verify`） | Medium |
| active 框架契约无 serving | `validateFrameworkServing`（phase0，继承 #2037） | Medium |
| renew listener 接受非本 CA 证书 | clientCAs = signer trust bundle + `RequireAndVerifyClientCert`（TLS 1.3） | Hard（握手层） |
| 越权 SAN（CSR SAN 超授权面） | `NewAuthorizedCertRequest` 校验 CSR SAN ⊆ granted allowance | Hard |

## 与 PR-10a authz ADR 的关系（重评）

PR-10a（`202606121400-1348`）建立「subject 从 ctx principal 派生 + RequirePermission 单一 PDP 路径」。
本 ADR **不冲突、扩展**：新增 `AuthorizeAs` 是与 `Authorize` **并列**的第二评估入口（共用评估核），用于
**非-HTTP、无 ctx principal** 的背景/cert 路径；HTTP 路径行为不变（`Authorize` 仍唯一入口，principal→descriptor
映射等价，回归测试守）。PR-10a 的「业务 handler 无 role-literal 授权分支」（`PERMISSION-BASED-AUTHZ-01`）
与本 ADR 的「业务 handler 不可调 `AuthorizeAs`」（D3）是**同向互补**的两条 caller 约束。

## Consequences

- **正向**：cert 授权获得「接口 + 内置默认（PDP-backed adapter）+ 可换接缝」可消费性模型（与 Signer 对称）；
  任意 cell（含外部）+ certlifecycle 复用同一 PDP，policy 不漂移；EST 前端开箱可用。
- **代价**：新增 1 sealed 类型 + 1 segregated 接口 + 1 adapter 包 + 2 archtest（funnel + 残差边界）+
  device-mTLS listener + 1 projection carve-out。
- **后续**：证书行持久化 / cert-issued&revoked emit（PR-9 #1905）；certlifecycle sweep 接线（PR-7/9/10）；
  status/revoke 端点实现（契约仍 draft）；device-mTLS 真实 server cert / mesh 终结（生产化）。

## 参考

- 开源：Cedar `cedar_policy::Request`、kubernetes `authorization/v1.SubjectAccessReviewSpec`、
  cert-manager `CertificateRequestSpec`、RFC 7030（EST）、RFC 5652 §5.1（degenerate certs-only SignedData）。
- 符号/盲区：`framework/runtime/auth/subject_descriptor.go`、`framework/runtime/certsigning/pdpauthz/`、
  `tools/archtest/authz_authorize_as_caller_funnel_test.go`、`tools/archtest/est_enroll_auth_boundary_test.go`。
