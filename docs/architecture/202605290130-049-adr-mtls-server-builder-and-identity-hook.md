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
   `*x509.Certificate`**：给定当前字段集，handler 无法从 PeerIdentity 读到
   不存在的字段，下游对 x509 内部表示的耦合不可达。字段集本身由 reflect 字段
   冻结 archtest `PEER-IDENTITY-FIELDS-FROZEN-01` 守卫（Hard：锁字段名/类型/
   可见性，禁 embedding、禁 raw-cert 字段，附非空 reverse self-check）——
   加/删字段或改类型即 CI 红。middleware 侧 `ownedPeerIdentity` 对 9 个
   pkix.Name 切片 + 每个 `*url.URL` + DNSNames 做防御性深拷贝，使 ctx 中的
   身份与连接缓存的证书完全隔离（见 `mtls.go`）。
5. **PeerIdentity 放 `pkg/ctxkeys/`**：与 `request_id` / `real_ip` / `trace_id`
   同族 request-scope；让 cells/ 与 runtime/ 共同消费时无 import 环。
6. **不引入 spiffe/go-spiffe 依赖**：URIs 保留 raw `*url.URL`，业务侧若需
   SPIFFE typed-ID 自行经 `spiffeid.Parse`（单 ID）/ `spiffeid.CellSetFromURIs`（证书 URI SAN 集合）解析。
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
| `NewServerMTLSConfig` 出厂 TLS1.3 + RequireAndVerify | emit 形态：builder 唯一成功路径无条件硬编字段（无参数/分支可产出弱 config）；post-return：返回 stdlib `*tls.Config` 可变结构 | emit-time **Hard**（type-system：唯一成功路径硬编）+ post-return-mutation **Soft**（`crypto/tls.Server` 接 `*tls.Config`，无 opaque wrapper 通道，升 Hard 不可行） |
| PeerIdentity 字段集 curated（不含 `Raw *x509.Certificate`） | reflect 字段冻结 archtest `PEER-IDENTITY-FIELDS-FROZEN-01`（锁字段名/类型/可见性，禁 embedding/raw-cert 字段，附非空 reverse self-check） | **Hard** |
| `applyListenerAuthChain` `case AuthMTLS:` 唯一 caller | sealed `ListenerAuth` interface + type-switch | Hard (type-system) |

> **Row 5 的 post-return-mutation Soft 天花板**：Go 生态约束。`crypto/tls.Server`/`http.ServeTLS` 强制接收 `*tls.Config` 标准类型；自定义 opaque wrapper 无法注入握手层，故 builder 返回后调用方仍可改写 `cfg.MinVersion`。godoc 警告（见 `server.go::NewServerMTLSConfig`）是该 post-return surface 唯一可行 enforcement。emit-time（builder 出厂即 TLS1.3 + RequireAndVerify）则是 type-system Hard——唯一成功路径无条件写入，无分支可产出弱 config。

> **sole-producer 有意不在 Hard 清单**：`runtime/http/middleware.MTLS` 是 `WithPeerIdentity` 今日唯一生产者，但这是约定、非 enforcement——跨包 `context.WithValue` setter 在 Go 无法 seal，caller-allowlist archtest 至多 Medium，且威胁模型显示 handler 伪造自身 ctx 身份无收益（不承重）。故**有意不立** sole-producer 守卫，也不声明其为 Hard（这正是修正前 Row 6 "single sanctioned holder = 结构上 Hard" 的错标，本版已移除）。

本表唯一 Soft 是 Row 5 的 post-return-mutation 分量：它不是新增 enforcement 立项，而是 stdlib `*tls.Config` 可变 API 带来的 residual risk；本 PR 不把该 post-return surface 声明为 Hard，也不新增 Soft enforcement。其余形态均为 Hard，含 Row 6 经 `PEER-IDENTITY-FIELDS-FROZEN-01` 实证落地。

ref: spiffe/go-spiffe v2/spiffetls/tlsconfig — MTLSServerConfig（curated 字段）
ref: kubernetes/apiserver pkg/server/secure_serving.go — server cert plumbing
ref: kubernetes/apiserver pkg/authentication/request/x509/x509.go — CN/SAN→user 提取
