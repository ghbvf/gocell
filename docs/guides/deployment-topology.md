# Deployment Topology

## What is `assembly.yaml topology`?

The optional `topology` section in `assembly.yaml` declares the **deployment
placement** of an assembly's cells: which cells run in the same process
(colocated) and which run as separate remote services (remote).

```yaml
topology:
  colocated:
    - accesscore
    - configcore
  remote:
    - cellID: auditcore
      endpoint: "auditcore.internal:9090"
```

## Default: omit for single-process (all-colocated)

Omitting `topology` entirely means **all cells are co-located in the same
process** — the zero-migration default. This is the correct choice for most
assemblies and requires no configuration change.

## Rules

- **Exhaustive partition**: every cell declared in the assembly's `cells[]`
  must appear in exactly one of `colocated[]` or `remote[]`.
- **Mutual exclusion**: a cell cannot appear in both lists simultaneously.
- **Endpoint format** (syntactic only): remote cell endpoints must be a bare
  `host:port` or an `http`/`https` URL with a non-empty host. Other schemes
  (e.g. `grpc://`) are rejected. Production TLS enforcement is US6 #1964.
- **Remote placement**: declaring `topology.remote` is now production-reachable.
  US4 #1963 wired in-process transport selection; US5 #1966 added the
  `RemoteHTTPTransport` and removed the TOPO-12 fail-close gate. A real event
  broker is required for split topologies (TOPO-13).

## Current status: `topology.remote` is production-reachable (US5 #1966)

`topology.remote` is now active. US4 #1963 wired topology-gated transport
selection (`celltransport.Resolve`) and in-process dispatch; US5 #1966
introduced `RemoteHTTPTransport` + `StaticResolver` and removed the TOPO-12
fail-close gate. Both `topology.colocated` and `topology.remote` are honored
by `gocell validate`, `gocell generate`, and the composition root.

A split topology requires a real event broker (TOPO-13 enforces this). See the
§Split topology requirements section below for the full infrastructure checklist.

## Endpoint format

Remote cell endpoints must be a **bare `host:port`** or an
**`http`/`https` URL with a non-empty host**. Other schemes (e.g. `grpc://`) are
rejected. This is a **syntactic** check only — production TLS/mTLS enforcement
for remote endpoints is handled separately by US6 #1964 (see ADR
`202606131142-1423` security-gap matrix). Loopback (`localhost`) is syntactically
accepted (useful for dev/compose multi-process topologies).

## Static enforcement by `gocell validate`

Four governance rules enforce deployment topology:

| Rule | What it checks |
|------|---------------|
| **TOPO-10** | Structural validity: mutual exclusion, exhaustive partition, valid endpoints |
| **TOPO-11** | Provider reachability: every contract consumed by a cell in the assembly must have its provider cell reachable (colocated or remote) within that assembly |
| **TOPO-13** | Active broker gate (US3 #1965): in a split topology, an event contract whose publisher and subscriber fall on opposite sides of the process boundary requires a real broker — the in-memory EventBus cannot deliver events across processes |

Run `gocell validate` to check all three. TOPO-12 (the former topology.remote
fail-close gate) was removed by US5 #1966 — `topology.remote` is now
production-reachable. TOPO-13 is the active event-specific broker gate for split
topologies: it fires when a split topology uses an in-memory EventBus for a
cross-process event contract. Correctness is proven by synthetic RED/GREEN unit
tests.

## Example YAML

```yaml
# assemblies/myassembly/assembly.yaml
id: myassembly
cells:
  - id: accesscore
  - id: auditcore
  - id: configcore

topology:
  colocated:
    - accesscore
    - configcore
  remote:
    - cellID: auditcore
      endpoint: "auditcore.svc:9090"
      # or: endpoint: "https://auditcore.internal/"
```

## Split topology requirements (US4 #1963 + US5 #1966, now active)

When cells are split across processes, the following infrastructure is required:

- **Event transport broker** (US3 #1965, enforced): remote cells need a real
  message broker (e.g. RabbitMQ) to exchange events across process boundaries.
  In postgres topology this is provisioned via `GOCELL_AMQP_URL`. A split
  topology combined with an in-memory EventBus is rejected by a **double gate**:
  the static `gocell validate` rule TOPO-13 and a bootstrap **phase0 runtime
  gate** (`validateSplitTopologyBroker`) that fail-fasts when the deployment has
  remote cells while the resolved event transport is not a real broker. Since
  #2211 that decision is the sealed `EventTransportKind` (minted only by
  `eventtransport.Resolve`, threaded via `bootstrap.WithEventTransportKind`): the
  gate checks `IsRealBroker()`, and an **unset** kind — e.g. a composition root
  that declared remote cells but forgot the option — is fail-closed. This
  replaced the earlier `StorageBackend() != postgres` proxy ("non-nil ≠ real
  broker" closed). The runtime gate is a coarse proxy: it fires on any remote
  cell, even one with only sync (no cross-process events); a precise
  codegen-derived signal is a follow-up (US7 #1967).
- **Remote sync transport** (US4 #1963 + US5 #1966, active): US4 wired
  topology-gated transport selection (`celltransport.Resolve`) and the
  in-process dispatch path. US5 added `RemoteHTTPTransport` + `StaticResolver`
  for synchronous cross-cell HTTP calls. The TOPO-12 fail-close gate was removed
  by US5. Composition roots wire the transport via `celltransport.Resolve`
  (CELLTRANSPORT-SELECT-FUNNEL-01 enforces this at PR-time).
- **共享 service secret**（必须，所有节点一致）：跨进程的 service token 验证要求
  所有 cell 进程共享**同一个** `GOCELL_SERVICE_SECRET`（HMAC-SHA256 密钥，≥ 32 字节）。
  各进程独立配置该环境变量时，值必须相同——任意一侧密钥不一致会导致 `/internal/v1/*`
  请求 MAC 验证失败（401）。密钥轮换使用 `GOCELL_SERVICE_SECRET_PREVIOUS`：在所有
  进程完成新密钥切换前，旧密钥应保留在 `GOCELL_SERVICE_SECRET_PREVIOUS` 中（旧→新
  overlap 窗口），避免滚动重启期间验证失败。完整语义见
  [`docs/ops/env-vars.md` §"Service Token / Controlplane Guard"](../ops/env-vars.md)。
  验证步骤：在每个 cell 进程上确认 `GOCELL_SERVICE_SECRET` 哈希一致（不要打印明文）；
  启动日志中若出现 `ERR_CONTROLPLANE_SERVICE_SECRET_MISSING` 或频繁 401 则为不一致信号。
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
- **TLS/mTLS enforcement** (US6 #1964): production transport security for
  remote endpoints. Bare `host:port` endpoints currently default to plaintext
  HTTP; bearer/principal headers are integrity-protected by MAC but not
  confidential. Non-loopback TLS enforcement is wired by US6.

Currently, `cmd/corebundle` is an all-colocated assembly and does not use
split topology in production. `topology.remote` is production-reachable as of
US5 #1966, pending US6 #1964 for TLS enforcement.

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
`docs/architecture/202606131142-1423-adr-cell-deployment-topology.md`
