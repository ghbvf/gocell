# ADR: archtest workspace-root model (nearest-go.mod → go.work + module-path map)

- Status: Accepted
- Date: 2026-06-04
- Issue: #1555 (go.work P0-2 keystone) — closes #1302/#1304 coordination
- Relates: #1554 (P0-1: go.work + manifest foundation), Plan D
  (`docs/plans/product-roadmap/202604300950-plan-d-go-workspace-multimodule-migration.md`)

## Context

`tools/archtest` resolved its production-scan root by walking up from cwd to the
**nearest `go.mod`** and using that single module's import path as the sole scan
prefix (single-module assumption). The moment any subtree is extracted into a
nested module (Plan D: `github.com/ghbvf/gocell/mdm`, `/zerotrust`,
separate-module examples), a root `./...` load under `GOWORK=off` no longer
covers it — archtest would **silently drop coverage** of the extracted module.
This is the keystone (P0-2): it MUST land before any P1–P7 extraction.

P0-1 (#1554) committed `go.work` (`use .`), `.gocell/manifest.yaml`, the
`kernel/metadata` Locator (multi-module metadata aggregation), and
`tools/packagesload`'s `ModeModule`/`ModeWorkspace` typed funnel —
`ModeWorkspace` exists precisely "for a future cross-module archtest loader".

## Decision

Replace the single-module model with a **workspace-root + module-set** model.
The scan set is DERIVED from `go.work`, not hand-maintained.

1. **Workspace enumeration** (`tools/workspace`): `WorkspaceRoot()` walks up
   **go.work-first**, falling back to the nearest go.mod only when no go.work
   exists. This is honest two-context mode detection, not a silent default:
   go.work-first guarantees the monorepo (whose go.work sits above every nested
   module) always anchors to the WORKSPACE root — never to a nested module's
   go.mod from a subdirectory (the bug #1555 fixes) — while the single-module
   fallback serves an external consumer repo (Operator-SDK, #1081) that has a
   go.mod but no go.work (RunStandardCellRules scanning its own module). A tree
   with neither marker errors. `Modules(root)` parses `go.work use` (via
   `gomodutil.ReadWorkUseDirs` → `modfile.ParseWork`) into `{Dir, ImportPath}`
   members and **cross-checks `manifest.modules ⊆ go.work.use`** (fail-closed on
   drift; a workspace without a manifest has no metadata modules to check); with
   no go.work it returns the single module read from root/go.mod. go.work is
   authoritative for the Go module set; the manifest may be a subset (pure-tool
   modules with no metadata are legal).

2. **module-set-aware classification** (`kernel/depgraph`): the single-module
   `LayerOf/CellOf/SliceOf` free functions are **deleted** and replaced by a
   `Classifier` that selects each import path's owning module by **longest
   prefix**, so a nested module path (a string subpath of the core module)
   classifies within its own module — not as `LayerUnknown` of the core.
   `Graph.Module string` → `Graph.Modules []string`; `FromNodes`/`FromPackages`
   take the set; `inModule` is explicit set membership. Single module = a
   one-element set, byte-identical to the former behavior.

3. **workspace production scan** (`typeseval.LoadProductionPackages`): loads via
   `ModeWorkspace` with one **relative-dir** `./<dir>/...` pattern per member.
   Relative-dir (not `<importPath>/...` module-path) patterns are mandatory: a
   module-path pattern makes `go` resolve that path as an external dependency at
   its required version (network fetch), whereas a directory pattern resolves the
   local workspace member. Each member's `<importPath>/generated/` prefix is
   filtered out (per-module generated exclusion preserves the existing
   `ProductionResolver` type funnel).

4. **cross-module import-direction ban** (`CROSS-MODULE-IMPORT-DIRECTION-01`): the
   base (core) module's packages must not import any other workspace member
   (Plan D: 顶层 = core; satellites depend on core, never the reverse). Now an
   **active multi-satellite gate** — the live workspace already holds satellite
   modules (adapters/*, corecells, examples/*, cellmodules, cmd/*, tools,
   generated, tests/*); it reports no violation only because the layering holds,
   not because the workspace is single-module (see Amendment 2026-06-13). The
   reverse fixture exercises the firing path.

5. **`gocell graph` CLI** spans the workspace (LoadWorkspace over per-module
   patterns) when `--root` has a `go.work`, falling back to single-module `Load`
   for a standalone module (explicit mode detection, not a silent default).

### Reverse fixture

`tools/archtest/testdata/workspace-multimodule` is a self-contained two-module
workspace whose satellite path (`example.test/wsmm/satellite`) is a string
subpath of the core (`example.test/wsmm`) — the Plan D shape.
`TestWorkspaceMultiModuleFixture` asserts both modules are scanned, the satellite
cell classifies as `LayerCells` (longest-prefix owner), and the real
satellite→core edge is observed.

## AI-robust ratings

| Mechanism | Rating | Form |
|-----------|--------|------|
| Scan set = `go.work use` (coverage not dropped on extraction) | **Hard by construction** | single-source derivation from the compiler's module truth; zero drift surface |
| `Production` generated filter (per-module prefix) | **Hard** | inherits `ProductionResolver` type funnel; reaching generated/ output is not expressible without renaming `Production`→`All` |
| `manifest ⊆ go.work` cross-check | Medium (fail-closed guard) | runtime consistency check; drift fails closed |
| `Classifier` (deleted single-module free funcs) | Medium | single classification entry; all callers migrated |
| `CROSS-MODULE-IMPORT-DIRECTION-01` (generic) | Medium | archtest type-aware via `Classifier.OwningModule` on real import edges; Hard unreachable (Go cannot express "module A ⊀ module B" — #851/#893/#1282 permanent ceiling) |
| per-satellite depguard path-ban (complement) | **Hard** (per satellite) | `.golangci.yml core-no-satellite-import` rule — path-level ban of each top-level satellite prefix from production core; appended per extraction (**realized gh #1590**) |
| CI never runs the production scan with `GOWORK=off` | Medium content-scan (Hard unreachable) | `ARCHTEST-CI-GOWORK-ACTIVE-01` scans `.github/workflows/*` + runtime `checkGOWORK()` fail-fast (**realized gh #1590**) |

The two Medium mechanisms each have a Hard complement: the cross-module ban is
backstopped per-satellite by depguard (gh #1590); the coverage guarantee is itself Hard by
construction.

## #1302 / #1304 coordination (close-out)

- **#1302** (migrate ~76 rule families to module-path-agnostic `CellRule`): NOT
  done here. This PR delivers the foundation those rules consume — the
  workspace-aware `Production` scope + `tools/workspace.Modules` enumeration +
  the `Classifier`. A migrated `CellRule` that scans the whole workspace uses the
  `Production` scope (now workspace-spanning); the ratchet baseline is untouched.
- **#1304** (extract archtest to an independent module / FreezingArchRule
  baseline / ratchet Hard-ization): three sub-items, NONE done in *this* PR but
  reconciled later —
  - *extract to independent module*: → reassigned to **#1561** (go.work P6 tools
    module), out of #1304 scope.
  - *external FreezingArchRule `BaselinePath`*: still **#1304 open, demand-gated**
    (needs a real brownfield external adopter; greenfield does not).
  - *ratchet Hard-ization + downstream residual*: **done (#1304)** — the
    `module_path_funnel.baseline` reached EMPTY (#1302 migration complete), so the
    bare-literal ratchet is a Medium pure-ban terminal state (NOT Hard — archtest
    CI-time, permanent Go ceiling); and the `no-reconstruction` sub-check upgraded
    from AST flatten to **typed const-eval** (closing const-of-const / cross-package
    residual — downstream Hard), with runtime string ops the permanent residual.
  archtest still stays in the root module, so the `hack/verify-archtest*.sh` three
  execution modes need **no change** — they run `go test ./tools/archtest` (root
  module) and the workspace awareness is internal Go. When archtest is later
  extracted (#1561), its own production scan of the workspace still works via
  `WorkspaceRoot()` (it walks up to the repo `go.work`, not the nearest go.mod).

## Operational notes (satellites have landed)

- The production scan requires `GOWORK` **active** (ModeWorkspace). Running
  archtest with `GOWORK=off` now silently drops satellite modules from the scan
  (the workspace is multi-module). This is guarded on two layers (gh #1590):
  the runtime `checkGOWORK()` fail-fast in `cmd/gocell/internal/archtestrunner`
  rejects `GOWORK=off` on the `gocell verify archtest` path (the sole
  whole-workspace scan entrypoint), and the `ARCHTEST-CI-GOWORK-ACTIVE-01`
  meta-archtest fails closed if any `.github/workflows/*` step running an archtest
  scan declares `GOWORK=off`. (`verify-workspace.sh` sets `GOWORK=off` for
  per-module isolation builds — a different gate, not the archtest scan.)
- At each satellite extraction: add the module to `go.work` (required to compile)
  AND to `.gocell/manifest.yaml` if it carries metadata (the cross-check enforces
  the subset), and — for a new TOP-LEVEL group (Plan D P1+: mdm, zerotrust) —
  append its import-path prefix to the `.golangci.yml core-no-satellite-import`
  deny list (the Hard complement to CROSS-MODULE-IMPORT-DIRECTION-01; new members
  under an existing group are auto-covered by the prefix ban, gh #1590).

## Amendment 2026-06-13 (#1590 close-out — satellites landed, complements realized)

Trigger: the workspace went multi-module via #1556 (examples/* split), #1559
(cellmodules + cmd split), #1560 (corecells), #1644 (examples/demo) — the
"satellites land" precondition of the two Medium mechanisms is now met (the
`CROSS-MODULE-IMPORT-DIRECTION-01` godoc was corrected to "active gate" in #1564).
The original "Vacuous today (single module)" framing above is superseded.

Threat-model re-evaluation (per the AI-robust charter: an amendment re-rates the
original safety model). Both Medium mechanisms now carry their landed complements;
neither relies on human memory for correctness:

- **`CROSS-MODULE-IMPORT-DIRECTION-01` (Medium) → Hard complement realized.** The
  `.golangci.yml core-no-satellite-import` depguard rule path-bans every current
  top-level satellite prefix from the production core (`gocell.go` + kernel / pkg /
  runtime; `tests/**` deliberately excluded — root-module fixture helpers
  legitimately cross modules). Compile-fast, `//nolint` gated by `nolintlint:
  require-explanation`. The depguard list is **intentionally partial** — the
  generic archtest stays the correctness catch-all that auto-covers satellites not
  yet enumerated, so a forgotten deny line loses fast feedback, never correctness.
  No completeness sync-guard is added (it would contradict that design).
- **`GOWORK!=off` in CI (Medium, Hard unreachable) → realized.** An env-var
  runtime precondition cannot be a compile error, so this is terminal-Medium:
  runtime `checkGOWORK()` (CLI path) + `ARCHTEST-CI-GOWORK-ACTIVE-01` (CI-YAML
  scan, defense-in-depth). Disclosed blind spot: a future entrypoint that runs a
  whole-workspace scan, bypasses the CLI, AND sets `GOWORK=off` internally evades
  both — but the established routing (all whole-workspace scans go through the CLI;
  the PR-time invariant subset excludes them) makes that an active violation.
