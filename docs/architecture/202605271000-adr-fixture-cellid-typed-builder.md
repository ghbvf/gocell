# ADR — Fixture Cell-ID Typed Builder Funnel

Date: 2026-05-27
Status: Accepted
Tracks: gh issue #681 (PR-FIXTURE-CELLID-TYPED-BUILDER-01)
Funnel ID: `FIXTURE-CELLID-TYPED-BUILDER-01`

## Problem

PR #484 unified the cell-id regex into a single source (`pkg/scaffoldid/scaffoldid.go`) and used `sed` to migrate legacy kebab-style cell-ids in `kernel/governance/` fixtures (`shared-validate` → `sharedvalidate`, etc.). However, the fixtures still embed cell-ids as **bare string literals** at every position — `map[string]*metadata.CellMeta` keys, `CellMeta.ID` fields, `L0DepMeta.Cell` references, `JourneyMeta.Cells` slice elements, and similar field positions across the `kernel/metadata.*` struct family.

This is precisely the "hand-crafted fixture by string literal" Soft formation listed in `.claude/rules/gocell/ai-robust.md` §适用范围. An AI co-author writing a new test fixture can re-introduce an invalid cell-id (kebab, uppercase, single character, leading digit, underscore) and existing governance rules will not catch it uniformly:

- **REF** rules guard cross-entity reference completeness (slice→cell), not cell-id literal format.
- **FMT-C1** guards `CellMeta.ID` format, but **only triggers in fixtures that actually run FMT-C1**. Fixtures consumed by other tests (e.g. `l0Project()` for L0 dependency tracking) can contain malformed cell-ids without any rule firing. Empirical evidence: `kernel/governance/location_integration_test.go:224` carried `"a"` (single character, not matching `^[a-z][a-z0-9]+$`) as a cell-id for years until this PR.

A **constructor-time fail-fast** typed builder closes this gap: every fixture that mentions a cell-id calls through `metadatatest.NewCellID(s)`, which `panic`s when `s` violates `metadata.MatchCellID`. The panic surfaces at fixture assembly time regardless of which validator the test eventually runs.

## Decision

Introduce a typed-builder funnel mirrored across three loci:

1. **Builder package**: `kernel/metadata/metadatatest.NewCellID(s string) string` panics via `panicregister.Approved("metadatatest-cell-id-invalid", errcode.Assertion(...))` when `s` violates `metadata.MatchCellID`. A closed enumeration of pre-validated package-level vars (`CellIDAccessCore`, `CellIDAuditCore`, …) covers the cell-ids used across multiple fixtures; one-off ids in individual tests use `NewCellID(literal)` directly.
2. **Fixture migration**: all bare cell-id literals in cell-id field positions across `kernel/` `*_test.go` files migrate to `metadatatest.NewCellID(literal)` or `metadatatest.<CellIDVar>`.
3. **Static enforcement (archtest `FIXTURE-CELLID-TYPED-BUILDER-01`)**:
   - **A1** (Hard downstream): typed-info funnel rejecting bare literals, Ident→BasicLit chains, dynamic NewCellID arguments, and non-CellID-prefixed metadatatest var refs at any of the 15 cell-id field positions enumerated in §1 below. Accepts three sanctioned forms: (a) `NewCellID(literal)`, (b) `metadatatest.<CellIDVar>`, (c) `metadata.FrameworkOwnerSentinel` const (see §1a below).
   - **A2** (Hard upstream): form-uniqueness lock on the `NewCellID` body — including TypesInfo-resolved callee identity for `metadata.MatchCellID`, `panicregister.Approved`, and `errcode.Assertion`. Any structural drift or package substitution breaks the test.
   - **A3** (meta self-test): `archtest_fixture` sub-package containing deliberate bad/good usages; asserts A1 fires on bad and stays silent on good. Includes `good_framework_sentinel.go` asserting sentinel does not produce false positives.
   - **A4** (consistency lock): asserts the carveout map in archtest matches §2 below character-by-character.
   - **A5** (Hard upstream — var initializer): every CellID-prefixed package-level var in `kernel/metadata/metadatatest` must have initializer = `NewCellID(BasicLit STRING)` with `NewCellID` TypesInfo-resolved to the metadatatest package. Closes the upstream half of the CellID* var funnel: without A5, `var CellIDBypass = "raw-evil"` would slip through A1's prefix check.
4. **Import scope guard** (`METADATATEST-IMPORT-SCOPE-01`, Medium): production code may not import `metadatatest`.

### §1a — FrameworkOwnerSentinel as third sanctioned source (#1939)

`metadata.FrameworkOwnerSentinel` (const `"_framework"`) was introduced in #1939 as the reserved `ownerCell` value for framework-owned contracts. It is a first-class legal value at owner/provider cell-id positions (`ContractMeta.OwnerCell`, `EndpointsMeta.Server`, `EndpointsMeta.Publisher`, `EndpointsMeta.Handler`, `EndpointsMeta.Provider`) but cannot go through the normal typed-builder path:

- `metadatatest.NewCellID("_framework")` **panics** at fixture load time — leading underscore is not a legal cell id per `metadata.MatchCellID`.
- A `CellIDFramework` package-level var cannot be added to metadatatest — A5 would require it to be initialized via `NewCellID(literal)`, which panics (same reason).

Therefore A1 accepts `metadata.FrameworkOwnerSentinel` as a third sanctioned source, identity-locked via TypesInfo: the resolved object must be `*types.Const` with `Pkg().Path() == metadataPkgPath` and `Name() == "FrameworkOwnerSentinel"`. A homonymous const from another package resolves to a different package path and is rejected. Accepting the sentinel at all cell-id positions (not just OwnerCell/Server/Publisher) is harmless: `"_framework"` is not a legal cell id, so if it inadvertently appears at e.g. `CellMeta.ID`, the existing `FMT-C1` rule will catch it independently.

Cross-reference: ADR `docs/architecture/202606130635-1939-adr-framework-owned-contract.md`.

## §1 — Cell-id field positions (15-field enumeration, schema-derived)

Source: `kernel/metadata/types.go` + `kernel/metadata/derived.go`. The enumeration covers 14 struct-field positions plus 1 map-key position (ProjectMeta.Cells). Any **new** cell-id field added to `kernel/metadata.*` must be added here AND to `cellIDFieldPositions` / `cellIDMapKeyValueStructs` in the archtest in the **same PR**.

| Struct                        | Field           | Type                          | Position                          |
|-------------------------------|-----------------|-------------------------------|-----------------------------------|
| `ProjectMeta`                 | `Cells`         | `map[string]*CellMeta`        | map key                           |
| `CellMeta`                    | `ID`            | `string`                      | direct                            |
| `SliceMeta`                   | `BelongsToCell` | `string`                      | direct                            |
| `L0DepMeta`                   | `Cell`          | `string`                      | direct                            |
| `ContractMeta`                | `OwnerCell`     | `string`                      | direct                            |
| `EndpointsMeta`               | `Server`        | `string`                      | direct (HTTP server cell)         |
| `EndpointsMeta`               | `Clients`       | `[]string`                    | slice element                     |
| `EndpointsMeta`               | `Publisher`     | `string`                      | direct                            |
| `EndpointsMeta`               | `Handler`       | `string`                      | direct (command handler cell)     |
| `EndpointsMeta`               | `Invokers`      | `[]string`                    | slice element                     |
| `EndpointsMeta`               | `Provider`      | `string`                      | direct (projection provider cell) |
| `EndpointsMeta`               | `Readers`       | `[]string`                    | slice element                     |
| `JourneyMeta`                 | `Cells`         | `[]string`                    | slice element                     |
| `AssemblyCellRef`             | `ID`            | `string`                      | direct (was `AssemblyMeta.Cells` slice element; the cell-id moved into the new `AssemblyCellRef.ID` struct field in #1086) |
| `CellWireSummary` (derived.go)| `CellID`        | `string`                      | direct                            |

Note: `CellWireSummary.CellID` fixtures live in `runtime/` (outside A1's current `kernel/` scope); this entry is forward-compatible and will be enforced once issue #1201 expands scope to non-kernel packages.

### Out of scope (independent typed concepts, mirror backlog)

Slice-id, contract-id, journey-id, assembly-id, and actor-id each share the cell-id pattern but are semantically distinct identifiers. The mirror upgrade for each is tracked under separate backlog issues (`pri-p3`, `flag-cond`, trigger: "when the corresponding domain fixture area is next touched"):

- `#1201` PR-FIXTURE-CELLID-EXPAND-NONKERNEL-01 — extend A1 scope to non-kernel packages
- `#1202` PR-FIXTURE-SLICEID-TYPED-BUILDER-01 — slice-id typed builder
- `#1203` PR-FIXTURE-CONTRACTID-TYPED-BUILDER-01 — contract-id typed builder
- `#1204` PR-FIXTURE-JOURNEYID-TYPED-BUILDER-01 — journey/assembly/actor-id typed builder

## §2 — Carveout Registry

Carveouts apply at **function-level** only (per `.claude/rules/gocell/ai-robust.md` "archtest carve-out 约束"). Each entry must appear character-identical here and in `fixtureCellIDCarveOuts` in `tools/archtest/fixture_cellid_typed_builder.go`; A4 fails if either side drifts. Names are **module-relative** (no platform module-path prefix) so the registry stays single-place across a module rename / `/v2` bump (#1708 F5).

| Carved-out function | Reason |
|---------------------|--------|
| framework/kernel/governance.TestValidator_FMTC1_CellIDPattern | FMT-C1 RED case: intentionally constructs invalid cell ids ("foo-bar", "FooBar", "1foo", "foo_bar", "a") as fixture content to verify FMT-C1 detection. metadatatest.NewCellID would panic at those literals; the test cannot use the builder. |

## §3 — 升级路径

- **New cell-id field added to `kernel/metadata`**: same PR must update §1 table AND `cellIDFieldPositions` / `cellIDMapKeyValueStructs` lists in the archtest. A1 would otherwise miss the new position (Soft regression).
- **RED case removed or refactored**: same PR removes the corresponding §2 entry AND the `fixtureCellIDCarveOuts` map entry. A4 enforces consistency.
- **Builder body refactor**: A2 is a body-form lock — any structural change to `NewCellID` (e.g. extracting a helper, swapping `errcode.Assertion` for another constructor, adding extra logging) requires a synchronized A2 update. The lock prevents silent erosion of the typed-marker funnel.
- **New CellID* var added to metadatatest**: A5 requires the initializer be `NewCellID(BasicLit STRING)`. The initializer expression and the var name (prefix `CellID`) are both part of the funnel contract. Any var with `CellID` prefix without this initializer shape fails A5; any new metadatatest var that should be downstream-acceptable as a sanctioned cell-id ref must take the `CellID*` name.
- **Production import accidentally added**: `METADATATEST-IMPORT-SCOPE-01` archtest catches it. Upgrade to Hard would require Go's test-only-package proposal; tracked alongside the broader `kernel/cell/celltest` import-boundary pattern.
- **Scope expansion** (mirror backlog issues #1201–#1204): when a mirror backlog issue migration is complete, the same PR must (1) add the new path prefix to `scopePrefixes` in `scanCellIDFixtureViolations` and to the `RunTyped` pattern list, (2) remove the corresponding allowlist entry from `tools/slowgate/allowlist.txt` if one was added for the expanded scope, and (3) close the corresponding mirror issue.

Mirror issue timeline: issues #1201-#1204 do not carry committed timelines; they activate when the corresponding domain fixture area is next touched (per their `flag-cond` labels). A1 scope expansion to non-kernel/ is gated by the migration completion of those issues.

## §4 — AI-robust 评级

- **Downstream Hard** (A1): typed-info callsite identity — Ident→BasicLit chains, third-party const refs, and dynamic `NewCellID(var)` arguments all fail uniformly. There is no AST shape that resolves to "metadatatest.NewCellID(literal) or metadatatest.<Var>" while not actually being one of those two forms.

  Documented blind spots (per ai-robust.md §载体决策原则 "强制盲区自检"):
  - **Ident-typed slice values**: when a slice field (e.g. `JourneyMeta.Cells`) is assigned via an `*ast.Ident` pointing to a pre-built `[]string` var rather than an inline `[]string{...}` composite literal, A1 silently skips the check (the outer `kv.Value` is not a `*ast.CompositeLit`). Downstream Hard (with documented blind spot: Ident-typed slice values for slice-field positions; see archtest godoc Known blind spots). Reverse self-test: `blind_spot_ident_slice.go` in A3 fixture asserts A1 does not report a violation for this shape.
  - **Assignment statement form** (`c.ID = id`): A1 scans `CompositeLit` nodes only; `var c = &metadata.CellMeta{}; c.ID = "rawassign"` is outside A1 scope. This form appears in `makeProject` helpers in `kernel/metadata/derived_test.go` and `assembly_derive_test.go`. Reverse self-test: `blind_spot_assign.go` in A3 fixture.

- **Upstream Hard** (A2 + A5): A2 shape-locks the single sanctioned construction site (`NewCellID` body); the `metadata.MatchCellID` / `panicregister.Approved` / `errcode.Assertion` callees are identity-locked via `TypesInfo.Uses` package-path verification. A5 shape-locks every CellID-prefixed metadatatest var initializer to `NewCellID(BasicLit STRING)` with the same TypesInfo identity check on `NewCellID`. Together A2 + A5 close the upstream half: any structural drift, package substitution, or non-sanctioned var initializer fails archtest immediately. Both load only `./kernel/metadata/metadatatest/...` (single package) instead of the full module type-graph.
- **Meta Hard** (A3, A4): A3 reverse self-test catches A1 regressions (over-broad or no-op), including new bad fixtures for dynamic `NewCellID(var)` and non-CellID-prefixed local var refs. A4 keeps the carveout truth-source synchronized between archtest and ADR.
- **Medium** (`METADATATEST-IMPORT-SCOPE-01`): path-based scope, not type-system. Go cannot express "test-only package" at the type level.

**PR-time vs nightly enforcement latency**: A1 (`TestFixtureCellIDTypedBuilder`) is **not** among the 4 core invariants run at PR-time via `hack/verify-archtest-invariants.sh` in `governance.yml`. It is covered by the nightly `archtest-nightly.yml` 16-shard matrix. This is an intentional latency tradeoff: A1 loads the full `./kernel/...` type graph twice (FlatNonDefaultTags + default), which takes 30–60s on cold-cache CI shards and does not fit the ~30–60s PR-time budget shared across all 4 invariants. The nightly shard matrix amortizes the type-graph cost across 16 parallel jobs.

The funnel forms the **string-typed concept funnel** template (per `.claude/rules/gocell/ai-robust.md` §Hard 范本目录): values are restricted to a sanctioned construction site (NewCellID) plus a closed enumeration of pre-validated constants, with callsite identity verified by go/types.

## Refs

- `kernel/metadata/metadatatest/cellid.go` — builder + const set
- `tools/archtest/fixture_cellid_typed_builder_test.go` — A1/A2/A3/A4/A5 + carveout map + import scope
- `tools/archtest/internal/fixturecellidnegfixture/` — A3 fixture
- `tools/archtest/cell_id_pattern_single_source_test.go` — sibling funnel (PR #484, Medium)
- `pkg/panicregister/panicregister.go` — Approved funnel
- `.claude/rules/gocell/ai-robust.md` §Hard 范本目录, §archtest carve-out 约束
- `docs/architecture/202605121800-adr-archtest-carveout-narrow.md` — function-level carveout discipline (referenced template)
