# Metadata Templates V2 Rewrite Plan

## Scope

- Rewrite only [metadata-templates-v2.md](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md).
- Do not preserve backward compatibility with the current V2 draft.
- Do not update other documents in this round.

## Rewrite Goals

1. Define a single canonical owner for every fact.
2. Separate canonical metadata, generated views, and delivery-only status.
3. Replace the unstable contract interaction model with kind-specific endpoints.
4. Make validation and tool-facing inputs self-contained in this document.
5. Align narrative text, YAML examples, required-field lists, and validation rules.

## Planned Decisions

### D1. Four Layers + Contracts + Actor Registry

Replace `Five-Layer Model` with `Four Layers + Contracts + Actor Registry`.

| Layer | File | Responsibility |
|-------|------|----------------|
| 1 | `journeys/*.yaml` | Single-journey acceptance spec |
| 2 | `cells/*/cell.yaml` | Runtime boundary and data sovereignty |
| 3 | `cells/*/slices/*/slice.yaml` | Work mapping and impact routing |
| 4 | `journeys/status-board.yaml` | Dynamic delivery state |

Cross-layer assets (not numbered layers):

- **Contract** (`contracts/**/contract.yaml`): cross-cell boundary definition.
- **Assembly** (`assembly.yaml`): physical packaging of cells.
- **Actor Registry** (`actors.yaml`): non-cell runtime actors.

### D2. Canonical Ownership Matrix

Add a `Canonical Ownership Matrix` near the top. Every fact has exactly one canonical owner; derived views are explicitly marked as generated.

| Fact | Canonical Owner | Notes |
|------|-----------------|-------|
| Which journeys exist | `journeys/J-*.yaml` glob | catalog is optional generated |
| What a journey means | `journeys/*.yaml` | goal, owner, cells, contracts, passCriteria |
| Journey focus contracts | `journeys/*.yaml` contracts | Curated subset, not exhaustive derivation |
| Which cell a slice belongs to | `slice.belongsToCell` | cell does not maintain reverse index |
| Cell consistency level | `cell.yaml` | slice inherits by default |
| Slice consistency level | `cell.yaml`, `slice.yaml` optional override | Must not exceed cell |
| Cell / slice owner | `cell.yaml`, `slice.yaml` optional override | slice inherits by default |
| Contract version | `contract.yaml` `version` field | Must match id suffix and directory path |
| Contract boundary (endpoints) | `contract.yaml` kind-specific fields | slice references via contractUsages |
| Which contracts a slice uses | `slice.yaml` contractUsages | Implementation-level mapping |
| Which cells are packaged together | `assembly.yaml` | Packaging only |
| Assembly boundary contracts | **generated** from cells + contracts | Optional hand-curated override |
| Assembly smoke targets | **generated** from cell.verify.smoke | Optional hand-curated override |
| Dynamic delivery state | `journeys/status-board.yaml` only | Forbidden in all other metadata files |

### D3. Remove Same-Assembly No-Contract Exception

Delete the current five-condition exception (current §65-74). All cross-cell interactions require a contract.

Add a guidance section: **"When to model lightweight contracts"**

> For simple, stable, assembly-internal Go interface calls between L0/L1 cells, use an `http` contract at `L1` with minimal schema. This keeps the model uniform while keeping overhead low.
>
> Indicators that a lightweight contract is appropriate:
>
> - Both cells are in the same assembly.
> - The interaction is a simple synchronous call with stable signatures.
> - No external consumer will ever need this interface.
>
> Indicators that a full contract is required:
>
> - The interaction appears in any journey's contracts list.
> - The interaction crosses an assembly boundary.
> - Either cell has consistencyLevel > L1.
> - The interaction has a public entrypoint.

### D4. Cell Owner Is Default; Slice Owner Is Optional Override

- `cell.owner` is required: `{ team, role }`.
- `slice.owner` is optional. When absent, inherits `cell.owner`.
- Validation: when `slice.owner` is present, it is an explicit override (no further constraint).

### D5. Cell ConsistencyLevel Is Default; Slice Is Optional Override

- `cell.consistencyLevel` is required.
- `slice.consistencyLevel` is optional. When absent, inherits `cell.consistencyLevel`.
- Validation: when present, must not exceed `cell.consistencyLevel`.

### D6. Kind-Specific Contract Endpoints

Replace the unstable `producer / consumers` with kind-specific endpoint fields. Each kind has exactly two named roles with unambiguous direction.

| Kind | Provider-side field | Client-side field | Provider meaning | Client meaning |
|------|--------------------|--------------------|-----------------|----------------|
| `http` | `http.server` | `http.clients` | Serves the endpoint | Calls the endpoint |
| `event` | `event.publisher` | `event.subscribers` | Emits the event | Receives the event |
| `command` | `command.handler` | `command.invokers` | Processes the command | Sends the command |
| `projection` | `projection.provider` | `projection.readers` | Materializes the view | Queries the view |

Common rules:

- `ownerCell` remains as governance ownership (who owns the contract lifecycle).
- Provider-side field is always a single actor.
- Client-side field is a list of actors, or `["*"]` for open/public contracts.
- `["*"]` means any registered actor may consume; validate-meta skips client membership checks but still requires actor registration for slices referencing the contract.
- `["*"]` must not be mixed with named actors in the same list.

### D7. Unified contractUsages[] on Slices

Replace `callsContracts / publishes / consumes` with a single `contractUsages` array:

```yaml
contractUsages:
  - contract: http.auth.login.v1
    role: serve
  - contract: http.config.get.v1
    role: call
  - contract: event.session.created.v1
    role: publish
```

Role-to-kind valid combinations:

| Kind | Provider role | Client role |
|------|--------------|-------------|
| `http` | `serve` | `call` |
| `event` | `publish` | `subscribe` |
| `command` | `handle` | `invoke` |
| `projection` | `provide` | `read` |

Validation:

- Each `contractUsages` entry must reference an existing contract.
- The `role` must be a valid value for that contract's `kind` per the table above.
- Provider role: `slice.belongsToCell` must equal the contract's provider-side endpoint actor.
- Client role: `slice.belongsToCell` must appear in the contract's client-side endpoint list (or list is `["*"]`).

### D8. Contract Status as Lifecycle Governance

Rename `status` to `lifecycle` with values: `draft | active | deprecated`.

- `lifecycle` is a governance fact (changes at version-migration cadence), not delivery status.
- Add explicit distinction in the document: delivery-dynamic fields (readiness/risk/blocker) are forbidden in metadata; lifecycle-governance fields (lifecycle) are permitted.
- Revise validation rule 19 to exclude `lifecycle` from the dynamic-status ban.
- `draft` allows empty client-side endpoint list. `active` requires at least one client (or `["*"]`).

### D9. Keep version Field; Enforce Triple Consistency

Keep `contract.version` as a required field for explicit readability and future update convenience.

Validation rule: `contract.version` must equal:
1. The last segment of `contract.id` (e.g., `http.auth.login.v1` → `v1`).
2. The version directory segment in the file path (e.g., `contracts/http/auth/login/v1/`).

All three representations must agree. The `version` field is the canonical authored value; `id` suffix and directory path are enforced to match.

### D10. Downgrade Derived Assembly Fields

- `assembly.exportedContracts`: downgrade to **optional generated**.
- `assembly.importedContracts`: downgrade to **optional generated**.
- `assembly.smokeTargets`: downgrade to **optional generated**.

When present as hand-curated values, validation checks:
- Listed contracts actually cross the assembly boundary.
- All boundary-crossing contracts are listed (completeness check).

When absent, tools derive them from `assembly.cells` + contract endpoint declarations.

Assembly required fields after revision: `id`, `cells`.

### D11. Executable References for Tools

#### passCriteria (run-journey)

Add `checkRef` required for `mode: auto`:

```yaml
passCriteria:
  - text: OIDC redirect completes
    mode: auto
    checkRef: journey.J-sso-login.oidc-redirect
    assert: { type: httpStatus, expect: 302 }
  - text: security review sign-off
    mode: manual
```

| Field | Condition | Description |
|-------|-----------|-------------|
| `text` | required | Human-readable acceptance criterion |
| `mode` | required | `auto` or `manual` |
| `checkRef` | required when `mode: auto` | Logical identifier for executable check |
| `assert` | optional | Structured assertion hint |

#### verify identifiers (verify-slice, verify-cell)

Define three-segment naming convention `{category}.{subject}.{scope}` with deterministic resolution:

| Category | Pattern | Resolution |
|----------|---------|------------|
| `smoke` | `smoke.{cell}.{name}` | `go test ./cells/{cell}/... -run Test_Smoke_{name}` |
| `unit` | `unit.{slice}.{scope}` | `go test ./cells/{cell}/slices/{slice}/... -run Test_{scope}` |
| `contract` | `contract.{contract-id}.{role}` | `go test ./contracts/{kind}/{path}/... -run Test_{role}` |
| `journey` | `journey.{journey-id}.{step}` | `go test ./tests/journey/... -run Test_{journey-id}_{step}` |

#### generate-assembly

Add `build` as required in assembly.yaml:

```yaml
build:
  entrypoint: cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s | compose | none
```

### D12. Actor Registry Enrichment

Extend `actors.yaml` entries with required `maxConsistencyLevel`:

```yaml
- id: edge-bff
  type: external
  description: API gateway / BFF layer
  maxConsistencyLevel: L1
```

Required fields per actor: `id`, `type`, `maxConsistencyLevel`.

Validation rules referencing `ownerCell.consistencyLevel` use `actor.maxConsistencyLevel` when the actor is external. Rules 6/7/8 match `slice.belongsToCell` against either cell or actor id.

### D13. allowedFiles Remains Required with Completeness Check

Keep `allowedFiles` as required on every slice.

Validation:
- No two slices may have overlapping `allowedFiles` globs (overlap = any filesystem path that matches both; tools use cross-match testing).
- All slices within a cell must collectively cover `cells/{cellDir}/slices/` implementation files (completeness check).

### D14. Event Contract Required Fields

Classify the three unclassified fields:

| Field | Classification | Rationale |
|-------|---------------|-----------|
| `idempotencyKey` | **required** for event | Core idempotency contract |
| `deliverySemantics` | **required** for event | at-least-once / exactly-once / at-most-once |
| `orderingSemantics` | **recommended** for event | Not all events require ordering guarantees |

Additional required for `projection`: `replayable`.

### D15. Schema Directory Naming

Unify directory segments to **singular form** matching `contract.kind`:

```
contracts/{kind}/{domain}/{action}/{version}/
```

Examples:
- `contracts/event/session/created/v1/`
- `contracts/http/auth/login/v1/`
- `contracts/command/device/enqueue/v1/`
- `contracts/projection/audit/timeline/v1/`

Add validation rule: directory kind segment must equal `contract.kind`.

### D16. schemaRefs Path Resolution

`schemaRefs` values are resolved **relative to the directory containing contract.yaml**. Bare filenames only; absolute or root-relative paths are forbidden.

### D17. Design Goal 5 Correction

List six tools explicitly:

> The model must be sufficient for these six tools: `validate-meta`, `generate-assembly`, `select-targets`, `verify-slice`, `verify-cell`, `run-journey`.

## Planned Changes By Section

### 1. Design Goals

- Rewrite goals to describe three categories: canonical facts, generated views, delivery-only facts.
- Fix tool count to six.
- Remove overclaiming language; state that the model provides tool input contracts, not execution logic.

### 2. Canonical Truth Rules + Ownership Matrix

- Add the Canonical Ownership Matrix (D2).
- Forbid legacy names in all metadata including generated artifacts.
- Generated index uses `id` not `sliceId`.

### 3. Layer Model

- Rename to "Four-Layer Model" (D1).
- Explicitly document contracts, assembly, and actor registry as cross-layer assets.
- Unify section headings: `## Layer 1: journeys/*.yaml` through `## Layer 4: journeys/status-board.yaml`.

### 4. Journey Model

- Rewrite as stable acceptance spec.
- `journey.contracts` is a curated focus subset (add clarifying note), not exhaustive derivation.
- Replace `passCriteria.verify` with structured `mode` + `checkRef` + optional `assert` (D11).
- Add `journey.participants` field to include external actors alongside cells when needed for rule 17 validation. Or rename `cells` to `participants`.

### 5. Cell and Slice Models

- `cell.owner` required; `slice.owner` optional override (D4).
- `cell.consistencyLevel` required; `slice.consistencyLevel` optional override (D5).
- `allowedFiles` required with completeness check (D13).
- Replace `callsContracts / publishes / consumes` with `contractUsages[]` (D7).
- Add `servesContracts` coverage through `contractUsages[].role: serve | handle | provide`.
- Remove `publicEntrypoints` from optional (no longer needed without the exception rule).

### 6. Contract Model

- Rewrite around kind-specific endpoints (D6).
- `ownerCell` remains as governance ownership.
- Keep `version` field; enforce triple consistency with id and directory (D9).
- Rename `status` to `lifecycle` (D8).
- Define `schemaRefs` as relative paths (D16).
- Classify event-specific fields (D14).
- Support `["*"]` for open contracts (D6).

### 7. Assembly Model

- Reduce to packaging facts: `id`, `cells`, `build` required.
- Downgrade `smokeTargets`, `exportedContracts`, `importedContracts` (D10).
- Add `build` section for generate-assembly (D11).

### 8. Actor Registry

- Add `maxConsistencyLevel` required (D12).
- Make actor references first-class in validation rules.

### 9. Schema Placement

- Unify directory naming to singular (D15).
- Define schemaRefs resolution rule (D16).

### 10. Select-Targets

Explicit routing matrix for all file types:

| Changed file pattern | Routing logic | Impact scope |
|---------------------|---------------|--------------|
| `cells/{cell}/slices/{slice}/**` | Match `slice.allowedFiles` → slice → cell → journeys | slice + cell + journey |
| `cells/{cell}/cell.yaml` | Cell → all its slices → journeys | cell full |
| `contracts/**/contract.yaml` | Find all slices with `contractUsages` referencing this contract → standard chain | slice + cell + journey |
| `contracts/**/*.schema.json` | Parent contract → same as contract.yaml change | slice + cell + journey |
| `journeys/J-*.yaml` | Journey itself + its `cells` list → all member cells | journey + cells |
| `journeys/status-board.yaml` | No routing (delivery state only) | none |
| `assembly.yaml` | All member cells → their slices → journeys | assembly full |
| `actors.yaml` | Find all contracts referencing changed actors → contract routing chain | per contract |

Coarse and precise modes use the same routing rules for non-slice files.

### 11. Validation Expectations

Restructure into four groups with explicit rules.

#### Group A: Identity

| # | Rule |
|---|------|
| A1 | `cell.id` globally unique, `kebab-case` |
| A2 | `slice.id` unique within parent cell, `kebab-case`; global qualified name = `{cell.id}/{slice.id}` |
| A3 | `contract.id` globally unique, format `{kind}.{domain}.{action}.v{N}` |
| A4 | `journey.id` globally unique, format `J-{kebab-case}` |
| A5 | `assembly.id` globally unique, `kebab-case` |
| A6 | `actor.id` (actors.yaml) globally unique, `kebab-case`; must not collide with any `cell.id` |

#### Group B: Reference Integrity

| # | Rule |
|---|------|
| B1 | `slice.belongsToCell` points to an existing cell |
| B2 | `journeys/*.yaml` `cells` entries point to existing cells; `contracts` entries point to existing contracts |
| B3 | Every `contractUsages[].contract` in a slice points to an existing contract |
| B4 | Contract endpoint actors (provider + clients) reference a cell or an actor in `actors.yaml` |
| B5 | `assembly.cells` entries point to existing cells |
| B6 | If `assembly.exportedContracts` / `importedContracts` are present, each entry points to an existing contract |
| B7 | `contract.version` matches id suffix and directory path segment |

#### Group C: Topology

| # | Rule |
|---|------|
| C1 | `contractUsages[].role` must be valid for the referenced contract's `kind` per D7 table |
| C2 | Provider role: `slice.belongsToCell` must equal the contract's provider-side endpoint actor |
| C3 | Client role: `slice.belongsToCell` must appear in client-side endpoint list (or list is `["*"]`) |
| C4 | `contract.consistencyLevel` must not exceed `ownerCell.consistencyLevel` (use `actor.maxConsistencyLevel` for external actors) |
| C5 | `slice.consistencyLevel` (when present) must not exceed `cell.consistencyLevel` |
| C6 | Consumer consistency: if a slice references a contract via client role, `slice.belongsToCell`'s cell `consistencyLevel` must be `>=` the contract's `consistencyLevel` |
| C7 | Contract kind + consistencyLevel valid combinations: `http >= L1`, `event >= L2`, `command >= L2`, `projection >= L3` |
| C8 | L0 isolation: L0 cells must not appear in any contract endpoint field; L0 slices must have empty `contractUsages` |
| C9 | No two slices may have overlapping `allowedFiles` globs |
| C10 | All slices within a cell must collectively cover `cells/{cellDir}/slices/` implementation files |
| C11 | Every contract in `journeys/*.yaml` contracts must have its provider or at least one client present in that journey's cells/participants |
| C12 | If `assembly.exportedContracts` / `importedContracts` are hand-curated: listed contracts must cross assembly boundary; all boundary-crossing contracts must be listed |

#### Group D: Execution

| # | Rule |
|---|------|
| D1e | `contract.lifecycle` value must be `draft`, `active`, or `deprecated` |
| D2e | `cell.type` value must be `core`, `edge`, or `support` |
| D3e | `status-board.state` value must be `todo`, `doing`, `blocked`, or `done` |
| D4e | `status-board.risk` value must be `low`, `medium`, or `high` |
| D5e | `status-board.blocker` is required string; empty string `""` when no blocker; null or omission is invalid |
| D6e | Delivery-dynamic fields (`readiness`, `risk`, `blocker`, `done`, `verified`, `nextAction`, `lastUpdated`) must not appear in `cell.yaml`, `slice.yaml`, `contract.yaml`, or `assembly.yaml`. `lifecycle` is a governance field and is exempt. |
| D7e | `slice.verify` must satisfy minimum verification level for its effective `consistencyLevel`. Inline the minimum constraints from consistency.md rather than delegating. |
| D8e | Every `passCriteria` entry with `mode: auto` must have a `checkRef` that resolves to an executable target per D11 naming convention |
| D9e | Schema directory kind segment must equal `contract.kind` (singular) |
| D10e | Generated indexes must use canonical field names (`id`, not `sliceId`) |

## Execution Order

1. Rewrite principles, canonical ownership matrix, and layer model (D1, D2, D3, D17).
2. Rewrite `journey`, `cell`, `slice` sections (D4, D5, D7, D11, D13).
3. Rewrite `contract` section (D6, D8, D9, D14, D15, D16).
4. Rewrite `assembly` and `actor registry` sections (D10, D12).
5. Rewrite `select-targets` routing matrix.
6. Rewrite validation expectations into four groups (A/B/C/D).
7. Normalize all examples, field tables, required/optional lists, and terminology across sections.

## Out of Scope

- Updating any file other than [metadata-templates-v2.md](/Users/shengming/Documents/code/gocell/docs/architecture/metadata-templates-v2.md)
- Keeping compatibility with the current V2 draft
- Updating referenced documents such as `consistency.md`
- Updating `CLAUDE.md` cell development rules (separate follow-up)
