# ADR: cellmodules as the platform metadata distribution surface

- Status: Accepted
- Date: 2026-06-20
- Issues: #1515 (this PR = bundle + drift guard); follow-ups #1515-PR2 (resolver), #1515-PR3 (source-module tracking)
- Relates: #1086 / ADR 202606030230 (cross-module composition, §Deferred superseded here); #1081 (Operator-SDK epic); #2299 (MDM dogfood); #303 (runtime contract registry — distinct model, see below)

## Context

`gocell generate assembly` (M5/#1086) is workspace-first: it discovers external
cell metadata through the M1 manifest locator only when every participating
module is local under the workspace root. The **Operator-SDK consumption model**
— an external repo (e.g. `externalcells/mdm`, `github.com/ghbvf/gocell-mdm`) that
depends on GoCell modules via `go.mod` and compiles platform cells *in-process*
through `composition.New().With(accesscore.Module(), …)` — cannot use it: the
platform `cell.yaml` lives in the module cache, not the local workspace. Today
MDM works around this with a **hand-written** composition root (`cmd/mdmd`), and
its `assembly.yaml` is declarative-only. #1515 is the tooling that lets such a
consumer *generate* the platform-cell composition instead of hand-writing it.

### Distinct from #303 (not redundant)
#303 (runtime contract registry) is the **out-of-tree** model: external cells run
as separate processes and register contracts to a *running* framework via API +
admin approval; the framework binary stays static and forwards events/HTTP. #1515
is the **in-process / library** model: the external repo is its own binary that
compiles platform cells into itself and needs platform metadata at *build* time.
Both coexist.

## Decision

### D1 — cellmodules distributes the platform metadata closure, not corecells raw

`cellmodules/<cell>` is already the Go API surface external consumers assemble
platform cells against (it distributes `Module()`). We make it **also** distribute
the corresponding metadata, as a generated bundle under
`cellmodules/.gocell/exported-metadata/`.

We explicitly reject reading `corecells` from the module cache directly: corecells
ships **only** cell.yaml + slice.yaml — its contracts live in the gocell **root**
module. Reading corecells alone leaves those contracts unresolved, so
`assembly.collectBrokerCells` silently under-counts broker cells (fail-open: a
broker cell missing → missing `GOCELL_<CELL>_AMQP_URL` wiring at runtime).
Publishing the **closure** (cells + slices + the root contracts those slices
reference + shared schemas) is what closes that fail-open gap at the root.

### D2 — bundle form: a byte-stable, re-parseable metadata subtree

The bundle is a verbatim copy of each closure file at its monorepo-relative path,
plus a synthesized `.gocell/manifest.yaml`. A consumer's
`metadata.NewParser(<bundleDir>)` re-parses it directly (manifest mode) — no new
deserializer, no round-trip fidelity risk, and `$ref`/`SchemaRefs` siblings
resolve unchanged because relative layout is preserved. The closure copies whole
referenced contract dirs + `contracts/shared/` (safe over-inclusion that avoids
enumerating every transitive schema `$ref`; Layer-2 confirms the reachable
*contract set* is complete regardless). The raw form (not pre-derived) is
deliberate: the #1515-PR2 resolver must re-run subscriber/webhook/projection
derivations on the merged consumer∪platform set, which pre-derived structs would
make inconsistent.

The bundle ships in the cellmodules module zip: `golang.org/x/mod/zip` excludes
only VCS dirs (`.bzr/.git/.hg/.svn`), submodules, and symlinks — not `.gocell/` —
so it reaches the module cache verbatim (verified against x/mod zip's
`listFilesInDir`).

### D3 — cell set = all corecells cells (single source, coverage by superset)

The bundle covers **every** corecells cell (identified via
`metadata.IsInCorecellsSubtree`, the funnel-scoped `corecells/` prefix). A
corecells cell without a `cellmodules/<x>` wrapper is harmlessly included (a
consumer cannot import its `Module()`, so it is never referenced). This avoids a
hand-maintained name list: adding a corecells cell auto-enters the closure, and a
forgotten regenerate is caught by the drift guard.

### D4 — enforcement: codegen funnel + golden (`gocell verify codegen-cellmodule-metadata`)

`tools/codegen/cellmodulemeta` computes the desired bundle once (`desiredBundle`);
both `Generate` (write + prune stale) and `Verify` consume it. The verify gate
(hack/verify-codegen-cellmodule-metadata.sh, CI codegen bucket) runs two layers:

| Layer | What | AI-robust |
|---|---|---|
| L1 byte regenerate golden | committed bundle byte-identical to fresh `Generate()`; missing/byte-drift/stale all red | **Hard** (codegen golden; modules_gen / shared-schema precedent) |
| L2 semantic closure equivalence | re-parsed bundle platform closure == monorepo platform closure (subsumes coverage + contract-completeness) | **Medium-Hard** (runtime guard, machine-judged each CI run) |

L2 is the honest ceiling for completeness: it defends broker-cell derivation from
a fail-open under-count by asserting the reachable-contract SET, which a
byte-self-consistent-but-incomplete bundle (L1-clean) cannot evade. Red-case +
anti-vacuity coverage in `cellmodulemeta_test.go`. A dedicated archtest funnel
(sanctioned-writer / coverage as separate named invariants) is defense-in-depth
over the byte golden and is an optional fast-follow.

## Deferred (backlog, not silent)

- **#1515-PR2 — resolver**: `gocell generate assembly` resolves an assembly cell
  ref through `cellModuleImportPath` → `go list -json <pkg>` → owning module dir →
  `NewParser(<dir>/.gocell/exported-metadata).Parse()` → merge into the consumer
  ProjectMeta (re-deriving on the merged set). External fixture acceptance (no
  local go.work; depends on cellmodules; assembly references accesscore; generate
  sees its cell/slice + referenced root contracts). Blocked-by this PR.
- **#1515-PR3 — CellMeta source-module tracking**: thread `MetadataSource.Module`
  through local discovery + a "declared module A but cell.yaml discovered in local
  module B (both exist)" governance cross-check. The cache path already cross-checks
  by construction; this is the broad local-discovery variant.

## References
- golang.org/x/mod/zip `listFilesInDir` (module-zip inclusion rules)
- ADR 202606030230 cross-module composition §Deferred (superseded by D1–D2)
- kubernetes/kubernetes hack/lib/verify-generated.sh (golden gate pattern)
- tools/codegen/sharedschema (mirror + drift-guard precedent)
