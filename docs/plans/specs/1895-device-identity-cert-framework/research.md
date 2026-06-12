# Research: 设备身份与证书框架化（探索结论）

**Date**: 2026-06-12 · **Epic**: #1895 · 来源：ship 探索阶段（2 个 explorer agent：OSS 对标 + 现状/issue 收口）。

## 0. 决策摘要（已与用户对齐）

| 维度 | 决策 |
|---|---|
| 证书深度 | 底座 + 内置软 CA + EST 前端 |
| 前期范围 | 4 中立契约 + 设备身份签发器（#1811） |
| epic 结构 | 1 合并 epic + 路线图 ADR；每 PR ≤2000 行 |

## 1. 现状真相（current-state）

- **零 PKI 代码**：repo 中无 CA / CSR / CRL / 签名 / 私钥生成。所有 `crypto/x509` 命中 = TLS 传输层消费已编码 PEM（`runtime/http/tlsutil/server.go`）或 `_test.go` 自签链（grpc/mqtt/mtls）。
- **今天的「证书」** = devices 行 `cert_epoch`(int64, ≥1) + `cert_expires_at`(timestamp) 两列（migration 056/057/062）。`deviceregister/service.go` 注册时 stamp `now+90d`（仅时间戳，无证书材料）。
- **续期** = `examples/iotdevice/.../devicecertrenewal` stateless reconciler，扫 `cert_expires_at<=now+Threshold` 发 `CommandType="rotate-cert"` 字符串（载于 `command.devicecommand.enqueue.v1`，payload `{epoch,notAfter}`）。正确性（至多一活跃 rotate）由**队列 active-uniqueness**owned（#1820），非 producer state。
- **可复用框架原语（成熟）**：
  - `kernel/reconcile`：`Reconciler{Reconcile(ctx,Request)(Result,error)}`（冻结）、Builder 唯一构造、TickerTrigger resync-all、leader-elect + `LeaseToken.Epoch` fencing + `FencedWriter`、system identity 清空 tenant（#1821）。
  - `runtime/command`：`EmitAsync[T]`（位置参 subject+commandID）、`WithActiveUniqueness(deadline)`、Claimer dedup key（tenant 来自 entry principal envelope）。
  - `runtime/auth`：service-token HMAC，`X-Tenant-ID` 折入签名材料（#1717）；mTLS 传输层（`tlsutil` + `middleware/mtls` + `ctxkeys.PeerIdentity` 冻结）。
  - `pkg/tenant`：`RowVisibility` sealed，`PrincipalDevice→RowScope=device`（`runtime/auth/rowscope.go`）。
- **关键缺口**：develop **无生产 authn 铸造 `PrincipalDevice`/super-admin**（rowscope.go:19-22 明示「type-foundation entries, no production issuer」）= **#1811，证书底座硬前置**。

## 2. OSS 对标（SPIFFE/SPIRE + cert-manager + step-ca 一致结论）

8 个普适接缝，三家画同一条线：

| 接缝 | 归属 | Go 形状 | CP/DP |
|---|---|---|---|
| Signer/Issuer（CSR→证书） | 框架定义接口，adapter 实现 | `Sign(ctx,CSR,opts)(Chain,error)` over `crypto.Signer` | DP（adapter CA） |
| 请求/证书模型 | 框架 | 协议无关 CertRequest + IssuedCert | CP |
| 生命周期状态机 | 框架 | requested→issued→active→nearExpiry→renewing→{revoked\|expired} | CP（reconcile） |
| 续期 reconcile | 框架 | 周期扫 notAfter 入队续期（level-triggered，jitter 70-90% 寿命） | CP（= GoCell 既有 reconcile） |
| 吊销/CRL/OCSP | adapter | `Revoke(scope,serial,reason)` + `RevocationList(scope)` + `Tidy` | DP |
| 注册协议 adapter | 消费方边缘 | EST/SCEP/ACME/XCEP 终结 wire → 内部 CertRequest | edge |
| 证明/身份绑定 | 框架 | `Authorize(claim)(grant,error)`——**与 Sign 分离** | CP（GoCell 既有 PDP） |
| 私钥托管 | adapter | `crypto.Signer`，**私钥永不跨边界** | DP |

**三条承重洞见**：①签名≠授权（step-ca `AuthorizeSign` vs `Authority.Sign`；cert-manager `Check` vs `Sign`；SPIRE 证明 vs `ServerCA`）→ GoCell 必须 `Authorize`/`Sign` 两接口。②私钥永不跨框架边界（SPIRE `KeyManager.SignData`，key 留 plugin）= Go `crypto.Signer`。③请求对象协议无关（cert-manager `CertificateRequestObject` 抽象 CR + CSR）；EST/SCEP/ACME/XCEP 都归约为「PKCS#10 in / PKCS#7 out」（RFC 7030 §4）。

**层放置**：每家都把「生命周期+请求模型+续期调度」放框架、「签名私钥」放 adapter、「wire 协议」放边缘。GoCell 已有其中 4 个接缝（reconcile/command/PDP/RowScope），缺的是 ①真实 Signer ②泛化的 lifecycle reconciler ③EST 前端 ④生产 device 主体。

`ref:` spiffe/spire `pkg/server/ca/ca.go`·keymanager/upstreamauthority proto；cert-manager `pkg/apis/certmanager/v1/types_{issuer,certificaterequest}.go`·issuer-lib `controllers/signer`；smallstep/certificates `authority/{tls.go,provisioner/provisioner.go}`；hashicorp/vault `api-docs/secret/pki`；kubernetes `client-go/util/certificate/certificate_manager.go`（jitter 70-90%）；IETF RFC 7030（EST）；MS-XCEP/MS-WSTEP。

## 3. 推荐接口签名（GoCell 风格）

```go
// runtime/certsigning — Authorize 与 Sign 分离；私钥在 adapter。
type Signer interface {
    Sign(ctx context.Context, req CertRequest) (IssuedCert, error)
    TrustBundle(ctx context.Context) (roots [][]byte, err error)
}
type Authorizer interface { // 独立于 Signer，复用 PDP；nil grant=deny
    AuthorizeEnroll(ctx context.Context, claim EnrollmentClaim) (SignConstraints, error)
}
type RevocationStore interface { // CertScope = {tenant,issuer,device}，与 Sign 同源隔离；漏传编译错（Hard）
    Revoke(ctx context.Context, scope CertScope, serial string, reason RevocationReason) error
    RevocationList(ctx context.Context, scope CertScope) (crlDER []byte, err error)
    Tidy(ctx context.Context, scope CertScope, before time.Time) (purged int, err error)
}
// CertScope/CertRequest/IssuedCert sealed（unexported + 唯一构造器；typed tenant+issuer+device，非裸 string/serial）
```
`certlifecycle.Reconciler` 实现冻结的 `reconcile.Reconciler`，`clock.Clock` 位置参 + `MustHaveClock`，复用 `kernel/reconcile.Loop` + `runtime/command`，不新造控制环。EST 前端在 `runtime/http/est` 终结 PKCS#10/PKCS#7，调用框架 `Signer`。

## 4. issue 收口 / 路线图冲突

- **收口** #995（X.509 框架化评估）：结论=进框架（推翻「不在框架 / 消费方自建」）。
- **关闭** #1811（设备主体签发器，硬前置）、#654（DEVICE-ENQUEUE-RBAC）。
- **部分提前** #1051（pkicell 框架底座下移 core；winmdm 仅留 WSTEP/SCEP 前端）、#1052（4 中立契约——路线图唯一允许提前项）。
- **关联** #1870（cert terminal-feedback，PR-7 续期主路径）、#1890（mTLS/SPIFFE service identity，邻接独立，softca 后续可复用）、#822/#1845/#1695/#1591（设备主体硬化）。
- **路线图冲突**（PR-1 ADR 修订）：#995「不在框架」立场、winmdm PRD pkicell 落点（mdm module）、plan-D §10「v1.0 前不建 mdm/」、#1051/#1052 门控日期、final-form 未列 PKI。**部分支持**：路线图自身把「pkicell WSTEP」列为 v1.0 P0（gocell-platform §3「P0 阻塞项」callout），与本提前一致。

## 5. 治理要求

- AI-robust：cert sealed 构造 + 单一 `Sign` funnel（Hard）；lifecycle fencing 复用 reconcile（Hard）；EST 鉴权边界 / Authorize-Sign 分离（Medium archtest）。**无 Soft**。
- 每条约束**同 PR 闭环**（静态守卫 + 反向自检 + godoc/ADR）。
- 契约扇出（PR-3/4/9）出 implementation matrix；破坏式 wire 走新版本目录。
- id no-dash（softca/certsigning/certlifecycle/deviceidentity）；私钥不出 adapter（`CERT-PRIVATE-KEY-CUSTODY-01`）。
