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
3. **路线图自身存在张力**：gocell-platform §3.7（line 271）已把「pkicell WSTEP 支持」列为
   **GoCell v1.0 P0 前置**——与 #995「不在框架」立场矛盾。本 ADR 消解该张力。

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

- 定义 `contracts/{deviceidentity,devicestate,devicecompliance,remotecommand}/v1`——路线图
  唯一允许提前的 MDM/ZT 前期项（#1052「可提前子项」）。`deviceidentity` 是证书/身份之家。
- 落地 #1811（生产 device / super-admin 主体签发器）——证书底座硬前置。
- **不建** `mdm/` / `zerotrust/` module 骨架；winmdm `pkicell` 的**框架级 PKI 原语下移 core**，
  winmdm 触发（2027 Q1）后只需建 WSTEP/SCEP 协议前端 + caworkflow，不再自建 PKI 原语。

## 威胁矩阵（PR-11 复检单源）

| 威胁 | 缓解 | 评级 |
|---|---|---|
| 包外伪造证书值 / 绕过签发 | `CERT-VALUE-SEALED-CONSTRUCTION-01` + `CERT-SIGN-FUNNEL-01`（Sign 唯一入口） | Hard |
| 私钥泄漏到 kernel/runtime | `CERT-PRIVATE-KEY-CUSTODY-01`（archtest 扫 certsigning 边界外无 PrivateKey 字段；私钥只在 adapter 的 `crypto.Signer`） | Hard/Medium |
| 授权漏洞放大为越权签发 | `Authorize` / `Sign` 分离；`AuthorizeEnroll` 复用 PDP；nil grant fail-closed；SignConstraints 由 Signer 强制 | Medium |
| 跨副本重复签发 | 复用 `RECONCILE-FENCED-WRITE-FUNNEL-01`（`LeaseToken.Epoch` 写路径 CAS）+ 队列 active-uniqueness | Hard |
| 多租户证书 reconcile 串租户 | reconcile system identity 清空 tenant（#1821）；scan + 命令 key + 签发请求自带 tenant 维度 | Medium |
| EST 注册鉴权绕过 | `EST-ENROLL-AUTH-BOUNDARY-01`（enroll=bootstrap/设备凭证；reenroll=现证书 mTLS；缺则 fail-closed） | Medium |
| 续期惊群 | jitter 续期窗口（k8s 70-90% 寿命模型） | —（工程） |
| device PII（serial/subject/device_id 入日志/wire） | 复用 `pkg/redaction`（#1695）；审计前 hash/redact | Medium |

每条 enforcement 与其实现**同 PR 闭环**（静态守卫 + 反向自检 + godoc/ADR）；无 Soft（AI-robust §分级）。

## Consequences

- **正向**：设备证书一次性框架化，winmdm / ZT / iotdevice 共享同一底座；内置 softca 使能力
  开箱可用；`Authorize`/`Sign` 分离 + 私钥边界对齐业界最佳实践。
- **代价**：推翻一条已锁定决策（#995）+ 修订 4 份路线图文档（见下）；新增 2 个 runtime 包 +
  1 个 adapter module + 4 个契约 + iotdevice 破坏式迁移（`rotate-cert` 字符串→真契约，无 shim）。
- **真值源**：`docs/plans/specs/1895-device-identity-cert-framework/`（spec/plan/tasks/research）。

## 路线图修订（本 ADR 同改动落地，冲突段落重写——AI-robust §审查）

| 文档 | 段落 | 修订 |
|---|---|---|
| #995（issue） | 全文 | 收口结论=进框架（评论 + PR-11 关闭）；推翻「不在框架 / 消费方自建」 |
| `202604301030-winmdm-prd-on-gocell.md` | §2 pkicell（line 72）+ §10 module 归属 | pkicell 框架级 PKI 原语下移 core；winmdm 仅留 WSTEP/SCEP 前端 + caworkflow |
| `202604300900-gocell-as-platform-foundation.md` | §3 pkicell 映射（191/206）+ §4 mtlscert | PKI 底座=框架 `runtime/certsigning`+`adapters/softca`；pkicell/mtlscert 为消费方 |
| `202604300950-plan-d-...md` | §10 阶段 0 | 加注：设备证书框架底座落 core（runtime/adapters/contracts），**不**建 mdm/，与「禁止预建 mdm/」一致 |
| `202604300800-final-form-capability-overview.md` | §3.7 + Tier B | final-form 增列 `runtime/certsigning` + `runtime/certlifecycle` + `adapters/softca` 能力 |
| #1051 / #1052（issue） | 锚点 | 评论：框架 PKI 原语 + 4 中立契约提前；其余项仍门控 2027/2029 |

> 与 §3.7（line 271）「pkicell WSTEP 作为 v1.0 P0」**一致**——本 ADR 把该 P0 的 PKI 原语部分
> 明确为框架交付（WSTEP 协议前端仍 winmdm），消解 #995 与 §3.7 的张力。

## 参考

- 对标：SPIFFE/SPIRE `pkg/server/ca/ca.go` + keymanager plugin；cert-manager Issuer/CertificateRequest；
  smallstep/certificates `authority/{tls,provisioner}`；Vault PKI；k8s kubelet `certificate.Manager`；IETF RFC 7030（EST）。
- 内部：`kernel/reconcile/doc.go`、`runtime/command`、`docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md`、
  `docs/architecture/202606101200-1821-adr-reconcile-system-producer-identity.md`、`.claude/rules/gocell/tenancy.md`。
