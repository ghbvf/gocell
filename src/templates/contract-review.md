# Contract Review: {kind}/{domain-path}/{version}

<!-- Contract review checklist — use this template when reviewing a new or
     modified contract before merging. -->

## Contract Summary

| Field              | Value                                                    |
|--------------------|----------------------------------------------------------|
| Contract Path      | {kind}/{domain-path}/{version}                           |
| Kind               | {sync / event / query}                                   |
| Lifecycle          | {draft / active / deprecated}                            |
| Provider Cell      | {cell-id}                                                |
| Consumer Cell(s)   | {cell-id, cell-id, ...}                                  |
| Reviewer           | {name / role}                                            |
| Review Date        | {YYYY-MM-DD}                                             |

---

## 1. Schema Compatibility

<!-- Verify schema definitions are correct, complete, and backward-compatible. -->

- [ ] Schema fields use correct types (string, int, bool, etc.)
- [ ] Required fields are clearly marked
- [ ] New fields added since last version are optional or have defaults
- [ ] No fields have been removed or renamed (breaking change)
- [ ] No field types have been changed (breaking change)
- [ ] DB fields use `snake_case`, JSON/Query/Path use `camelCase`
- [ ] Response follows unified format: `{"data": ..., "total": ..., "page": ...}`

**Notes:** {Any schema observations or concerns}

## 2. Role-Kind Matching

<!-- Verify the contract kind matches the interaction pattern. -->

- [ ] **sync**: Provider exposes an endpoint, consumer calls it directly
- [ ] **event**: Provider publishes events, consumer subscribes asynchronously
- [ ] **query**: Provider exposes read-only query, consumer reads projections

| Role     | Cell ID          | Verified |
|----------|------------------|----------|
| Provider | {cell-id}        | [ ]      |
| Consumer | {cell-id}        | [ ]      |
| Consumer | {cell-id}        | [ ]      |

**Notes:** {Any role or kind mismatch observations}

## 3. Consistency Level Constraints

<!-- Verify the contract respects the consistency levels of participating Cells. -->

- [ ] Provider Cell consistency level supports this contract kind
- [ ] Consumer Cell consistency level is compatible
- [ ] L2+ events use outbox pattern (not direct publish)
- [ ] L3 cross-Cell flows document eventual consistency behavior
- [ ] L4 device-latency flows document timeout and retry strategy

| Cell ID          | Declared Level | Required for Contract | Compatible |
|------------------|----------------|-----------------------|------------|
| {provider}       | {L0-L4}        | {minimum level}       | [ ]        |
| {consumer}       | {L0-L4}        | {minimum level}       | [ ]        |

**Notes:** {Any consistency level concerns}

## 4. Provider / Consumer Identification

<!-- Verify all participants are correctly identified and registered. -->

- [ ] Provider Cell exists and has a valid cell.yaml
- [ ] All consumer Cells exist and have valid cell.yaml files
- [ ] Contract is listed in relevant slice.yaml contractUsages
- [ ] External actors (if any) are registered in actors.yaml
- [ ] No Cell directly imports another Cell's internal/ (except L0)

**Notes:** {Any identification issues}

## 5. Lifecycle Status

<!-- Verify the lifecycle field is appropriate. -->

- [ ] **draft**: Contract is under development, not yet in production
- [ ] **active**: Contract is in production use
- [ ] **deprecated**: Contract has a replacement; deprecation period >= 2 sprints (4 weeks)
- [ ] Lifecycle status matches actual deployment state
- [ ] If deprecated, replacement contract path is documented

**Notes:** {Any lifecycle concerns}

## 6. Event-Specific Checks

<!-- Complete this section only for event-kind contracts. Skip for sync/query. -->

- [ ] Events are replayable (consumers can reprocess without side effects)
- [ ] Each event includes `event_id` (UUID) for idempotency key construction
- [ ] `idempotencyKey` format documented: `{prefix}:{group}:{event-id}`
- [ ] `deliverySemantics` specified: {at-least-once / at-most-once / exactly-once}
- [ ] Dead letter queue (DLQ) configured for L2+ consumers
- [ ] Payload changes are backward-compatible (new fields are optional)
- [ ] Stream naming follows convention; constants defined (not duplicated)
- [ ] Consumer declares: consumer group, ACK timing, retry strategy

**Notes:** {Any event-specific concerns}

## 7. API Versioning (sync contracts only)

<!-- Complete this section only for sync-kind contracts. Skip for event/query. -->

- [ ] Endpoint uses `/api/v1/` prefix (or `/internal/v1/` for internal)
- [ ] No breaking changes within the same version
- [ ] New parameters have default values
- [ ] Response fields only added, not removed

**Notes:** {Any versioning concerns}

---

## Review Verdict

<!-- Choose one: APPROVED / APPROVED_WITH_CONDITIONS / CHANGES_REQUESTED / REJECTED -->

**Verdict:** {APPROVED / APPROVED_WITH_CONDITIONS / CHANGES_REQUESTED / REJECTED}

### Conditions (if applicable)

- {Condition 1 that must be met before merge}
- {Condition 2}

### Requested Changes (if applicable)

- {Change 1}
- {Change 2}

## References

- {Link to related ADR}
- {Link to contract.yaml file}
- {Link to provider/consumer Cell designs}
