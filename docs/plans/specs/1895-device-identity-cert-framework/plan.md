# Implementation Plan: 设备身份与证书框架化 + MDM/ZT 前期地基

**Branch**: `1895-device-identity-cert-framework` | **Date**: 2026-06-12 | **Spec**: [spec.md](./spec.md) | **Epic**: #1895

## Summary

把设备证书从 iotdevice 示例提升为框架能力：在 `runtime/` 加证书签发底座（`Signer`/`Authorizer`/`RevocationStore` 接口 + sealed 请求模型 + `certlifecycle` reconciler），在 `adapters/softca` 加内置软 CA，在 `runtime/http` 加 EST 前端；同步落地 MDM/ZT 前期地基（4 个中立设备契约 + 生产设备主体签发器 #1811）；并以一份 ADR 收口 #995 + 修订已锁定路线图。技术路线对标 SPIFFE/SPIRE + cert-manager + step-ca：控制面属框架、签名私钥在 adapter、协议前端在边缘、`Authorize` 与 `Sign` 分离。**最大复用既有 `kernel/reconcile` + `runtime/command` + PDP，不新造控制环。**

## Technical Context

**Language/Version**: Go 1.24（go.work 多 module：core 顶层 + per-adapter module）
**Primary Dependencies**: 标准库 `crypto/x509`·`crypto/ecdsa`·`encoding/pem`·`crypto/tls`；既有 `kernel/reconcile`·`runtime/command`·`runtime/auth`·`pkg/tenant`·`pkg/errcode`·`kernel/clock`。**不引入**外部 PKI 库（softca 用标准库；step-ca/Vault 留后续 adapter）。
**Storage**: PostgreSQL（device_certs / ca_certs / cert_revocations 表 + RLS）；softca CA key 经 adapter 托管（dev：内存/文件；后续：KMS/HSM adapter）。
**Testing**: table-driven 单测（kernel/runtime ≥90% / 其余 ≥80%）；契约级测试；testcontainers 集成（softca 签发 + EST 往返 + certlifecycle reconcile）；archtest 反向自检。
**Target Platform**: Linux server（core module）。
**Project Type**: 框架底座（library）+ 平台契约 + 示例迁移。
**Performance Goals**: 单次 softca 签发 p95 < 10ms（无网络）；certlifecycle resync sweep 有界（按 cutoff 范围扫描，不全表）。
**Constraints**: 私钥永不出 adapter；`Sign` 唯一签发入口；reconcile 多租户须自带 tenant 维度（system identity 清空 tenant）；EST 鉴权 fail-closed。
**Scale/Scope**: ~11 PR / ~15,100 行；新增 3 个 runtime 包（certsigning/certlifecycle/http·est）+ 1 个 adapter module（softca）+ 4 个契约 + 1 个 auth 签发器 + iotdevice 迁移 + 2 ADR。

## Constitution Check

*GATE：Phase 0 前必过；Phase 1 设计后复检。*

| 宪法约束 | 本 epic 落点 | 通过 |
|---|---|---|
| 分层依赖（kernel 不依赖 runtime/adapters） | 证书底座放 `runtime/`（需 crypto/x509 + reconcile/command 上层 wiring），**不**入 kernel；softca 入 adapters 实现 runtime 接口 | ✅ |
| Cell 只经 contract 通信 | 4 中立契约定义跨边界 wire；证书事实经 outbox L2 事件 | ✅ |
| 一致性等级 | certlifecycle = L4（设备长延迟闭环，同 devicecertrenewal）；cert-issued 事件 = L2；EST enroll = L2（本地 tx + outbox） | ✅ |
| 错误用 errcode；无导出 `Err* =` | 新 cert 错误走 errcode；新增 `ERR_CERT_` 前缀须注册 + golden（ERRCODE-PREFIX-OWNERSHIP-01） | ✅ |
| 认知复杂度 ≤15 / 覆盖率 | 每 PR TDD；reconciler 状态机切小函数 | ✅ |
| AI-robust 新机制 ≥ Medium | cert sealed 构造 + 单一 Sign funnel（Hard）；lifecycle fencing（Hard，复用既有）；EST 鉴权边界 / Authorizer-Signer 分离（Medium archtest）；**无 Soft** | ✅ |
| 引入约束同 PR 闭环 | 每 PR 自带守卫 + 反向自检 + godoc/ADR（不甩 backlog） | ✅ |
| id no-dash | `softca`/`certsigning`/`certlifecycle`/`deviceidentity` 全 concat | ✅ |
| 契约扇出闭环 | PR-3/4/9 出 implementation matrix（contract/generated/cell-slice/tests/docs） | ✅ |

**无违规需 Complexity Tracking。**

## Project Structure

### Documentation (this feature)

```text
docs/plans/specs/1895-device-identity-cert-framework/
├── plan.md          # 本文件
├── research.md      # 探索结论（OSS 对标 + 现状 + issue 收口）
├── spec.md          # 功能规格（US1-US6 / FR / SC）
└── tasks.md         # PR 分解 + 依赖波次
```

### Source Code (repository root)

```text
runtime/
├── certsigning/                 # PR-5：接口 + sealed 类型 + funnel
│   ├── signer.go                #   Signer / TrustBundle
│   ├── authorizer.go            #   Authorizer / EnrollmentClaim / SignConstraints（独立于 Signer）
│   ├── request.go               #   CertRequest / IssuedCert sealed（unexported + 唯一构造器）
│   ├── revocation.go            #   RevocationStore
│   └── doc.go                   #   §Enforced invariants（CERT-SIGN-FUNNEL / CERT-VALUE-SEALED）
├── certlifecycle/               # PR-7：生命周期 reconciler（复用 kernel/reconcile）
│   ├── reconciler.go            #   reconcile.Reconciler 实现；状态机；jitter 续期
│   ├── repository.go            #   DeviceCertRepository 接口（scan near-expiry / persist）
│   └── doc.go
└── http/est/                    # PR-8：EST(RFC 7030) 前端
    ├── handler.go               #   /cacerts /simpleenroll /simplereenroll
    └── doc.go

adapters/softca/                 # PR-6：内置软 CA（per-adapter go module）
├── go.mod
├── signer.go                    #   实现 certsigning.Signer（crypto.Signer 托管，私钥不出）
├── revocation.go                #   实现 RevocationStore（CRL）
└── ca.go                        #   CA 层级 + key 托管接口（dev mem/file；KMS 留后续）

runtime/auth/                    # PR-2：设备主体签发器（#1811）
├── deviceprincipal.go           #   生产 PrincipalDevice / super-admin 铸造
└── (扩 principal.go / rowscope.go wiring)

contracts/                       # PR-3/4：4 中立设备契约
├── http/deviceidentity/{enroll,renew,revoke,status}/v1/
├── event/deviceidentity/{cert-issued,cert-revoked}/v1/
├── command/deviceidentity/rotate/v1/      # PR-9：rotate-cert 真契约
├── {devicestate,devicecompliance}/v1/
└── command/remotecommand/v1/

examples/iotdevice/              # PR-9：迁移到框架证书底座
└── cells/devicecell/...         #   deviceregister 经 Signer；certlifecycle 替代字符串命令；#654 RBAC

cellmodules/ + bootstrap         # PR-10：composition wiring + WithCertSigner/WithCertLifecycle + readyz
docs/architecture/               # PR-1 路线图修订 ADR；PR-11 跨层 ADR
```

**Structure Decision**：证书底座入 **`runtime/`** 而非 kernel（需 `crypto/x509` + 在 reconcile/command 之上 wiring，kernel 只依赖 stdlib+pkg），对标 `kernel/reconcile` 定义接口、`adapters/{redis,postgres}` 实现 `LeaderElector` 的既有范式——本 epic：`runtime/certsigning` 定义 `Signer`、`adapters/softca` 实现。证书生命周期**复用** `kernel/reconcile.Loop`，不在 kernel 加 cert 概念。中立契约入 `contracts/`（平台跨 Cell 边界）。设备主体签发器入 `runtime/auth`（authn 边界）。**不**新建 corecell（证书底座是 primitive 不是平台 Cell；winmdm 的 `pkicell` 仍是未来 mdm module 的消费方 Cell，本 epic 只下移其依赖的框架原语）。

## Complexity Tracking

> 无宪法违规需 justify。唯一需记录的张力：**路线图修订**——本 epic 推翻 #995「PKI 不在框架」+ winmdm PRD pkicell 落点（mdm module）+ #1051/#1052 门控日期。处置：PR-1 出 ADR 显式修订 4 份路线图文档 + 重评威胁矩阵；不静默改、不留冲突段落（遵循 AI-robust §审查「ADR amendment 同步重评」+ memory「先沟通再落文档」——范围已与用户对齐）。
