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
- **Remote placement fail-closed** (INTERIM): declaring `topology.remote` fails
  `gocell validate` and `gocell generate` until US4 #1963 wires transport.

## Current status: `topology.remote` is fail-closed (until US4 #1963)

`topology.remote` is part of the schema (US4-ready shape) but **currently
fail-closed**: declaring any `remote` cell in `assembly.yaml` fails both
`gocell validate` and `gocell generate` with an error until US4 #1963 wires
cross-process transport. Without that transport, cells declared as remote would
still be composed locally (silent degrade), which is worse than a clear error.

**Only `topology.colocated` (or omitting `topology` entirely) is honored now.**

US4 #1963 removes the fail-close gate (TOPO-12 rule + one codegen check) when
it makes composition honor the partition.

## Endpoint format

Remote cell endpoints (when US4 lands) must be a **bare `host:port`** or an
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
| **TOPO-12** | INTERIM: topology.remote fail-closed until US4 #1963 wires transport |
| **TOPO-13** | Broker-mandatory static gate (US3 #1965): in a split topology, an event contract whose publisher and subscriber fall on opposite sides of the process boundary requires a real broker — the in-memory EventBus cannot deliver events across processes |

Run `gocell validate` to check all four. TOPO-12 fires if any assembly declares
`topology.remote`; remove it until US4 lands. TOPO-13 runs normally in
`gocell validate` and would fire on cross-process event pub/sub, but its trigger
condition (split topology with cross-process event pub/sub) is currently
**unreachable** because TOPO-12 issues a blanket rejection of all
`topology.remote` declarations first — TOPO-13 is not disabled, it simply has
no valid input to check until TOPO-12 is removed. Correctness is proven by
synthetic RED/GREEN unit tests. When TOPO-12 is removed (US5 #1966), TOPO-13
becomes the event-specific broker guard for split topologies.

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

## Split topology requirements (future — US4 #1963 + US5 #1966)

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
- **Remote sync transport** (US4 #1963 / US5 #1966): a remote dispatch
  transport layer for synchronous cross-cell calls (planned, not yet
  implemented). US4 also removes the TOPO-12 fail-close gate.
- **TLS/mTLS enforcement** (US6 #1964): production transport security for
  remote endpoints. The endpoint syntax check (TOPO-10) is syntactic-only;
  non-loopback TLS enforcement is deferred to US6.

Currently, `cmd/corebundle` is an all-colocated assembly and does not use
split topology in production. `topology.remote` is fail-closed until US4 lands.

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
