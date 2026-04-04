<!--
Sync Impact Report
===================
Version change: 1.2.0 → 1.3.0
Modified sections:
  - Principle II "Test-First Development" — added RED LINE: t.Skip MUST NOT be treated as passing
  - Principle IV "Schema Isolation" — added RED LINE: new cross-schema linked queries FORBIDDEN
  - Principle VI "Security by Default" — added 3 new items: localhost fallback FORBIDDEN,
    noop/in-memory publisher FORBIDDEN in production, internal API trust model clarified,
    security-sensitive "not found" MUST be logged
  - Principle VIII "Data Consistency Guarantees" — added RED LINE 5 (mis-classifying
    execution state as L3) and RED LINE 6 (copying existing best-effort pattern
    without re-classification)
Added sections: none
Removed sections: none
Templates requiring updates:
  - .specify/templates/plan-template.md ✅ (no structural change needed;
    existing 6-question consistency pre-check covers all new red lines)
  - .specify/templates/spec-template.md ✅ (no structural change needed;
    Consistency Table, Consumer Contracts, Read Model Rebuild already present)
  - .specify/templates/tasks-template.md ✅ (no structural change needed)
Follow-up TODOs: none
-->

# WinMDM MVP Constitution

## Core Principles

### I. Strict Layered Architecture (DDD)

All backend code MUST follow four strict layers with unidirectional
dependencies:

- **handler**: Parameter binding and response serialization ONLY.
  Business logic is FORBIDDEN.
- **application**: Use-case orchestration. Coordinates domain,
  repository, and EventBus. MUST NOT contain domain rules.
- **domain**: Aggregates, entities, value objects, domain services.
  MUST NOT depend on any framework or infrastructure.
- **repository**: Data persistence ONLY. Business logic is FORBIDDEN.

Entities MUST be rich (behavior methods), NOT anemic. State changes
MUST go through methods (`entity.Cancel()`), direct field assignment
is FORBIDDEN. Entities MUST NOT be serialized as API responses; DTO
conversion is REQUIRED. Interface definitions live in `domain/`;
implementations live in `repository/` or `infrastructure/`.

### II. Test-First Development (NON-NEGOTIABLE)

TDD is mandatory for all code:

1. Write tests FIRST and confirm they FAIL.
2. Implement until tests PASS.
3. Modifying tests to force a pass is FORBIDDEN.

Coverage targets:
- Domain layer: pure unit tests (mock repository), >= 80%
- Application layer: integration tests (testcontainers + real DB), >= 60%
- Handler layer: `httptest`, covering parameter validation and error codes

Testing toolchain: `testify` for assertions, mocks in `internal/mock/`.

**RED LINE — Test Skip Abuse**: A `t.Skip(...)` call MUST NOT be
treated as a passing test. Critical consistency tests (outbox writes,
consumer idempotency, read model rebuild) MUST NOT be skipped by
default. Every skip site MUST have a documented justification comment
and a linked tracking issue. A PR that adds `t.Skip` to a consistency
test without documented justification is grounds for rejection.

### III. Dual-Channel Decoupling (ADR-01)

MDM and Agent are completely independent channels:

| Channel | Service | Protocol | Device Table | Auth |
|---------|---------|----------|--------------|------|
| MDM Native | winmdm-mdm :8081 | OMA-DM SyncML / HTTPS | `mdm_devices` | mTLS |
| Agent Extended | winmdm-agent :8084 | HTTPS polling (MVP) | `agent_devices` | JWT RS256 |

Cross-channel correlation key is `smbios_uuid` + `serial_number`,
used ONLY at the API layer for correlated queries. Direct cross-channel
data access is FORBIDDEN.

### IV. Schema Isolation

Each service owns an exclusive database schema:

| Service | Schema | Core Tables |
|---------|--------|-------------|
| winmdm-api | schema_api | users, roles, certificates |
| winmdm-mdm | schema_mdm | mdm_devices, mdm_sessions |
| winmdm-agent | schema_agent | agent_devices, agent_checkins |

Cross-schema JOINs are FORBIDDEN. Cross-service data exchange MUST
go through EventBus. Each schema change MUST have an up/down migration
pair. Existing migration files MUST NOT be modified.

**RED LINE — Cross-Schema Linked Queries**: Introducing a new query
that JOINs or uses a correlated subquery spanning two or more schemas
is grounds for immediate PR rejection, regardless of test coverage or
query performance. If cross-service data is required, it MUST be
pre-materialized in a read model within the consuming service's own
schema via EventBus-driven updates, or fetched via an explicit
internal API call. "It was easier to JOIN" is not a valid justification.

### V. Event-Driven Communication

Cross-aggregate and cross-service communication MUST use EventBus
(`eventbus.mode: redis/rabbitmq`). Direct cross-aggregate repository
calls are FORBIDDEN.

Every CUD operation MUST publish a corresponding domain event
(`created` / `updated` / `deleted`). Event consumers MUST be
idempotent (deduplicate by event ID).

Defined streams:

| Stream | Producer Events | Consumer |
|--------|----------------|----------|
| `device-events` | device.registered / info_updated / attribute_changed | Group Engine |
| `mgmt-events` | device.command_issued | Agent Gateway |
| `group-events` | group.membership_changed | Policy Engine |
| `policy-events` | policy.assigned / policy.aborted | Agent Gateway |

### VI. Security by Default

- All endpoints MUST have JWT middleware or be explicitly declared
  in the authentication whitelist.
- Certificate and key operations MUST produce audit log entries.
- List endpoints MUST enforce pagination with `page_size` upper
  bound <= 500.
- New queries MUST have corresponding indexes (verified via
  `EXPLAIN ANALYZE`).
- Agent heartbeat/check-in paths MUST NOT introduce synchronous
  external calls.
- Agent policy commands MUST verify server-side signatures.
- SQLite file permissions MUST be 0600.
- Sensitive fields (cert paths, keys) MUST NOT appear in plaintext
  log output.
- **RED LINE — Fail-Fast Infrastructure**: Production configuration
  MUST NOT fall back to `localhost` defaults, in-memory or noop
  publishers, or no-op EventBus implementations when the real
  infrastructure is unavailable. Infrastructure failures at startup
  MUST surface as hard errors (fail-fast), not silent degradation.
  A service that starts successfully with a noop EventBus provides
  false confidence and masks critical data loss.
- **RED LINE — Internal API Trust**: Internal APIs (`/internal/v1/…`)
  MUST NOT be treated as implicitly trusted simply because they are
  not publicly routed. They MUST have explicit network-level isolation
  (private subnet or service-mesh policy) and MUST NOT be callable
  by arbitrary callers without authentication. New internal endpoints
  MUST declare their caller allowlist in code comments or architecture
  docs.
- **RED LINE — Security-Sensitive Not-Found**: On security-sensitive
  paths (authentication, enrollment, token validation, authorization
  checks), a "resource not found" or "user not found" result MUST be
  treated as a security event: logged with `slog` at WARN level with
  request context, and — where appropriate — rate-limited. Silently
  returning a generic 404 without logging is FORBIDDEN on these paths.

### VII. Simplicity & Incremental Delivery

- Start with the minimum viable implementation. YAGNI applies.
- MVP uses HTTPS polling (15-min interval); WebSocket push is
  Phase 1 scope.
- Group and Policy services are STUBS in MVP (health endpoint only).
- Avoid over-engineering: do not add features, abstractions, or
  configurability beyond what is explicitly requested.
- Three similar lines of code are preferable to a premature
  abstraction.

### VIII. Data Consistency Guarantees

Every state change MUST be classified into one of three consistency
levels before implementation begins. The classification determines
the required propagation mechanism and review criteria.

#### Consistency Levels

| Level | Name | Rule | Allowed Mechanism |
|-------|------|------|-------------------|
| L1 | Local Strong Consistency | Same-service multi-table writes or derived fields | Single transaction or recomputable derivation; best-effort FORBIDDEN |
| L2 | Cross-service State Propagation | State that other services or read models depend on | Transactional outbox by default; direct publish FORBIDDEN for critical paths |
| L3 | Bypass Notification | Audit logs, cache invalidation, non-critical metrics | Best-effort publish permitted |

#### Mandatory Pre-Implementation Checklist

Before writing any implementation code for a feature that involves
state changes, the following six questions MUST be answered in the
spec's Consistency Table (see spec-template):

1. **Source of truth**: What is the authoritative write model for
   this change?
2. **L1 boundary**: Which states within the same service MUST be
   kept strongly consistent (single transaction or recomputable)?
3. **L2 propagation**: Which states are propagated to other services
   or read models via events?
4. **Outbox vs best-effort**: For each L2 event, will a lost event
   produce an incorrect or unrecoverable state? If yes, transactional
   outbox is REQUIRED; otherwise best-effort MAY be used.
5. **Consumer contract**: For each event consumer, what is the
   idempotency key, ACK strategy, and failure/retry policy?
6. **Read model rebuild**: Can each downstream read model be fully
   rebuilt from the source of truth? If NO, it MUST NOT rely on
   best-effort (lossy) events as its only update path.

#### Review Red Lines

The following patterns are grounds for immediate PR rejection:

- **RED LINE 1**: A critical state-propagation event (L2) published
  via best-effort (fire-and-forget) without a transactional outbox.
- **RED LINE 2**: "Write to DB, then publish event" where the event
  is acknowledged as droppable on the critical path. The pattern
  `repo.Update(...)` followed immediately by `eventbus.Publish(...)`
  in the same function body — without an outbox — is FORBIDDEN for
  L2 events, because the publish can fail silently after a successful
  write.
- **RED LINE 3**: An event consumer that ACKs a malformed or
  incomplete critical message without compensation or dead-letter
  routing. `return nil` after a failed `json.Unmarshal` or schema
  validation on a critical consumer is FORBIDDEN; the message MUST
  be routed to a dead-letter stream or trigger an alert.
- **RED LINE 4**: A read model that cannot be rebuilt from source
  of truth, yet is driven exclusively by best-effort events.
- **RED LINE 5**: Classifying an execution-state event (device
  compliance result, policy execution outcome, read model membership
  sync) as L3 (bypass notification / best-effort) when the downstream
  consumer cannot tolerate the resulting staleness or inconsistency.
  "It's just a notification" is not a valid classification when the
  downstream model's correctness depends on it. When in doubt,
  classify as L2 and justify any downgrade to L3 explicitly in the
  spec's Consistency Table.
- **RED LINE 6**: Copying an existing `eventbus.Publish(...)` pattern
  from the codebase into a new event chain without first independently
  classifying the new event's consistency level. The existence of
  prior best-effort publishes in the repo (e.g., in `group/service.go`
  or `management_service.go`) is NOT evidence that best-effort is
  acceptable for new chains. Every new producer MUST complete the
  Consistency Table entry; copy-paste propagation of L3 patterns into
  L2 contexts is FORBIDDEN.

## Technology Stack & Constraints

- **Language**: Go (latest stable)
- **Web Framework**: Standard library `net/http` + chosen router
- **Database**: PostgreSQL (one instance, schema-per-service)
- **Cache / EventBus**: Redis Streams (default) or RabbitMQ
- **Frontend**: Vue 3 + TypeScript + Pinia (Sprint 2+)
- **Containerization**: Docker Compose for local development
- **Logging**: `slog` only; `fmt.Println` / `fmt.Printf` FORBIDDEN
- **Error Handling**: Unified `errcode` package; bare `errors.New`
  FORBIDDEN for external exposure; all errors MUST be handled
  (`_` suppression FORBIDDEN)
- **Context**: All function first parameters MUST be `context.Context`
- **Naming**: DB fields `snake_case`, JSON fields `camelCase`
- **Commits**: Conventional Commits (`feat/fix/refactor/docs`);
  references to "Claude" or "AI-generated" are FORBIDDEN

## Development Workflow

1. **Branch before coding**: All work happens on feature branches.
2. **TDD cycle**: Red → Green → Refactor (see Principle II).
3. **Commit discipline**: Conventional Commits; update relevant
   `README.md` after modifications.
4. **New resource checklist**: Follow `.claude/rules/design-card.md`
   for any new aggregate root or top-level API resource.
5. **Existing object modification**: Consult the corresponding
   object summary in `.claude/rules/objects/`.
6. **Migration discipline**: Up/down pairs; never modify existing
   migration files; new NOT NULL columns MUST have defaults;
   large-table indexes use `CREATE INDEX CONCURRENTLY`.
7. **Security gate**: Before any new endpoint goes live, verify
   all items in the Security by Default principle (VI).
8. **Consistency gate**: Before any implementation involving state
   changes, complete the Consistency Table (Principle VIII) in the
   spec. Implementation MUST NOT begin until all six questions
   are answered.
9. **Spec-kit pipeline**: For feature-level work, follow
   `/speckit.specify` → `/speckit.plan` → `/speckit.tasks` →
   `/speckit.implement`.
   `/speckit.specify` MUST create a dedicated feature branch from the
   latest `develop` baseline before proceeding. When the repository
   workflow uses `git worktree`, it SHOULD create or switch into a
   dedicated worktree for that branch before continuing. All
   subsequent pipeline steps operate inside that branch/worktree
   context.
10. **Sprint-level tasks**: Use `/plan` command for ad-hoc work
    within a sprint.

### Work Type Numbering

All work items (branches, spec directories, migration prefixes)
MUST use a three-digit numeric prefix that indicates the work
category. The prefix determines the branch name pattern
`{NNN}-{short-description}`, the worktree path
`worktrees/{category}/{NNN}-{short-description}/`, and the spec
directory `specs/{NNN}-{short-description}/`.

| Range | Category | Worktree Folder | Description | Examples |
|-------|----------|----------------|-------------|----------|
| 001–199 | Feature | `feature/` | New functionality or enhancements | `021-audit-dashboard` |
| 200–399 | Fix / Refactor | `fix/` | Bug fixes, hardening, and code restructuring | `201-login-timeout-fix` |
| 800–899 | Docs / Architecture | `docs/` | Documentation and architectural decisions | `800-adr-docs` |
| 900–999 | Experiment / Spike | `experiment/` | Exploratory or throwaway prototypes | `901-websocket-poc` |

> Ranges 400–799 are reserved for future categories.

Rules:

- Numbers MUST be assigned sequentially within each range.
- Existing items (001–020) retain their current numbers; new
  features continue from the next available number in the
  001–199 range.
- A work item MUST NOT change its number after creation.
- The category range is determined at creation time based on
  the nature of the work; if ambiguous, default to Feature
  (001–199).
- Branch names remain flat (`{NNN}-{short-description}`);
  the category folder applies only to the worktree path.

## Governance

- This constitution is the highest-authority document for the
  WinMDM MVP project. It supersedes all other practice guides
  when conflicts arise.
- All code reviews and PRs MUST verify compliance with these
  principles.
- Amendments REQUIRE: (a) documented rationale, (b) version bump
  per semantic versioning, (c) propagation check across dependent
  templates.
- Versioning policy: MAJOR for principle removal/redefinition,
  MINOR for new principle/section, PATCH for clarifications.
- Runtime development guidance lives in `CLAUDE.md` and
  `.claude/rules/` files; those MUST stay consistent with this
  constitution.
- Complexity beyond these principles MUST be explicitly justified
  in the plan's Complexity Tracking table.

**Version**: 1.3.0 | **Ratified**: 2026-02-21 | **Last Amended**: 2026-03-16
