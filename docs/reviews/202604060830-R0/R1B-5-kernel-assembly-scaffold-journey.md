# R1B-5: kernel/assembly, kernel/scaffold, kernel/journey Review

**Reviewer**: R1B-5 Agent (Seats 1+3+5 composite)
**Review Basis Commit**: ce03ba1 (develop HEAD)
**Date**: 2026-04-06
**Scope**: `src/kernel/assembly/`, `src/kernel/scaffold/`, `src/kernel/journey/`

---

## Summary

| Module | LOC (src) | LOC (test) | Files | Findings |
|--------|-----------|------------|-------|----------|
| kernel/assembly | ~240 (assembly.go) + ~254 (generator.go) + 5 (gentpl/embed.go) + 4 (doc.go) | ~367 (assembly_test.go) + ~436 (generator_test.go) | 6 + 2 .tpl | 7 |
| kernel/scaffold | ~229 (scaffold.go) + 14 (templates.go) + 3 (doc.go) | ~597 (scaffold_test.go) | 3 + 7 .tpl | 3 |
| kernel/journey | ~114 (catalog.go) + 3 (doc.go) | ~396 (catalog_test.go) | 3 | 2 |

**Overall quality**: Solid. Well-tested, correct lifecycle semantics, clean dependency graph. Findings are primarily P1/P2 with one P0.

---

## Findings

### F-1: Health() reads a.cells without holding the mutex -- data race

| Field | Value |
|-------|-------|
| Seat | S1 (Architecture) / S3 (Test) |
| Severity | **P0** |
| Category | Concurrency Safety |
| File | `src/kernel/assembly/assembly.go:166-172` |
| Status | OPEN |

**Evidence**:

```go
// assembly.go lines 166-172
func (a *CoreAssembly) Health() map[string]cell.HealthStatus {
    result := make(map[string]cell.HealthStatus, len(a.cells))
    for _, c := range a.cells {
        result[c.ID()] = c.Health()
    }
    return result
}
```

`Health()` iterates over `a.cells` without acquiring `a.mu`. While `Register()` (which appends to `a.cells`) is guarded by the mutex, a concurrent call to `Health()` while another goroutine is calling `Register()` creates a data race on the slice header. Even after Start completes, the slice is shared state. Compare with `CellIDs()` (line 225) which correctly acquires the lock.

**Fix**: Acquire `a.mu.Lock()` / `defer a.mu.Unlock()` at the start of `Health()`, or take a snapshot of the cells slice under lock and iterate the snapshot outside the lock.

---

### F-2: Start() and StartWithConfig() -- near-identical ~40 LOC duplicated

| Field | Value |
|-------|-------|
| Seat | S5 (DX/Maintainability) |
| Severity | **P1** |
| Category | Code Duplication / Cognitive Complexity |
| File | `src/kernel/assembly/assembly.go:83-132` and `assembly.go:176-222` |
| Status | OPEN |

**Evidence**: `Start(ctx)` (lines 83-132) and `StartWithConfig(ctx, cfgMap)` (lines 176-222) share identical logic for state-check, init loop, start loop with rollback, and state transition. The only difference is how `deps.Config` is populated -- `make(map[string]any)` vs the provided `cfgMap`.

**Fix**: Extract the shared lifecycle orchestration into a private method such as `startInternal(ctx, cfgMap)` and have both `Start` and `StartWithConfig` delegate to it. This reduces maintenance risk for the rollback logic.

---

### F-3: Stop() returns only the first error, silently swallowing subsequent errors

| Field | Value |
|-------|-------|
| Seat | S1 (Architecture) |
| Severity | **P2** |
| Category | Error Handling |
| File | `src/kernel/assembly/assembly.go:140-163` |
| Status | OPEN |

**Evidence**:

```go
// assembly.go lines 148-157
var firstErr error
for i := len(a.cells) - 1; i >= 0; i-- {
    if err := a.cells[i].Stop(ctx); err != nil {
        if firstErr == nil {
            firstErr = errcode.Wrap(...)
        }
    }
}
```

Subsequent stop errors are dropped entirely -- not logged, not joined. The fx reference pattern also does "best-effort" stop, but fx uses `multierr.Append` to collect all errors. In a debugging scenario, knowing which cells failed to stop is critical.

**Fix**: Use `errors.Join` (Go 1.20+) or at minimum `slog.Warn` for errors after the first, similar to the rollback path (line 116-118) which correctly logs failures.

---

### F-4: Scaffold generates templates.go doc.go with conflicting package-level doc comments

| Field | Value |
|-------|-------|
| Seat | S5 (DX/Maintainability) |
| Severity | **P2** |
| Category | Documentation / godoc |
| File | `src/kernel/scaffold/doc.go:1-2` and `src/kernel/scaffold/templates.go:1-9` |
| Status | OPEN |

**Evidence**:

`doc.go` lines 1-2:
```go
// Package scaffold generates boilerplate files (cell.yaml, slice.yaml,
// Go source stubs) for new Cells and Slices using embedded templates.
package scaffold
```

`templates.go` lines 1-9:
```go
// Package scaffold generates directory structures and YAML metadata files
// for GoCell cells, slices, contracts, and journeys.
//
// Design decisions (ref: go-zero goctl):
//   - embed templates: .tpl files are embedded via //go:embed
//   - skip-on-conflict: existing files are never overwritten
//   - genFile abstraction: unified template render + write function
//   - strong-typed context: struct opts replace map[string]any (divergence from goctl)
package scaffold
```

Both files declare package-level doc comments. Go tooling picks one arbitrarily (usually `doc.go`). The richer design-decision comment in `templates.go` would be lost. The `doc.go` description is also stale -- it says "Go source stubs" but the package does not generate Go source files; it generates YAML metadata files.

**Fix**: Keep the detailed comment only in `doc.go` and remove the package comment from `templates.go`. Update `doc.go` to mention contracts and journeys in addition to cells and slices.

---

### F-5: Journey Catalog does not validate that referenced contracts/cells exist

| Field | Value |
|-------|-------|
| Seat | S1 (Architecture) / S3 (Test) |
| Severity | **P1** |
| Category | Missing Validation |
| File | `src/kernel/journey/catalog.go:18-35` |
| Status | OPEN |

**Evidence**: `NewCatalog` accepts a `*metadata.ProjectMeta` and indexes journeys and status-board entries, but never validates:
- Whether `JourneyMeta.Cells` entries exist in `project.Cells`
- Whether `JourneyMeta.Contracts` entries exist in `project.Contracts`
- Whether `StatusBoardEntry.JourneyID` references an existing journey

The Catalog is purely a query layer. However, this means broken references silently propagate. The `CellJourneys(cellID)` and `ContractJourneys(contractID)` methods will include journeys that reference nonexistent cells/contracts without any warning.

**Fix**: Add a `Validate() []error` method (or validation in the constructor that logs warnings) that cross-references journey cells/contracts against the project metadata. This aligns with the `ErrReferenceBroken` error code already defined in `pkg/errcode`.

---

### F-6: Assembly generator boundary.yaml.tpl uses `assemblyId` (camelCase in YAML) instead of `assembly_id` (snake_case)

| Field | Value |
|-------|-------|
| Seat | S5 (DX/Maintainability) |
| Severity | **P2** |
| Category | Naming Convention |
| File | `src/kernel/assembly/gentpl/boundary.yaml.tpl:4` |
| Status | OPEN |

**Evidence**:

```yaml
# boundary.yaml.tpl line 4
assemblyId: {{.AssemblyID}}
```

The project convention states: DB fields `snake_case`, JSON/Query/Path `camelCase`. YAML metadata files (cell.yaml, slice.yaml, contract.yaml) consistently use `camelCase` keys (`ownerCell`, `consistencyLevel`, `belongsToCell`, `contractUsages`). However, `sourceFingerprint` (line 3) is also camelCase, so `assemblyId` is consistent within this file. BUT the metadata struct uses `yaml:"id"` for the assembly ID field, and `boundary.yaml` is a generated artifact, not a metadata spec file. The inconsistency with the existing `AssemblyMeta.ID` field tag `yaml:"id"` is minor.

**Note**: This is informational. The existing YAML files already use camelCase consistently, so `assemblyId` in boundary.yaml is acceptable. Downgraded from an initial P1 to P2 informational.

---

### F-7: Scaffold cell.yaml template uses hardcoded `l0Dependencies: []` -- missing from CLAUDE.md required fields

| Field | Value |
|-------|-------|
| Seat | S1 (Architecture) |
| Severity | **P2** |
| Category | Template / Metadata Model Alignment |
| File | `src/kernel/scaffold/templates/cell.yaml.tpl:12` |
| Status | OPEN |

**Evidence**:

```yaml
# cell.yaml.tpl line 12
l0Dependencies: []
```

The cell.yaml template includes `l0Dependencies: []`, which matches the metadata model (`CellMeta.L0Dependencies`). This is correct and produces valid metadata. However, per CLAUDE.md the cell.yaml required fields are: `id / type / consistencyLevel / owner / schema.primary / verify.smoke`. The template satisfies all of these. The `l0Dependencies` field is optional and its inclusion is acceptable.

Additionally, the template does not include a `lifecycle` field. Per CLAUDE.md, `lifecycle` (draft/active/deprecated) is a governance field allowed in contract.yaml. Cell.yaml does not appear to require it, so this is fine.

**Note**: Informational only. No action required.

---

### F-8: assembly doc.go conflicts with assembly.go package comment

| Field | Value |
|-------|-------|
| Seat | S5 (DX/Maintainability) |
| Severity | **P2** |
| Category | Documentation / godoc |
| File | `src/kernel/assembly/doc.go:1-3` and `src/kernel/assembly/assembly.go:1-9` |
| Status | OPEN |

**Evidence**:

`doc.go`:
```go
// Package assembly provides the CoreAssembly that orchestrates Cell lifecycle
// (register, init, start, stop, health). It resolves Cell dependency order and
// manages graceful shutdown sequencing.
package assembly
```

`assembly.go`:
```go
// Package assembly provides the CoreAssembly that orchestrates Cell
// lifecycle (register, start, stop, health).
//
// Design ref: uber-go/fx app.go, lifecycle.go
//   - FIFO Start / LIFO Stop
//   - Start 失败自动 rollback 已启动的 Cell
//   - Stop 尽力而为，合并错误
//   - 状态机防止重入
package assembly
```

Dual package comments. `doc.go` mentions "resolves Cell dependency order" but the current implementation does NOT resolve dependency order -- it starts cells in registration order. This is misleading.

**Fix**: Keep the comment with design-ref in `assembly.go`, remove or reduce `doc.go` to a bare `package assembly`, and correct the misleading "resolves Cell dependency order" claim.

---

### F-9: scaffold renderToFile has a TOCTOU race between Stat and WriteFile

| Field | Value |
|-------|-------|
| Seat | S1 (Architecture) |
| Severity | **P2** |
| Category | Correctness |
| File | `src/kernel/scaffold/scaffold.go:190-225` |
| Status | OPEN |

**Evidence**:

```go
// scaffold.go lines 190-193
if _, err := os.Stat(outPath); err == nil {
    return errcode.New(ErrScaffoldConflict, ...)
}
// ... (lines 196-225: template render, MkdirAll, WriteFile)
```

There is a time-of-check-to-time-of-use (TOCTOU) gap: another process could create the file between the `os.Stat` check and the `os.WriteFile` call. The `os.WriteFile` call uses `0o644` which will silently overwrite.

**Fix**: Use `os.OpenFile(outPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)` which atomically checks for existence and creates the file. This eliminates the race.

---

### F-10: Journey Catalog -- no concurrency protection

| Field | Value |
|-------|-------|
| Seat | S3 (Test) |
| Severity | **P2** |
| Category | Concurrency Safety |
| File | `src/kernel/journey/catalog.go` (entire file) |
| Status | OPEN |

**Evidence**: The `Catalog` struct has no mutex and its maps (`journeys`, `statusBoard`) are only written during construction via `NewCatalog`. After construction, all methods are read-only. This is safe IF the Catalog is never modified after creation, which is the current design (no mutator methods exist). However, the struct fields are exported indirectly via returned `*metadata.JourneyMeta` pointers -- callers could mutate the data.

**Note**: This is a P2 because the current API is read-only and the risk is theoretical. If mutation methods are added in the future, a mutex would be needed.

---

### F-11: assembly doc.go claims "resolves Cell dependency order" -- not implemented

| Field | Value |
|-------|-------|
| Seat | S1 (Architecture) |
| Severity | **P1** |
| Category | Documentation Accuracy |
| File | `src/kernel/assembly/doc.go:2` |
| Status | OPEN |

**Evidence**:

```go
// Package assembly provides the CoreAssembly that orchestrates Cell lifecycle
// (register, init, start, stop, health). It resolves Cell dependency order and
// manages graceful shutdown sequencing.
```

There is no dependency resolution in `CoreAssembly`. The `Start` method (line 100-126) iterates `a.cells` in registration order. The `Dependencies` struct (line 93-97) provides a map of all cells but no topological sorting occurs. The doc claim "resolves Cell dependency order" is factually incorrect.

**Fix**: Either implement dependency resolution (topological sort based on `L0Dependencies` or contract graph), or update the doc.go to accurately state "starts cells in registration order".

---

### F-12: Scaffold journey template produces empty `contracts: []` -- user must manually edit

| Field | Value |
|-------|-------|
| Seat | S5 (DX/Maintainability) |
| Severity | **P2** |
| Category | Template Completeness |
| File | `src/kernel/scaffold/templates/journey.yaml.tpl:9-10` |
| Status | OPEN |

**Evidence**:

```yaml
contracts: []
passCriteria: []
```

The `JourneyOpts` struct has `Cells` but no `Contracts` or `PassCriteria` fields. The template always emits `contracts: []` and `passCriteria: []`. This means every scaffolded journey requires manual editing for these critical fields. While the scaffold is meant to produce a starting point, contracts are a mandatory conceptual part of a journey.

**Fix**: Add `Contracts []string` and optionally `PassCriteria []string` to `JourneyOpts`. If empty, default to `[]` as today.

---

## Dependency Compliance Check (All Seats)

| Check | Result |
|-------|--------|
| kernel/assembly imports runtime/adapters/cells? | PASS -- only imports kernel/cell, kernel/metadata, kernel/registry, pkg/errcode, stdlib |
| kernel/scaffold imports runtime/adapters/cells? | PASS -- only imports pkg/errcode, stdlib |
| kernel/journey imports runtime/adapters/cells? | PASS -- only imports kernel/metadata, stdlib |
| Cross-Cell import via internal/? | N/A -- these are kernel packages, not Cell implementations |
| New CUD ops annotated with consistency level? | N/A -- no CUD operations in these packages |
| ref: tag present in commits touching kernel/? | PASS -- assembly.go, generator.go, templates.go contain ref: comments |

---

## Test Coverage Assessment (Seat 3)

### kernel/assembly

| Area | Covered | Notes |
|------|---------|-------|
| Happy path Start/Stop | Yes | `TestAssemblyStartStopHealthy` |
| LIFO stop order | Yes | `TestAssemblyStopReverseOrder` |
| Init failure | Yes | `TestAssemblyInitFailure` |
| Start failure + rollback | Yes | `TestAssemblyStartFailureRollback` |
| Stop continues on error | Yes | `TestAssemblyStopContinuesOnError` |
| Double start prevention | Yes | `TestAssemblyDoubleStartPrevented` |
| Register after start | Yes | `TestAssemblyRegisterAfterStartRejected` |
| Duplicate cell ID | Yes | `TestAssemblyDuplicateCellID` |
| Empty cell ID | Yes | `TestAssemblyEmptyCellID` |
| StartWithConfig | Yes | 4 tests |
| CellIDs, Cell lookup | Yes | 2 tests |
| Generator entrypoint | Yes | 5 tests |
| Generator boundary | Yes | 8 tests |
| Fingerprint determinism | Yes | 2 tests |
| **Concurrent access** | **NO** | No test for concurrent Register/Start/Health |

**Estimated coverage**: ~85-90% line coverage (good). Missing: concurrent access tests.

### kernel/scaffold

| Area | Covered | Notes |
|------|---------|-------|
| CreateCell happy path + defaults | Yes | 3 tests |
| CreateCell conflict/validation | Yes | 4 tests |
| CreateSlice happy/sad paths | Yes | 5 tests |
| CreateContract all 4 kinds | Yes | 4 tests |
| CreateContract validation | Yes | 6 tests |
| CreateContract ID parsing | Yes | 5 table cases |
| CreateJourney happy/sad paths | Yes | 5 tests |
| Integration flow | Yes | 1 comprehensive test |
| Template embedding check | Yes | 1 test |

**Estimated coverage**: ~90%+ line coverage (excellent).

### kernel/journey

| Area | Covered | Notes |
|------|---------|-------|
| NewCatalog nil/zero/populated | Yes | 3 table cases |
| Get existing/missing/empty | Yes | 3 table cases |
| List sorted/empty | Yes | 2 table cases |
| CellJourneys | Yes | 4 table cases |
| ContractJourneys | Yes | 4 table cases |
| Status found/missing | Yes | 4 table cases |
| CrossCellJourneys | Yes | 3 table cases |
| Count | Yes | 3 table cases |
| NoPanic edge cases | Yes | 4 nil/zero variants |

**Estimated coverage**: ~95%+ line coverage (excellent).

---

## Findings Summary

| ID | Severity | Category | File | Description |
|----|----------|----------|------|-------------|
| F-1 | **P0** | Concurrency | assembly.go:166-172 | Health() reads a.cells without mutex |
| F-2 | P1 | Duplication | assembly.go:83-222 | Start/StartWithConfig 40-line duplication |
| F-3 | P2 | Error Handling | assembly.go:148-157 | Stop() swallows errors after the first |
| F-4 | P2 | godoc | scaffold/doc.go + templates.go | Conflicting package doc comments |
| F-5 | P1 | Validation | journey/catalog.go:18-35 | No cross-reference validation for cells/contracts |
| F-8 | P2 | godoc | assembly/doc.go + assembly.go | Conflicting package doc comments |
| F-9 | P2 | Correctness | scaffold.go:190-193 | TOCTOU race in renderToFile |
| F-10 | P2 | Concurrency | journey/catalog.go | Returned pointers allow mutation (informational) |
| F-11 | P1 | Documentation | assembly/doc.go:2 | Claims "resolves dependency order" -- not implemented |
| F-12 | P2 | DX | journey.yaml.tpl:9-10 | No way to scaffold contracts/passCriteria |

**Totals**: P0=1, P1=3, P2=6

---

## Verdict

**BLOCK on P0** (F-1). The `Health()` data race must be fixed before merge. P1 items (F-2, F-5, F-11) should be addressed in the same or immediate follow-up PR.
