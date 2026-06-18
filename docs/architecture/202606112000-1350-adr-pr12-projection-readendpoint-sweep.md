# ADR: PR-12 ResourceProjection read-endpoint sweep — atomic conversion + EPIC ≤2000 exception

- Status: Accepted
- Date: 2026-06-11
- Issue: #1350 (EPIC #1337 PR-12, US5 / FR-016 / FR-017)
- Scope: this ADR records the PR-12-specific delivery decisions only. The
  cross-cutting column-masking design (threat model, obligation source, layering)
  is owned by PR-13's consolidation ADR (tasks.md T13.1); this ADR cross-references
  it rather than restating it.

## Context

PR-11 (#1349) shipped the sealed `pkg/projection.ResourceProjection` carrier and its
upstream seal (`RESOURCE-PROJECTION-SEALED-01`), explicitly deferring the *downstream
handler callsite lock* to PR-12. PR-12 makes the column-masking funnel load-bearing
on real read endpoints. Two decisions in PR-12 are non-obvious and need a record.

### Decision 1 — convert ALL GET resource-reads atomically (not just auditquery)

FR-016 states the global invariant: *every read-endpoint response type contains only
`ResourceProjection`; a handler cannot return an un-masked full view (compile-time
block).* To make that invariant **machine-enforced** rather than a remembered
convention, PR-12 adds `RESOURCE-PROJECTION-COVERAGE-01`: every `kind:http` GET
contract whose response carries a top-level `data` resource MUST set
`endpoints.http.responseProjection: true`.

A repo scan found **10** such GET resource-reads (not the 4–5 originally scoped):
`http.audit.list.v1` (masking) plus `auth.role.list`, `auth.role.check`,
`auth.user.get`, `auth.setup.status`, `config.get`, `config.list`,
`config.internal.get`, `config.flags.get`, `config.flags.list` (identity projection).

The coverage guard admits a function-level carve-out registry for genuine non-resource
GETs. We chose to keep that registry **empty** (zero debt) by converting all 10 reads
in this PR, rather than convert a subset and carve-list the rest as "pending
conversion". Rationale (`.claude/rules/gocell/ai-robust.md`): a pending-conversion
carve-out list is a tracked-Soft smell, and splitting the conversion across PRs would
leave `RESOURCE-PROJECTION-COVERAGE-01` carrying debt entries at every intermediate
commit/PR — a worse steady state than the cohesive atomic conversion. The conversion
is mechanically uniform (one codegen marker + a generic `Response.Data` field-type
rewrite + `ToMap()` funnel call per endpoint), so the 10-endpoint sweep is one
coherent change, not ten independent ones.

### Decision 2 — `ToMap()` emits the full column set (no omitempty fission)

> **Amended 2026-06-18 (#1875)** — this decision was silently reverted by #2159 and
> is now restored + corrected; see [§Amendment 2026-06-18 — #1875](#amendment-2026-06-18--1875-tomap-full-column-set-restored--nullable-as-absent).

The generated `ToMap()` includes every column (the masking funnel replaces masked
values with `<REDACTED>`, keeping the key present). For an identity projection this
means previously-`omitempty` empty fields now serialize as their zero value rather
than being omitted. This is intentional and aligned with the masking model's
"no response-shape fission" principle (PR-11): a **stable, uniform column set is a
security-positive property for a data-permission API** — field *presence* never
reveals whether a masked column held data, closing a presence-based side channel.
~~The response schemas mark these columns optional, so the wider wire shape is
schema-valid~~; GoCell is pre-GA with no external wire consumers.

> **Correction (#1875):** the struck-out claim is FALSE for a column that carries a
> `format` constraint — its zero value `""` is NOT schema-valid (it violates e.g.
> `format: date-time`). "optional" only permits *absence*, not an invalid present
> value, and the full-column-set rule keeps the column *present*. Such columns must
> be **nullable** (`type: ["<scalar>", "null"]`) so their schema-valid "no value" is
> JSON `null`. See the amendment.

## Decision

Land PR-12 as a single cohesive change converting all 10 GET resource-reads, with the
two guards (`RESOURCE-PROJECTION-CALLSITE-LOCK-01` downstream Hard + go/types,
`RESOURCE-PROJECTION-COVERAGE-01` Medium metadata scan) and the `omitempty`-free
`ToMap()` semantics above.

## EPIC ≤2000-line exception

EPIC #1337 caps each PR at ≤2000 net changed lines. PR-12 lands at ~2.2k net churn —
a modest (~9%) overage. The overage is a direct consequence of Decision 1: closing the
coverage invariant with zero carve-out debt requires the atomic 10-endpoint
conversion. Splitting to stay under 2000 would either (a) leave the new coverage guard
with pending-debt carve-outs (rejected, see Decision 1), or (b) ship the guard in a
later PR while the conversion is partial — re-opening the silent-bypass gap FR-016
exists to close. We judged a one-time, well-tested, mechanically-uniform overage
preferable to either. The bulk of the diff is generated `types_gen.go` deltas (10
files, mechanical) + golden/fixtures + tests, not hand-written logic.

This exception is scoped to PR-12 and does not relax the EPIC cap for other PRs.

## Consequences

- The entire GET read surface is projection-typed; a new resource-read GET that omits
  the marker fails `RESOURCE-PROJECTION-COVERAGE-01` (CI red), and a contractgen
  regression that un-projects a marked `Response.Data` fails
  `RESOURCE-PROJECTION-CALLSITE-LOCK-01`.
- The 9 non-audit reads use the identity projection today; their per-principal mask
  obligations arrive when the ABAC policy engine is wired into the request path
  (PR-10 #1348) — the PEP funnel is unchanged by that swap (the mask *source* moves
  from the identity→RowScope derivation to `Decision.Obligations().FieldMask`).
- `auditFieldMask` (the PR-12-era identity→mask derivation) is replaced, not extended,
  by the policy Decision in PR-10; it is interim enforcement, not a fallback.
- The wider (omitempty-free) wire shape for projected responses is the accepted
  steady state; consumers must treat the full column set as always-present.

## Amendment 2026-06-18 — #1875 (`ToMap()` full column set restored + nullable-as-absent)

**Status: Accepted.** This amendment (a) records that Decision 2 was silently
reverted and is now restored, (b) corrects an over-broad claim in Decision 2, and
(c) extends it with nullable-as-absent semantics for `format`-constrained columns.
Per the AI-robust charter (ADR amendment must re-evaluate the threat model), the
threat matrix below supersedes the relevant Decision-2 prose.

### What happened

PR-12 (#1350) landed Decision 2: `ToMap()` emits the **full, stable column set**
so field *presence* never leaks whether a masked column held data. #2159
("fix(codegen): ToMap 尊重 omitempty") then re-introduced omitempty fission into
`ToMap()` to "align the projection path with `json.Marshal`". That alignment was
chasing a **wire path that does not exist for projection contracts**: a
`responseProjection` contract's `Response.Data` is the sealed
`projection.ResourceProjection`, whose **only** populator is `ToMap()` — the
`ResponseDataItem` struct is never marshaled directly. So #2159 re-opened the exact
presence side channel Decision 2 closed — and it did so on the **masked diagnostic
columns** (`correlationId` / `traceId` / `subjectId`), where the leak matters most —
without amending this ADR, because the Decision-2 invariant lived only as prose
(Soft) with no machine guard.

### Decision (restored + extended)

1. **`ToMap()` is the full, stable column set again.** It is a single
   `return map[string]any{ <one entry per field> }` literal — every column always
   present, no conditional omission. The omitempty-fission `ToMap()` of #2159 is
   **superseded**.
2. **Decision-2 correction.** "optional column ⇒ wire is schema-valid" is false for
   a `format`-constrained column: its zero value `""` violates the format. "optional"
   permits *absence*, not an invalid *present* value, and the stable-column rule keeps
   the column present.
3. **Nullable-as-absent.** A `format`-constrained optional column is declared
   `type: ["<scalar>", "null"]` and generated as a pointer (`*T`); its zero is a
   distinct nil that marshals to JSON `null`. Three states stay distinguishable and
   all schema-valid: **value** / **`null`** (no value, column present, maskable) /
   **`<REDACTED>`** (masked). Scope: `http.audit.list.v1` / `http.audit.get.v1`
   (`occurredAt`), `http.deviceidentity.status.v1` (`renewalTime`),
   `http.devicestate.v1` (`lastSeenAt`) — the complete set of optional + `format`
   projection columns at this date.
   - **Why not `timestamp`**: a `format`-constrained column that is **required**
     needs no nullable treatment — the ledger protocol stamps it on write, so there
     is no zero-value path. `occurredAt` is the only audit time column that is
     optional (empty for legacy rows), hence the only one made nullable.
   - **audit vs device**: `audit.*` is **active** — this is a fix for a live
     `format: date-time` violation (#1875). `deviceidentity.status` /
     `devicestate` are `lifecycle: draft`, `ownerCell: _framework`, not yet served;
     aligning them now is **preventive** (the same nullable-as-absent pattern is
     baked in before activation), not a fix for a live wire.

### Threat matrix (re-evaluated; supersedes Decision-2 prose where it conflicts)

| Threat | Pre-#2159 (Decision 2) | #2159 regression | Post-#1875 (this amendment) |
|---|---|---|---|
| Presence side channel on a masked column (absent vs `<REDACTED>` reveals whether it held data) | Closed (key always present) | **OPEN** on `correlationId`/`traceId`/`subjectId` | Closed (key always present) |
| `format`-column zero on the wire | `occurredAt:""` → **format violation** | Hidden by omission, but side channel re-opened | `occurredAt:null` → schema-valid, key present |
| Silent recurrence of omitempty fission | Prose-only (Soft) — **#2159 slipped through** | n/a | `PROJECTION-TOMAP-FULL-COLUMN-SET-01` (Medium AST guard) + golden byte-lock (Hard on drift) + wire schema-validation test |

### Enforcement (AI-robust)

- **`PROJECTION-TOMAP-FULL-COLUMN-SET-01`** (Medium, `tools/archtest`): AST-asserts
  every generated `ToMap()` body is a single full-column-set map literal with no
  `if`. Strictly stronger than the golden byte-lock — it stays red even if a
  regressor `-update`s the goldens (which is exactly how #2159 passed). RED/GREEN
  fixture + anti-vacuity floor. A load-bearing funnel column-set check (Hard) is a
  documented ceiling, deliberately not built (it couples the PEP to per-contract
  column constants for a property this static scan already covers).
- **Wire gate**: `TestHttpAuditListV1Serve_ZeroOccurredAt_NullValidates` validates a
  zero-`occurredAt` response against the nullable schema (`null` accepted, key
  present) — the schema and the full-column-set `ToMap()` agree on `null`.

### Why not "naive omit" (the rejected alternative #2159 took)

Omitting an empty column makes "key absent" distinguishable from "key present but
`<REDACTED>`", letting a reader infer the column was empty for masked rows — the
presence side channel. Nullable-as-absent keeps the column present, so absence is
never observable, while `null` keeps it schema-valid.

## Amendment 2026-06-19 — #2359 (schema as the single source of the projection column contract)

**Status: Accepted.** #1875 restored the full-column-set `ToMap()` and fixed the
per-column `format` zero (occurredAt/renewalTime/lastSeenAt → nullable), but the
"full column set" wire contract still lived **only in generated Go** — the JSON
Schema `required` of 5 of the 16 `responseProjection` contracts still under-claimed
it. A schema-validating client therefore could **not** rely on the column-presence
property, which is the data-permission API's security property (presence never
reveals whether a masked column held data). This amendment closes the schema side and
re-evaluates the threat model per the AI-robust charter.

### Decision

1. **`required` = the full item column set (schema truth-source closure).** For a
   `responseProjection` contract the item schema's `required` MUST list **every**
   item property — there are **no optional (not-in-`required`) projection columns**.
   The full-column-set `ToMap()` emits every key unconditionally, so an "optional"
   projection column is a category error: its key is never absent on the wire, yet
   the schema tells clients it MAY be, and its Go zero/nil (`""` / `null`) reaches the
   wire where it can silently violate the schema. Value-optionality is expressed
   **only** by `nullable` (`["scalar","null"]`, #1875), never by absence from
   `required`. Closed the 5/6 gap: `deviceidentity.status`(renewalTime),
   `devicestate`(lastSeenAt, tenantId), `devicecompliance`(tenantId),
   `policy.{get,list}`(description, via the shared `policy.schema.json`).
2. **Decision-2 framing fully retired.** The struck-out "optional ⇒ schema-valid"
   claim is not patched per-column (#1875) but **eliminated**: projections have no
   optional columns. This also subsumes #1875's `format`-nullable fix — a format
   column is now either required-non-nullable (always stamped, e.g. notBefore /
   notAfter / observedAt — #1875 §"Why not `timestamp`" confirms these need no
   nullable) or required-nullable (empty-able, e.g. occurredAt).
3. **`required` widening on the shared `policy.schema.json`** makes `description`
   present in **every** Policy response (get/list **and** create/update responses) —
   a coherent "the Policy object always carries description" closure. The create/update
   **request** schemas are separate and unchanged (description stays optional on input).
   Pre-GA wire window (api-versioning.md): in-place tightening, no version dir.
   - **Consumer note**: an empty description now serializes as `"description": ""`
     (present) rather than being omitted; likewise an empty audit `payload` serializes
     as `null` (the `{}` true-schema accepts it). Consumers must treat empty-value as
     equivalent to the former absent-key (in-repo `edge-bff` updates atomically; pre-GA,
     no external consumers). This uniform "present, possibly-empty" shape is the same
     security-positive stable column set Decision 2 / #1875 establish.

### Enforcement (AI-robust) — two legs, one invariant

- **Hard (primary, codegen build-fail):** `contractgen.applyResponseProjection`
  (`requireProjectionItemFullColumnSet`) rejects a `responseProjection` item DTO with
  any non-required column at GENERATE time — the drift cannot be materialized into
  generated code.
- **Medium (CI schema-file scan):** `PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01`
  (`tools/archtest`) verifies the SAME invariant directly on contract schema files
  (resolved via `contractgen.Parse`), so it trips on a schema edit even before
  re-generation, with RED/GREEN fixtures + anti-vacuity floor. Mirrors
  `PROJECTION-TOMAP-FULL-COLUMN-SET-01`'s "golden Hard + archtest Medium" structure.

### Threat matrix (re-evaluated; extends the #1875 matrix)

| Threat | Post-#1875 | Post-#2359 (this amendment) |
|---|---|---|
| Schema-validating client cannot rely on column presence (security property invisible at schema layer) | **OPEN** (5 contracts under-claim `required`) | Closed (`required` = full column set, codegen Hard + archtest Medium) |
| New projection contract adds an optional column whose zero is schema-invalid (`""`/`null`) | Prose-only follow-up (no guard) | Closed (optional projection column is build-fail + CI-red) |
| Required non-nullable `array`/`object` whose producer emits nil → JSON `null` violates `type` | n/a | **Residual** — out of schema reach (`["array"/"object","null"]` rejected #2340 F1; no nil→`[]`/`{}` normalization, zero such column today). Tracked: codegen nil-normalization, gated on first such column. |
| Required non-nullable `format` column whose producer emits `""` | n/a | **Residual** — authoring discipline: declare empty-able `format` columns nullable (as occurredAt). |
