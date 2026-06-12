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

The generated `ToMap()` includes every column (the masking funnel replaces masked
values with `<REDACTED>`, keeping the key present). For an identity projection this
means previously-`omitempty` empty fields now serialize as their zero value rather
than being omitted. This is intentional and aligned with the masking model's
"no response-shape fission" principle (PR-11): a **stable, uniform column set is a
security-positive property for a data-permission API** — field *presence* never
reveals whether a masked column held data, closing a presence-based side channel.
The response schemas mark these columns optional, so the wider wire shape is
schema-valid; GoCell is pre-GA with no external wire consumers.

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
