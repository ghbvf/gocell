# ADR 049 — mTLS server builder + peer-identity ctx hook

Date: 2026-05-29
Status: Accepted
Related: WM-32 (#631) — backlog_later §7

## Context

GoCell 之前的 mTLS 支撑只到 "presence-only guard"：`kernel/auth.AuthMTLS{}`
是 sealed marker；`runtime/bootstrap.WithListenerTLS(*tls.Config)` verbatim
注入；phase0 `validateMTLSTLSConfig` 校验 `ClientAuth >= VerifyClientCertIfGiven`
+ `ClientCAs != nil`；`mtlsMiddleware` 仅 `r.TLS == nil ||
len(r.TLS.PeerCertificates) == 0 → 401`。

业务侧（cells/、examples/）0 个 peer cert 消费者：即使握手成功，handler 也拿
不到对端证书身份，使 AuthMTLS 实际只是"是 mutual 握手"的 binary 标志，丢失
mTLS 最重要的应用层属性 peer identity。

WM-32 trade-off 明文：**大规模 mTLS 卸载在 K8s/Service Mesh 解决，框架仅提
供构建器**。本 ADR 锁定 "framework enabling minimum"，避免后续 reviewer 把
"该不该做 X" 重复评估为 WM-32 范围。

## Decision

1. **PEM-bytes 输入**：新 `runtime/http/tlsutil.NewServerMTLSConfig(certPEM,
   keyPEM []byte, clientCAs *x509.CertPool) (*tls.Config, error)` 接 PEM 字节
   不接 file path——runtime/ 层不引入 FS 依赖，callers 自己 `os.ReadFile`。
2. **MinVersion = TLS 1.3**：builder 出厂 config 硬编 `cfg.MinVersion =
   tls.VersionTLS13`。fail-closed 默认；与 spiffe/go-spiffe `spiffetls.
   MTLSServerConfig` 一致；V1.1 启动期无 legacy client 包袱。
3. **ClientAuth = RequireAndVerifyClientCert + 必填 ClientCAs**：builder
   出厂 config 同样硬编；nil pool 返回 error。
4. **PeerIdentity 字段集 curated**：`pkg/ctxkeys.PeerIdentity{Subject pkix.
   Name, DNSNames []string, URIs []*url.URL}`——3 字段。**不暴露 raw
   `*x509.Certificate`**，下游耦合 x509 内部"在 type system 不可表达"。字段
   集 curation 仅 godoc 约定，不立 archtest enforcement（Cx2 scope；若将
   来需 Hard 守卫，按 ai-robust §适用范围单独评级）。
5. **PeerIdentity 放 `pkg/ctxkeys/`**：与 `request_id` / `real_ip` / `trace_id`
   同族 request-scope；让 cells/ 与 runtime/ 共同消费时无 import 环。
6. **不引入 spiffe/go-spiffe 依赖**：URIs 保留 raw `*url.URL`，业务侧若需
   SPIFFE typed-ID 自行调 `spiffeid.FromURI`。
7. **不做动态证书 reload / OCSP / `GetCertificate` callback / 出站 client
   builder**：K8s/mesh 领域 + 当前 0 消费者。

## Consequences

- 业务 handler `ctxkeys.PeerIdentityFrom(ctx)` 一行拿身份；无 *x509 依赖
- 不向后兼容：`runtime/bootstrap/auth_plan_apply.go` 内 `mtlsMiddleware()`
  函数体 + section header + history comment 删除，等价覆盖迁移到
  `runtime/http/middleware/mtls.go`，老 test `TestMtlsMiddleware_PeerCertPresence`
  同 PR 删除并改写为 `mtls_test.go` + 集成 sub-test
- `runtime/bootstrap/auth_plan_describe.go::chainContainsInternalGuard` godoc
  同步更新："mTLS alone 不建立 caller_cell allowlist mapping（service-token
  仍是 sole producer）"——保留正确语义、消除 "mTLS 不产生任何 identity"
  的误读
- 未来若需 SPIFFE helper，应放 cells/ 或新 `runtime/spiffe/`，不污染
  pkg/ctxkeys

## Alternatives rejected

- **PEM file-path 入参**——runtime/ 引入 FS 依赖，testability 差
- **暴露 raw `*x509.Certificate`**——下游耦合 x509 内部，PeerIdentity 字段
  演化困难；k8s `UserConversionFunc`、SPIFFE `Authorizer` 都用 curated 视图
- **bootstrap.WithListenerMTLS(...) 便捷 option**——caller 两行可达
  (`tlsutil.NewServerMTLSConfig + WithListenerTLS + auth.AuthMTLS{}`)，YAGNI
- **k8s `UserConversionFunc` 可插拔 identity extractor**——Cx2 scope 过重，
  framework 不应预设 multi-tenant 认证模型
- **MinVersion = TLS 1.2**（与 k8s apiserver 默认一致）——为了兼容老 client，
  但 V1.1 启动期无该约束，TLS 1.3 fail-closed 更稳

## AI HARD 形态分级（implementation 层面）

| 约束 | 形态 | 等级 |
|------|------|------|
| `runtime/http/middleware/mtls.go` 不构造 `kauth.AuthMTLS{}` | 既有 AUTH-PLAN-04 archtest | Hard |
| 错误信息不含裸 `"mtls"` 小写字面量 | 既有 AUTH-PLAN-01 archtest | Hard |
| `MTLS()` 函数与 kernel/auth 包零交互 | 函数签名零 auth 类型参 + body 只依赖 stdlib+pkg/* | Hard (type-system) |
| `runtime/http/tlsutil/` 只接 PEM bytes | 函数签名 `[]byte` 类型 | Hard (type-system) |
| `NewServerMTLSConfig` 出厂 TLS1.3 + RequireAndVerify | builder body 硬编字段写入；返回 stdlib `*tls.Config` 可变结构 | Soft (godoc 约定) — 升 Hard 不可行（`crypto/tls.Server` 接 `*tls.Config`，无 opaque wrapper 通道） |
| PeerIdentity 字段集 curated（不含 `Raw *x509.Certificate`） | pkg/ctxkeys 包边界 = single sanctioned holder | 结构上 Hard |
| `applyListenerAuthChain` `case AuthMTLS:` 唯一 caller | sealed `ListenerAuth` interface + type-switch | Hard (type-system) |

> Row 5 Soft 的天花板限制：Go 生态约束。`crypto/tls.Server`/`http.ServeTLS` 强制接收 `*tls.Config` 标准类型；自定义 opaque wrapper 无法注入握手层。godoc 警告（见 `server.go::NewServerMTLSConfig`）是该 surface 唯一可行 enforcement。

Row 5 Soft 属 Go 生态技术上限（`crypto/tls.Server` 接受 `*tls.Config` 标准类型，无 opaque wrapper 通道），物理不可行升 Hard，符合 ai-robust §"Soft 严禁立项"之豁免条件。其余行均为 Hard。

ref: spiffe/go-spiffe v2/spiffetls/tlsconfig — MTLSServerConfig（curated 字段）
ref: kubernetes/apiserver pkg/server/secure_serving.go — server cert plumbing
ref: kubernetes/apiserver pkg/authentication/request/x509/x509.go — CN/SAN→user 提取
