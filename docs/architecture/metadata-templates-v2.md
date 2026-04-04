# GoCell Metadata Model V2

## Design Goals

This document defines the structural and topological rules for GoCell's metadata model. It is one part of the V2 specification — operational semantics (e.g., consistency level runtime behavior) are defined in companion documents. It is derived from first principles:

1. Every fact has exactly one **authoritative source**. Redundant anchors are permitted for readability when they are deterministically derivable and tool-validated for consistency (see "derived-anchor" category).
2. Every metadata field belongs to exactly one value category: **canonical**, **derived-anchor**, **inherited**, **generated**, or **delivery-only** (see Value Resolution Model).
3. Dynamic delivery status does not belong in canonical metadata.
4. Contract structure belongs in versioned schema files, not in implementation directories.
5. The model must provide sufficient input contracts for these six tools:
   - `validate-meta`
   - `generate-assembly`
   - `select-targets`
   - `verify-slice`
   - `verify-cell`
   - `run-journey`
6. Derivable facts should be generated or validated, not hand-maintained.

## Model Guardrails

The following four statements are axiomatic constraints on the model. They bound interpretation of all subsequent rules and templates.

### G1. Dependency graph authority is not symmetric

`slice.contractUsages` is the **authoritative implementation-level truth** for which contracts a slice touches. `journey.contracts` is an **acceptance-level curated view** — a human-selected subset of contracts relevant to a journey's pass criteria. These two are not parallel authorities. Any decision requiring a complete dependency graph must aggregate from `slice.contractUsages`, never from `journey.contracts`.

### G2. Journey semantic roles are explicitly ranked

A journey spec carries three roles with distinct canonical strength:

| Role | Canonical strength | Field |
|------|--------------------|-------|
| Acceptance specification | **canonical** — defines what "done" means | `goal`, `passCriteria` |
| Routing anchor | **best-effort** — validated against curated contracts only (C13) | `cells` |
| Acceptance curation | **curated view** — human-selected, not exhaustive | `contracts` |

Journey is NOT a canonical dependency source. `cells` is the best available routing anchor but is not a proven exhaustive set. `contracts` must never be used as input to decisions requiring completeness.

### G3. consistencyLevel is a coarse capability label, not a semantic proxy

`consistencyLevel` (L0-L4) represents a cell's or contract's **governance capability tier** — a single-axis total order used for topological validation (C4, C5, C6, C7). It does NOT define or imply:

- Delivery semantics (use `deliverySemantics` on event contracts)
- Replay guarantees (use `replayable` on event/projection contracts)
- Idempotency scope (use `idempotencyKey` on event contracts)
- Runtime behavior (defined in `docs/architecture/consistency-levels.md`)

These operational properties are **independent fields** on the contract or slice, not derivable from `consistencyLevel`. C6's kind-minimum constraints (e.g., "event requires >= L2") encode domain prerequisites, not semantic definitions.

### G4. status-board is delivery-only — no architectural gate

`journeys/status-board.yaml` is a **delivery-only** artifact. It tracks project-management state (doing/blocked/done), not architectural correctness.

Consequences:

- `validate-meta` MUST NOT use status-board absence or content to block CI on feature branches.
- `validate-meta` MAY emit a warning when a journey has no status-board entry.
- Release-branch gating of status-board completeness, if desired, is a **CI pipeline policy**, not a `validate-meta` structural rule.

This means rule D9 is a **CI-pipeline recommendation**, not a model-level invariant. It should be documented as such.

### G5. select-targets is advisory until non-contract dependency graph exists

`select-targets` output is **advisory** — a best-effort recommendation for test targeting. It must not be used as proof that untested code is safe. The current model only tracks contract-mediated dependencies (`contractUsages`) and L0 direct imports (`l0Dependencies`). Non-contract coupling (shared in-process state, runtime configuration, convention-based routing) is invisible to the model. Until a V3 explicit non-contract dependency graph is introduced, `select-targets` results should be treated as optimization guidance, not as a completeness guarantee.

## Canonical Truth Rules

### Canonical Field Names

Use these names as the canonical field set:

- `id`
- `owner`
- `schema.primary`
- `belongsToCell`
- `contractUsages`
- `endpoints` (kind-specific subfields)
- `lifecycle`
- structured `verify`

### Forbidden Legacy Names

Do not use these names in any metadata — including hand-authored and generated files:

- `cellId`
- `sliceId`
- `contractId`
- `assemblyId`
- `ownedSlices`
- `authoritativeData`
- `producer` / `consumers` (replaced by kind-specific endpoints)
- `callsContracts` / `publishes` / `consumes` (replaced by `contractUsages`)
- `status` on contracts (replaced by `lifecycle`)
- `version` on contracts (derived from `id` last segment)

### Dynamic Status vs Lifecycle Governance

Metadata fields fall into two categories of mutability:

**Delivery-dynamic fields** change frequently (per sprint or more often). They are forbidden in canonical metadata files and belong only in `journeys/status-board.yaml`:

- `readiness`
- `risk`
- `blocker`
- `done`
- `verified`
- `nextAction`
- `updatedAt`

**Lifecycle-governance fields** change at version-migration cadence (a few times across the entire contract lifetime). They are permitted in canonical metadata:

- `lifecycle` (`draft` / `active` / `deprecated`) on contracts

### Canonical Ownership Matrix

Every fact has exactly one canonical owner. Derived views are explicitly marked.

| Fact | Canonical Owner | Category | Notes |
|------|-----------------|----------|-------|
| Which journeys exist | `journeys/J-*.yaml` glob | canonical | catalog is optional generated index |
| What a journey means | `journeys/*.yaml` | canonical | goal, owner, cells, contracts, passCriteria |
| Journey focus contracts | `journeys/*.yaml` `contracts` | canonical | Curated subset, not exhaustive derivation |
| Which cell a slice belongs to | `slice.belongsToCell` | derived-anchor | Must equal directory path cell-id (B10) |
| Cell consistency level | `cell.yaml` `consistencyLevel` | canonical | slice and contract constrained by this |
| Slice consistency level | `cell.yaml`, override in `slice.yaml` | inherited | effective = child ?? parent |
| Cell / slice owner | `cell.yaml`, override in `slice.yaml` | inherited | effective = child ?? parent |
| Contract kind | `contract.kind` | derived-anchor | Must equal id first segment (D12) |
| Contract version | Derived from `contract.id` last segment | derived-anchor | Must match directory path (B7) |
| Contract boundary | `contract.yaml` kind-specific endpoints | canonical | Slice references via `contractUsages` |
| Which contracts a slice uses | `slice.yaml` `contractUsages` | canonical | Implementation-level mapping |
| Slice file ownership | `slice.allowedFiles` or convention default | canonical | Default: `cells/{cell-id}/slices/{slice-id}/**`; always authoritative |
| Which cells are packaged together | `assembly.yaml` `cells` | canonical | Packaging only |
| Assembly boundary contracts | **generated** from cells + contracts | generated | Optional hand-curated transition override |
| Assembly smoke targets | **generated** from `cell.verify.smoke` | generated | Optional hand-curated transition override |
| Dynamic delivery state | `journeys/status-board.yaml` only | delivery-only | Forbidden in all other metadata files |

### Value Resolution Model

Every metadata field belongs to exactly one of five categories. Validation rules operate on **effective values**.

| Category | Definition | Resolution Rule |
|----------|-----------|-----------------|
| **canonical** | Hand-authored, unique fact source, no other source can substitute | Use declared value directly |
| **derived-anchor** | Value is deterministically computable from another canonical field; optional — when absent, tool derives; when declared, tool validates declared == computed | All derived-anchor fields use **optional-with-validation**: omit to let tools derive, declare to make explicit (tool checks consistency) |
| **inherited** | Value inherited from parent-layer canonical field; declaration overrides but is constrained | effective = child declared value ?? parent value |
| **generated** | Produced by tooling, not hand-authored in steady state | Tool derives; hand-curated allowed as transition override |
| **delivery-only** | Exists only in `journeys/status-board.yaml` | Not subject to architectural topology validation or impact routing. Enum validity rules (A9-A11) and format rule (D8) still apply. Per guardrail G4, status-board completeness is a CI-pipeline policy, not a model invariant. |

**Note on `allowedFiles`**: `allowedFiles` is classified as **canonical** (not derived-anchor). When absent, the convention default `cells/{cell-id}/slices/{slice-id}/**` is used as the initial canonical value. When declared, the declared value is the canonical value. In both cases, the effective value is authoritative — there is no "computed vs declared" validation. This preserves the "exactly one value category" principle: `allowedFiles` is always canonical, the convention merely provides a sensible default.

Implication:

- `cell.yaml` should not hand-maintain `slices`, `journeys`, or `contracts` lists.
- If those summaries are useful, generate them into a registry or derived view.
- `journeys/catalog.yaml` is an optional generated artifact. Journey discovery is by `journeys/J-*.yaml` glob.

## Three-Layer Model

| Layer | File | Canonical Responsibility |
|-------|------|--------------------------|
| 1 | `journeys/*.yaml` | Single-journey acceptance spec |
| 2 | `cells/*/cell.yaml` | Governance partition: runtime boundary (L1+) or computation partition (L0) |
| 3 | `cells/*/slices/*/slice.yaml` | Work mapping and impact routing |

Cross-layer assets (not numbered layers):

- **Contract** (`contracts/**/contract.yaml`): cross-cell boundary definition, referenced by layers 1-3.
- **Assembly** (`assemblies/*/assembly.yaml`): physical packaging of cells, references layer 2 entities.
- **Actor Registry** (`actors.yaml`): non-cell runtime actors that participate in contracts.
- **Status Board** (`journeys/status-board.yaml`): journey delivery-status projection (delivery-only). Not a structural decomposition layer — it is orthogonal to the three-layer hierarchy.

All cross-cell interactions require a contract, with one exception: L0 cells (computation partitions) may be directly imported by sibling cells in the same assembly without a contract (see L0 Cell Interaction Model). For all other cells (L1+), see "When to Model Lightweight Contracts" in the contract section for guidance on keeping overhead low.

## Layer 1: journeys/*.yaml

A journey spec is a stable acceptance specification. It defines what "done" means for an end-to-end user scenario.

```yaml
id: J-sso-login
goal: user completes SSO login and receives valid session
owner:
  team: platform
  role: journey-owner
primaryActor: end-user
cells:
  - access-core
  - audit-core
  - config-core
fixtures:
  - fixture-oidc-provider
  - fixture-user-basic
contracts:
  - http.auth.login.v1
  - event.session.created.v1
passCriteria:
  - text: OIDC redirect completes
    mode: auto
    checkRef: journey.J-sso-login.oidc-redirect
    assert: { type: httpStatus, expect: 302 }
  - text: callback token exchanged
    mode: auto
    checkRef: journey.J-sso-login.token-exchange
  - text: session created in DB
    mode: auto
    checkRef: journey.J-sso-login.session-db
    assert: { type: rowExists, table: sessions, key: session_id }
  - text: JWT cookie set
    mode: auto
    checkRef: journey.J-sso-login.jwt-cookie
  - text: user info accessible via /me
    mode: auto
    checkRef: journey.J-sso-login.auth-me
    assert: { type: httpStatus, expect: 200 }
```

`contracts` is a **curated focus subset** — not an exhaustive derivation. Inclusion criterion: a contract is listed if it is **directly asserted by a `passCriteria` entry** (i.e., the contract is exercised by at least one auto-check in this journey). Routing completeness is provided by `journey.cells` on a best-effort basis (rule C13 validates cells against the curated contracts, not against the full contract universe). Any decision requiring a complete contract list must use `slice.contractUsages` aggregation, not `journey.contracts`.

`journey.contracts` and `slice.contractUsages` answer different questions and are not redundant: `contractUsages` records which contracts a slice implementation touches (implementation-level); `journey.contracts` records which contracts a journey's acceptance criteria exercise (acceptance-level). Neither is derived from the other.

**Design trade-off — multi-role object**: Journey spec intentionally serves three roles: acceptance specification (`goal` + `passCriteria`), test plan (`checkRef` + `fixtures`), and routing anchor (`cells` for `select-targets`). This coupling simplifies governance (one file, one owner) at the cost of semantic purity. In particular, `journey.cells` is the best available routing anchor but is not a mathematically proven exhaustive set — it is validated only against the curated contracts list (C13), and contract-inferred mode is a best-effort refinement, not a precise dependency graph.

### passCriteria Fields

| Field | Condition | Description |
|-------|-----------|-------------|
| `text` | required | Human-readable acceptance criterion |
| `mode` | required | `auto` (validated by `run-journey`) or `manual` (human sign-off) |
| `checkRef` | required when `mode: auto` | Logical identifier resolving to an executable check (see verify naming convention) |
| `assert` | optional | Structured assertion hint: `{ type, expect, ... }` |

`run-journey` executes all `auto` criteria via their `checkRef` targets and outputs `manual` criteria as a checklist for human review.

### Required Fields

- `id`
- `goal`
- `owner`
- `cells`
- `contracts`
- `passCriteria`

### Recommended Optional

- `primaryActor`
- `fixtures`

### Fixture Convention

Fixtures prepare the test environment for `run-journey`. Each fixture id in a journey spec must correspond to a fixture definition file.

**File location**: `fixtures/{fixture-id}.yaml`

**Structure**:

```yaml
id: fixture-oidc-provider
type: service-mock
description: mock OIDC provider for SSO login testing
setup:
  image: ghcr.io/gocell/mock-oidc:latest
  env:
    ISSUER_URL: http://localhost:9090
    CLIENT_ID: test-client
teardown: automatic
```

**Fields**:

| Field | Required | Description |
|-------|----------|-------------|
| `id` | yes | Must match the fixture id referenced in journey specs |
| `type` | yes | `service-mock` (container), `seed-data` (DB seed), `config-override` (runtime config) |
| `description` | no | Human-readable purpose |
| `setup` | yes | Type-specific setup configuration |
| `teardown` | no | `automatic` (default) or `manual` |

**Loading order**: `run-journey` loads fixtures in declaration order before executing `passCriteria`. Fixtures with `teardown: automatic` are cleaned up after the owning journey run completes (all `passCriteria` evaluated), regardless of pass/fail result. For `teardown: manual`, cleanup is deferred to operator.

### Fixture Isolation Model

- **Scope**: per-journey-run. Each journey run gets its own fixture instances. Shared fixture ids across journeys result in independent setup/teardown cycles.
- **seed-data isolation**: `seed-data` fixtures must use unique key prefixes or dedicated test schemas. Convention: include a `namespace` field that is runtime-injected:

```yaml
id: fixture-user-basic
type: seed-data
setup:
  table: users
  data: [...]
  namespace: "{{runId}}"
teardown: automatic
```

| type | namespace behavior |
|------|-------------------|
| `service-mock` | Container name suffixed with namespace; port isolation |
| `seed-data` | Data keys prefixed with namespace |
| `config-override` | Not needed (override is process-local) |

`namespace` is optional. When absent, no isolation (sufficient for serial single-journey execution).

**Validation**: `validate-meta` checks that every fixture referenced in a journey spec has a corresponding `fixtures/{fixture-id}.yaml` file (rule B9).

## journeys/status-board.yaml (Delivery-Only)

```yaml
- journeyId: J-sso-login
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
  targetDate: 2026-04-18
  evidenceRefs:
    - tests/journey/J-sso-login.log

- journeyId: J-audit-login-trail
  state: todo
  risk: medium
  blocker: waiting for audit-core scaffolding
  updatedAt: 2026-04-02
  evidenceRefs: []
```

### Required Fields

- `journeyId` — must reference an existing journey; must be unique within status-board (one entry per journey). Per guardrail G4, missing entries trigger warning only (see D9).
- `state` — `draft` | `todo` | `doing` | `blocked` | `done`
- `risk` — `low` | `medium` | `high`
- `blocker` — required string; `""` when no blocker (null or omission is invalid)
- `updatedAt` — ISO date, updated each time the entry changes
- `evidenceRefs`

### Recommended Optional

- `targetDate`
- `updatedBy`

`updatedAt` is an ISO date, updated each time the entry changes. Tooling may write it automatically. A `doing` entry with stale `updatedAt` is a signal for review.

## Layer 2: cell.yaml

`cell.yaml` owns governance-partition facts — runtime boundary and data sovereignty for L1+ cells, computation partition metadata for L0 cells. It does not maintain reverse indexes of slices, journeys, or contracts.

**Directory convention**: The cell directory name must equal `cell.id`. For example, `cell.id: access-core` lives at `cells/access-core/cell.yaml`. This convention is used by verify resolution and `select-targets` — no separate directory field is needed.

```yaml
id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
noSplitReason:
  - session creation and identity verification share one transaction boundary
```

### Required Fields

- `id`
- `type` — `core` | `edge` | `support`
- `consistencyLevel`
- `owner` — `{ team, role }`
- `schema.primary` — required for L1+ cells (data sovereignty requires schema ownership); optional for L0 cells (computation partitions do not own data)
- `verify.smoke`

### L0 Cell Interaction Model

**Design trade-off**: L0 is an intentional relaxation of the cell boundary concept. Cells at L1+ are runtime boundaries with data sovereignty and contract-mediated communication. L0 cells are **computation partitions** — they retain cell-level governance (ownership, smoke testing, select-targets routing) without runtime isolation. This pragmatic extension trades architectural purity for reduced contract overhead on pure-computation code.

L0 cells (`consistencyLevel: L0`) are **pure computation libraries** compiled and linked directly within an assembly binary. They expose functionality through Go interfaces and are called in-process by sibling cells in the same assembly — no contract is needed.

- L0 cells **may** be directly imported by other cells in the same assembly (exception to the contract requirement for L1+ cells).
- L0 cells **must not** hold mutable state, run as independent processes, or appear in any contract endpoint field (enforced by C7).
- L0 cells differ from `pkg/` in that they have cell-level metadata (`cell.yaml`), participate in ownership tracking, and are subject to `verify.smoke`.

**Explicit dependency declaration**: Cells that import an L0 cell must declare the dependency in their `cell.yaml`:

```yaml
# cell.yaml of the importing cell
l0Dependencies:
  - cell: shared-crypto
    reason: deterministic hashing utilities
```

`l0Dependencies` is required when a cell imports any L0 cell. `validate-meta` checks that each referenced L0 cell exists, has `consistencyLevel: L0`, and is in the same assembly. `select-targets` uses `l0Dependencies` to route L0 cell changes to dependent cells.

### Recommended Optional

- `noSplitReason`
- `schema.tables`

### Extension Examples

```yaml
allowedDependencies:
  - config-core
servedRoles:
  - end-user
stakeholders:
  - security
```

Extensions are not part of the minimum stable model. They may be useful for specific teams or tools.

## Layer 3: slice.yaml

`slice.yaml` is the canonical source for work mapping and contract usage at the implementation level.

**Directory convention**: The slice directory name must equal `slice.id`. For example, `slice.id: session-login` lives at `cells/{cell-id}/slices/session-login/`. This convention is used by verify resolution and `select-targets` routing.

```yaml
id: session-login
belongsToCell: access-core
owner:
  team: platform
  role: slice-owner
consistencyLevel: L2
contractUsages:
  - contract: http.auth.login.v1
    role: serve
  - contract: http.config.get.v1
    role: call
  - contract: event.session.created.v1
    role: publish
traceAttrs:
  extra:
    - session_id
    - user_id
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
    - contract.event.session.created.v1.publish
allowedFiles:
  - cells/access-core/slices/session-login/**
```

### Required Fields

- `id`
- `contractUsages` — list of `{ contract, role }` entries (may be `[]` for L0/L1 slices without cross-cell interactions)
- `verify.unit`
- `verify.contract` — list (may be `[]` for L0/L1 slices without contract usages)
- `verify.waivers` — optional list of explicit waivers for contractUsages not covered by `verify.contract`:

```yaml
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
  waivers:
    - contract: http.config.get.v1
      owner: platform-team
      reason: config read-only call, tested via integration suite
      expiresAt: 2026-06-01
```

**Verify/waiver closed loop**: For every provider-role `contractUsages` entry, the slice must have either a matching `verify.contract` identifier OR a `verify.waivers` entry for that contract. This is enforced by rule C19 (error, not warning). Waivers require `owner`, `reason`, and `expiresAt`. Expired waivers (past `expiresAt`) are treated as missing — C19 reports error.

### Optional Fields (Derived-Anchor)

- `belongsToCell` — when absent, derived from directory path `cells/{cell-id}/slices/...`. When declared, must equal the derived value (B10). Retained for readability.

### Optional Fields (Inherited from Cell)

- `owner` — when absent, inherits `cell.owner`
- `consistencyLevel` — when absent, inherits `cell.consistencyLevel`; when present, must not exceed cell value

### Optional Fields (Convention Default)

- `allowedFiles` (canonical with convention default) — when absent, defaults to `cells/{cell-id}/slices/{slice-id}/**` per directory convention. Only declare explicitly when a slice owns files outside its conventional directory (e.g., shared proto files, cross-directory generated code). When present, the declared value replaces (not extends) the convention default.

### Optional Fields (Migrations)

- `migrations` — list of active contract version migrations this slice is performing:

```yaml
migrations:
  - contract: http.auth.login.v1
    target: http.auth.login.v2
    deadline: 2026-05-01
```

When present, C17 permits referencing the deprecated source contract. `validate-meta` checks that `target` exists and has `lifecycle: active`.

### Recommended Optional

- `traceAttrs.extra` — domain-specific trace attributes beyond the platform envelope

### contractUsages Role-to-Kind Mapping

Contract endpoint fields use **identity nouns** (server, clients — who participates), while `contractUsages.role` uses **behavior verbs** (serve, call — what the slice does). The two vocabularies describe the same concept from different perspectives; the mapping is fixed and 1:1.

Each `contractUsages` entry has a `contract` (referencing an existing contract by id) and a `role`. Valid combinations:

| Kind | Provider role | Client role |
|------|--------------|-------------|
| `http` | `serve` | `call` |
| `event` | `publish` | `subscribe` |
| `command` | `handle` | `invoke` |
| `projection` | `provide` | `read` |

Validation:

- Provider role → `slice.belongsToCell` must equal the contract's provider-side endpoint actor.
- Client role → `slice.belongsToCell` must appear in the contract's client-side endpoint list (or list is `["*"]`).

### Verify Naming Convention

Verify identifiers use a **prefix-dispatched** format. The first dot-separated segment determines the category; the remaining segments are category-specific. Segment count is not fixed.

| Category | Format | Example | Resolution |
|----------|--------|---------|------------|
| `smoke` | `smoke.{cell-id}.{name}` | `smoke.access-core.startup` | `go test ./cells/access-core/... -run TestSmoke_access_core_startup` |
| `unit` | `unit.{slice-id}.{scope}` | `unit.session-login.service` | `go test ./cells/access-core/slices/session-login/... -run TestUnit_session_login_service` |
| `contract` | `contract.{contract-id}.{role}` | `contract.http.auth.login.v1.serve` | `go test ./contracts/http/auth/login/v1/... -run TestContract_http_auth_login_v1_serve` |
| `journey` | `journey.{journey-id}.{step}` | `journey.J-sso-login.oidc-redirect` | `go test ./tests/journey/... -run TestJourney_J_sso_login_oidc_redirect` |

**Normalization rules** for generating Go test function names and paths:

- Category prefix is stripped; **all remaining segments** form the scope — no truncation.
- For `contract` category, the boundary between `{contract-id}` and `{role}` is determined by the version segment: the `v{N}` segment (matching `v\d+`) is always the last segment of the contract-id, and the segment immediately following it is the `{role}`. No global state or contract registry lookup is needed.
- Hyphens (`-`) in identifiers are converted to underscores (`_`) in Go test names.
- Dots (`.`) in identifiers are converted to underscores (`_`) in Go test names.
- The test function name is `Test{Category}_{all_remaining_segments_normalized}` (e.g., `contract.http.auth.login.v1.serve` → `TestContract_http_auth_login_v1_serve`).

**Contract-category parsing**: For `contract.{contract-id}.{role}`, parsing is deterministic without global state. Split on `.`, then: the last segment must be a known role (`serve|call|publish|subscribe|handle|invoke|provide|read`); the second-to-last segment must match `v\d+` (version terminator); everything between the `contract` prefix and the role is the `{contract-id}`. Example: `contract.http.auth.login.v1.serve` → contract-id = `http.auth.login.v1`, role = `serve`.

**Path resolution**:

- `smoke` and `unit`: cell directory is `cells/{cell-id}/`, derived from `slice.belongsToCell`.
- `contract`: directory is `contracts/{kind}/{domain-path}/{version}/`, parsed from the contract id (first segment = kind, last segment = version, middle segments = directory path).
- `journey`: directory is always `tests/journey/`.

Platform trace envelope fields (`traceId`, `journeyId`, `callerCellId`, `calleeCellId`) are runtime standards, not per-slice metadata. `traceAttrs` should only describe additional domain attributes.

## Contract Model

Contracts define cross-cell boundary agreements. Every cross-cell interaction between L1+ cells requires a contract. L0 cells (computation partitions) are exempt — they are imported directly within the same assembly (see L0 Cell Interaction Model).

### Common Required Fields

All contract kinds share these required fields:

- `id` — format: `{kind}.{domain-path}.v{N}` where `{domain-path}` is one or more dot-separated segments (e.g., `auth.login`, `device.enqueue`). Parsing rule: first segment = `kind`, last segment = `v{N}` version, all middle segments = domain path. The domain path maps to the directory structure under `contracts/{kind}/`. The version is derived from the last segment of `id` — there is no separate `version` field.
- `kind` — `http` | `event` | `command` | `projection` (derived-anchor, optional — when absent, derived from the first segment of `id`; when declared, must equal it. Validated by D7. Retained for readability.)
- `ownerCell` — the cell responsible for contract lifecycle (governance ownership). Inherited: when absent, defaults to the provider-side endpoint actor (if it is a cell); when declared, must reference a `cell.id`, not an external actor. Only declare when governance owner differs from provider.
- `consistencyLevel` — value from the totally ordered set `L0 < L1 < L2 < L3 < L4`. All comparison operators in validation rules (C4, C5, C6, C7) use this ordering. The operational semantics of each level (write confirmation, idempotency boundaries, replay guarantees) are defined in `docs/architecture/consistency-levels.md`; this document defines only the structural and topological constraints.
- `lifecycle` — `draft` | `active` | `deprecated`
- `schemaRefs` — paths relative to the contract directory
- `endpoints` — kind-specific (see below)

### Common Recommended Fields

- `summary`
- `semantics` — recommended key per kind:

| Kind | Recommended key | Meaning |
|------|----------------|---------|
| `http` | `semantics.operation` | What operation this endpoint performs |
| `event` | `semantics.fact` | What fact this event declares has occurred |
| `command` | `semantics.action` | What action this command requests |
| `projection` | `semantics.view` | What view this projection presents |

### Kind-Specific Endpoints

Each contract kind defines exactly two endpoint fields with unambiguous direction:

| Kind | Provider-side field | Client-side field | Provider meaning | Client meaning |
|------|--------------------|--------------------|------------------|----------------|
| `http` | `endpoints.server` | `endpoints.clients` | Serves the endpoint | Calls the endpoint |
| `event` | `endpoints.publisher` | `endpoints.subscribers` | Emits the event | Receives the event |
| `command` | `endpoints.handler` | `endpoints.invokers` | Processes the command | Sends the command |
| `projection` | `endpoints.provider` | `endpoints.readers` | Materializes the view | Queries the view |

Rules:

- Provider-side field is always a **single actor** (cell id or external actor id).
- Client-side field is a **list of actors**, or `["*"]` for open/public contracts.
- `["*"]` means any registered actor may consume. `validate-meta` skips client membership checks but still requires actor registration for any slice referencing the contract.
- `["*"]` must not be mixed with named actors in the same list.
- `ownerCell` is governance ownership (who owns the contract lifecycle). It must be a cell, not an external actor. `ownerCell` carries three specific responsibilities: **(1)** version evolution decisions (when to deprecate, when to publish v2), **(2)** schema compatibility approval, **(3)** breaking-change migration coordination. `ownerCell` does NOT carry runtime responsibilities — runtime guarantees are the provider-side actor's domain. It is often the same as the provider-side actor, but not always (e.g., a cell may own the lifecycle of a contract whose provider is an external gateway). When ownerCell equals the provider (the majority case), the duplication is intentional: governance and runtime are distinct semantic roles even when assigned to the same actor. When ownerCell != provider, defaults to provider (see inherited ownerCell below).

### Lifecycle Values

| Value | Meaning |
|-------|---------|
| `draft` | Contract defined but not yet serving traffic. Client-side endpoint list may be `[]`. |
| `active` | Contract is live. At least one client expected (or `["*"]`). |
| `deprecated` | Contract is scheduled for removal. Clients should migrate. |

**State transitions** are one-directional: `draft → active → deprecated`. Reverting from `deprecated` to `active` or from `active` to `draft` is not permitted — create a new version instead. Retention period for `deprecated` contracts before deletion is a team policy decision, not enforced by `validate-meta`.

### Contract Defaults

Unless overridden in an individual contract:

- `compatibilityPolicy`: `{ breaking: [remove_field, change_field_semantics], nonBreaking: [add_optional_field] }`
- `traceRequired`: `true`

Contracts only need to declare these fields when deviating from defaults.

### HTTP Contract

```yaml
id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
summary: authenticate user and create login session
endpoints:
  server: access-core
  clients:
    - edge-bff
schemaRefs:
  request: request.schema.json
  response: response.schema.json
```

### Event Contract

```yaml
id: event.session.created.v1
kind: event
ownerCell: access-core
consistencyLevel: L2
lifecycle: active
summary: session creation finalized and visible to downstream consumers
endpoints:
  publisher: access-core
  subscribers:
    - audit-core
    - config-core
schemaRefs:
  payload: payload.schema.json
  headers: headers.schema.json
semantics:
  fact: session creation completed
replayable: true
idempotencyKey: eventId
deliverySemantics: at-least-once
orderingSemantics: aggregateId+sequence
```

Additional required for `event`:

- `replayable`
- `idempotencyKey`
- `deliverySemantics` — `at-least-once` | `exactly-once` | `at-most-once`

Additional recommended for `event`:

- `orderingSemantics`

### Command Contract

```yaml
id: command.device.enqueue.v1
kind: command
ownerCell: device-command-core
consistencyLevel: L2
lifecycle: active
summary: request command execution on target device
endpoints:
  handler: device-command-core
  invokers:
    - edge-bff
schemaRefs:
  request: request.schema.json
  ack: ack.schema.json
  result: result.schema.json
semantics:
  action: enqueue device command
```

Note: `endpoints.handler: device-command-core` is the cell that processes the command, while `endpoints.invokers` lists the callers. `ownerCell` matches the handler in this example.

### Projection Contract

```yaml
id: projection.audit.timeline.v1
kind: projection
ownerCell: audit-core
consistencyLevel: L3
lifecycle: active
summary: read-only audit timeline view
endpoints:
  provider: audit-core
  readers:
    - edge-bff
schemaRefs:
  projection: projection.schema.json
replayable: true
```

Additional required for `projection`:

- `replayable`

### Schema Placement

Cross-boundary schemas belong to the contract version directory, not to cell implementation directories. Directory segments use **singular form** matching `contract.kind`:

```
contracts/{kind}/{domain-path...}/{version}/
```

The `{domain-path}` segments match the middle segments of `contract.id` (between `kind` and `v{N}`), with dots replaced by directory separators.

```text
contracts/http/auth/login/v1/
  contract.yaml
  request.schema.json
  response.schema.json
  examples/

contracts/event/session/created/v1/
  contract.yaml
  payload.schema.json
  headers.schema.json
  examples/

contracts/command/device/enqueue/v1/
  contract.yaml
  request.schema.json
  ack.schema.json
  result.schema.json

contracts/projection/audit/timeline/v1/
  contract.yaml
  projection.schema.json
```

### schemaRefs Path Resolution

`schemaRefs` values are resolved **relative to the directory containing contract.yaml**. Bare filenames only. Absolute or root-relative paths are forbidden.

Example: in `contracts/http/auth/login/v1/contract.yaml`, `schemaRefs.request: request.schema.json` refers to `contracts/http/auth/login/v1/request.schema.json`.

### When to Model Lightweight Contracts

All cross-cell interactions require a contract. For simple, stable, assembly-internal interactions between low-consistency cells, use a lightweight contract to keep overhead low.

Indicators that a lightweight contract is appropriate:

- Both cells are in the same assembly.
- The interaction is a simple synchronous call with stable signatures.
- No external consumer will ever need this interface.

A lightweight contract is still a `contract.yaml` with full validation — it just has minimal schema (e.g., a single request/response JSON schema) and may start at `lifecycle: draft`.

Indicators that a full contract with detailed schemas and multiple consumers is required:

- The interaction crosses an assembly boundary.
- Either cell has `consistencyLevel` > L1.
- The interaction will have external consumers.
- The interaction is directly asserted by any journey's `passCriteria`.

## assembly.yaml

`assembly.yaml` owns physical packaging and build configuration.

A repo may contain **multiple assemblies**. Each assembly lives at `assemblies/{assembly-id}/assembly.yaml`. Rule C12 ensures no cell belongs to more than one assembly.

```yaml
# assemblies/core-bundle/assembly.yaml (hand-authored)
id: core-bundle
cells:
  - access-core
  - audit-core
  - config-core
build:
  entrypoint: cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
killSwitches:
  - kill.audit-consumer
```

```yaml
# assemblies/core-bundle/generated/boundary.yaml (tool-generated, do not edit)
generatedAt: "2026-04-04T10:30:00Z"
sourceFingerprint: "sha256:b5e6f7..."
exportedContracts:
  - http.auth.login.v1
  - http.auth.me.v1
  - http.config.get.v1
importedContracts: []
smokeTargets:
  - smoke.access-core.startup
  - smoke.audit-core.startup
  - smoke.config-core.startup
```

### Required Fields

- `id`
- `cells`
- `build` — `{ entrypoint, binary, deployTemplate }`

### Optional Generated Fields

These fields are **generated-only**. They are derived by `generate-assembly` from `assembly.cells` + contract endpoint declarations and written to `assemblies/{assembly-id}/generated/boundary.yaml` (not inline in `assembly.yaml`). Hand-curation is no longer permitted — generated content lives in dedicated generated files, never mixed with hand-authored metadata.

`validate-meta` reads `boundary.yaml` for C11 completeness checks. If `boundary.yaml` is absent or stale (sourceFingerprint mismatch), `validate-meta` emits warning (CI) or error (release branch).

- `exportedContracts` — contracts whose provider is inside the assembly and at least one client is outside
- `importedContracts` — contracts whose provider is outside the assembly and at least one client is inside
- `smokeTargets` — aggregation of `verify.smoke` from all member cells

### Recommended Optional

- `killSwitches`
- `flags`

Tool-specific generator configuration and generated artifact metadata:

```yaml
generated:
  outputDir: assemblies/core-bundle/generated
  generatedAt: "2026-04-04T10:30:00Z"
  sourceFingerprint: "sha256:b5e6f7..."
```

When `exportedContracts`, `importedContracts`, or `smokeTargets` are tool-generated (not hand-curated), their freshness metadata (`generatedAt` + `sourceFingerprint`) is stored in the `generated` block. `validate-meta` checks the `sourceFingerprint` against current source files (rule D15).

## Actor Registry: actors.yaml

Contract endpoint fields reference runtime actors. An actor is either:

1. A cell declared via `cells/*/cell.yaml`.
2. An external actor declared in `actors.yaml` at the repo root.

External actors are systems outside the cell model that participate in contracts (e.g., BFF gateways, third-party services).

```yaml
- id: edge-bff
  type: external
  maxConsistencyLevel: L1
  description: API gateway / BFF layer, not managed as a cell
```

### Required Fields Per Actor

- `id` — must not collide with any `cell.id`
- `type` — `external`
- `maxConsistencyLevel` — the highest consistency level this actor can **provide** (i.e., act as provider-side endpoint). This does not constrain client-side participation — an L1 actor may consume/read contracts at any level, since consistency level describes write guarantees, not consumption capability.

### Recommended Optional

- `description`

Validation rule C4 uses `actor.maxConsistencyLevel` when the **provider-side actor** is an external actor. `ownerCell` is always a cell, so its `consistencyLevel` is always available directly.

## journeys/catalog.yaml (Optional Generated)

`catalog.yaml` is not a canonical source. Journey discovery is by `journeys/J-*.yaml` glob.

If the team wants a browsable index, tooling (e.g. `gocell journey list`) can generate it by aggregating `id / goal / owner / state / risk` from journey specs and status-board. The generated file should not be hand-edited.

## verify-cell

`verify-cell` executes cell-level smoke verification.

- **Input**: `cell.yaml` `verify.smoke` identifiers.
- **Behavior**: Resolves each smoke identifier via the verify naming convention and executes the corresponding `go test` commands.
- **Output**: Cell-level smoke pass/fail result.
- **Relationship to other tools**: `verify-cell` runs smoke tests (cell boundary health); `verify-slice` runs unit + contract tests (slice implementation correctness). `generate-assembly` aggregates smoke targets from member cells for assembly-level verification.

## select-targets Impact Routing

**Guarantee level: advisory.** `select-targets` determines which slices, cells, and journeys are *likely* affected by a set of changed files. Its output is a best-effort recommendation, not a proven-complete target set. Until an explicit non-contract dependency graph exists (see V3 roadmap), `select-targets` results must not be used as the sole gate for "safe to skip testing." Teams should treat its output as optimization guidance, not as proof of non-impact.

### Routing Matrix

| Changed file pattern | Routing logic | Impact scope |
|---------------------|---------------|--------------|
| `cells/{cell}/slices/{slice}/**` | Match `slice.allowedFiles` (or convention default `cells/{cell-id}/slices/{slice-id}/**`) → slice → cell → journeys | slice + cell + journey |
| `cells/{cell}/cell.yaml` | Cell → all its slices → journeys | cell full |
| `cells/{cell}/**` (not matching slice or cell.yaml patterns above) | Cell-level shared code → cell → all its slices → journeys | cell full |
| `contracts/**/contract.yaml` | Find all slices with `contractUsages` referencing this contract → standard chain | slice + cell + journey |
| `contracts/**/*.schema.json` | Parent contract → same as `contract.yaml` change | slice + cell + journey |
| `journeys/J-*.yaml` | Journey itself + its `cells` list → all member cells | journey + cells |
| `journeys/status-board.yaml` | No routing (delivery state only) | none |
| `assemblies/*/assembly.yaml` (`cells` changed) | All member cells → their slices → journeys | assembly full |
| `assemblies/*/assembly.yaml` (`build` changed) | Assembly build + smokeTargets only | assembly smoke |
| `actors.yaml` | Find all contracts referencing changed actors → contract routing chain | per contract |
| `fixtures/{fixture-id}.yaml` | Find all journeys whose `fixtures` list contains this fixture id → journey + its `cells` | journey + cells |
| Any other path | Match against all slices' effective `allowedFiles` (declared or convention default). If matched, route as slice file change. If no match, no routing (file is outside metadata governance). | depends on match |

Coarse and precise modes use the **same routing rules** for non-slice files. They differ only in slice-to-journey resolution granularity.

### Coarse Mode (Always Available)

Route: `changedFile → slice (allowedFiles or convention default) → cell (belongsToCell) → journey (spec.cells)`.

This is cell-level granularity. If a cell has multiple slices, any file change in one slice triggers all journeys involving that cell. Acceptable when cells are small or have few journeys.

### Contract-Inferred Mode (Generated Index)

For cells with many slices and journeys, `select-targets` can consume a generated index that maps each slice to the journeys it likely affects, based on contract usage overlap.

The index is derived from: journey spec `cells` → each cell's slices → each slice's `contractUsages` → match contracts back to journeys. This derivation uses `journey.cells` as the anchor, not `journey.contracts` (curated subset), to reduce false negatives. Rule C13 validates `journey.cells` against the curated contracts list (best-effort completeness). Per guardrail G1, `journey.contracts` is a curated view — the gap between curated and exhaustive is a design choice, not a defect to be patched by warning rules.

**Limitation**: this mode infers slice-journey affinity via contract usage, which does not capture non-contractual coupling (shared in-process state, convention-based routing, runtime configuration). A slice that affects a journey through non-contract paths will be missed. The result is a best-effort refinement over coarse mode, not an exact target set.

Index file: `generated/indexes/journey-slice-map.yaml`, generated by `validate-meta` or `generate-assembly`. Structure:

```yaml
# generated — do not edit
generatedAt: "2026-04-04T10:30:00Z"
sourceFingerprint: "sha256:a1b2c3d4..."
entries:
  - id: session-login
    cell: access-core
    journeys:
      - J-sso-login
```

`sourceFingerprint` is a hash of all input files (journey specs, cell.yaml, slice.yaml, contract.yaml) that were used to compute this index. `validate-meta` recomputes the fingerprint from current source files and compares — a mismatch means the index is stale.

**Freshness**: `validate-meta` recomputes the index on every run and diffs against the existing file. If the recomputed result differs from the file, `validate-meta` emits a warning (CI) or error (release branch). In CI pipelines, `select-targets` must execute **after** `validate-meta` to ensure it consumes a fresh index.

When the index is present and fresh, `select-targets` uses it for contract-inferred slice-level granularity. When absent or stale, it falls back to coarse mode.

## V3 Roadmap (Out of Scope for Current Version)

The following changes are planned for the next major version. They are documented here as design direction, not as current commitments.

1. **Split journey roles**: Separate acceptance spec (`J-*.spec.yaml`), routing declaration (`J-*.routing.yaml`), and test plan (`J-*.plan.yaml`). This eliminates the multi-role coupling documented in G2.
2. **Explicit non-contract dependency graph**: Introduce a `dependencies.yaml` per assembly that declares non-contract coupling (shared state, configuration injection, convention-based routing). Once available, `select-targets` can be upgraded from advisory (G5) to sound.
3. **Rename L0 to module or library-partition**: Once `l0Dependencies` has been used in production for two release cycles, evaluate whether L0 should be formally separated from the cell concept.

## Validation Expectations

`validate-meta` enforces the following rules, organized into four groups. Groups execute in order: **A → B → C → D**. Group B depends on A (ID uniqueness/existence must pass before reference integrity checks). Group C depends on B (references must be valid before topology checks). Group D is independent of C (all D rules check field values and formats, not topology). Within each group, rules may execute in parallel; a failing rule does not block sibling rules in the same group.

**Prerequisite — consistencyLevel ordering**: All comparison operators (`<=`, `>=`, `<`, `>`) in validation rules use the total order `L0 < L1 < L2 < L3 < L4`. This ordering is axiomatic within this document. The operational semantics of each level are defined in `docs/architecture/consistency-levels.md`.

### Group A: Identity + Enum Validity

| # | Rule |
|---|------|
| A1 | `cell.id` is globally unique. Format: `kebab-case` (lowercase letters, digits, hyphens). |
| A2 | `slice.id` is unique within its parent cell. Format: `kebab-case`. Global qualified name: `{cell.id}/{slice.id}`. |
| A3 | `contract.id` is globally unique. Format: `{kind}.{domain-path}.v{N}`. First segment = kind, last segment = version, middle segments = domain path (one or more). |
| A4 | `journey.id` is globally unique. Format: `J-{kebab-case}`. |
| A5 | `assembly.id` is globally unique. Format: `kebab-case`. |
| A6 | `actor.id` in `actors.yaml` is globally unique. Format: `kebab-case`. Must not collide with any `cell.id`. |
| A7 | `contract.lifecycle` value must be `draft`, `active`, or `deprecated`. |
| A8 | `cell.type` value must be `core`, `edge`, or `support`. |
| A9 | `status-board.state` value must be `draft`, `todo`, `doing`, `blocked`, or `done`. |
| A10 | `status-board.risk` value must be `low`, `medium`, or `high`. |
| A11 | `status-board.blocker` is a required string. Value is `""` when no blocker. Null or field omission is invalid. |

### Group B: Reference Integrity

| # | Rule |
|---|------|
| B1 | `slice.belongsToCell` points to an existing cell. |
| B2 | Every `journeys/*.yaml` `cells` entry points to an existing cell. Every `contracts` entry points to an existing contract. |
| B3 | Every `contractUsages[].contract` in a slice points to an existing contract. |
| B4 | Every contract endpoint actor (provider-side and client-side entries) references a cell id or an actor id in `actors.yaml`. |
| B5 | `assembly.cells` entries point to existing cells. |
| B6 | If `assembly.exportedContracts` / `importedContracts` are present, each entry points to an existing contract. |
| B7 | The version segment parsed from `contract.id` (last dot-separated segment) must match the version directory segment in the file path. |
| B8 | `contract.ownerCell` must reference a `cell.id`, not an external actor. |
| B9 | Every fixture referenced in a `journeys/*.yaml` `fixtures` list must have a corresponding `fixtures/{fixture-id}.yaml` file. |
| B10 | `slice.belongsToCell` must equal the `{cell-id}` segment parsed from the slice's directory path `cells/{cell-id}/slices/{slice-id}/`. |
| B11 | Every `schemaRefs` value in a contract must resolve to an existing file in the contract's version directory. |
| B12 | `cell.id` must equal the directory name containing `cell.yaml` (e.g., `cells/access-core/cell.yaml` requires `id: access-core`). |
| B13 | `slice.id` must equal the directory name containing `slice.yaml` (e.g., `cells/access-core/slices/session-login/slice.yaml` requires `id: session-login`). |

### Group C: Topology

| # | Rule |
|---|------|
| C1 | `contractUsages[].role` must be a valid value for the referenced contract's `kind` per the role-to-kind table. |
| C2 | Provider role: `slice.belongsToCell` must equal the contract's provider-side endpoint actor. |
| C3 | Client role: `slice.belongsToCell` must appear in the contract's client-side endpoint list (or list is `["*"]`). |
| C4 | `contract.consistencyLevel` must not exceed the provider-side actor's consistency level (`cell.consistencyLevel` for cells, `actor.maxConsistencyLevel` for external actors) — this is a hard constraint (error). Additionally, if `contract.consistencyLevel` exceeds `ownerCell.consistencyLevel`, `validate-meta` emits a warning (the governance team may lack operational experience at that level), but this is not a blocking error. |
| C5 | `slice.consistencyLevel` (when present) must not exceed `cell.consistencyLevel`. |
| C6 | **Domain constraint** (not derived from topology): contract `kind` + `consistencyLevel` minimum — `http` requires `>= L1` (request handling needs local-tx), `event` requires `>= L2` (reliable delivery needs outbox), `command` requires `>= L2` (same), `projection` requires `>= L3` (materialization needs cross-cell eventual consistency). |
| C7 | L0 isolation: L0 cells must not appear in any contract endpoint field. L0 slices must have empty `contractUsages`. L0 cells may be directly imported by sibling cells in the same assembly (see L0 Cell Interaction Model). |
| C8 | No two slices may have overlapping effective `allowedFiles` patterns (declared or convention default). Overlap means any filesystem path matches both globs. Tools use cross-match testing. |
| C9 | All slices within a cell must collectively cover `cells/{cell-id}/slices/` implementation files via their effective `allowedFiles` (declared or convention default). |
| C10 | Every contract in a `journeys/*.yaml` `contracts` list must have its provider-side actor or at least one client-side actor present in that journey's `cells` list, or be a registered external actor in `actors.yaml`. |
| C11 | If `assembly.exportedContracts` / `importedContracts` are hand-curated: each listed contract must actually cross the assembly boundary. All contracts that cross the boundary must be listed (completeness). |
| C12 | A cell must belong to at most one assembly. No `cell.id` may appear in multiple `assembly.cells` lists. |
| C13 | `journey.cells` best-effort completeness: for every contract in `journey.contracts`, the provider-side actor (if it is a cell) must be present in `journey.cells`. For client-side actors: if the client list is `["*"]`, skip client-side check for that contract; otherwise, all client-side actors that are cells must be present in `journey.cells`. External actors are excluded. Note: this validates cells against the curated contracts list only — it cannot prove completeness against the full contract universe. |
| C14 | Active contract usage: every contract with `lifecycle: active` whose provider-side actor is a cell must have at least one slice declaring a provider-role `contractUsages` entry for it. Contracts whose provider is an external actor are exempt — external actors have no slices. |
| C16 | Role-verify direction consistency: for each `slice.contractUsages` entry `{contract, role}`, if `slice.verify.contract` contains an identifier matching that contract-id, the verify entry's role suffix must equal the declared role. Example: `contractUsages role: serve` requires verify entry to end in `.serve`, not `.call`. Scope: only checks entries present in both lists (not a coverage rule). |
| C17 | Deprecated contract reference restriction: `slice.contractUsages` must not reference a contract with `lifecycle: deprecated` unless the slice declares a `migrations` entry for that contract (see Migrations). Warning severity. |
| C18 | Cross-assembly contract dependency: for each contract referenced by cells in multiple assemblies, `validate-meta` emits the dependency edge (provider-assembly → client-assembly) into `generated/indexes/assembly-dependency-graph.yaml`. Informational in CI; error on release branch when `exportedContracts`/`importedContracts` are present but the dependency graph is missing. |
| C19 | Verify/waiver closed loop: for every provider-role `contractUsages` entry in a slice, the slice must have either a matching `verify.contract` identifier (by contract-id + role suffix) OR a `verify.waivers` entry with valid `owner`, `reason`, and non-expired `expiresAt`. Missing coverage without waiver is an error. Expired waivers are treated as missing. |
| C20 | L0 dependency declaration: every cell that imports an L0 cell must declare the dependency in `cell.yaml` `l0Dependencies`. Each referenced cell must exist, have `consistencyLevel: L0`, and be in the same assembly. |

### Group D: Execution

| # | Rule |
|---|------|
| D1 | Delivery-dynamic fields (`readiness`, `risk`, `blocker`, `done`, `verified`, `nextAction`, `updatedAt`) must not appear in `cell.yaml`, `slice.yaml`, `contract.yaml`, or `assembly.yaml`. `lifecycle` is a governance field and is exempt. |
| D2 | `slice.verify` must satisfy minimum verification requirements for its effective `consistencyLevel`: L0-L1 require non-empty `verify.unit`; L2+ require non-empty `verify.unit` and non-empty `verify.contract`. All cells require non-empty `verify.smoke`. D2 is an intentional minimum threshold — it does not require 1:1 coverage between `contractUsages` entries and `verify.contract` entries. Teams may impose stricter standards via extension rules. |
| D3 | All verify identifiers — in `passCriteria.checkRef`, `cell.verify.smoke`, `slice.verify.unit`, and `slice.verify.contract` — must follow the verify naming convention (prefix-dispatched format). For `contract` category, the penultimate segment must match `v\d+` and the final segment must be a known role. `validate-meta` checks format validity; `verify-slice` and `run-journey` check runtime resolvability. |
| D4 | Schema directory kind segment must equal `contract.kind` (singular form). |
| D5 | Generated indexes and artifacts must use canonical field names (e.g. `id`, not `sliceId`). |
| D6 | Contract `endpoints` field names must match the kind-specific endpoint table: `http` must use `server`/`clients`, `event` must use `publisher`/`subscribers`, `command` must use `handler`/`invokers`, `projection` must use `provider`/`readers`. Any other field name under `endpoints` is invalid. |
| D7 | `contract.kind` must equal the first dot-separated segment of `contract.id`. For example, `kind: http` requires `id` to start with `http.`. |
| D8 | `status-board.evidenceRefs` entries must be valid relative path format (no absolute paths, no `..` traversal). |
| D9 | `status-board.journeyId` must be unique within `status-board.yaml`. Per guardrail G4, this is a **CI-pipeline recommendation**, not a model-level invariant: `validate-meta` emits warning when a journey has no status-board entry, never error. Release-branch gating is a pipeline policy decision outside this specification. |
| D10 | Generated artifacts (`journey-slice-map.yaml`, assembly optional-generated fields) must include `generatedAt` (ISO timestamp) and `sourceFingerprint` (hash of all input files used to compute the artifact). `validate-meta` recomputes the fingerprint from current source files on every run; a mismatch means the artifact is stale — warning in CI, error on release branch. Only `validate-meta` computes `sourceFingerprint`; `generate-assembly` writes field values but defers fingerprint computation to `validate-meta`. |
| D-W1 | (warning) For each slice with non-empty `contractUsages`, if any entry has no corresponding `verify.contract` identifier (matched by contract-id), emit warning listing uncovered contracts. Teams may promote to error via `cell.yaml` extension: `verifyPolicy: { contractCoverage: strict }`. |
