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
