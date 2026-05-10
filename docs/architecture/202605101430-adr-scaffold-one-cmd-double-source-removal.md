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

`kernel/metadata.parseContract` now inspects the yaml AST: when `codegen:` is absent, `m.Codegen = true`. Explicit `codegen: false` is the only opt-out. The 5 deferred `kind=command` contracts under `examples/iotdevice/contracts/command/devicecommand/` (filesystem path retains the legacy hyphen for git history; ADR refers to the logical contract bundle) were marked explicit `codegen: false` to preserve their K#06 deferred status under the new default-true world.

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
- `gocell scaffold contract` 的 inline draft skeleton 显式 emit `codegen: false`，与 5 个 deferred `kind=command` 合约对称：funnel 默认 true 适用于 ScaffoldCellBundle 产出的完整 contract（含 schemaRefs），standalone draft 写入显式 opt-out 直到 schemas 填充后再翻转。
- archtest `SCAFFOLD-BUNDLE-MARKER-01` 与 `SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01` 通过 `ScaffoldCellBundle` 产出的实际 cell.go / contract.yaml 做 AST 断言（real-source capture），AI-rebust **Medium**。Hard 防线（parser AST funnel `contractYAMLHasKey`）已在 `kernel/metadata.parseContract` 落实；archtest 是产出层冗余守。

## Alternatives considered

- **Keep `kernel/scaffold` indefinitely.** Rejected. The package was dead in the cell path post-K#04; carrying ~400 LOC of parallel templates indefinitely violates the L2 audit requirement (no double-source).
- **New `tools/codegen/assemblygen/` subpackage.** Rejected. K#10 already owned the assembly codegen path; adding a fourth method is one extension, not a new package.
- **`Codegen *bool` instead of yaml AST inspection.** Rejected. `*bool` would force every reader (`tools/codegen/contractgen`, `kernel/governance/rules_http`, `tools/generatedverify`) to handle nil — invasive churn for an internal default. AST inspection is local to the parser and zero-API-change.
- **Leave the deferred 5 `kind=command` contracts as-is and tolerate archtest failures.** Rejected. Failures cascade into other governance rules; the explicit `codegen: false` line documents the K#06 → K#09 transition cleanly.

## Round-4 amendment (2026-05-10): scaffold write funnel

PR #442 round-4 closes the remaining safety + reliability gaps via a single funnel: `pkg/pathsafe.WritePlannedFiles` is now the only filesystem write entry for scaffold/codegen. All six scaffold writers (`ScaffoldCell` / `ScaffoldCellBundle` / `Generator.Scaffold` / `scaffoldSlice` / `scaffoldContract` / `scaffoldJourney`) collect a `[]pathsafe.PlannedFile` and delegate writing to `WritePlannedFiles`, which performs:

1. **Containment** — `ContainPath` resolves every parent component through `filepath.EvalSymlinks` and rejects any path whose ancestors resolve outside `realRoot`. Symlink escape (`contracts/http/foo → /tmp/outside`) becomes unrepresentable; the previous round only protected `cell.go`/`cell.yaml`.
2. **All-or-nothing conflict detection** — every `AbsPath` is `os.Stat`-checked before any write. A conflict on `contract.yaml` no longer leaves `cells/<id>/cell.go` half-written.
3. **Atomic write with rollback** — on the first write/mkdir failure, every file written and every directory created during the call is removed; the original error is wrapped with the rollback outcome.

Conflict errors use `errcode.ErrConflict` (HTTP 409) and put the failing path in `WithDetails(slog.String("path", …))` so 4xx CLI output and the public envelope both surface it (round-2 used `ErrValidationFailed` + `WithInternal`, hiding the path from CLI users).

### AI-Hard archtest funnel

`tools/archtest/scaffold_write_funnel_test.go` (`INVARIANT: SCAFFOLD-WRITE-FUNNEL-01`) statically forbids any direct `os.MkdirAll` / `os.WriteFile` / `os.Mkdir` / `os.Create` / `os.OpenFile` call inside `tools/codegen/cellgen/...`, `kernel/assembly/...`, and `cmd/gocell/app/scaffold*.go`. The only allowed implementer is `pkg/pathsafe/pathsafe.go`. Bypass requires (a) adding to the archtest's package allowlist **and** (b) re-introducing an `os.*` call — both visible in diff review. AI-rebust evaluation:

- **Hard** under ai-collab.md `载体决策原则` #2 (typed function call as the violation defense). The funnel itself is the type-system contract; the archtest is the static defense layer that prevents accidental drift through new `os` imports.
- Real-source AST scan via `scanner.EachFile` with concrete-package allowlist; not string-anchor or comment-exemption — meets the `Cannot be Soft` bar.
- Residual escape: the archtest's package allowlist is a string list; adding a new scaffold subpackage requires updating that list. Documented in the archtest godoc as a known extension contract; mitigation tracked under `SCAFFOLD-WRITE-FUNNEL-HARD-UPGRADE` for future typed-Writer abstraction.
- **Documented exemption**: `cmd/gocell/app/generate_catalog.go` 与 `cmd/gocell/app/export.go writeOut` 接收用户 `--out` 路径，输出位置不在 root containment 语义范围内，由 archtest 的 `scaffoldOnlyPred` 显式排除并在 archtest 文件级 godoc 中记录扩展约束。

### File permission alignment

`pathsafe.PlannedFile.DirMode` defaults `0o755`, `FileMode` defaults `0o644` (helm/helm `pkg/chartutil/create.go` convention). Round-2 used `0o750`/`0o600`; the looser default unblocks multi-user CI environments. Secrets-bearing scaffolds (none currently) can still pass explicit `0o600` per file.

### Kebab cell ID rejection

Round-2 silently rewrote `--id=test-cell` to package `testcell` via `strings.ReplaceAll`. Round-4 rejects kebab on the `gocell scaffold cell` path (matching the existing `gocell scaffold slice` behavior at line 403). No silent-rewrite migration debt.

### Closed by round-4

| Round-2 ID | Round-4 outcome |
|------------|-----------------|
| F8/F9 SCAFFOLD-DRY-RUN-COMPLETE-INVENTORY | dry-run prints the full plan via `pathsafe.PlannedPaths` |
| F10 SCAFFOLD-HELP-COMPLETE | help.go synced for `--with-{http,events,both,skip-generate}` + `scaffold assembly` |
| F11 SCAFFOLD-KEBAB-MIGRATION-DOC | resolved by rejection at validation; no migration-doc needed |
| F12 SCAFFOLD-FILE-PERMISSION-ALIGN | `PlannedFile` defaults `0o644`/`0o755` |
| F13 SCAFFOLD-FAILURE-CLEANUP-RECOVERY | `WritePlannedFiles` rollback on failure |
| F14 SCAFFOLD-GENERATOR-PURE-BYTES | `Generator.Scaffold` returns plan; `WritePlannedFiles` is the writer |
| F16 SCAFFOLD-CONFLICT-ERRCODE-SEMANTICS | `ErrConflict` + `WithDetails(path)` |
| F7 SCAFFOLD-AUTOGENERATE-TEST-COVERAGE | `TestRunScaffoldCell_BundleWithAutoGenerate` |
| (new) SCAFFOLD-WRITE-FUNNEL-01 | Hard archtest |

Carryover: `SCAFFOLD-INLINE-TEMPLATE-ARCHTEST` (F15, independent archtest theme) and `ASSEMBLY-RUN-RUNTIME-SMOKE` (F17, stub-content theme) remain backlog follow-ups.

## References

- Roadmap: `docs/plans/202605011500-029-master-roadmap.md` #09
- Plan: `/Users/shengming/.claude-ming/plans/docs-plans-202605011500-029-master-road-atomic-emerson.md`
- Round-4 plan: `/Users/shengming/.claude-ming/plans/1-p1-scaffold-virtual-waffle.md`
- ref: `zeromicro/go-zero` `tools/goctl/api/gogen/gen.go` (multi-file scaffold orchestrator pattern)
- ref: `kubernetes-sigs/kubebuilder` `pkg/plugins/golang/v4/scaffolds/api.go` (scaffold flag conventions, path validation)
- ref: `helm/helm` `pkg/chartutil/create.go` (0o644/0o755 file/dir mode default)
- ref: `cmd/corebundle/run.go` (canonical three-layer composition root mirrored by the run.go template)
