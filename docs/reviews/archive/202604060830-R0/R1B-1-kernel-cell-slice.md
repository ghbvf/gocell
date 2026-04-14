# R1B-1: kernel/cell + kernel/slice Review Report

- **Reviewer**: Kernel Guardian
- **Date**: 2026-04-06
- **Scope**: `kernel/cell/` (~850 LOC, 7 files) + `kernel/slice/` (~225 LOC, 3 files)
- **Test Coverage**: kernel/cell 99.2%, kernel/slice 94.2%
- **All tests pass**: Yes (including `-race` for concurrent test)

---

## Findings

| ID | Severity | File:Line | Description | Suggested Fix |
|----|----------|-----------|-------------|---------------|
| R1B1-01 | Medium | base.go:165-171 | `AddSlice`, `AddProducedContract`, `AddConsumedContract` mutate shared slice fields without holding the mutex. While these are typically called during setup (before Start), the Cell interface and doc comment promise "all state-accessing methods are protected by a mutex" (line 37-38). A concurrent call to `OwnedSlices()` while `AddSlice` is appending could race. | Either (a) add `b.mu.Lock()/Unlock()` to Add* methods and read-accessors for slices/contracts, or (b) document that Add* must only be called before Init/Start and remove the "all methods protected" claim. Option (a) is safer and consistent with the doc. |
| R1B1-02 | Medium | base.go:64-82 | `OwnedSlices()`, `ProducedContracts()`, `ConsumedContracts()` read mutable slice fields (`b.slices`, `b.produced`, `b.consumed`) without holding the mutex. Combined with R1B1-01, this is a potential data race. | Wrap each method body with `b.mu.Lock()/defer b.mu.Unlock()`. |
| R1B1-03 | Low | base.go:85-93 | `Init` does not reset `shutdownCtx`/`shutdownCancel` to nil. After a Stop->Init cycle, `ShutdownCtx()` returns the stale cancelled context from the previous lifecycle. Any code calling `ShutdownCtx()` between re-Init and re-Start sees a cancelled context. | Add `b.shutdownCtx = nil; b.shutdownCancel = nil` inside Init. |
| R1B1-04 | Low | base.go:39 | `sync.Mutex` is used for all state access, but `Health()`, `Ready()`, and `ShutdownCtx()` are pure read operations on `b.state`/`b.shutdownCtx`. Using `sync.RWMutex` would allow concurrent reads without blocking. | Change `mu sync.Mutex` to `mu sync.RWMutex`; use `RLock/RUnlock` for read-only methods (Health, Ready, ShutdownCtx, OwnedSlices, ProducedContracts, ConsumedContracts). |
| R1B1-05 | Info | types.go:62-65 | `HealthStatus.Status` is a plain `string` with values documented only in a comment (`"healthy" | "degraded" | "unhealthy"`). No constants are defined, so consumers can introduce typos. | Define string constants `HealthStatusHealthy`, `HealthStatusDegraded`, `HealthStatusUnhealthy` and use them in BaseCell.Health(). |
| R1B1-06 | Info | interfaces.go:79-87 | The `Slice` interface does not expose `ContractUsages()`. This is by design (contractUsages live in the metadata/governance layer), but it means the runtime Slice type cannot be queried for its contract participations. This is acceptable for the current architecture but worth noting for future needs. | No change required. Document the design rationale. |
| R1B1-07 | Info | verify.go:51-79 | `VerifySlice` does not use the verify spec declared in slice.yaml (`Verify.Unit`, `Verify.Contract`). It always runs `go test ./cells/{cellID}/slices/{sliceID}/... -v`, regardless of what the slice.yaml declares. The declared verify paths are ignored. | Consider reading `SliceMeta.Verify.Unit` and `SliceMeta.Verify.Contract` to build the test command arguments, or document that the slice.yaml verify fields are advisory-only. |
| R1B1-08 | Info | (cross-module) | Governance rules do not validate `cell.owner.team`, `cell.owner.role`, or `cell.verify.smoke` as required fields for cell.yaml. The CLAUDE.md mandates these as required (`owner{team,role}` and `verify.smoke`). This gap is in `kernel/governance/rules_fmt.go`, not in kernel/cell itself. | Add a FMT rule that checks: `c.Owner.Team != ""`, `c.Owner.Role != ""`, `len(c.Verify.Smoke) > 0` for all cells. |

---

## Known Finding Verification

### PR#17: BaseCell mutex + LIFO shutdown fix

- **Mutex protection**: Verified. `Init`, `Start`, `Stop`, `Health`, `Ready`, `ShutdownCtx` all acquire `b.mu.Lock()` before accessing `b.state`. The fix from PR#17 is present and working.
  - **Caveat**: The Add* methods and collection accessors (OwnedSlices, ProducedContracts, ConsumedContracts) do NOT hold the mutex (see R1B1-01, R1B1-02). The PR#17 fix may have been scoped to lifecycle state only.
- **LIFO shutdown**: Not implemented at the BaseCell level (BaseCell.Stop only cancels shutdownCtx). LIFO shutdown is correctly implemented at the assembly level (`kernel/assembly/assembly.go:150` -- `for i := len(a.cells) - 1; i >= 0; i--`). The design is appropriate: individual cells do not manage sub-cell ordering; the assembly orchestrator does.
- **Race detector**: `go test -race` passes for `TestBaseCellConcurrentHealthReady`. However, this test only exercises Health/Ready concurrently, not Add*/OwnedSlices concurrently.

### PR#3: Lifecycle concerns

- State machine is complete and correct: `new -> initialized -> started -> stopped -> (re-init)`.
- Invalid transitions are rejected with `errcode.ErrLifecycleInvalid`.
- Stop from `new` or `initialized` is a safe no-op.
- Double-init from initialized state is correctly rejected.
- Restart (stop -> init -> start) works correctly (tested in `TestBaseCellRestart`).

---

## Naming Convention Check

| Check | Status | Evidence |
|-------|--------|----------|
| Go abbreviation: `ID` not `Id` | PASS | All identifiers use `ID` consistently: `CellMetadata.ID`, `BaseCell.ID()`, `BaseSlice.ID()`, `BaseContract.ID()` |
| YAML field casing (camelCase) | PASS | `metadata/types.go` uses `yaml:"id"`, `yaml:"belongsToCell"`, `yaml:"consistencyLevel"`, `yaml:"contractUsages"` -- all camelCase |
| Cell/Slice IDs in test data | PASS | Test data uses kebab-case: `"auth-core"`, `"login-slice"`, `"access-core"`, `"session-create"` |
| No banned field names in YAML tags | PASS | Grep for `yaml:"ownedSlices"`, `yaml:"cellId"`, etc. returns zero matches |
| `OwnedSlices()` Go method name | PASS | This is a Go interface method, not a YAML field name. The CLAUDE.md ban applies to YAML field names. The Go method is an appropriate runtime API name. |
| Package naming | PASS | `package cell`, `package slice` -- both lowercase, no underscores |

---

## Error Handling Check

| Check | Status | Evidence |
|-------|--------|----------|
| No bare `errors.New` in production code | PASS | Grep for `errors.New` in `kernel/cell/*.go` and `kernel/slice/*.go` (excluding tests) returns 0 matches |
| Uses `pkg/errcode` consistently | PASS | `base.go` imports and uses `errcode.New()` for lifecycle errors; `types.go` uses `errcode.New()` for parse errors; `verify.go` uses `errcode.New()` and `errcode.Wrap()` |
| Error context wrapping | PASS | All errors include context: cell ID, state, or description (e.g., `fmt.Sprintf("cell %q: Init requires state new or stopped, current state: %d", ...)`) |
| `verify.go` wraps exec errors | PASS | `errcode.Wrap(errcode.ErrTestExecution, "go test execution failed", runErr)` at line 216 |

---

## Dependency Isolation Check

| Package | Allowed Dependencies | Actual Dependencies | Status |
|---------|---------------------|---------------------|--------|
| `kernel/cell` | std lib + `pkg/` + `kernel/` (no runtime, adapters, cells) | `context`, `fmt`, `sync`, `net/http` (std); `pkg/errcode`; `kernel/outbox` | PASS |
| `kernel/slice` | std lib + `pkg/` + `kernel/` (no runtime, adapters, cells) | `bytes`, `context`, `errors`, `fmt`, `os/exec`, `path/filepath`, `strings` (std); `pkg/errcode`; `kernel/metadata` | PASS |
| `kernel/outbox` (transitive) | std lib only | `context`, `time` (std only) | PASS |

Note: `kernel/cell/registrar.go` imports `net/http` (std lib) for `http.Handler` and `kernel/outbox` for `outbox.Subscriber`. Both are acceptable under the layering rules. The `net/http` import is minimal (only the `Handler` interface type) and keeps the kernel router-agnostic.

---

## Risk Assessment

### Overall: LOW-MEDIUM

The kernel/cell and kernel/slice packages are well-structured with high test coverage (99.2% and 94.2% respectively). The primary risks are:

1. **Data race potential (R1B1-01, R1B1-02)**: The Add* and collection accessor methods lack mutex protection. While current usage patterns (Add during setup, read after Start) are safe, the documented contract ("all state-accessing methods are protected by a mutex") is not fully honored. This is the most actionable finding and should be fixed to prevent future regressions.

2. **Stale shutdown context (R1B1-03)**: Minor edge case in re-initialization. Low likelihood of causing real issues since the typical lifecycle is single-use.

3. **Missing governance rules (R1B1-08)**: The `owner.team`, `owner.role`, and `verify.smoke` required-field checks are absent from the governance validator. This means invalid cell.yaml files could pass validation. This is a cross-module concern (governance, not cell) but impacts metadata compliance.

### Strengths

- Clean separation between runtime interface (kernel/cell) and metadata validation (kernel/governance).
- Compile-time interface compliance checks (`var _ Cell = (*BaseCell)(nil)`).
- Defensive copies in OwnedSlices/ProducedContracts/ConsumedContracts (tested).
- Comprehensive table-driven tests for all Parse* functions.
- Path traversal protection in `parseSliceKey`.
- Proper use of `errcode` package throughout -- no bare `errors.New`.
- `shutdownCtx` pattern is clean: goroutines get a context cancelled on Stop.
- Assembly-level LIFO shutdown is correctly implemented.
- No forbidden upward dependencies (verified via `go list -deps`).
