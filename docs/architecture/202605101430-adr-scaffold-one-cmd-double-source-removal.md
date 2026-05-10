# ADR: SCAFFOLD-ONE-CMD double-source removal + Codegen default flip

- **Status**: Accepted (2026-05-10)
- **Roadmap reference**: `docs/plans/202605011500-029-master-roadmap.md` #09 SCAFFOLD-ONE-CMD
- **Related ADRs**:
  - `202605061800-adr-assembly-yaml-minimal-derivation.md` — K#10 minimal `assembly.yaml`
  - `202605041700-adr-contractgen-errors-defer-to-c5.md` — K#06 contract codegen opt-in temporary state
  - `202605051300-adr-cellmeta-single-source.md` — K#04 CellMeta typed single source

## Context

After K#04 (codegen-cell-gen) shipped, the project carried two parallel scaffold abstractions:

1. `kernel/scaffold` — fluent `Scaffolder.New(root).WithDryRun(b).CreateCell/CreateSlice/CreateContract/CreateJourney`. Pre-K#04 implementation; pre-built embedded YAML templates.
2. `tools/codegen/cellgen.ScaffoldCell` — the K#04 entry point that powered `gocell scaffold cell` via `cmd/gocell/app/scaffold.go`.

`cmd/gocell/app/scaffold.go` already routed cell scaffolding through `cellgen.ScaffoldCell`; only `scaffoldSlice` / `scaffoldContract` / `scaffoldJourney` still imported `kernel/scaffold`. The package was **dead code in practice** — the only public surface still being exercised was the legacy slice/contract/journey templates that hadn't migrated.

Independently, K#06 PR-2 introduced contract.yaml `codegen: true` as an **opt-in flag** (default false) so the codegen path could land incrementally without breaking unmigrated contracts. K#06 PR-4 (PR#376) opted-in 64 contracts; the 5 `kind=command` contracts were deferred. The K#06 ADR documented `codegen:` as a temporary opt-in to be reversed once migration completed.

K#09 SCAFFOLD-ONE-CMD's headline goal is "one-shot scaffold producing a compilable + testable bundle". Closing it required:

- a single scaffold orchestrator (cell + slice + contract + JSON schemas + auto-generate);
- a way for new scaffold output to land **without** writing `codegen:` boilerplate that the K#06 ADR already marked as transient.

## Decision

**1. Delete `kernel/scaffold/` entirely.**

The package is excised in this PR (~400 LOC + embedded templates). `cmd/gocell/app/scaffold.go` now owns inline templates for the bare slice / contract / journey paths and delegates the cell bundle to `cellgen.ScaffoldCellBundle`. Single source of scaffold templates lives under `tools/codegen/cellgen/templates/` (cell + slice + contract bundle) and `kernel/assembly/gentpl/` (assembly + run.go + app.go).

**2. Extend `kernel/assembly.Generator` instead of creating a new subpackage.**

The K#09 plan originally called for a new `tools/codegen/assemblygen/` subpackage. K#10 already shipped `kernel/assembly.Generator` with the three derived-file methods (`GenerateModulesGen` / `GenerateEntrypoint` / `GenerateBoundary`). Adding a fourth `Scaffold(spec)` method to the same Generator avoids a parallel subpackage and keeps assembly codegen + scaffold colocated — the templates live alongside the existing K#10 templates in `kernel/assembly/gentpl/`.

**3. Flip `ContractMeta.Codegen` default from false to true (parser funnel).**

`kernel/metadata.parseContract` now inspects the yaml AST: when `codegen:` is absent, `m.Codegen = true`. Explicit `codegen: false` is the only opt-out. The 5 deferred `kind=command` contracts in `examples/iotdevice/contracts/command/device-command/*` were marked explicit `codegen: false` to preserve their K#06 deferred status under the new default-true world.

This change closes the K#06 ADR's pending "remove the opt-in" item: the funnel mechanism is the parser, not a per-contract flag. Scaffold output (`tools/codegen/cellgen/templates/scaffold-contract.tmpl`) **never emits `codegen:`** — guarded by archtest `SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01`.

## Consequences

### Positive

- **Single source of truth for scaffolding.** Every scaffold path now goes through one of two well-known modules (`tools/codegen/cellgen` for cell+slice+contract, `kernel/assembly.Generator` for assembly). New scaffold features extend the existing files; no parallel registry.
- **K#09 funnel makes contract codegen opt-in unrepresentable in scaffold output.** The contract template doesn't have a place to write `codegen:`; the parser enforces the default at load time. This is the AI-Hard form of the rule: violation isn't merely caught after the fact — it can't be expressed.
- **Net code reduction.** ~400 LOC of `kernel/scaffold/` removed; the inline replacement in `cmd/gocell/app/scaffold.go` is ~120 LOC carrying the same surface (slice/contract/journey skeletons).

### Negative / known carve-outs

The following carve-outs are explicit and tracked in `docs/backlog.md`:

1. **`examples/{ssobff,todoorder,iotdevice}/assembly.yaml` simplification to K#10 minimal form** — deferred from this PR (per ADR 202605061800). Tracked: `EXAMPLES-ASSEMBLY-MINIMAL-CLEANUP`.
2. **`gocell new` project bootstrap (kubebuilder init / goctl new analog)** — out of scope. K#09 solves "add a cell to an existing project"; bootstrapping from empty needs separate design. Tracked: `SCAFFOLD-PROJECT-INIT-CMD`.
3. **Removal of explicit `codegen: true` lines on the 64+ already-migrated contracts** — the parser default makes them redundant, but a batch sed over 64+ files would dilute the K#09 review. Tracked: `CONTRACT-YAML-CODEGEN-DEFAULT-CLEANUP`.
4. **Idempotent re-scaffold (kubebuilder MainUpdater pattern)** — not needed: K#10 made `modules_gen.go` fully data-driven from `assembly.yaml`. Re-adding a cell is "edit yaml + re-run `gocell generate assembly`"; no marker injection.

### Operational

- `kernel/scaffold` removal is **not back-compat**. Any external caller (none in-tree) would break; project rule "不向后兼容" applies — no deprecation shim.
- The 5 `kind=command` contracts now carry an explicit `codegen: false` line documenting the deferred status. When command-kind codegen is implemented, those lines become the migration switch.
- archtest `SCAFFOLD-BUNDLE-MARKER-01` and `SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01` lock the two K#09 invariants. Both archtests are AI-rebust **Soft** (string anchor on template content); the **Hard** enforcement is the parser funnel in `kernel/metadata.parseContract` via `contractYAMLHasKey` AST inspection — the archtests serve as belt-and-suspenders against template regression. Upgrade path tracked in `docs/backlog.md` as `SCAFFOLD-BUNDLE-ARCHTEST-HARDEN`.

## Alternatives considered

- **Keep `kernel/scaffold` indefinitely.** Rejected. The package was dead in the cell path post-K#04; carrying ~400 LOC of parallel templates indefinitely violates the L2 audit requirement (no double-source).
- **New `tools/codegen/assemblygen/` subpackage.** Rejected. K#10 already owned the assembly codegen path; adding a fourth method is one extension, not a new package.
- **`Codegen *bool` instead of yaml AST inspection.** Rejected. `*bool` would force every reader (`tools/codegen/contractgen`, `kernel/governance/rules_http`, `tools/generatedverify`) to handle nil — invasive churn for an internal default. AST inspection is local to the parser and zero-API-change.
- **Leave the deferred 5 `kind=command` contracts as-is and tolerate archtest failures.** Rejected. Failures cascade into other governance rules; the explicit `codegen: false` line documents the K#06 → K#09 transition cleanly.

## References

- Roadmap: `docs/plans/202605011500-029-master-roadmap.md` #09
- Plan: `/Users/shengming/.claude-ming/plans/docs-plans-202605011500-029-master-road-atomic-emerson.md`
- ref: `zeromicro/go-zero` `tools/goctl/api/gogen/gen.go` (multi-file scaffold orchestrator pattern)
- ref: `kubernetes-sigs/kubebuilder` `pkg/plugins/golang/v4/scaffolds/api.go` (scaffold flag conventions)
- ref: `cmd/corebundle/run.go` (canonical three-layer composition root mirrored by the run.go template)
