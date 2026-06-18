# ADR: split 拓扑跨 cell 传输 mTLS 对等认证（ZT-1）

- 状态：Accepted
- 日期：2026-06-17
- Issue：#2263（ZT-1），Closes #2263
- 范围：**实施决策**。本 ADR 裁定 split 拓扑（`topology.groups`）跨 cell 同步调用的传输层安全
  策略，落地强制 mTLS + SPIFFE-ID 对等认证，消除 ADR `202606131142-1423` 威胁矩阵中登记的
  「无 mTLS 对等认证」残留缺口。**不覆盖**：证书自动颁发/轮换（follow-up）、SPIFFE Workload
  API / SPIRE agent dial（ZT-4，独立 roadmap）。

## 背景

ADR `202606131142-1423`（部署拓扑 seam）§`#2153 Amendment §残留` 明文记录：

> mTLS / SPIFFE-SVID 对等认证 → #2263。与 token 层 per-cell 身份正交且不构成死锁：
> token 层落地后 split 已密码学 fail-closed 可上（私网部署补偿明文），mTLS 是叠加的传输层
> 防御（端点对等认证 + wire 机密性），自带独立大工作流（证书/SVID 签发/分发/轮换）——
> 真正可排下一环，非循环借口。

在 #2153（per-cell provisioned keyring）合入后，token 层 callerCell 身份已密码学
fail-closed（`ProvisionedKeyring`，master 在 cell 进程缺席）。但当时的 split 调用仍为
**明文 HTTP**：service-token MAC 保证消息完整性，对 `callerCell` + principal 的伪造在密码学上
封闭，**但无传输层对等认证**——攻击者可实施中间人（MitM）或端点伪造：

- **MitM（传输层）**：private network 假设下，攻击者若能控制链路，可观测或注入明文请求，绕
  过 MAC 拦截到 token 后在 TTL 内重放（nonce store 仅有效 5min30sec，长存活链路可静默观测）。
- **端点伪造**：DNS/ARP 欺骗使 caller 连到攻击者控制的端点，伪 server 可收集到完整请求（含
  service token、principal 头），再 replay / proxy 到真 server。
- **wire 无机密性**：header 中的 principal 信息（`X-Gocell-Principal`）、business payload
  在网络上明文传输；即使 MAC 完整性正常，内容对网络可见者可读。

本 ADR 决策：对**非 loopback**的 split 跨 cell 调用，**强制 mTLS**，消除以上三条威胁，并
把「trusted/private network 作为补偿」这一 soft 约束移除，改为 fail-closed 的 hard 技术边界。

### 与 ADR 049 的关系

ADR 049（`202605290130`）落地了 server 侧 mTLS builder（`tlsutil.NewServerMTLSConfig`）+
PeerIdentity ctx hook（`pkg/ctxkeys.PeerIdentity`），并显式推迟了「出站 client builder」——
「K8s/mesh 领域 + 当前 0 消费者」。#2263 填补该推迟项：在 split topology 场景下，框架本身
就是那个「消费者」。本 ADR 的 client 端 mTLS config 构造与 ADR 049 server 端 builder 在设计
哲学上对称（PEM bytes in / sealed type out / Go-stdlib tls.Config）。

### GoCell 不 vendor go-spiffe 的决策

ADR 049 已裁定「不引入 spiffe/go-spiffe 依赖」（Decision §6）——URIs 保留 raw `*url.URL`，
业务侧需要 SPIFFE typed-ID 自行调 `spiffeid.FromURI`。#2263 遵循同一决策，在 framework 层
不引入 `github.com/spiffe/go-spiffe`：SPIFFE URI SAN 用 raw `net/url.URL` 解析，对等认证
逻辑由框架内置的 `framework/pkg/spiffeid` 最小类型 + 原生 URI SAN 解析实现。

## 核心决策

### C1 — SPIFFE-ID 身份约定

每个 cell 的 leaf 证书携带 SPIFFE URI SAN，格式：

```
spiffe://<trustDomain>/cell/<cellID>
```

其中：
- `trustDomain` 来自环境变量 `GOCELL_SPIFFE_TRUST_DOMAIN`（如 `gocell.internal`）。
- `cellID` 是 assembly 声明的 cell id（`no-dash` 约定，如 `accesscore`、`auditcore`）。
- leaf cert 必须同时声明 `ExtKeyUsageServerAuth` + `ExtKeyUsageClientAuth` 双 EKU——
  同一证书兼作 server cert（被 peer 验证）和 client cert（向 peer 出示）。

`framework/pkg/spiffeid` 提供最小 `SPIFFEID` 类型，内部存 cellID + trustDomain，
暴露 `ParseFromURI(uri *url.URL) (SPIFFEID, error)` + `CellID() string` + `TrustDomain() string`。
只做类型解析，不做 CA 信任决策。

### C2 — 客户端 mTLS config 构造（client side）

当 caller cell 拨向 remote peer 时，客户端 TLS config 的构造逻辑：

```
InsecureSkipVerify: true   // 禁用 Go stdlib 默认的 hostname 验证
VerifyConnection: func(state tls.ConnectionState) error {
    // 步骤 1：完整证书链验证（对 trust-root CA bundle 做 x509 chain verify）
    // 步骤 2：提取 leaf 证书的 URI SANs
    // 步骤 3：解析 SPIFFE ID，要求：
    //   - trust domain 与本 cell 配置一致
    //   - cellID == 预期 target cell（来自 assembly topology 声明）
    // 步骤 1-3 全通则握手成功，否则拒绝连接
}
```

**这不是 fail-open**：`InsecureSkipVerify:true` 关闭的是 Go 默认的 hostname 检查，被
**更强的** identity+chain 双重检查替代——chain 验证比 hostname 验证更严格（SPIFFE 规范的
标准做法，对标 go-spiffe `MTLSClientConfig` + `AuthorizeID` 组合）。

客户端 TLS config 由 sealed `tlsutil.ClientIdentity` 类型承载（unexported 字段），
唯一 minter = `celltls.Resolve`（topology-gated），包外不可 struct-literal 构造。

### C3 — 服务端 mTLS 接线（server side）

internal listener 在 split topology 下的服务端 mTLS 配置（ADR 049 已有构建器，本 PR 做接线）：

- `tlsutil.NewServerMTLSConfig(certPEM, keyPEM, clientCAs)` 出厂 config：
  TLS 1.3 + `RequireAndVerifyClientCert` + trust-root CA bundle 作 `ClientCAs`。
- 经 `bootstrap.WithListenerTLS(serverMTLSConfig)` 注入 internal listener。
- `kernel/auth.AuthMTLS{}` 在 auth chain 中提取握手 peer identity（经 ADR 049 的
  `middleware.MTLS()` 写入 `pkg/ctxkeys.PeerIdentity`）。

### C4 — cross-bind（对等认证的核心）

仅有传输层握手是不够的——TLS 握手只证明 peer 持有某个证书，不证明它就是 service token 里
声明的 `callerCell`。#2153 合入后 token 层 `callerCell` 已密码学 fail-closed；本 PR 在 internal
listener 的 auth chain 增加一个 middleware，将**两层身份绑定**：

```
peer cert SPIFFE cell ID  ==  service token callerCell claim
```

若不一致（如误配置、cert 与 key 部署到了错误的 cell 进程），请求被拒（401）。这使
「拿到有效 cert 但 token 用另一个 cellID 签」的误配置在首次请求时即暴露，而非仅运维侧才发现。

**为何 #2153 合入后才能做 cross-bind**：在 #2153 之前，`callerCell` 是 self-reported（任何
持 keyring 的进程都能声明任意 `callerCell`）；cross-bind 一个 self-reported field 没有密码学
价值。#2153 使 `callerCell` 密码学 fail-closed 后，cross-bind 才从「声明绑定」升级为「两层
密码学身份的交叉验证」，消除了两层独立攻击面的 union risk。

### C5 — 证书供应：静态 operator 供给（本 PR）

本 PR 采用**静态 PEM 文件路径**方式供应证书材料，通过四个环境变量：

| 变量 | 含义 |
|------|------|
| `GOCELL_TRANSPORT_TLS_CERT_FILE` | 本 cell 的 leaf cert PEM 文件路径（URI SAN `spiffe://<td>/cell/<cellID>`，双 EKU） |
| `GOCELL_TRANSPORT_TLS_KEY_FILE`  | 配套私钥 PEM 文件路径 |
| `GOCELL_TRANSPORT_TLS_CA_FILE`   | trust-root CA bundle PEM 文件路径（单根签发所有 cell cert） |
| `GOCELL_SPIFFE_TRUST_DOMAIN`     | SPIFFE trust domain（如 `gocell.internal`） |

四个变量**全有或全无（all-or-nothing）**：只设部分视同未设（fail-closed），不尝试降级明文。

框架在 composition root 阶段读取 PEM 文件，在内存中以 `tls.Certificate` + `x509.CertPool`
形态持有，不在请求路径重读文件。**不实现**文件变更监听/热重载（follow-up）。

### C6 — fail-closed 双闸（消除明文 fallback）

本 PR **移除**「非 loopback split 建议部署在可信网络」的 soft 约束，代之以两道 fail-closed 技术闸：

**闸 1：静态（`gocell validate`，构建时）**

新增 governance rule：`assembly.yaml` 中 `topology.groups` 的 group endpoint 若 **非 loopback
（localhost/127.x.x.x/::1）**，scheme 必须为 `https`（含裸 `host:port` 写法的升级：裸
`host:port` 不被视为 https，非 loopback 时 validate 拒绝）。

loopback endpoint 保持不变（本地多进程开发，明文可接受）。

**闸 2：运行时（启动期 fail-fast）**

`cellmodules/celltls.Resolve` 在 topology 含非 loopback remote cell 时：
- 若 TLS material（四环境变量）缺失 → 启动 fail-fast，明确错误信息。

`cellmodules/celltransport.Resolve` 逐 peer 检查：
- 若 peer endpoint 非 loopback 且无 client identity → fail-fast，不降级明文。

当 TLS material 存在时（operator opt-in），**无论 endpoint 形式如何均 honor**——即 loopback
remote + TLS material 配置仍会启用 mTLS（不静默忽略 TLS config）。

## 威胁矩阵更新（对照 ADR 1423 §安全模型）

| 威胁 | 本 PR 前状态 | 本 PR 后状态 | 评级 |
|------|------------|------------|------|
| **中间人 / wire 窃听** | 明文 HTTP，仅 private network 补偿（Soft） | TLS 1.3 强制加密，非 loopback 无明文路径 | Hard（技术闸，非文档约束） |
| **端点伪造（DNS/ARP 欺骗）** | 无传输层 peer auth，仅 private network 补偿 | mTLS 双向证书验证 + SPIFFE-ID 精确匹配，伪端点无有效 cert → 握手失败 | Hard |
| **cert 与 token callerCell 错配（误配置）** | 不存在（无 cert 层） | cross-bind middleware 在首请求即 401，不静默通过 | Medium（middleware 可被绕过如不 wire，但 `CELLTLS-CROSSBIND-WIRING-FUNNEL-01` archtest 守卫） |
| **cert InsecureSkipVerify 滥用（bypass chain check）** | 不适用 | raw `tls.Config{InsecureSkipVerify:true}` 在 wiring 层被 ban（参见 §AI-robust 档位）；stdlib `*tls.Config` 不可 seal 是文档化 Go 天花板 | Medium（archtest ban） |
| **loopback/demo 明文降级** | 明文 | loopback + 无 TLS material → 明文（设计许可，本地开发）；loopback + 有 TLS material → honor mTLS | Hard（双闸仅对非 loopback 生效） |
| **cert 轮换停机** | 不适用 | 静态 PEM：operator 手动轮换 + 重启；自动轮换属 follow-up（见下）| Soft（操作流程，技术闸不覆盖） |

**本 PR 后残留（显式登记）**：
- **split mTLS = 一进程一 cell（本 PR 强制 + 文档化的最小缓解；codex pr-review F1）**：mTLS 绑定
  **一进程一 cell SPIFFE 身份**（internal listener 持一张 cell 证书），故一个进程不能在同一非 loopback
  mTLS endpoint 承载多个 cell。本 PR 把该隐式假设**显式 fail-closed**：TLS 材料已配置 + topology 把
  同一非 loopback endpoint 分给 ≥2 个 remote cell → `celltls.Resolve` 启动期报错
  （`bootstrap.DeploymentTopology.SharedNonLoopbackRemoteEndpoint` 派生信号；loopback/明文共址豁免）。
  **完整解**（解除一进程一 cell 限制）= per-caller-cell TLS identity resolver / workload-vs-cell 双层
  身份模型 + peer-authorize 允许集合 —— epic 级 follow-up（可对标 go-spiffe `spiffetls/tlsconfig`
  的 per-workload SVID + Authorizer 模型）。评级：运行时 fail-closed guard = Medium。
- **cert 自动轮换**：静态 PEM 需要手动轮换 + 重启；`runtime/certlifecycle` reconciler
  自动颁发/续期是独立 follow-up（涉及：reconciler DB + leader election + EST/ACME 对接，
  已有独立子系统，不塞入本 PR）。
- **SPIFFE Workload API / SPIRE agent dial**（ZT-4）：独立 roadmap item；需要 `adapters/spiffe`
  + SPIRE sidecar / agent 集成，与本 PR 静态 PEM 路径正交。
- **轮换期 downtime**：轮换到新 cert 需要重启进程（hot-reload 是 follow-up）。

## AI-robust 档位表

| 约束 | 载体 / 机制 | 评级 |
|------|------------|------|
| sealed `tlsutil.ClientIdentity`（包外不可构造） | unexported 字段 + 单一 constructor `celltls.Resolve` | **Hard**（downstream，sealed construction） |
| `celltls.Resolve` 是唯一 minter | caller-funnel archtest `CELLTLS-MATERIAL-RESOLVE-FUNNEL-01`（type-aware AST scan，wiring 层） | **Medium**（upstream；Go 可见性不可表达「只此 minter」，与 `EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01` 同族 Go 天花板） |
| 非 loopback remote endpoint 禁 http scheme（assembly.yaml） | `gocell validate` governance rule（静态，`TOPO-14`） | **Hard-leaning**（任何通过 validate 的 assembly 配置均满足；CI `gocell validate` 必跑） |
| cross-bind wiring（internal listener 必有 cross-bind middleware） | archtest `CELLTLS-CROSSBIND-WIRING-FUNNEL-01`（wiring 层 AST scan） | **Medium**（wiring 层 Go 可见性不可表达「middleware 必存在」） |
| raw `tls.Config{InsecureSkipVerify:true}` 在 wiring 层 ban | archtest（wiring 层 struct-literal `InsecureSkipVerify:true` scan，含 RED fixture） | **Medium**（stdlib `*tls.Config` 不可 seal，文档化 Go 天花板，同 ADR 049 §"post-return-mutation Soft 天花板"机制，但此处在 wiring 层 ban） |
| all-or-nothing TLS material 启动 fail-fast | `cellmodules/celltls.Resolve` bootstrap guard | **Medium**（运行时 guard，type system 不可表达「四 env 联动」） |
| VerifyConnection 替换 hostname check（非 fail-open） | `tlsutil.ClientIdentity` builder 唯一成功路径无条件写入 `VerifyConnection`；`InsecureSkipVerify:true` 与 `VerifyConnection` 同构造，无法分离 | **Hard**（emit-time，与 ADR 049 §`NewServerMTLSConfig` Hard 同族） |

**Funnel 双向说明**（按 AI-robust 章程「只锁 callsite 不是闭环 funnel」）：

- 下游（`tlsutil.ClientIdentity` sealed construction）= **Hard**：包外不可构造，minter 唯一路径为 constructor。
- 上游（「只 `celltls.Resolve` mint」）= **Medium**：`NewClientIdentity` 是 exported constructor，跨
  framework/cellmodules 模块边界，`internal/` 不可桥接，Go 可见性无法封死唯一 minter。与
  `CROSSCELLOBS-MINTER-FUNNEL-01`、`EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01` 同族的文档化永久 Go/module
  天花板，不开 fake Hard-upgrade issue。

## 推迟项及理由

| 推迟项 | 理由 |
|--------|------|
| **证书自动颁发/续期**（via `runtime/certlifecycle` reconciler） | 独立子系统：reconciler + DB schema + leader election + EST/ACME client；`runtime/certlifecycle` 已有独立 PR（#1895）实现 reconciler harness，对接外部 CA（EST/ACME）是单独 roadmap item。静态 PEM 先落地，使 mTLS 功能可用，不以自动化阻塞安全改善。 |
| **SPIFFE Workload API / SPIRE agent 集成**（ZT-4） | 需要 `adapters/spiffe` 包 + SPIRE agent 在 sidecar 或 DaemonSet 形态运行，是独立 infra 依赖，超出本 PR 范围。GoCell 的 SPIFFE-ID 身份约定（C1）在 wire 上与 SPIRE 签发的 cert 完全兼容——ZT-4 实现时只需把静态 PEM 换成 SPIRE WorkloadAPI 的 X.509-SVID，不需改 VerifyConnection 逻辑。 |
| **cert hot-reload（不重启轮换）** | 需要 `tls.Config.GetCertificate` / `GetConfigForClient` 动态回调 + 文件 watcher。ADR 049 已显式推迟；cert 生命周期管理（含 hot-reload）归 `runtime/certlifecycle`。 |
| **gRPC cross-cell mTLS** | gRPC cross-cell transport 有独立 seam（ADR 1423 D2 明文：`grpc` 走独立路径）；gRPC 的 `credentials.TransportCredentials` 与本 `tls.Config` seam 正交，届时另出 ADR。 |

## 参考

- 本 ADR 关闭的缺口：ADR `202606131142-1423` §安全模型 行「无 mTLS 对等认证」。
- server 侧 mTLS builder：ADR `202605290130-049`（`tlsutil.NewServerMTLSConfig` / `PeerIdentity` / `PEER-IDENTITY-FIELDS-FROZEN-01`）。
- per-cell keyring（cross-bind 前置条件）：ADR `202606131142-1423` §#2153 Amendment。
- SPIFFE ID 标准：SPIFFE X.509 SVID（`spiffe.io/id`，URI SAN 格式）。
- go-spiffe 对标（未 vendor）：`github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig — MTLSClientConfig + AuthorizeID`。
- 架构约束：`docs/guides/deployment-topology.md`（部署操作），`docs/ops/env-vars.md`（环境变量）。
