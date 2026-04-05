# Cell Design: {cell-id}

<!-- Cell design document — use this template when designing a new Cell
     or performing a major redesign of an existing one. -->

## Overview

| Field              | Value                                                    |
|--------------------|----------------------------------------------------------|
| Cell ID            | {cell-id, e.g. access-core}                              |
| Type               | {core / edge / support}                                  |
| Consistency Level  | {L0 / L1 / L2 / L3 / L4}                                |
| Owner              | {team or individual}                                     |
| Status             | {draft / active / deprecated}                            |

### Type Selection Rationale

<!-- Explain why this Cell type was chosen:
     - core: Domain-critical, owns authoritative data
     - edge: Interfaces with external systems or user-facing APIs
     - support: Shared utilities, cross-cutting concerns -->

{Explain the rationale for the chosen Cell type.}

### Consistency Level Rationale

<!-- Reference the L0-L4 definitions and explain why this level is appropriate:
     - L0 LocalOnly: Pure computation, no side effects
     - L1 LocalTx: Single Cell local transaction (e.g. session creation)
     - L2 OutboxFact: Local tx + outbox event publishing (e.g. session.created)
     - L3 WorkflowEventual: Cross-Cell eventual consistency (e.g. query projections)
     - L4 DeviceLatent: Device long-latency closed loop (e.g. command receipts) -->

{Explain why this consistency level is required and not a lower one.}

## Slice Inventory

<!-- List all Slices belonging to this Cell.
     Each Slice must have a slice.yaml with required fields. -->

| Slice ID           | Purpose                        | Consistency Level | Contract Usages       |
|--------------------|--------------------------------|-------------------|-----------------------|
| {slice-id}         | {Brief description}            | {inherits or override} | {contract paths}  |
| {slice-id}         | {Brief description}            | {inherits or override} | {contract paths}  |

## Contract Inventory

<!-- List all contracts this Cell participates in, as provider or consumer. -->

| Contract Path                | Role      | Kind    | Description                    |
|------------------------------|-----------|---------|--------------------------------|
| {kind}/{domain}/{version}    | provider  | {sync/event/query} | {What this contract does} |
| {kind}/{domain}/{version}    | consumer  | {sync/event/query} | {What this contract does} |

## Data Model Overview

<!-- Describe the primary data entities this Cell owns.
     Reference schema.primary from cell.yaml. -->

### Primary Entities

| Entity             | Table/Collection     | Description                          |
|--------------------|----------------------|--------------------------------------|
| {EntityName}       | {table_name}         | {What this entity represents}        |
| {EntityName}       | {table_name}         | {What this entity represents}        |

### Key Relationships

<!-- Describe how entities relate to each other within this Cell.
     Cross-Cell relationships must go through contracts. -->

{Describe entity relationships, cardinality, and any invariants.}

## Dependencies

### L0 Dependencies

<!-- List any L0 (pure computation) Cells this Cell directly imports.
     Only L0 Cells within the same assembly may be imported without a contract. -->

| L0 Cell ID         | What is imported               | Justification                  |
|--------------------|--------------------------------|--------------------------------|
| {cell-id}          | {package/function}             | {Why direct import is needed}  |

<!-- If none, write: "No L0 dependencies." -->

### External Systems

<!-- List external systems this Cell integrates with via adapters. -->

| System             | Adapter              | Purpose                              |
|--------------------|----------------------|--------------------------------------|
| {e.g. PostgreSQL}  | {adapters/postgres}  | {Primary data storage}               |
| {e.g. RabbitMQ}    | {adapters/rabbitmq}  | {Event publishing via outbox}        |

## Verify Strategy

### Smoke Tests

<!-- Define the minimal set of tests that prove this Cell is operational.
     These map to cell.yaml verify.smoke. -->

| Test ID            | Description                    | Expected Result                |
|--------------------|--------------------------------|--------------------------------|
| {smoke-001}        | {What the test does}           | {Expected outcome}             |

### Contract Tests

<!-- Define contract tests for each contract this Cell participates in.
     These map to slice.yaml verify.contract. -->

| Contract Path      | Test ID              | Description                          |
|--------------------|----------------------|--------------------------------------|
| {contract path}    | {ct-001}             | {What the contract test verifies}    |

### Unit Test Coverage

<!-- Target: >= 80% for cells/, >= 90% for kernel/ layer code.
     List critical paths that must be covered. -->

- {Critical path 1: e.g. "Session creation happy path and all error branches"}
- {Critical path 2}

## Open Questions

<!-- List any unresolved design questions or decisions pending. -->

- [ ] {Question 1}
- [ ] {Question 2}

## References

- {Link to related ADR}
- {Link to journey specification (J-*.yaml)}
- {Link to framework comparison entry}
