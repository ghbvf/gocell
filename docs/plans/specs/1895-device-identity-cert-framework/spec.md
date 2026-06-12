# Feature Specification: 设备身份与证书框架化 + MDM/ZT 前期地基

**Feature Branch**: `1895-device-identity-cert-framework`
**Epic**: #1895
**Created**: 2026-06-12
**Status**: Draft
**Input**: 把设备证书从 iotdevice 示例提升为框架能力（底座 + 内置软 CA + EST 前端）；落地 MDM/ZT 前期工作（4 个中立设备契约 + 生产级设备主体签发器）。收口 #995，部分提前 #1051/#1052，修订已锁定路线图。

## 设计基线（探索结论摘要）

**现状真相**：repo 中**零** CA / CSR / CRL / 签名 / 私钥代码。今天的「设备证书」= devices 行上 `cert_epoch`(int64) + `cert_expires_at`(timestamp) 两列，一个 stateless reconciler 扫近期过期并发 `CommandType="rotate-cert"` 字符串命令（`examples/iotdevice/.../devicecertrenewal`）。所有 `crypto/x509` 命中均为 TLS 传输层消费已编码 PEM 或 `_test.go` 自签链。

**开源一致结论**（SPIFFE/SPIRE + cert-manager + step-ca）：成熟证书身份平台都画同一条线——

> **控制面（生命周期 reconcile + 请求模型 + 续期调度）属框架；签名私钥在 adapter（`crypto.Signer` 永不出边界）；协议前端在消费方边缘。`Authorize`（能否拿证书）与 `Sign`（CSR→证书）是两个接口。**

| 接缝 | GoCell 落点 | 默认 fail |
|---|---|---|
| Signer（CSR→证书） | `runtime/certsigning` 接口，`adapters/softca` 实现（私钥在 adapter） | fail-closed（签发失败不降级） |
| Authorize（设备能否拿证书） | 复用既有 PDP（`auth.RequirePermission` / authorizationdecide），独立于 Sign | fail-closed（缺授权 deny） |
| CertLifecycle（状态机 + 续期） | `runtime/certlifecycle`，复用 `kernel/reconcile.Loop`（leader/fencing/system-identity） | level-triggered；transient 退避 |
| Revocation/CRL | `runtime/certsigning.RevocationStore`（方法带 `CertScope` typed 位置参），PG/softca 实现 | fail-closed（跨租户/issuer 吊销/查询拒绝） |
| 设备身份绑定 | 生产 `PrincipalDevice` 签发器（#1811）；证书即设备凭证的统一路径 | service/anonymous fail-closed |
| 中立设备信号 | 4 个 domain 契约（deviceidentity/devicestate/devicecompliance/remotecommand），kind-qualified 落 `contracts/{http,event,command}/<domain>/.../v1`（见 FR-013） | 契约边界 |
| 协议前端 | 框架内 EST(RFC 7030)，挂版本化 cell 路径 `/api/v{N}/deviceidentity/est/*`；XCEP/WSTEP/SCEP 留 winmdm 消费方 | 鉴权边界 fail-closed（非 setup-bootstrap） |

**核心安全不变式**：私钥永不进 kernel/runtime（只持 `crypto.Signer` 句柄，SPIRE KeyManager 范式）；`Sign` 是包外铸造设备证书的唯一入口（sealed funnel）；签发与吊销共用 `CertScope` 隔离（吊销不可凭裸 serial 跨租户）；`Authorize` 与 `Sign` 分离——证书签发授权漏洞不得放大为越权签发。

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 设备拥有可信框架身份 (Priority: P1) 🎯 MVP

设备主体经生产级签发器获得真实 `PrincipalDevice`（含 tenant + 设备 subject），从而沿请求链派生 `RowScope=device`，并成为后续证书签发的被授权主体。今天 `runtime/auth/rowscope.go` 的派生规则已就位，但 develop **无任何生产 authn 铸造 `PrincipalDevice`/super-admin**（#1811）——US1 补齐这一硬前置。

**Why this priority**：地基。证书签发、行级隔离、设备态势评估全部以「可信设备主体」为前提；没有它，证书底座只能跑测试桩。

**Independent Test**：用设备凭证通道认证 → ctx 得 `principalKind=device` + 设备 subject + tenant；查命令队列只返回 `device_id=self` 行；缺设备凭证 / service-token 冒充 → fail-closed。

**Acceptance Scenarios**：
1. **Given** 设备出示有效设备凭证，**When** 进入业务端点，**Then** ctx Principal 为 `PrincipalDevice`，subject=设备 ID，tenant 来自凭证。
2. **Given** super-admin 凭证，**When** 跨租户运维端点，**Then** 派生 `RowScope=all` 且强制审计（复用 ROWSCOPEALL-AUDIT-FUNNEL-01）。
3. **Given** service-token 冒充设备，**When** 解析主体，**Then** 不得派生 `PrincipalDevice`（service ≠ device，概念隔离）。
4. **Given** 设备主体，**When** 查命令/证书列表，**Then** 仅 `device_id=self`（`RowScope=device`）。

---

### User Story 2 - 框架级证书签发底座 (Priority: P1) 🎯 MVP

框架能为设备签发 / 续期 / 吊销 X.509 证书，CA 后端可插拔，消费方零重建。内置 `adapters/softca`（`crypto.Signer` 托管 CA）使能力**无外部依赖即可用**；证书生命周期由复用 `kernel/reconcile` 的 `certlifecycle` reconciler 驱动（把 iotdevice 种子从「发字符串命令」泛化为「真实签发状态机」）。

**Why this priority**：这是「设备证书成为框架的一部分」的本体。P1 与 US1 并列——没有真实签发，设备身份只是时间戳。

**Independent Test**：构造一个近期过期设备证书 → certlifecycle 触发 → `Authorizer` 放行 → `Signer.Sign(CSR)` 返回证书链 → 持久化 epoch+1/notAfter → 发 `cert-issued` L2 事实；吊销 → CRL 含该 serial；签名失败 / 缺授权 → 不签发（fail-closed），不损坏现有证书。

**Acceptance Scenarios**：
1. **Given** 设备证书 `notAfter` 进入续期窗口，**When** certlifecycle reconcile，**Then** 经 `Authorizer` 放行后 `Signer.Sign` 签发新证书，epoch 单调 +1，持久化 notAfter，发 `deviceidentity.cert-issued.v1`。
2. **Given** `Signer`（softca）不可用，**When** reconcile，**Then** transient 退避重试，**不**损坏既有证书状态（level-triggered）。
3. **Given** `Authorizer` deny，**When** 续期，**Then** 不签发（fail-closed），记审计。
4. **Given** 吊销请求，**When** `RevocationStore.Revoke(CertScope, serial)`，**Then** `RevocationList(CertScope)` 含该 serial，发 `cert-revoked.v1`。
7. **Given** tenant-A 主体持 tenant-B 证书的 serial，**When** 调 `Revoke` / `RevocationList`，**Then** fail-closed 拒绝（`CertScope` 隔离，绝不凭裸 serial 跨租户吊销/查询）。
5. **Given** 开发者试图在 adapter 外构造证书值或绕过 `Sign`，**When** 编译/archtest，**Then** 失败（sealed 构造 + 单一签发 funnel）。
6. **Given** 多副本部署，**When** 两副本同时 reconcile 同设备，**Then** `LeaseToken.Epoch` fencing + 队列 active-uniqueness 保证至多一次有效签发。

---

### User Story 3 - 设备经 EST 开箱自助注册/续期证书 (Priority: P2)

框架提供 EST(RFC 7030) 注册前端：`/cacerts`（取信任根）、`/simpleenroll`（首次注册 PKCS#10 CSR → PKCS#7 证书）、`/simplereenroll`（凭现证书续期）。设备无需消费方实现协议即可自助拿证书；Windows 专属 XCEP/WSTEP/SCEP 仍留 winmdm。

**Why this priority**：P2，依赖 US2 的 `Signer`/`Authorizer`。EST 是「batteries-included」的开箱注册路径（用户决策含 EST 前端）。

**Independent Test**：设备 POST CSR 到版本化 `/api/v1/deviceidentity/est/simpleenroll`（enrollment-credential / device-token 鉴权）→ 得证书链；`/.../simplereenroll` 用现证书 TLS-client-auth 续期；未授权 / 畸形 CSR → 4xx fail-closed；`/.../cacerts` 返回 softca 信任根。

**Acceptance Scenarios**（EST 操作挂版本化 cell 路径 `/api/v1/deviceidentity/est/*`）：
1. **Given** 合法 enrollment-credential / device-token + 合法 PKCS#10，**When** POST `/simpleenroll`，**Then** 返回 PKCS#7 证书链，状态 200。
2. **Given** 已有证书设备，**When** `/simplereenroll`（mTLS client-auth），**Then** 续期新证书，旧证书可吊销。
3. **Given** 缺/错鉴权 **或** 用 setup `auth.bootstrap:true` 冒充，**When** enroll，**Then** 401/403（fail-closed，经 `Authorizer`；EST 非 setup-admin 路径，FMT-28 拒绝 bootstrap 凭据）。
4. **Given** 畸形 / 越权 SAN 的 CSR，**When** enroll，**Then** 4xx，签名约束（SignConstraints）拒绝。
5. **Given** 任意客户端，**When** GET `/cacerts`，**Then** 返回 softca 信任根（PKCS#7）。

---

### User Story 4 - 中立设备信号契约 (Priority: P2)

定义 4 个中立设备契约（domain：deviceidentity / devicestate / devicecompliance / remotecommand，kind-qualified 落库见 FR-013），使 MDM / ZT 能消费**任意** device provider（winmdm / Intune / Jamf / 自建），而非硬绑单一实现。`deviceidentity` 是证书 / 身份之家；`remotecommand` 泛化既有 `command.devicecommand.enqueue.v1`。

**Why this priority**：P2，路线图唯一允许提前的 MDM/ZT 前期项（#1052 可提前子项）。契约形状提前冻结，避免 2029 重写。

**Independent Test**：每个契约有 contract.yaml + schema + generated code + governance；契约级测试覆盖正常 schema / 参数错误 / 鉴权边界；契约扇出闭环（5 载体同步）。

**Acceptance Scenarios**：
1. **Given** `deviceidentity/v1` 契约，**When** codegen，**Then** 生成 handler/client/types，registration glue 派生自 slice.yaml `contractUsages`。
2. **Given** 任一中立契约破坏式 wire 变更，**When** 提交，**Then** 走新版本目录（不偷改旧版本语义）。
3. **Given** `remotecommand/v1`，**When** 与既有 devicecommand enqueue 比对，**Then** 字段形状对齐可承接（PR-9 迁移）。

---

### User Story 5 - iotdevice 迁移示范（端到端证明接缝） (Priority: P2)

把 `examples/iotdevice/devicecell` 从 bare-string `rotate-cert` 切到框架证书底座：用 `deviceidentity` 契约 + `certlifecycle` reconciler + `softca` 签发真实证书；把 `rotate-cert` 字符串提升为真契约 `command.deviceidentity.rotate.v1`；补设备粒度 enqueue 鉴权（#654）。

**Why this priority**：P2，依赖 US2/US4。示例迁移是框架能力的端到端验证（消费方视角），也是 winmdm/ZT 的参考实现。

**Independent Test**：iotdevice 设备注册 → 签发真实证书（softca）→ 近期过期 → certlifecycle 续期 → 新证书生效；`rotate-cert` 走真契约；非授权设备 enqueue 命令被拒。

**Acceptance Scenarios**：
1. **Given** iotdevice 注册设备，**When** deviceregister，**Then** 经 `Signer` 签发真实初始证书（非仅时间戳），持久化证书 + epoch。
2. **Given** 迁移后，**When** 扫描续期，**Then** 经 `certlifecycle` 真实签发，`command.deviceidentity.rotate.v1` 取代字符串命令。
3. **Given** 设备 A 试图 enqueue 设备 B 的命令，**When** Enqueue，**Then** 设备粒度授权拒绝（#654）。

---

### User Story 6 - 治理闭环 + 路线图修订 (Priority: P3)

把上述 enforcement 按 AI-robust 三档固化：cert sealed 构造 + 单一 `Sign` funnel（Hard）、lifecycle fencing 复用 reconcile（Hard）、EST 鉴权边界（Medium archtest）、Authorizer/Signer 分离（Medium）。出**路线图修订 ADR**：收口 #995（结论：进框架）、修订 winmdm PRD pkicell 落点（mdm→core 注记）、plan-D §10 例外、final-form 增列 PKI 能力、更新 #1051/#1052 锚点。

**Why this priority**：P3，随各层增量补；但每条 enforcement 守卫与其实现**同 PR 闭环**。本故事收口跨层 ADR + 路线图一致性 + 威胁矩阵重评。

**Independent Test**：`make verify`（含 archtest）全绿；每条新 invariant 有反向自检；路线图 4 份文档与本 epic 一致（无残留「PKI 不在框架」表述）。

---

### Edge Cases

- **私钥泄漏面**：私钥永不进 kernel/runtime；adapter 只暴露 `Sign` 操作（`crypto.Signer`）。任何让原始私钥跨 `runtime/certsigning` 边界的代码 = archtest 红。
- **reconcile 单租户系统身份**：`kernel/reconcile` 在 system identity 下运行且**清空 tenant**（#1821）；多租户证书 reconcile 必须在 scan + 命令 key + 签发请求中**自带 tenant 维度**，不能依赖 ctx tenant。
- **续期抖动**：续期窗口加 jitter（k8s 70–90% 寿命模型）避免整队同时续期惊群。
- **EST 落点与鉴权**：EST 操作挂所属 cell 版本化路径（`/api/v{N}/deviceidentity/est/*`），非顶级裸路径；首次 `/simpleenroll` 用专用 enrollment-credential / device-token，`/simplereenroll` 用现证书 mTLS——两条路径显式声明，不混；**不复用** setup `auth.bootstrap:true`（FMT-28 限其只在 setup/admin，EST 复用会被 fail-closed 拒绝）。
- **跨租户吊销**：`Revoke`/`RevocationList` 带 `CertScope`（tenant+issuer+device）；tenant-A 不可凭裸 serial 吊销/查询 tenant-B 证书（与签发同源隔离，fail-closed）。
- **吊销与续期竞态**：吊销正在续期的证书 → epoch CAS（fencing）保证终态一致。
- **决策不跨 async**：证书签发授权结论不随事件携带；跨边界只传设备 identity，消费侧重决策。
- **device PII**：`device_id` 日志 redaction（#1695）；证书 serial / subject 写审计前按 redaction 包处理。

---

## Requirements *(mandatory)*

### Functional Requirements

**设备主体（US1，#1811）**
- **FR-001**: 系统 MUST 提供生产级设备主体签发器，铸造 `PrincipalKind=device`（subject=设备 ID、tenant 来自可信凭证），消除「develop 无生产 PrincipalDevice 来源」现状。
- **FR-002**: 系统 MUST 提供 super-admin 主体签发，`RowScope=all` 为显式独立路径并强制审计。
- **FR-003**: service-token MUST NOT 派生 `PrincipalDevice`（概念隔离）；caller-cell ID ≠ device subject。

**证书签发底座（US2）**
- **FR-004**: 系统 MUST 提供 `Signer` 接口（`Sign(ctx, CertRequest) (IssuedCert, error)` + `TrustBundle`），**唯一**铸造设备证书入口；`adapters/softca` 实现，私钥（`crypto.Signer`）永不出 adapter 边界。
- **FR-005**: 系统 MUST 提供 `Authorizer`（`AuthorizeEnroll(ctx, EnrollmentClaim) (SignConstraints, error)`），**独立于** `Signer`，复用既有 PDP；nil grant fail-closed deny；返回 SignConstraints（max TTL / 允许 SAN）由 Signer 强制。
- **FR-006**: 系统 MUST 提供 `CertRequest`/`IssuedCert` sealed 类型（unexported 字段 + 唯一构造器），包外不可字面量伪造证书材料。
- **FR-007**: 系统 MUST 提供 `RevocationStore`（`Revoke(ctx, CertScope, serial, reason)` / `RevocationList(ctx, CertScope)` / `Tidy(ctx, CertScope, before)`），其中 `CertScope` 是与签发**同源**的 typed 隔离值（`{tenant, issuer, device}`，复用 `CertRequest` 的 scope 维度）。吊销与 CRL 查询 MUST 带 `CertScope` 强制 typed 位置参——**漏传为编译错**（Hard，对齐 tenant typed-param 范式）；跨租户/跨 issuer 吊销或查询 fail-closed（绝不凭裸 serial 跨隔离域操作）。
- **FR-008**: 系统 MUST 提供 `certlifecycle.Reconciler`（实现冻结的 `reconcile.Reconciler`），驱动状态机 requested→issued→active→near-expiry→renewing→{rotated|revoked|expired}，**复用** `kernel/reconcile.Loop`（leader/fencing/system-identity）+ `runtime/command`（设备往返腿），不新造控制环。
- **FR-009**: 续期窗口 MUST 经注入 `clock.Clock`（禁 `time.Now()`）+ jitter；签发 epoch 单调，跨副本经 `LeaseToken.Epoch` fencing 写路径 CAS。
- **FR-010**: 证书签发 MUST 发 L2 事实 `deviceidentity.cert-issued.v1` / `cert-revoked.v1`（outbox），供 ZT 信任根 / 审计消费侧消费。

**EST 前端（US3）**
- **FR-011**: 系统 MUST 提供 EST(RFC 7030) 操作 `cacerts` / `simpleenroll` / `simplereenroll`（PKCS#10 in / PKCS#7 out，wiring `Authorizer`→`Signer`），挂在所属 cell 的**版本化路径**下（`/api/v{N}/deviceidentity/est/{cacerts,simpleenroll,simplereenroll}`），**不**用顶级裸 EST 路径（对齐 api-versioning「无顶级命名空间，端点挂所属 cell 版本前缀」）。
- **FR-012**: EST 鉴权 MUST 用**专用 enrollment-credential / device-bootstrap-token** scheme（首次 enroll）+ 现证书 mTLS client-auth（reenroll）两条显式声明路径，缺鉴权 fail-closed 4xx。**不复用** setup `auth.bootstrap:true`（FMT-28 限其只在 `^/api/v\d+/[^/]+/setup/admin$`，EST 非 setup-admin 路径，复用会被 fail-closed 拒绝）；若未来确需复用 bootstrap 凭据，MUST 先出 ADR 扩展 FMT-28（本 epic 不走此路）。

**中立契约（US4）**
- **FR-013**: 系统 MUST 定义 4 个中立设备契约（domain shorthand：`deviceidentity` / `devicestate` / `devicecompliance` / `remotecommand`），按仓库契约布局单源 `{kind}/{domain-path}/{version}/` 落 **kind-qualified** 路径——`contracts/http/deviceidentity/{enroll,renew,revoke,status}/v1`、`contracts/event/deviceidentity/{cert-issued,cert-revoked}/v1`、`contracts/command/deviceidentity/rotate/v1`、`contracts/http/devicestate/v1`、`contracts/http/devicecompliance/v1`、`contracts/command/remotecommand/v1`。各含 contract.yaml（鉴权/caller/schema）+ generated code + slice 派生 registration；走契约扇出闭环。「4 个中立契约」是 domain 简写，**不是**目录真源（目录必带 kind 维度）。
- **FR-014**: `deviceidentity/v1` MUST 承载证书 enroll/renew/revoke + 当前证书状态；`remotecommand/v1` MUST 承接既有 `command.devicecommand.enqueue.v1` 形状（PR-9 迁移）。

**迁移（US5）**
- **FR-015**: iotdevice MUST 迁移到框架证书底座（deviceregister 经 `Signer` 签真实初始证书；续期经 `certlifecycle`）；`rotate-cert` 字符串 MUST 提升为真契约 `command.deviceidentity.rotate.v1`。
- **FR-016**: 设备命令 Enqueue MUST 加设备粒度授权（#654），设备不可操作他设备命令。

**治理 + 路线图（US6）**
- **FR-017**: 每条 enforcement MUST 与守卫（archtest / sealed type / governance rule）同 PR 落地，按 Hard/Medium 评级，附反向自检 + godoc/ADR。
- **FR-018**: MUST 出路线图修订 ADR：收口 #995（进框架）、修订 winmdm PRD pkicell 落点注记、plan-D §10 例外、final-form 增列 PKI 能力、更新 #1051/#1052 锚点；冲突段落同改动重写，重评威胁矩阵。

### Key Entities

- **PrincipalDevice（签发）**：生产铸造的设备主体（kind=device + subject + tenant），派生 `RowScope=device`。
- **CertScope**：与签发同源的 typed 隔离值（`{tenant, issuer, device}`），Sign / Revoke / RevocationList / Tidy 共用；裸 serial 不可跨隔离域操作。
- **CertRequest**：协议无关签发请求 sealed 类型（CSR + DeviceSubject + SANs + TTL + Usages；scope 维度即 `CertScope`）；EST/SCEP/XCEP 解码到此。
- **IssuedCert**：签发结果 sealed 类型（证书 + 链 + serial + notAfter + epoch）。
- **Signer**：CSR→证书的 CA 接缝（adapter，私钥不出边界）；**唯一**签发入口。
- **Authorizer**：设备能否拿证书的决策（复用 PDP），返回 SignConstraints；独立于 Signer。
- **RevocationStore**：吊销 + CRL + tidy，方法带 `CertScope` 强制 typed 位置参（跨租户/issuer fail-closed）。
- **CertLifecycle.Reconciler**：复用 kernel/reconcile 的证书生命周期状态机。
- **4 中立契约**：deviceidentity（证书之家）/ devicestate / devicecompliance / remotecommand。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 设备经生产签发器获得 `PrincipalDevice`，端到端派生 `RowScope=device`（US1 三类主体 e2e 通过）；service-token 不可冒充 device。
- **SC-002**: 框架可经内置 softca 签发 / 续期 / 吊销真实 X.509 证书，**无外部 CA 依赖**；签名私钥不出 adapter（archtest 守卫）。吊销/CRL 查询带 `CertScope`，跨租户/issuer 吊销/查询 100% fail-closed（漏传 scope 编译失败）。
- **SC-003**: 包外伪造证书值或绕过 `Sign` 入口**编译/archtest 失败**（sealed 构造 + 单一 funnel，Hard）；`Authorizer` 与 `Signer` 为两接口。
- **SC-004**: 证书生命周期复用 `kernel/reconcile`（无新控制环）；多副本 fencing + 队列 active-uniqueness 保证至多一次有效签发；签发失败不损坏既有证书（fail-closed 有测试覆盖）。
- **SC-005**: 设备经版本化 EST 端点（`/api/v1/deviceidentity/est/*`）开箱注册/续期；首次用 enrollment-credential、续期用现证书 mTLS；缺鉴权或 setup-bootstrap 冒充 fail-closed（FMT-28 边界）。
- **SC-006**: 4 个中立契约就位，契约级测试 + 扇出闭环全绿；破坏式变更走新版本目录。
- **SC-007**: iotdevice 端到端经框架能力签发真实证书；`rotate-cert` 走真契约；设备粒度鉴权拒绝越权 enqueue（#654 关闭）。
- **SC-008**: `make verify`（含 archtest）全绿；每条新 invariant 有反向自检 + AI-robust 评级登记于其 archtest godoc；路线图 4 份文档无残留「PKI 不在框架」表述（#995 收口）。

## Assumptions

- 内置软 CA（`adapters/softca`）足以支撑 dev/test + 小型 ZT；step-ca/Vault adapter 留接口位，后续 PR（不在本 epic）。
- 不引入外部 PDP；证书签发授权复用 accesscore 既有 policy 引擎。
- 不向后兼容：`rotate-cert` 字符串直接换真契约，无 shim；iotdevice 直接迁移。
- 设备凭证通道（设备 token / 证书 mTLS）首版由 #1811 签发器提供 device token；证书 mTLS 派生 `PrincipalDevice` 为统一终态（EST `/simplereenroll` 已用 client-cert），二者本 epic 内可并存。
- 不建 `mdm/`/`zerotrust/` module（plan-D §10）；本 epic 全部落 core（顶层 module）。
- Windows 专属 XCEP/WSTEP/SCEP/MS-MDE 留 winmdm 消费方；本 epic 只到 EST。
- #1890（service mTLS/SPIFFE identity）邻接但独立；softca 后续可被其复用。
