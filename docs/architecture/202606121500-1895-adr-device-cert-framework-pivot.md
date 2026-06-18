# ADR: 设备身份与证书框架化决议（device-cert pivot）

- Status: Accepted
- Date: 2026-06-12
- Issue: #1895 (EPIC), #1897 (PR-1, US6 / FR-018); 收口 #995; 部分提前 #1051 / #1052
- Scope: 本 ADR 锁定「设备证书成为框架的一部分」的范围决策 + 层放置 + 威胁矩阵，
  并修订 4 份已锁定路线图文档的冲突段落。各 PR 的实现级 invariant 符号 / 评级证明
  写入其 archtest godoc；跨层威胁矩阵复检由 PR-11（#1907）收尾，本 ADR 是其单源。

## Context

`docs/plans/product-roadmap/`（2026-04-30 锁定）把设备证书 / PKI 定位为**消费方自建**：
#995 明文「X.509 证书管理…**当前不在框架内**——定位为消费方（winmdm）在 `cells/cert-core`
自建或集成 step-ca / Vault PKI」，框架最多给 hook 接口；winmdm `pkicell`
（slices `wstep/scep/caworkflow/rotation`）落在 `mdm` module，门控 v1.0 GA / 2027 Q1（#1051）；
zerotrust 门控 2029 Q1（#1052）。

三个事实促成转向（2026-06）：

1. **多租户已大幅推进**（epic #1337 PR-1..12 落地）。设备主体模型 `principalKind=device` /
   `RowScope=device` 就位（`runtime/auth/rowscope.go`），但 develop **无生产 authn 铸造
   `PrincipalDevice`**（rowscope.go 自述 "type-foundation entries, no production issuer"）——#1811。
2. **「设备证书」今天是时间戳，不是 PKI**。repo 中**零** CA / CSR / CRL / 签名 / 私钥代码；
   唯一「证书」= devices 行 `cert_epoch` + `cert_expires_at` 两列 + 一个发 `rotate-cert`
   字符串命令的 stateless reconciler（`examples/iotdevice/.../devicecertrenewal`）。
3. **路线图自身存在张力**：gocell-platform §3「P0 阻塞项（GoCell v1.0 前置）」callout 已把
   「pkicell WSTEP 支持」列为 **v1.0 P0 前置**（同文 §3.7 时间估算却把 pkicell 排到 Stage 1 /
   2027 Q1，本身已不自洽）——且与 #995「不在框架」立场矛盾。本 ADR 消解该张力。

开源对标（SPIFFE/SPIRE + cert-manager + step-ca，三家一致）：**控制面（生命周期 reconcile +
请求模型 + 续期调度）属框架；签名私钥在 adapter（`crypto.Signer` 永不出边界）；协议前端在
消费方边缘；`Authorize`（能否拿证书）与 `Sign`（CSR→证书）是两个接口**。GoCell 已有其中
4 个接缝（`kernel/reconcile` / `runtime/command` / PDP / `RowScope`），缺真实 Signer、泛化
lifecycle、EST 前端、生产 device 主体。

## Decisions

### D1 — 设备证书进框架（推翻 #995「不在框架」）

设备证书全生命周期的**通用控制面**提升为框架能力，结束「每个 MDM 消费方各自重造」。
#995 的触发条件（「WinMDM 或设备管理产品线正式启动，需框架级 PKI；或 ≥2 消费方需证书管理」）
由本决策按 fiat 满足（MDM + ZT 双消费方 + 设备身份框架化诉求）。#995 收口结论 = **进框架**。

### D2 — 深度 = 底座 + 内置软 CA + EST 前端（不做完整 PKI corecell）

- **框架拥有**：`runtime/certsigning`（`Signer` / `Authorizer` / `RevocationStore` 接口 +
  `CertRequest` / `IssuedCert` sealed 类型）、`runtime/certlifecycle`（泛化 iotdevice 种子的
  `reconcile.Reconciler`）、`runtime/http/est`（EST RFC 7030 注册前端）、`adapters/softca`
  （内置软 CA，标准库 `crypto/x509`，**无外部 CA 依赖即可用**）。
- **留 adapter**：真实 CA 后端 step-ca / Vault PKI（本轮只留接口位，后续 PR）。
- **留消费方**：Windows 专属 XCEP / WSTEP / SCEP / MS-MDE 协议前端（winmdm）；ZT 的
  mTLS / SPIFFE Workload-API 边缘。
- **不做**：`certcore` 平台 corecell（证书底座是 primitive 不是平台 Cell；强行做 corecell
  与 #995「cells/cert-core」消费方语义 + winmdm `pkicell` 落点都冲突）。

### D3 — 层放置：runtime + adapters + contracts，全落 core，不入 kernel、不建 module

- 证书底座入 **`runtime/`**（需 `crypto/x509` + 在 reconcile/command 之上 wiring；kernel 只依赖
  stdlib+pkg，不入 kernel）。对标既有范式：`kernel/reconcile` 定义 `LeaderElector` 接口、
  `adapters/{redis,postgres}` 实现——本 epic：`runtime/certsigning` 定义 `Signer`、
  `adapters/softca` 实现。
- 证书生命周期**复用** `kernel/reconcile.Loop`（leader-elect / `LeaseToken.Epoch` fencing /
  system-identity），**不新造控制环**。
- 中立契约入 `contracts/`；设备主体签发器入 `runtime/auth`。
- **全部落 core 顶层 module**，不建 `mdm/` / `zerotrust/`（守 plan-D §10）。

### D4 — `Authorize` 与 `Sign` 分离；私钥永不出 adapter；`Sign` 唯一签发入口

- `Authorizer.AuthorizeEnroll(ctx, EnrollmentClaim) (SignConstraints, error)` 与
  `Signer.Sign(ctx, CertRequest) (IssuedCert, error)` 是**两个接口**（对标 step-ca
  `AuthorizeSign` vs `Authority.Sign`）。授权**复用既有 PDP**，不新建决策引擎。
- 签名私钥（`crypto.Signer`）**永不进 kernel/runtime**，只在 adapter 内（SPIRE KeyManager 范式）。
- `Signer.Sign` 是包外铸造设备证书的**唯一入口**（sealed funnel）；`CertRequest` / `IssuedCert`
  sealed 构造（包外不可字面量伪造证书材料）。

### D5 — MDM/ZT 前期工作 = 4 中立契约 + 设备主体签发器（部分提前 #1051/#1052）

- 定义 4 个中立设备契约（domain：deviceidentity / devicestate / devicecompliance / remotecommand）——
  路线图唯一允许提前的 MDM/ZT 前期项（#1052「可提前子项」）。`deviceidentity` 是证书/身份之家。
  目录按仓库单源 `{kind}/{domain-path}/{version}/` 落 **kind-qualified** 路径（`contracts/http/deviceidentity/.../v1`、
  `contracts/event/deviceidentity/.../v1`、`contracts/command/{deviceidentity/rotate,remotecommand}/v1` 等，详见 spec FR-013）；
  「4 个契约」是 domain 简写，非目录真源。**归属机制**：这些中立契约由**框架**归属（不绑单一 Cell，
  对齐 cert-manager/SPIFFE/k8s 4/4 范式），经 sealed `ContractOwner`（`ownerCell: _framework`）落地——
  详见 ADR `202606130635-1939-adr-framework-owned-contract.md`（#1939，blocks #1899）。
- 落地 #1811（生产 device / super-admin 主体签发器）——证书底座硬前置。
- **不建** `mdm/` / `zerotrust/` module 骨架；winmdm `pkicell` 的**框架级 PKI 原语下移 core**，
  winmdm 触发（2027 Q1）后只需建 WSTEP/SCEP 协议前端 + caworkflow，不再自建 PKI 原语。

## 威胁矩阵（PR-11 复检单源）

| 威胁 | 缓解 | 评级 |
|---|---|---|
| 包外伪造证书值 / 绕过签发 | `CERT-VALUE-SEALED-CONSTRUCTION-01`（sealed 构造）+ `CERT-SIGN-FUNNEL-01`（Sign 唯一入口） | 上游 Hard（sealed 值构造）/ 下游 Medium（archtest 扫单一 Sign callsite） |
| 私钥泄漏到 kernel/runtime | `CERT-PRIVATE-KEY-CUSTODY-01`（私钥只在 adapter 的 `crypto.Signer`） | 上游 Hard（`Signer` 接口不暴露 key getter）/ 下游 Medium（archtest 扫 certsigning 边界外无 PrivateKey 字段） |
| 授权漏洞放大为越权签发 | `Authorize` / `Sign` 分离；`AuthorizeEnroll` 复用 PDP；nil grant fail-closed；SignConstraints 由 Signer 强制 | Medium |
| 跨副本重复签发 | 复用 `RECONCILE-FENCED-WRITE-FUNNEL-01`（`LeaseToken.Epoch` 写路径 CAS）+ 队列 active-uniqueness | Hard |
| 吊销与续期竞态（吊销正在续期的证书） | epoch 单调 CAS（fencing）保证终态一致；`certlifecycle` 状态机 revoke 转换串行化 | Hard（复用 fencing）/ Medium（状态机测试） |
| 多租户证书 reconcile 串租户 | reconcile system identity 清空 tenant（#1821）；scan + 命令 key + 签发请求自带 tenant 维度 | Medium |
| 跨租户吊销（凭裸 serial 吊销他租户证书） | `Revoke`/`RevocationList`/`Tidy` 带 `CertScope`（tenant+issuer+device）typed 位置参，与签发同源；漏传编译错（`CERT-REVOKE-SCOPED-01`）；跨租户/issuer fail-closed | 上游 Hard（typed scope 漏传编译错）/ 下游 Medium（fail-closed 测试） |
| EST 注册鉴权绕过（路由声明半） | `EST-ENROLL-AUTH-BOUNDARY-01`（EST `auth.Route` 显式声明 enroll=专用 enrollment-credential、reenroll=现证书 mTLS scheme，**不复用** setup `auth.bootstrap:true`；EST 挂版本化 cell 路径，缺则 fail-closed）——**路由声明半归 G2/PR-8b**（`runtime/http/est`） | Medium |
| enrollment-credential 旁路 wrapper / 越权签发（凭据层半，#2303 G4） | `ENROLLMENT-CREDENTIAL-MINT-CALLER-01`：`EnrollmentCredentialIssuer.Issue` 是唯一 `(*JWTIssuer).Issue(TokenIntentEnrollment,…)` 出口（强制 device + 短 TTL + jti + 非空 tenant/subject）；`EnrollmentCredentialVerifier` fail-closed（intent 隔离 + device 断言 + 构造器拒空）；sealed `EnrollmentIdentity`（unexported 字段，包外不可伪造可用身份） | 下游 Hard（sealed identity 包外不可构造 + TokenIntent 隔离 = 类型/密码学事实）/ 上游 Medium（archtest 扫单一 enrollment Issue callsite，Go 天花板，同族 `COMMAND-ASYNC-EMIT-CALLER-01`） |
| 续期惊群 | jitter 续期窗口（k8s 70-90% 寿命模型） | —（工程） |
| device PII（serial/subject/device_id 入日志/wire） | 复用 `pkg/redaction`（#1695）；审计前 hash/redact | Medium |

每条 enforcement 与其实现**同 PR 闭环**（静态守卫 + 反向自检 + godoc/ADR）；无 Soft（AI-robust §分级）。

## Consequences

- **正向**：设备证书一次性框架化，winmdm / ZT / iotdevice 共享同一底座；内置 softca 使能力
  开箱可用；`Authorize`/`Sign` 分离 + 私钥边界对齐业界最佳实践。
- **代价**：推翻一条已锁定决策（#995）+ 修订 4 份路线图文档（见下）；新增 2 个 runtime 包 +
  1 个 adapter module + 4 个契约 + iotdevice 破坏式迁移（`rotate-cert` 字符串→真契约，无 shim）。
- **实施规格 / 设计分解 / 后续 PR 输入**：`docs/plans/specs/1895-device-identity-cert-framework/`
  （spec/plan/tasks/research）。**项目状态 / 优先级 / wave / 父子关系的唯一真源是 GitHub Issues #1895 + Project**
  （PROJECT.md / agent-instruction-surfaces 单源）；speckit 目录只承载实施规格内容，不承载项目管理状态。

## 路线图修订（本 ADR 同改动落地，冲突段落重写——AI-robust §审查）

| 文档 | 段落 | 修订 |
|---|---|---|
| #995（issue） | 全文 | 收口结论=进框架（评论 + PR-11 关闭）；推翻「不在框架 / 消费方自建」 |
| `202604301030-winmdm-prd-on-gocell.md` | §2 pkicell（line 72）+ §10 module 归属 | pkicell 框架级 PKI 原语下移 core；winmdm 仅留 WSTEP/SCEP 前端 + caworkflow |
| `202604300900-gocell-as-platform-foundation.md` | §3 pkicell 映射（M6 / cell 表 / Stage1 / M1）+ §3 P0 callout + §4 mtlscert | inline 重写：PKI 底座=框架 `runtime/certsigning`+`adapters/softca`+EST；pkicell/mtlscert 为消费方；**`rotation` slice 退役**；v1.0 P0 由「pkicell WSTEP」**替换为「框架证书底座（certsigning/softca/EST）」**，winmdm WSTEP/SCEP 前端归 2027 Stage 2 |
| `202604300950-plan-d-...md` | §10 阶段 0 + §3.1 树（pkicell 内部CA）+ §9 PKIIssuer + PR A1.4 | 加注 + inline：设备证书框架底座落 core（runtime/adapters/contracts），**不**建 mdm/，与「禁止预建 mdm/」一致；pkicell 去 `rotation`/内部CA，改消费框架 |
| `202604301030-winmdm-prd-on-gocell.md` | §2 pkicell（line 80）+ §10 module + §3 链路 + §7 mTLS | pkicell 框架级 PKI 原语下移 core；winmdm 仅留 WSTEP/SCEP 前端 + caworkflow；`rotation` 退役；CA 私钥归 softca |
| `202604300800-final-form-capability-overview.md` | §3.7 运行时能力清单 | final-form 增列「设备身份与证书底座」（`runtime/certsigning` + `runtime/certlifecycle` + `adapters/softca` + EST）能力条目 |
| #1051 / #1052（issue） | 锚点 | 评论：框架 PKI 原语 + 4 中立契约提前；其余项仍门控 2027/2029 |

> **v1.0 P0 重定义**：路线图原把「pkicell WSTEP 支持」列为 v1.0 P0（同文 §3.7 时间估算却把 pkicell
> 排到 Stage 1 / 2027 Q1，本身不自洽）。本 ADR **替换**该 P0 为「框架证书底座（`runtime/certsigning` +
> `adapters/softca` + EST）」——这是真正属于 core v1.0 的能力；winmdm 的 WSTEP/SCEP 协议前端归 2027
> Stage 2，不塞进 core v1.0 P0。一并消解 #995「不在框架」与原 P0 callout 的张力。

## Amendment 2026-06-18 — #2303（epic #2299 G4）enrollment-credential scheme

落地 FR-012 的**凭据层**：device 首次 EST enroll 的专用鉴权凭据签发器 + 验证器
（`framework/runtime/auth`），让示例 PR-2 与 EST 前端（G2/PR-8b）有真正的框架原语可依赖，
不再用「最简 enrollment token 占位」。

- **机制**：enrollment-credential = 带专用 `TokenIntentEnrollment`（typ `enroll+jwt` +
  `token_use=enrollment`）的短期（`EnrollmentCredentialTTL`=5min）device-scoped 签名 JWT，
  复用既有 RS256 `JWTIssuer`/`JWTVerifier`。**FR-012「不复用」是类型/密码学事实**：双通道
  token-confusion 防御（typ 头 + token_use claim 交叉校验）使 enrollment 凭据不能当 access
  token 用、反之亦然——无需运行时策略。**不复用** setup `auth.bootstrap:true`（FMT-28 正交，
  本改动不碰）。
- **概念澄清（防混淆）**：enrollment-credential 与 device **access** token **都是
  `principal_kind=device` 的 JWT**，唯一区别是 `token_use`（enrollment vs access）。前者证明
  「被授权以设备 X 身份注册」（注册期、无证书），后者证明「已注册设备 X」（业务期）。
  两者经 `token_use` 隔离，互不可用。
- **sealing**：`EnrollmentIdentity`（验证产物）全 unexported 字段 + 唯一构造器拒空 tenant/subject
  → 包外不可构造可用身份（下游 Hard）。mint funnel `ENROLLMENT-CREDENTIAL-MINT-CALLER-01`
  钉单一 `Issue(TokenIntentEnrollment,…)` 出口（上游 Medium，Go 天花板）。见上「威胁矩阵」新增行。
- **归 G2/PR-8b（本 issue OUT）**：EST 端点（`runtime/http/est`）+ `auth.Route` scheme 声明
  （`EST-ENROLL-AUTH-BOUNDARY-01` 路由声明半）；`EnrollmentIdentity → certsigning.EnrollmentClaim`
  适配器（避免 auth↔certsigning 互 import 的分层陷阱）；**一次性/replay**（jti 账本/nonce 消费——
  本 issue 已把随机 `jti` 写入凭据并经 `EnrollmentIdentity.JTI()` 暴露作前向 hook，消费需 enroll
  请求上下文，归 EST 前端）。本 issue 无契约扇出（无 contract.yaml/generated/event/command）。

## 参考

- 对标：SPIFFE/SPIRE `pkg/server/ca/ca.go` + keymanager plugin；cert-manager Issuer/CertificateRequest；
  smallstep/certificates `authority/{tls,provisioner}`；Vault PKI；k8s kubelet `certificate.Manager`；IETF RFC 7030（EST）。
- 内部：`kernel/reconcile/doc.go`、`runtime/command`、`docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md`、
  `docs/architecture/202606101200-1821-adr-reconcile-system-producer-identity.md`、`.claude/rules/gocell/tenancy.md`。
