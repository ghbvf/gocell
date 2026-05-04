# ADR — K#05 markergen + cellgen unified single source

- **Date**: 2026-05-05
- **Status**: Accepted (shipped via refactor/510-k05-markergen-merge)
- **Supersedes**: roadmap K#05 PR-V1-CODEGEN-MARKER-MIGRATE 3-PR split plan (PR-A1 ✅ shipped 2026-05-04 commit `9ccef27a`; PR-A2 + PR-B merged into this single PR after deeper analysis — see "Decision 1" below)
- **Related**: ADR `202605051300-adr-kernel-cellmeta-single-source.md` (K#05 PR-A1 type-layer)

## Context

Pre-K#05, GoCell had three layers of duplicate "single sources of truth" between yaml metadata and Go code:

| Layer | Pre-K#05 dual source | Symptom |
|---|---|---|
| Type | `kernel/cell.{CellMetadata,Owner,SchemaConfig,CellVerify,L0Dep}` (5 types, 7 fields, enum-typed) **vs** `kernel/metadata.CellMeta` (12 fields, string) | 5 fields had drifted between the two type sets |
| Content | 5 cell `cell.go` constructors with `cell.NewBaseCell(&metadata.CellMeta{ID: "foo", Type: "core", …})` literals **vs** the same fields declared in `cell.yaml` | hand-written literals diverged from yaml (e.g. ordercell `verify.smoke = "ordercell/smoke"` in cell.go vs `smoke.ordercell.startup` in cell.yaml) |
| Wire | `reg.RouteGroup(…)` / `reg.Subscribe(…)` calls in `cell.go`/`cell_init.go` **vs** `cell.yaml.listeners` (only ordercell) **vs** `slice.yaml.{routeMounts,subscribes}` (only ordercell) | cellgen had to read both yaml and Go to assemble cell_gen.go; new routes risked silent yaml/Go drift |

PR-A1 (shipped) eliminated the type layer. This PR eliminates the content + wire layers in one shot.

## Decision 1: Merge PR-A2 + PR-B into a single PR (rather than the originally-planned 3-PR sequence)

The original roadmap split K#05 into A1 (type) → A2 (content) → B (wire) to keep PRs reviewable. After PR-A1 shipped we re-evaluated and found:

- Both A2 and B touch the same 5 `cell.go` files (constructors and Init). Splitting forces every cell to be edited twice with intermediate state where cell.go uses `loadCellMetadata()` but still hand-rolls `reg.RouteGroup`. That intermediate state has no value (consumers can't observe it; cellgen verify gate doesn't care) and doubles diff churn.
- A2 introduces a `cellmetagen` subpackage that is rendered redundant by B (after K#04 opt-in for all 5 cells, `cell_gen.go` is the natural home for the metadata literal — adding a third generated file is overengineering).
- The user explicitly opted for "no intermediate dual-source state".

→ Single PR delivers W1 RED (5 archtest gates) → W2-W5 GREEN waves → W7 cleanup. ~13 commits, ~70 files.

## Decision 2: Drop the cellmetagen subpackage; emit `loadCellMetadata()` from cellgen's existing `cell.tmpl`

Plan v2 considered a separate `tools/codegen/cellmetagen/` subpackage. Architect review (3-role deep audit) flagged this as redundant: after K#05 W4 every cell is K#04-opted-in, so `cell_gen.go` is always emitted. Adding `loadCellMetadata()` as a template block in `cell.tmpl` is one block; a separate subpackage is one extra archtest gate, one extra test file, and one extra mental concept for new contributors. The "GoStructName-as-opt-in single source" K#04 invariant is preserved unchanged.

Saving: ~6h dev, ~5 archtest → 4 archtest, ~10 fewer files.

## Decision 3: Closed-set switch for marker dispatch (drop kubebuilder Registry/Definition abstraction)

`tools/codegen/markergen` adopts the **formal grammar** of `kubernetes-sigs/controller-tools pkg/markers/parse.go` (`splitMarker` at L751-L773; `// +<prefix>:<name>[:subname]=k=v[,k=v…]`) and the **error aggregation** pattern (`MaybeErrList` at `collect.go:L94-L106`). It does **not** adopt the `Registry` + `Definition` + `reflect.Type` 3-layer abstraction.

Why: controller-tools is built for kubebuilder's plugin ecosystem with 50+ marker types registered dynamically. GoCell has a **closed set of 3 markers** (`cell:listener`, `slice:route`, `slice:subscribe`) that are forever co-located in the markergen package. A `switch markerName { case "cell:listener": parseListener(…) }` dispatch is ~50 LOC and lets every marker's required-field validation be hand-written next to the spec it produces. The Registry abstraction would be ~350 LOC for zero behavioral benefit at our scale.

Saving: ~350 LOC, ~4h W2 dev. Trade-off: adding a 4th marker type costs ~10 LOC of switch + parse function; if GoCell ever needs ~10+ marker types we can introduce Registry then. Per CLAUDE.md「规则不超前于代码库现状」.

## Decision 4: Drop `metadata.CellMeta.Listeners` / `RouteMountMeta` / `SubscribeDeclMeta` / `ListenerDeclMeta` backing types

Plan v3 originally proposed deleting `CellMeta.Listeners` immediately to make "wire single source = marker" hold at the type layer. Implementation revealed that markergen's fallback path (yaml→bundle when cell.go has no markers yet) needs those fields during the W2→W5 wave migration. After W4 + W5 the yaml fields are empty (NO-WIRE-FIELDS-IN-YAML-01 archtest enforces this) so the markergen→cellgen pipeline runs marker-only at ship; the CellMeta wire fields exist but always carry marker-derived values.

Implementation: shipped in this PR — `CellMeta.Listeners`, `SliceMeta.RouteMounts`, and `SliceMeta.Subscribes` fields were removed during the W2 cleanup wave. `markergen.Merge` no longer falls back to ProjectMeta yaml fields. The 3 backing types (`ListenerDeclMeta`, `RouteMountMeta`, `SubscribeDeclMeta`) are also removed in this PR's cleanup commit (Cx1 review finding ARCH-02/06). Marker-only wire path is now the exclusive code path at both the runtime and type layers.

## Decision 5: Marker syntax — minimal field set + smart defaults

```go
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
// +cell:listener:ref=cell.InternalListener,prefix=/internal/v1

// +slice:route:slice=ordercreate,subPath=/orders
// +slice:route:slice=devicecommand,listener=cell.InternalListener,subPath=,method=RegisterInternalRoutes

// +slice:subscribe:slice=configsubscribe,topic=event.config.entry-upserted.v1,handler=HandleEntryUpserted,group=configcore
```

Fields and defaults:
- `cell:listener`: `ref` required, `prefix` optional (empty = mount on root)
- `slice:route`: `slice` required; `listener` defaults to `cell.PrimaryListener` (covers ~90% of single-listener cells); `subPath` optional (empty = direct attach to listener prefix); `method` optional (defaults to `RegisterRoutes`; alternative is `RegisterInternalRoutes` for devicecell internal routes); `HandlerField` auto-derived from the AST field name on which the marker sits
- `slice:subscribe`: `slice`, `topic`, `handler`, `group` all required; `SliceField` auto-derived from the AST field name

Multiple markers on the same field are legal (e.g. devicecell `commandHandler` carries both a primary `RegisterRoutes` marker and an internal `RegisterInternalRoutes` marker).

## Decision 6: Catalog augment (`CellWireSummary` exposure via `/api/v1/devtools/catalog`)

Implementation: shipped in this PR — `metadata.DeriveCellWireSummaries` builds the projection; `cmd/corebundle.BuildCellWireSummaries` wires it into the bootstrap path; `cmd/gocell/app/export.attachWireSummaries` injects into CLI export. Producer-side path unified across HTTP and CLI surfaces. Fix landed in commit `251a9936` (K#05 review P1-1). Backlog item `K05-CATALOG-WIRE-SUMMARY` closed (delivered).

## Decision 7: Routes considered and rejected

The 3-role cross-review (architect / product-manager / open-source explorer) considered multiple alternatives:

| Alternative | Rejected because |
|---|---|
| Functions-as-markers (Google `wire` model — `wireListener(...)` calls in `init()`) | `init()` side effects with build-tag isolation has higher mental cost than doc comments |
| Schema-as-code (Facebook `ent` model — `func (c *Cell) WireSchema() WireSchema { ... }`) | Requires `go run` subprocess at codegen time; GoCell cells depend on adapters that aren't constructible without injection — non-trivial to make cells `go run`-able in isolation |
| External DSL file (zeromicro `goctl` `.api` files with ANTLR) | Loses Go-language refactoring (rename, find-references); requires antlr toolchain |
| Discriminated union `WireMarker { Kind, ... }` | Architect review: pseudo-DRY; field validation per-Kind ends up more code than 3 specific structs |
| `cell_init.go` merge into `cell.go` | architect review: accesscore would balloon to 600+ lines, hurting readability |
| Drop `slice.yaml` entirely (move governance to markers) | architect review: slice.yaml carries `contractUsages`/`verify.*`/`allowedFiles` which are governance SoR — moving to markers requires rewriting ADV-06 / VERIFY-01 / SCOPE-01 (~30-50h, out of scope) |
| Derive `goStructName` via AST heuristics | architect review: `goStructName` is a no-cost dual source (one string, archtest-validated O(1)); heuristic is harder to reason about |

## Decision 8: CLI flag defaults and scaffold template

Product review suggested:
- Default `--all` + `--local` for `gocell generate cell` (covers 99% of dev workflow)
- Scaffold template emits stub markers when scaffolding a new cell

Implementation: shipped in this PR — `gocell generate cell` defaults to `--all`; `gocell verify` defaults to `--local`; `gocell scaffold cell` template emits stub markers (`// +cell:listener:` + `// +slice:route:` + `// +slice:subscribe:`). The `--type` and `--level` flags now flow through to `cell.yaml` (previously ignored; the YAML hardcoded `type: core` / `consistencyLevel: L1`). Backlog item `K05-CLI-FLAG-DEFAULT-AND-SCAFFOLD` closed.

## Consequences

**Eliminated dual sources**:
- `&metadata.CellMeta{...}` literals: 0 in `cell.go` files (was 5)
- `cell.yaml.listeners`: removed (was 1)
- `slice.yaml.{routeMounts,subscribes}`: removed (was 2 — both ordercell)
- Hand-rolled `reg.RouteGroup(...)` / `reg.Subscribe(...)` calls in `cell.go` / `cell_init.go`: 0 (were 9 RouteGroups + 19 Subscribes across 4 cells)

**Net add**:
- `tools/codegen/markergen/` (4 files, ~430 LOC + ~640 LOC tests)
- 5 `cell_gen.go` files (re-emitted from yaml + markers; ordercell pre-existed and was regenerated)
- 4 `slice_gen.go` files (auditappend / configreceive / configsubscribe / sessionlogout — emit typed `eventHandlerService` interface)
- 5 archtest gates (`NO-METADATA-LITERAL-IN-CELLGO-01`, `MARKER-WIRE-SINGLE-SOURCE-01`, `NO-WIRE-FIELDS-IN-YAML-01`, `MARKERGEN-DRIFT-VERIFY-01`, `MARKER-MISSING-FOR-WIRE-CALL-01`); 1 deleted (`CODEGEN-MARKER-NONE-01` is now reversed semantically)

**Backlog spawned** (not blocking ship):
- Wire-summary catalog augment (Decision 6) — delivered in commit `251a9936`; backlog item closed

## References

- `ref: kubernetes-sigs/controller-tools pkg/markers/parse.go@main` (L751-L773 splitMarker — adopted)
- `ref: kubernetes-sigs/controller-tools pkg/markers/collect.go@main` (L94-L106 MaybeErrList — adopted)
- `ref: kubernetes-sigs/controller-tools pkg/markers/parse.go@main` (L530-L551 Registry/Definition — rejected)
- `ref: google/wire internal/wire/parse.go@main` (function-call markers — rejected)
- `ref: ent/ent entc/load/load.go@master` (schema-as-code — rejected)
