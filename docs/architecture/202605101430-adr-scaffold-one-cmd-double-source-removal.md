# ADR: SCAFFOLD-ONE-CMD double-source removal + Codegen default flip

- **Status**: Accepted (2026-05-10)
- **Roadmap reference**: `docs/plans/archive/202605011500-029-master-roadmap.md` #09 SCAFFOLD-ONE-CMD
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
| F8/F9 SCAFFOLD-DRY-RUN-COMPLETE-INVENTORY | dry-run prints the full plan via `(pathsafe.PlanSet).Paths()` (was `pathsafe.PlannedPaths(plan)` pre-PR #555 PlanSet typed funnel) |
| F10 SCAFFOLD-HELP-COMPLETE | help.go synced for `--with-{http,events,both,skip-generate}` + `scaffold assembly` |
| F11 SCAFFOLD-KEBAB-MIGRATION-DOC | resolved by rejection at validation; no migration-doc needed |
| F12 SCAFFOLD-FILE-PERMISSION-ALIGN | `PlannedFile` defaults `0o644`/`0o755` |
| F13 SCAFFOLD-FAILURE-CLEANUP-RECOVERY | `WritePlannedFiles` rollback on failure |
| F14 SCAFFOLD-GENERATOR-PURE-BYTES | `Generator.Scaffold` returns plan; `WritePlannedFiles` is the writer |
| F16 SCAFFOLD-CONFLICT-ERRCODE-SEMANTICS | `ErrConflict` + `WithDetails(path)` |
| F7 SCAFFOLD-AUTOGENERATE-TEST-COVERAGE | `TestRunScaffoldCell_BundleWithAutoGenerate` |
| (new) SCAFFOLD-WRITE-FUNNEL-01 | Hard archtest |

Carryover: `SCAFFOLD-INLINE-TEMPLATE-ARCHTEST` (F15, independent archtest theme) and `ASSEMBLY-RUN-RUNTIME-SMOKE` (F17, stub-content theme) remain backlog follow-ups.

## Round-5 amendment (2026-05-10): funnel completeness + leaf-symlink + YAML safety

PR #442 round-4 funnel claimed coverage was scaffold *skeleton* writes only — auto-generate paths (`tools/codegen/contractgen` derived `.gen.go` writes via `tools/codegen/writer.go Write`, `tools/codegen/cellgen` derived `cell_gen.go`) still ran bare `os.MkdirAll` + `os.WriteFile`. Round-5 closes that gap and addresses three additional safety findings.

### What round-4 ADR overstated

Round-4 §"Round-4 amendment" claimed:

> All filesystem writes funnel through `pkg/pathsafe.WritePlannedFiles`.

The accurate statement at the time was: **scaffold skeleton writes** funnel through pathsafe; **derived codegen writes** still went through `codegen.Write` which used direct `os.MkdirAll` / `os.WriteFile`. Round-5 makes the round-4 sentence true by routing `codegen.Write` through `pathsafe.WriteFileForce`, expanding archtest scope, and forbidding bare `os.*` writes across the entire codegen subtree.

### Closed by round-5

| Round-5 finding | Outcome |
|-----------------|---------|
| P1-1 leaf symlink (dangling + race) | `conflictPass` uses `os.Lstat`; `writePass` uses `OpenFile O_WRONLY\|O_CREATE\|O_EXCL\|O_NOFOLLOW` (Unix). Windows fallback documented in `pkg/pathsafe/nofollow_windows.go`. |
| P1-2 funnel scope | `tools/codegen/writer.go` Write delegates to new `pathsafe.WriteFileForce` (overwrite-allowed variant for codegen artifacts: explicit `os.Remove` + `writeFileNoFollow`). Archtest `SCAFFOLD-WRITE-FUNNEL-01` extended to span `tools/codegen/contractgen/...`, `tools/codegen/cellgen/generate_*.go`, and `tools/codegen/writer.go`. |
| P1-2 auto-generate scope | `contractgen.Options.Scope` sealed interface (`ScopeAll{}` / `ScopeContracts(ids)` / `ScopeCell(id)`). Zero-value `Options.Scope == nil` rejected at `Generate` entry; scaffold cell uses `ScopeCell(cellID)` so auto-generate only processes the new cell's contracts (no whole-project scan). `OnlyContract` field deleted (no back-compat shim). |
| P1-3 YAML safety | `pkg/yamlsafe.Scalar` typed marker + `Quote(raw) Scalar` single funnel for user input flowing into scaffold templates. All inline + scaffold-* template data structs use `yamlsafe.Scalar` for user-input fields so raw `string` to template flow is visible in types. Journey template emits `lifecycle: experimental` (schema-required default). |
| P1-4 verify scripts | `hack/verify-scaffold-bundle.sh` + `hack/verify-scaffold-assembly.sh` default `MODE=sandbox` (was `local`). `make verify` / CI no longer mutate the working tree. `--local` opt-in remains for dev fast-path. |
| P1-5 test fixes | dry-run conflict assertion uses nodash id + `errors.As(err, &ec)` + `errcode.ErrConflict` structured check. `pathsafe` rollback test rewritten so the failure happens during `writePass` (mkdir failure under read-only parent), not during `conflictPass`. |

### AI-Hard tightenings (T1-T4)

- **T1 (kebab reject in exported API)**: `cellgen.ScaffoldCellBundle` rejects kebab `CellID` at `validateScaffoldSpec` (matches the CLI's `scaffoldCell` rejection). Silent `strings.ReplaceAll("-", "")` removed across cellgen.
- **T2 (archtest scope expansion)**: see P1-2 above. Allowlist remains `pkg/pathsafe/pathsafe.go` (and `nofollow_*.go`) only.
- **T3 (Scope sealed interface)**: `contractgen.Scope` interface has unexported `contractScope()` marker method; only `ScopeAll{}`, `ScopeContracts(ids)`, `ScopeCell(id)` implement it. New caller cannot construct an unintended scope by accident — must pick one of the three explicit sentinels.
- **T4 (typed safeYAMLScalar)**: `yamlsafe.Scalar` is a string-based newtype. Template `data` structs accept `Scalar`, not `string`; raw user input must transit `Quote` to compile. Reverting back to plain `string` requires changing every template data type — visible in diff review.

### AI-rebust evaluation

| Defense | Rating | Rationale |
|---------|--------|-----------|
| `SCAFFOLD-WRITE-FUNNEL-01` (round-4 + round-5) | Hard | Real-source AST scan via `scanner.EachFile` with concrete-package allowlist + `pkg/pathsafe` is the only allowed implementer. Documented exemption for `cmd/gocell/app/generate_catalog.go` + `cmd/gocell/app/export.go writeOut` (user `--out` paths). |
| `pathsafe.WritePlannedFiles` + `WriteFileForce` (typed function) | Hard | Single typed entry; bypass requires both archtest allowlist edit AND a new `os.*` call. |
| `O_NOFOLLOW \| O_EXCL` write | Hard (Unix) | Syscall-level guarantee; race-window symlink swap fails open. Documented Windows fallback. |
| `contractgen.Options.Scope` sealed interface | Hard | Zero-value rejected at entry; sealed interface with unexported marker method. |
| `yamlsafe.Scalar` typed newtype | Hard | Type system rejects raw string in template data; reverting requires diff-visible type change. |
| `cellgen.ScaffoldCellBundle` reject kebab | Hard | `validateScaffoldSpec` is the funnel; export API and CLI now share the same rejection rule. |

### Carryover (independent themes)

- `SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01` (round-5 R6): cross-package typed `ScaffoldID` value type + shared validator covering `cellgen.ScaffoldSpec` + `assembly.AssemblyScaffoldSpec` + `cmd/gocell/app` flag wiring. Cx3-4 refactor; minimal slice of `T1` already landed (`ScaffoldCellBundle` reject kebab).
- `SCAFFOLD-INLINE-TEMPLATE-ARCHTEST` (F15) and `ASSEMBLY-RUN-RUNTIME-SMOKE` (F17) remain from round-4.

## Round-6 amendment (2026-05-11): cross-stage plan merge — render/execute split

PR #442 round-6 introduces a kernel render-only API and collapses the previous
two-stage assembly scaffold pipeline. This amendment updates the previously
stated "six scaffold writers" list and clarifies the funnel inventory.

### Symbol rename

Round-4 amendment §"Round-4 amendment" listed six scaffold writers:
`ScaffoldCell` / `ScaffoldCellBundle` / **`Generator.Scaffold`** / `scaffoldSlice` /
`scaffoldContract` / `scaffoldJourney`. Round-6 deletes `Generator.Scaffold` and
introduces `Generator.PlanAssemblyScaffold(spec) ([]pathsafe.PlannedFile, error)`
as a pure-render function. The CLI then drives a single `pathsafe.WritePlannedFiles`
call, so the **execute** side of the funnel is now owned by CLI, not by kernel.

Resulting funnel inventory after round-6:

- **5 scaffold-write entry points** to `pathsafe.WritePlannedFiles`:
  `ScaffoldCell` / `ScaffoldCellBundle` / `scaffoldSlice` / `scaffoldContract` /
  `scaffoldJourney`. Plus the `cmd/gocell/app/scaffold_assembly.go` CLI call
  site (which feeds `PlanAssemblyScaffold`'s output into the funnel).
- `Generator.Scaffold` (executor) is gone; its responsibility (rendering 3
  skeleton files) is folded into `PlanAssemblyScaffold` together with the
  former K#10 auto-generate stage (3 derived files), yielding a single 6-file
  plan per `gocell scaffold assembly` invocation.

### AI-Hard archtest scope (unchanged)

`SCAFFOLD-WRITE-FUNNEL-01` still rejects bare `os.MkdirAll` / `os.WriteFile`
inside `tools/codegen/cellgen/...`, `kernel/assembly/...`, and
`cmd/gocell/app/scaffold*.go`. Round-6 strictly reduces the surface area —
removing the second-stage `tools/codegen.Write` call in
`autoGenerateAssemblyArtifacts` collapses one independent write path into the
existing pathsafe funnel — so the archtest's allowlist remains correct.

`tools/codegen.Write` is itself a pathsafe consumer (calls
`pathsafe.WriteFileForce` for generated-file overwrite), so any claim that
"codegen.Write bypasses pathsafe" is incorrect. The two paths
(`WritePlannedFiles` for first-time creation, `WriteFileForce` for regenerate)
are both inside the pathsafe funnel; the round-6 round trip removes the
mixed-mode use in scaffold assembly.

### BUNDLE archtest AI-rebust rating (clarification)

Round-2 reflection in earlier PR description claimed "Zero Medium/Soft" for
the K#09 archtest set. This is imprecise:

- `SCAFFOLD-WRITE-FUNNEL-01` — Hard (typed function call + AST allowlist)
- `SCAFFOLD-CONTRACT-CODEGEN-DEFAULT-TRUE` — Hard (parser AST funnel)
- `SCAFFOLD-BUNDLE-MARKER-01` — **Medium** (real-source AST capture; marker
  is a hand-written string in `text/template`, no type-system enforcement
  available; see archtest file-level godoc "Cannot be Hard")
- `SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01` — **Medium** (same rationale)

Hard upgrade for the BUNDLE pair is tracked under
`SCAFFOLD-BUNDLE-ARCHTEST-HARDEN` (backlog). The two Medium archtests are
acceptable per `ai-collab.md` §"立项硬门槛: ≥ Medium"; Hard is the goal but
not the current state.

### `synthesizeAssemblyMeta` field-sync risk (new Medium)

`PlanAssemblyScaffold` synthesizes an in-memory `metadata.AssemblyMeta` so the
three `Generate*` methods can run without a re-parse. The synthesis is
field-by-field; future additions to `AssemblyMeta` consumed by `Generate*`
must extend `synthesizeAssemblyMeta` (verified by grep at round-6 time).
Tracked as `ASSEMBLY-META-SYNTHESIS-FIELD-GUARD` for Hard upgrade via reflect
field-count guard.

### Round-6 carryover (independent themes)

- `ASSEMBLY-META-SYNTHESIS-FIELD-GUARD` — see above (Medium → Hard candidate).
- `SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01` — symmetric problem for
  `scaffold cell`. Solution requires extending `pathsafe.PlannedFile` with a
  `ForceOverwrite bool` field so contractgen/cellgen regenerate writes can
  join the single funnel (Cx3-4 independent PR).
- `PATHSAFE-COLLECT-MISSING-DIRS-EACCES-01` — `collectMissingDirs` silently
  breaks on EACCES (treats as "exists"), leaving orphan dirs when rollback
  is triggered. Trigger surface is narrow (mid-tree dir without read
  permission), so deferred to follow-up PR.

## References

- Roadmap: `docs/plans/archive/202605011500-029-master-roadmap.md` #09
- Plan: `/Users/shengming/.claude-ming/plans/docs-plans-202605011500-029-master-road-atomic-emerson.md`
- Round-4/5 plan: `/Users/shengming/.claude-ming/plans/1-p1-scaffold-virtual-waffle.md`
- ref: `kubernetes-sigs/kubernetes` `pkg/volume/util/subpath/subpath_linux.go` (`O_NOFOLLOW` for symlink-safe open)
- ref: `cyphar/filepath-securejoin` (handle-based root containment patterns)
- ref: `kubernetes-sigs/kubebuilder` `pkg/machinery/scaffold.go` (file-model + write strategy)
- ref: `zeromicro/go-zero` `tools/goctl/api/gogen/gen.go` (multi-file scaffold orchestrator pattern)
- ref: `kubernetes-sigs/kubebuilder` `pkg/plugins/golang/v4/scaffolds/api.go` (scaffold flag conventions, path validation)
- ref: `helm/helm` `pkg/chartutil/create.go` (0o644/0o755 file/dir mode default)
- ref: `cmd/corebundle/run.go` (canonical three-layer composition root mirrored by the run.go template)
