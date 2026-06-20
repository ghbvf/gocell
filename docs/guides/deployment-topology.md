# Deployment Topology

## What is `assembly.yaml topology`?

The optional `topology` section in `assembly.yaml` declares the **complete
deployment-partition graph** of an assembly's cells: one `group` per deployment
role, each hosting a set of cells reachable at an `endpoint`. A process selects
one role at startup (`GOCELL_CELL_ROLE`, #1423 PR-2); that group's cells run
in-process, every other group is a remote peer.

```yaml
topology:
  groups:
    - role: core
      cells: [accesscore, configcore]
      endpoint: "https://core.internal:9443"
    - role: edge
      cells: [auditcore]
      endpoint: "https://auditcore.internal:9090"
```

## Default: omit for single-process (all-colocated)

Omitting `topology` entirely means **all cells are co-located in the same
process** — the zero-migration default (the single-binary monolith). This is the
correct choice for most assemblies and requires no configuration change.

## Rules

- **Exhaustive partition**: every cell declared in the assembly's `cells[]`
  must appear in exactly one `group`.
- **Mutual exclusion**: a cell cannot appear in more than one group.
- **Unique role**: each group's `role` is non-empty and unique within the assembly.
- **Endpoint per group**: every group declares a reachable `endpoint` (from any
  other role's perspective it is a remote peer). The endpoint must be a bare
  `host:port` or an `http`/`https` URL with a non-empty host. Other schemes
  (e.g. `grpc://`) are rejected. **Non-loopback group endpoints must use `https`
  scheme** in a split (≥2 groups) topology (enforced by `gocell validate` rule
  TOPO-14, #2263). Loopback endpoints (`localhost`/`127.x.x.x`/`::1`) may use
  bare `host:port` or `http` for local multi-process dev.
- **Cross-process events require a broker**: when an active amqp event's publisher
  and subscriber land in different groups, a real event broker is required. This is
  enforced at startup by the bootstrap runtime gate from a codegen-derived signal
  (the former static `gocell validate` rule TOPO-13 was removed in #2196); a
  sync-only split (remote cells, no cross-process events) does NOT require a broker.

## Current status: `topology.groups` authoring + runtime role selection (PR-2)

`topology.groups` is the authoring model (#2278 PR-1, replacing the earlier
single-process `colocated/remote` form). `gocell validate` (TOPO-10/11/14),
`gocell generate` (emits `generatedTopologyGroups()`), and the catalog export all
operate on groups.

Runtime role selection landed in #2278 PR-2: the composition root calls
`bootstrap.SpecForRole(generatedTopologyGroups(), os.Getenv("GOCELL_CELL_ROLE"))`
and mounts the result via `composition.NewForRole`:

- **empty `GOCELL_CELL_ROLE` + 0/1 group** → all-colocated monolith (zero spec).
- **empty role + ≥2 groups** → fail-fast: a multi-group topology is a split
  deployment, so the process MUST select its role (a silent monolith would mask
  the misconfiguration).
- **`GOCELL_CELL_ROLE=<role>`** → this process hosts that group's cells
  (colocated) and reaches every other group's cells as remote.
- **unknown role** → fail-fast (the server log lists the declared `availableRoles`).

The bootstrap `MOUNTED-EQUALS-COLOCATED-01` phase guard fail-fasts if a process
ever mounts a cell declared remote for its role (the upstream backstop for
`NewForRole`). End-to-end dual-topology acceptance (same image, 2 processes +
broker + PG) is tracked by #1967 (PR-4).

A split topology with cross-process events requires a real event broker (the
bootstrap runtime gate enforces this from a codegen-derived signal; the static
TOPO-13 rule was removed in #2196). See the §Split topology requirements section
below for the full infrastructure checklist.

## Endpoint format

Remote cell endpoints must be a **bare `host:port`** or an
**`http`/`https` URL with a non-empty host**. Other schemes (e.g. `grpc://`) are
rejected. **Non-loopback endpoints must use `https`** (enforced by `gocell
validate` rule TOPO-14, #2263): bare `host:port` is accepted only for loopback
addresses (`localhost`, `127.x.x.x`, `::1`). Loopback addresses are plaintext-eligible
for local multi-process dev (e.g. Docker Compose with multiple GoCell processes).

Production split deployments MUST use `https` endpoints and configure mTLS
material via the four env vars described in §Split mTLS 配置 checklist below.

## Static enforcement by `gocell validate`

Three governance rules enforce deployment topology statically:

| Rule | What it checks |
|------|---------------|
| **TOPO-10** | Structural validity: groups exhaustively + mutually-exclusively partition the assembly's cells; unique non-empty roles; valid per-group endpoints |
| **TOPO-11** | Provider reachability: every contract consumed by a cell in the assembly must have its provider cell as a member of that assembly |
| **TOPO-14** | mTLS scheme gate (#2263): in a split topology, a non-loopback group endpoint must use `https` scheme — bare `host:port` or `http://` is rejected for non-loopback addresses |

Run `gocell validate` to check all three. TOPO-12 (the former topology.remote
fail-close gate) was removed by US5 #1966. The **broker-mandatory requirement**
for cross-process events is NOT a static rule: the former static gate **TOPO-13
was removed in #2196** because a static rule cannot see the runtime-injected
broker and therefore over-constrained legal broker-backed splits. It is now a
single codegen-derived fact (`assembly.collectCrossProcessBrokerEventRoles` →
`bootstrap.DeploymentTopology.RequiresBrokerForCrossProcessEvents`) enforced
solely by the bootstrap **phase0 runtime gate** (see §Split topology requirements below).

### Missing-dependency fail-fast (sync dimension)

A consumed `http` (sync) contract whose provider cell is not reachable in the
deployment topology is rejected at **two** layers, so a missing sync dependency
never surfaces only at request time:

- **Static** (`gocell validate` **TOPO-11**, above): the provider must be a member
  of the assembly — with TOPO-10's exhaustive partition that means it is placed in
  some group (co-located here, or remote elsewhere).
- **Runtime** (`celltransport.Resolve`, eager at module-wiring / startup): the
  composition root resolves each consumed sync client's transport against the
  sealed topology. A provider that is neither co-located nor remote fails closed
  with `KindInternal`; the error bubbles up and the process does not start
  (FR-004). `CELLTRANSPORT-SELECT-FUNNEL-01` makes this seam the sole construction
  site for cross-cell sync transports, so no sync call can bypass it. The diagnostic
  names two failure classes: `topology under-declared` (the provider has no place in
  the topology) and `local dependency missing` (the provider is co-located but not
  mounted in this process).

The offending `cellID` is on the `KindInternal` error in the **server log**
(`InternalAttr`, never on the wire). Operator action by class:

- `topology under-declared` → the provider is not in any group: add it to the right
  `topology.groups` group (co-located here, or another group whose endpoint this
  process reaches as remote).
- `local dependency missing` → the provider is declared co-located for this role but
  its module was not wired: confirm the composition root mounts that cell (e.g. it is
  in the role's group and `composition.NewForRole` selected it).

The event dimension is covered separately by the broker gate (bootstrap phase0
`validateSplitTopologyBroker`, US3 #1965; the static TOPO-13 rule was removed in
#2196). There is **no** separate phase0 missing-dependency gate — the
eager `celltransport.Resolve` seam already provides the runtime fail-fast (see ADR
`202606131142-1423` §#1967 Amendment).

## Example YAML

```yaml
# assemblies/myassembly/assembly.yaml
id: myassembly
cells:
  - id: accesscore
  - id: auditcore
  - id: configcore

topology:
  groups:
    - role: core
      cells: [accesscore, configcore]
      endpoint: "https://core.svc:9443"
    - role: edge
      cells: [auditcore]
      endpoint: "https://auditcore.svc:9090"
```

## Split topology requirements (US4 #1963 + US5 #1966, now active)

When cells are split across processes, the following infrastructure is required:

- **Event transport broker** (US3 #1965, enforced): remote cells need a real
  message broker (e.g. RabbitMQ) to exchange events across process boundaries.
  In postgres topology this is provisioned per cell via `GOCELL_<CELLID>_AMQP_URL`
  (falling back to the assembly-wide `GOCELL_AMQP_URL`; #2152 PR-2 — colocated cells
  dedup to one connection, distinct broker URLs are currently fail-closed). A process
  that participates in **cross-process events** while the resolved event transport is
  not a real broker is rejected by a single bootstrap **phase0 runtime gate**
  (`validateSplitTopologyBroker`). Its trigger (since #2196) is the precise
  codegen-derived `DeploymentTopology.RequiresBrokerForCrossProcessEvents` signal —
  single-sourced from the assembly topology + contractUsages, emitted onto
  `generatedTopologyGroups()` and sealed at phase0 — NOT the coarse `HasRemoteCells`
  proxy: a **sync-only split** (remote cells, only CellTransport/HTTP contracts) needs
  no *cross-process* broker — on **demo** topology the in-memory bus is permitted. The
  former static `gocell validate` rule TOPO-13 was removed in #2196 (it could not see the
  runtime-injected broker and so over-constrained legal broker-backed splits); the runtime
  gate is now the sole **topology-derived (cross-process-event)** broker-mandatory
  enforcement point. The **postgres storage-durability** broker requirement is orthogonal
  and independent: on postgres, `eventtransport.Resolve` always provisions a real broker
  for the durable outbox relay (keyed off `generatedBrokerCells()`), regardless of
  cross-process events — so on a correctly-wired postgres process the gate's
  `IsRealBroker()` check is structurally satisfied, and the gate does real broker-rejection
  work only for demo storage. Since #2211 the "is it a real broker?" decision
  is the sealed `EventTransportKind` (minted only by `eventtransport.Resolve`, threaded
  via `bootstrap.WithEventTransportKind`): the gate checks `IsRealBroker()`, and an
  **unset** kind — e.g. a composition root that declared cross-process events but
  forgot the option — is fail-closed. This replaced the earlier
  `StorageBackend() != postgres` proxy ("non-nil ≠ real broker" closed).
- **Remote sync transport** (US4 #1963 + US5 #1966, active): US4 wired
  topology-gated transport selection (`celltransport.Resolve`) and the
  in-process dispatch path. US5 added `RemoteHTTPTransport` + `StaticResolver`
  for synchronous cross-cell HTTP calls. The TOPO-12 fail-close gate was removed
  by US5. Composition roots wire the transport via `celltransport.Resolve`
  (CELLTRANSPORT-SELECT-FUNNEL-01 enforces this at PR-time).
- **Per-cell service-token 密钥分发**（#2153，split 推荐 / Hard）：split cell 进程
  **不应共享 master**（`GOCELL_SERVICE_SECRET`）—— 持 master 的任一进程被攻陷即可派生**任意** cell
  子密钥、伪造任意 `callerCell`。推荐：每个 split cell 进程**只持自身子密钥**，由 operator 在部署时用
  `gocell derive-service-keys --cell <id> --callers <caller-list>` 从 master 派生该 cell 的签名子密钥 +
  其声明 caller 的验签子密钥，注入为 env（`GOCELL_SERVICE_CELL` / `GOCELL_SERVICE_SIGNING_KEY` /
  `GOCELL_SERVICE_VERIFY_KEYS` [+ `_PREVIOUS`]）。`--callers` 枚举**调用本 cell** `/internal/v1/*` 的每个
  caller cell（本 cell 须验签的入站调用方），如 `--cell configcore --callers accesscore,auditcore`；**省略
  `--callers` 仅对无入站 internal 端点的纯出站 cell 合法**——否则生成空 `GOCELL_SERVICE_VERIFY_KEYS`，
  该 cell 将拒绝一切入站 internal 调用（运行期 401），CLI 会在 stderr 显式提示这一空集。此模式下 master **不出现在** cell 进程中——被攻陷 cell 只暴露自身子密钥 + 其声明
  caller 的验签子密钥，**无法伪造第三 cell**（密码学 fail-closed）。composition root 经 env 层互斥守卫
  强制：master 与 provisioned env **二选一**（皆设或皆缺 → 启动 fail-closed）。
  - 轮换：`master rotation` → 重跑 `derive-service-keys` 重新分发 → 验签 try current 后 previous，
    重叠窗口平滑切换（`GOCELL_SERVICE_*_PREVIOUS`）；wire 格式不变。
  - **monolith / 同址 fallback**：所有 cell 同进程时用 master（`GOCELL_SERVICE_SECRET` [+ `_PREVIOUS`]），
    per-cell 子密钥在进程内派生 —— 单一信任域，**不提供** per-cell 隔离（进程一破即得 master），可接受；
    **不要**把它用于跨信任边界的真实分进程部署。
  - 完整语义见 [`docs/ops/env-vars.md` §"Service Token / Controlplane Guard"](../ops/env-vars.md) 与 ADR
    `202606131142-1423` §#2153 Amendment。验证：split cell 进程**不**应设 `GOCELL_SERVICE_SECRET`；
    频繁 401 多为子密钥分发不一致（重跑 `derive-service-keys` 用同一 master）。
- **Internal listener 绑定与可达性**（split topology 时须绑定到可达地址）：默认
  internal listener 绑定 `127.0.0.1:9090`（环回地址），这在 split topology 中会导致
  远端 caller cell 的 HTTP 请求无法到达（连接被拒绝）。分拆部署时须将
  `GOCELL_HTTP_INTERNAL_ADDR` 设置为 pod/网络可达地址（如 `:9090` 或
  `0.0.0.0:9090`），并在 Kubernetes/VPC 层通过 NetworkPolicy 或等价机制将
  `/internal/v1/*` 的入站流量限制为授权 caller pod（`/internal/v1/*` contract 上的
  caller-cell allowlist 仍然生效，但网络层隔离是纵深防御的第一道门）。注意：corebundle
  当前没有对 internal listener 做类似 health listener 的对称可达性校验（`BOOTSTRAP-INTERNAL-LOCAL-ONLY-FAIL-FAST-01`
  backlog）；环回绑定错误只会在远端首次调用时以连接失败暴露，不在启动期 fail-fast。
  详见 [`docs/ops/listener-topology.md` §"Deployment Recommendations"](../ops/listener-topology.md)。
  验证步骤：从 caller cell 进程（或同等网络位置）向 `<被调用cell地址>:9090/internal/v1/...`
  发起探测请求，确认可达（401 是预期鉴权响应，connection refused 或 timeout 表示绑定错误）。
- **mTLS（#2263，已落地，split 拓扑强制）**：非 loopback split 跨 cell 调用现强制 mTLS —— 见
  §Split mTLS 配置 checklist。「部署在可信私有网络」不再是 mTLS 的替代品；该 soft 约束已被 #2263 的
  fail-closed 技术边界取代。以下约束**仍然成立**（纵深防御，不是 mTLS 的替代品）：
  ① **split cell 进程使用 provisioned 子密钥而非共享 master**（#2153，见上方 §Per-cell service-token
  密钥分发 bullet）；
  ② **internal listener 绑定到可达地址 + NetworkPolicy/VPC 限制 ingress 至授权 caller**（见上方
  §Internal listener 绑定与可达性）；
  ③ **服务端 RequireCallerCell allowlist**：每个 `/internal/v1/*` contract 在 contract.yaml 声明
  `callers` 闭集，框架令牌验证后校验 `callerCell` claim（令牌层，与网络层正交，独立 fail-closed）。
  完整威胁矩阵见 ADR `202606171200-2263-adr-cross-cell-transport-mtls.md` §威胁矩阵。

Currently, `cmd/corebundle` is an all-colocated assembly and does not use
split topology in production. A split `topology.groups` topology is
production-reachable as of US5 #1966; token-layer per-cell identity isolation
landed in #2153; transport-layer mTLS peer authentication landed in #2263
(non-loopback split now requires mTLS, fail-closed — see §Split mTLS 配置
checklist below).

## Split mTLS 配置 checklist

适用于 `topology.groups` 含**非 loopback** group endpoint 的所有 split 生产部署（#2263，ZT-1）。

### 前置条件：证书要求

每个进程（不论承载单个还是多个 cell）需要一张 **workload leaf cert**，满足：

1. **多-SAN SPIFFE URI**（#2297，`spiffe://<trustDomain>/cell/<cellID>`，**每个本地 cell 一条**）：
   - `trustDomain` = `GOCELL_SPIFFE_TRUST_DOMAIN` 环境变量的值（如 `gocell.internal`）。
   - `cellID` = assembly.yaml 中声明的 cell id（如 `accesscore`、`auditcore`）。
   - **一个进程可承载多个 cell**：其 workload cert 必须包含每个本地 cell 对应的 URI SAN，例如
     承载 `accesscore` + `configcore` 的进程需要两条 URI SAN：
     `spiffe://gocell.internal/cell/accesscore` 和 `spiffe://gocell.internal/cell/configcore`。
   - `celltls.Resolve` 在启动期校验：**本地证书 URI SAN 集合必须精确等于 topology 声明的本进程
     colocated cell 集合**（`==`，非超集）——既抓「漏配某本地 cell」也抓「越权多配本进程不承载的
     cell」，违反此约束启动 fail-fast。
2. **双 EKU**：同时声明 `ExtKeyUsageServerAuth` + `ExtKeyUsageClientAuth`——同一证书兼作
   server cert（接受对端验证）和 client cert（向对端出示）。
3. **单根 CA 签发**：所有进程的 leaf cert 由同一 trust-root CA 签发（CA cert 作为
   `GOCELL_TRANSPORT_TLS_CA_FILE` 的内容，分发给每个进程）。
4. **TLS 1.3 兼容**：框架强制 `tls.VersionTLS13`，确保 leaf cert / CA cert 的签名算法
   和密钥长度满足 TLS 1.3 要求（RSA 2048+ 或 ECDSA P-256+）。

生成自签 CA + workload cert 的工具示例（本地测试，承载 `accesscore` + `configcore` 的多 cell 进程）：

```bash
# 生成 CA
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -days 3650 \
  -keyout ca.key -out ca.crt -subj "/CN=gocell-test-ca" -nodes

# 生成 core 进程 workload cert（承载 accesscore + configcore，两条 URI SAN）
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout core.key \
  -out core.csr -subj "/CN=core-workload" -nodes

openssl x509 -req -in core.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 365 -out core.crt \
  -extfile <(printf "subjectAltName=URI:spiffe://gocell.internal/cell/accesscore,URI:spiffe://gocell.internal/cell/configcore\nextendedKeyUsage=serverAuth,clientAuth")

# 生成 edge 进程 workload cert（仅承载 auditcore，一条 URI SAN）
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout edge.key \
  -out edge.csr -subj "/CN=edge-workload" -nodes

openssl x509 -req -in edge.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 365 -out edge.crt \
  -extfile <(printf "subjectAltName=URI:spiffe://gocell.internal/cell/auditcore\nextendedKeyUsage=serverAuth,clientAuth")
```

生产环境请使用正式 PKI / cert-manager / SPIRE 签发（SPIRE 签发的多-SAN SVID 与本格式完全兼容）。

### 四个必填环境变量（all-or-nothing）

| 变量 | 含义 | 示例 |
|------|------|------|
| `GOCELL_TRANSPORT_TLS_CERT_FILE` | workload 的 leaf cert PEM 文件路径（URI SAN 含本进程全部 cell 的 SPIFFE ID，双 EKU） | `/etc/gocell/tls/core.crt` |
| `GOCELL_TRANSPORT_TLS_KEY_FILE`  | 配套私钥 PEM 文件路径 | `/etc/gocell/tls/core.key` |
| `GOCELL_TRANSPORT_TLS_CA_FILE`   | trust-root CA bundle PEM 文件路径 | `/etc/gocell/tls/ca.crt` |
| `GOCELL_SPIFFE_TRUST_DOMAIN`     | SPIFFE trust domain（不含 `spiffe://` 前缀） | `gocell.internal` |

**All-or-nothing 语义**：四个变量必须同时设置或同时不设。只设部分视同全部未设——
`celltls.Resolve` 在此情况下启动 fail-fast（含非 loopback remote cell 时）。

> **Warning — loopback remote cell 配置了 TLS 变量仍会强制 mTLS（反直觉行为）：**
> `celltls.Resolve` 在四个 TLS 变量全部设置时 **无论 remote endpoint 是否为 loopback**，都会
> 强制使用 mTLS 材料。这意味着即便 remote peer 是 `localhost:9090`，只要这四个变量已设，
> 就必须提供有效的 cert/key/CA，否则 TLS 握手失败。
>
> **demo / dev 环境如需 loopback 明文通信，请勿设置这四个变量。**
> 只在真正需要 mTLS 的生产或测试场景才配置它们，并确保提供合法的 cert/key/CA（可使用下方
> openssl 自签示例）。

**Fail-closed 行为**：
- topology 含非 loopback remote cell + TLS material 缺失 → **启动 fail-fast**（不降级明文）。
- 本地证书 URI SAN 集合 ≠ topology 声明的 colocated cell 集合 → **启动 fail-fast**（#2297，最小权限精确匹配）。
- 各 peer 逐个检查：非 loopback peer + 无 client identity → **启动 fail-fast**。
- cert chain 验证失败 → **TLS 握手拒绝**（连接终止，不静默通过）。
- server cert cell 集合不含 targetCell → **TLS 握手拒绝**（出站 `VerifyConnection` 集合成员判定失败）。
- client cert cell 集合不含 callerCell（service-token 声明的调用方） → **403**（入站 cross-bind middleware `verifyCrossBind`，`CellSet.Contains`）；**401 仅出现在无 peer 证书时**（cross-bind 前 peer 层未携带 cert）。

### assembly.yaml 要求

非 loopback remote endpoint 必须使用 `https` scheme：

```yaml
topology:
  groups:
    - role: core
      cells: [accesscore, configcore]   # 两个 cell 同进程，cert 必须含两条 URI SAN
      endpoint: "https://core.internal:9443"
    - role: edge
      cells: [auditcore]
      endpoint: "https://auditcore.internal:9090"  # 正确：https
      # endpoint: "auditcore.internal:9090"         # 错误：非 loopback 裸 host:port 被 TOPO-14 拒绝
      # endpoint: "http://auditcore.internal:9090"  # 错误：非 loopback http 被 TOPO-14 拒绝
      # endpoint: "localhost:9090"                  # 可接受：loopback，本地 dev 用
```

运行 `gocell validate` 验证：TOPO-14 在构建/CI 阶段静态检查。

### 纵深防御（在 mTLS 之上仍然成立）

mTLS 是传输层安全，以下纵深防御层与之正交，**仍然必须配置**：

1. **Per-cell provisioned keyring**（#2153）：split cell 进程不持 master secret，只持自身子密钥。
   见上方 §Per-cell service-token 密钥分发。
2. **Internal listener 可达性 + NetworkPolicy**：见上方 §Internal listener 绑定与可达性。
3. **RequireCallerCell allowlist**：每个 `/internal/v1/*` contract 必须声明 `callers` 闭集。

### 已知局限（follow-up 登记）

- **cert 自动轮换**：本 PR 使用静态 PEM 文件，轮换需要手动替换文件 + 重启进程。自动颁发/续期
  via `runtime/certlifecycle` reconciler 是独立 follow-up（参考 ADR
  `docs/architecture/202606171200-2263-adr-cross-cell-transport-mtls.md` §推迟项）。
- **SPIFFE Workload API / SPIRE agent**（ZT-4）：独立 roadmap，需要 `adapters/spiffe` +
  SPIRE agent sidecar。本 PR 的静态 PEM 与 SPIRE 签发的 cert 在 wire 上完全兼容（相同
  SPIFFE URI SAN 格式），ZT-4 落地时只需替换证书供给方式。
- **hot-reload**：cert 文件变更后需重启（文件 watcher 是 follow-up）。
- **静态 PEM 的证书轮换操作流程（无 rolling path）：** 当前 mTLS 使用静态 PEM 文件，进程启动
  时一次性读入内存，运行期不重新加载。cert 轮换没有零停机 rolling 路径，操作员必须按以下顺序
  执行以最小化服务中断：

  1. **信任包扩展（trust-bundle overlap）：** 在将 leaf cert 切换到新 CA 签发之前，先把新 CA
     cert **追加**到现有 CA bundle 文件（`GOCELL_TRANSPORT_TLS_CA_FILE`），使 CA bundle 同时
     包含旧 CA 和新 CA。把更新后的 CA bundle 分发到所有进程并重启，完成后每个进程同时
     信任旧 CA 和新 CA 签发的 leaf cert。
  2. **Leaf cert 滚动（coordinator）：** 依次为每个进程生成新 CA 签发的 workload leaf cert
     （URI SAN 含该进程承载的全部 cell），替换 `GOCELL_TRANSPORT_TLS_CERT_FILE` /
     `GOCELL_TRANSPORT_TLS_KEY_FILE`，重启该进程。因其他进程仍信任新旧两个 CA，期间 TLS
     握手不会中断。
  3. **全进程重启窗口：** 完成所有 leaf cert 替换后，视情况收缩 CA bundle（移除旧 CA 并再次
     重启）。期间 `<peer>_remote_ready` probe 会在每次重启时短暂降级（`unhealthy`）并在进程
     重新上线后恢复；kubelet 会据此短暂摘除 pod 流量，这是预期行为。
  4. **监控 `<peer>_remote_ready` probe：** 整个轮换窗口期间持续观察各 peer 的
     `_remote_ready` probe，确认每次重启后都恢复 `healthy` 再继续下一步。

  热重载（文件 watcher 驱动的无重启轮换）是 follow-up（hot-reload roadmap）。


**Diagnosing broker status via `/readyz?verbose`**: the framework-level
`Topology.AdapterInfo()` method returns `"in-memory"` by default. In a postgres
topology, the composition root (`cmd/corebundle`) overrides the `event_bus`
value with the broker kind — currently the literal `"rabbitmq"` (the broker
*kind*, never the connection URL, which would leak the AMQP credentials). When
interpreting the `event_bus` field in verbose readyz output, use the value
reported by the composition root override — the framework default is not
meaningful in a postgres deployment.

## ADR reference

For design decisions, threat model, and phase plan, see:

- `docs/architecture/202606131142-1423-adr-cell-deployment-topology.md` — 部署拓扑 seam 决策、安全模型、历次 amendment。
- `docs/architecture/202606171200-2263-adr-cross-cell-transport-mtls.md` — split mTLS 对等认证（ZT-1）、SPIFFE-ID 约定、fail-closed 双闸、AI-robust 档位表；#2297 Amendment：allow-set 成员制解除一进程一 cell。
- `docs/architecture/202605290130-049-adr-mtls-server-builder-and-identity-hook.md` — server 侧 mTLS builder 与 PeerIdentity ctx hook。
