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
- **Endpoint format**: remote cell endpoints must be a bare `host:port` or an
  `http`/`https` URL with a non-empty host. Other schemes (e.g. `grpc://`) are
  rejected. Loopback (`localhost`) is accepted for dev/compose topologies.

## Static enforcement by `gocell validate`

Two governance rules enforce deployment topology:

| Rule | What it checks |
|------|---------------|
| **TOPO-10** | Structural validity: mutual exclusion, exhaustive partition, valid endpoints |
| **TOPO-11** | Provider reachability: every contract consumed by a cell in the assembly must have its provider cell reachable (colocated or remote) within that assembly |

Run `gocell validate --strict` to check both.

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

## Split topology requirements

When cells are split across processes, the following infrastructure is required:

- **Event transport broker** (US3 #1965): remote cells need a real message
  broker (e.g. RabbitMQ) to exchange events across process boundaries. In
  postgres topology this is provisioned via `GOCELL_AMQP_URL`.
- **Remote sync transport** (US4/US5): a remote dispatch transport layer for
  synchronous cross-cell calls (planned, not yet implemented).

Currently, `cmd/corebundle` is an all-colocated assembly and does not use
split topology in production.

## ADR reference

For design decisions, threat model, and phase plan, see:
`docs/architecture/202606131142-1423-adr-cell-deployment-topology.md`
