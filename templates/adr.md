# ADR-{NNN}: {Short Title of Decision}

<!-- Architecture Decision Record — MADR format adapted for GoCell -->

## Status

<!-- proposed | accepted | deprecated | superseded by ADR-{NNN} -->
**proposed**

## Date

{YYYY-MM-DD}

## Decision Makers

<!-- List the people or roles involved in making this decision -->
- {Name / Role}

## GoCell Scope

<!-- Identify which GoCell artifacts this decision affects -->

| Aspect             | Value                                                  |
|--------------------|--------------------------------------------------------|
| Cell(s) affected   | {cell-id, e.g. access-core, audit-core, config-core}  |
| Slice(s) affected  | {slice-id, e.g. session-manager, audit-writer}         |
| Contract(s) affected | {contract path, e.g. event/access/v1}               |
| Consistency Level  | {L0-L4 — state current and proposed if changing}       |

## Context

<!-- Describe the forces at play, including technical, business, and political.
     What is the problem or opportunity? Why is a decision needed now?
     Reference relevant journeys (J-*.yaml) or contracts if applicable. -->

{Describe the context and problem statement here.}

## Decision Drivers

<!-- List the key factors influencing this decision -->
- {Driver 1, e.g. "Cell boundary isolation must be maintained"}
- {Driver 2, e.g. "L2 OutboxFact guarantees required for event delivery"}
- {Driver 3}

## Considered Options

### Option 1: {Title}

<!-- Describe this option. Include rough implementation sketch if helpful. -->

{Description}

### Option 2: {Title}

{Description}

### Option 3: {Title}

{Description}

## Decision

<!-- State the decision clearly. Use active voice:
     "We will use..." / "We decided to..." -->

{State the decision and the rationale for choosing it over the alternatives.}

## Consequences

### Positive

- {Positive consequence 1}
- {Positive consequence 2}

### Negative

- {Negative consequence 1 — include mitigation if known}
- {Negative consequence 2}

### Neutral

- {Neutral observation, e.g. "No impact on existing contracts"}

## Consistency Level Impact

<!-- If this decision changes the consistency level of any Cell or Slice,
     explain the before/after and why the change is justified.
     Reference the L0-L4 definitions from the GoCell architecture. -->

| Cell/Slice         | Before | After | Justification                          |
|--------------------|--------|-------|----------------------------------------|
| {cell-id/slice-id} | {L0-L4} | {L0-L4} | {Why this change is needed}         |

<!-- If no consistency level change, write: "No consistency level changes." -->

## References

- {Link to related ADR, journey, contract, or external resource}
- {Link to framework comparison if referencing an external pattern}
