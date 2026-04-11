# Wave C Architecture Design -- Product Review

> Reviewer: Product Manager
> Date: 2026-04-11
> Input: `docs/designs/20260411-wave-c-architecture-design.md`
> Verdict: **CONDITIONAL PASS** (2 conditions must be met before merge)

---

## 1. CS-AR-2: Dependencies Interface Minimization

### 1.1 Consumer Impact Assessment

**Severity: LOW (internal breaking change, zero external impact)**

The `Dependencies` struct is part of the `kernel/cell` exported API. Removing `Cells` and `Contracts` fields is a **compile-time breaking change** for any consumer who references these fields by name in struct literals. However:

- **Zero production callers** use `deps.Cells` or `deps.Contracts` (verified by grep across the entire codebase).
- **examples/ are unaffected**: `sso-bff/main.go`, `todo-order/main.go`, `iot-device/main.go` never construct `Dependencies` -- the assembly handles it internally.
- **~30 test file occurrences** across `cells/*/cell_test.go` construct `Dependencies{}` with `Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract)`. These will fail to compile and must be updated.

The `Cell.Init(ctx, deps Dependencies)` signature is unchanged, so any code that only passes `Dependencies` through (e.g., `assembly.go`) or calls `deps.Config` is unaffected.

**[兼容性风险] The design document correctly identifies this as low risk. However, it is still a breaking change to an exported type.** Pre-v1.0 frameworks typically accept such changes, but a CHANGELOG entry is required.

### 1.2 Consumer Impact Detail

| Consumer Type | Impact | Action Required |
|---------------|--------|-----------------|
| Go developer using `go get` + building a custom Cell | Compile error if their `Init()` constructs `Dependencies{Cells: ...}` | Remove the field from struct literal |
| Go developer using `examples/` as starting point | None -- examples don't touch `Dependencies` | None |
| Go developer running `go test ./...` on cells/ | Test compile errors on ~30 lines | Mechanical fix: delete `Cells:` and `Contracts:` lines |
| GoCell CLI (`gocell validate/check/verify`) | None -- CLI does not construct `Dependencies` | None |

### 1.3 Suggestions

1. **[验收标准缺失]** The design says "~15 test file updates" but grep shows ~30 occurrences across 6 test files. The PR acceptance criteria should list exact files and line counts.

2. **[开发者体验]** The remaining `Dependencies` struct has a single field (`Config map[string]any`). The design correctly notes Option B (ConfigProvider interface) as a future evolution. However, a single-field struct is awkward for Go developers -- they will wonder why this isn't just `map[string]any` directly. **Recommendation**: Add a godoc comment explaining why the struct wrapper exists (extensibility, future fields like `Secrets` or `ServiceLocator`).

3. **[开发者体验]** The `noopWriter` pattern is duplicated 4 times across test files and once in `examples/sso-bff/main.go`. The backlog already tracks `P4-TD-01: shared NoopOutboxWriter`. Consider bundling this cleanup into the same PR since it touches the same files.

### 1.4 Acceptance Criteria

| # | Priority | Criterion | Verification |
|---|----------|-----------|--------------|
| AC-1 | P1 | `Dependencies` struct has only `Config map[string]any` field | Code review |
| AC-2 | P1 | `Cell.Init` signature unchanged: `Init(ctx context.Context, deps Dependencies) error` | Code review |
| AC-3 | P1 | `go build ./...` passes (zero compile errors) | CI |
| AC-4 | P1 | `go test ./...` passes (all test literals updated) | CI |
| AC-5 | P2 | `Dependencies` godoc explains the struct wrapper rationale | Code review |
| AC-6 | P2 | CHANGELOG entry documents the breaking change with migration guidance | Code review |
| AC-7 | P3 | `assembly.go` no longer constructs `Cells` or `Contracts` maps (reduced allocation) | Code review |

---

## 2. CS-AR-3: net/http in kernel/cell

### 2.1 Consumer Impact Assessment

**Severity: NONE (documentation-only change)**

The design recommends accepting `net/http` as a permitted stdlib dependency and adding a comment to `registrar.go`. No code changes. No API changes. No consumer impact.

**The analysis is thorough and the conclusion is sound.** `net/http.Handler` is the Go ecosystem's universal handler interface. Every Go developer expects it. Creating a parallel type system would be a DX regression.

### 2.2 Future Protocol Risk

**[范围偏移] The design mentions gRPC/WebSocket as future concerns.** This is appropriate forward-looking analysis, but the product scope for Wave C does not include protocol abstraction. The design correctly defers this: "a TransportRegistrar generic interface can be added alongside HTTPRegistrar without breaking changes." This is the right answer.

The `celltest.TestMux` proves the interface is implementable with pure stdlib, meaning developers writing unit tests are not forced into a chi/gorilla dependency. This is a strong DX property worth preserving.

### 2.3 Suggestions

1. **[验收标准缺失]** The design says "add architectural decision comment" but does not specify the exact comment text as a requirement. The PR should include the ADR-style comment shown in the design (section 2.4, Option A).

2. **[开发者体验]** Consider adding a one-line note in `kernel/cell/doc.go` (package-level godoc) explaining that `net/http` is the only non-`pkg/` external import and why. This helps developers who browse godoc understand the design rationale without reading internal design docs.

### 2.4 Acceptance Criteria

| # | Priority | Criterion | Verification |
|---|----------|-----------|--------------|
| AC-8 | P1 | `registrar.go` contains architectural decision comment (CS-AR-3) explaining net/http rationale | Code review |
| AC-9 | P2 | No code changes to `registrar.go` beyond comments | Code review (diff must be comment-only) |
| AC-10 | P3 | Package godoc mentions the net/http dependency rationale | Code review |

---

## 3. F-OB-01: Writer Batch Write

### 3.1 Consumer Impact Assessment

**Severity: LOW (additive, backward-compatible)**

The design adds a new `BatchWriter` interface and a `WriteBatchFallback` helper function. The existing `Writer` interface is unchanged. This is a textbook Go interface extension pattern (cf. `io.Reader` vs `io.ReadCloser`).

**Consumer impact by persona:**

| Consumer Type | Impact | Action Required |
|---------------|--------|-----------------|
| Cell developer using `outbox.Writer` | None -- existing code compiles and works | None |
| Cell developer needing batch writes | New capability -- use `WriteBatchFallback(ctx, w, entries)` | Opt-in |
| Adapter author (postgres OutboxWriter) | Should implement `BatchWriter` for optimal performance | Implement `WriteBatch` method |
| Test author with `noopWriter` | None -- `noopWriter` still satisfies `Writer`; `WriteBatchFallback` will use sequential fallback | None |

### 3.2 API Design Review

**The `WriteBatchFallback` helper is excellent DX.** It solves the "capability detection" problem without forcing every caller to do a type assertion. A Cell developer writes `outbox.WriteBatchFallback(ctx, w, entries)` and it "just works" whether the underlying writer supports batch or not. This is the Go idiom (`io.Copy` detecting `io.WriterTo`).

**Error semantics are well-defined:** All-or-nothing, validate-before-write. This matches developer expectations for a transactional outbox.

### 3.3 Suggestions and Issues

1. **[验收标准缺失]** The design says "all entries are validated before any write occurs" but does not specify what happens with an **empty slice**. The postgres sketch shows `if len(entries) == 0 { return nil }`. This should be explicitly documented in the `WriteBatch` godoc and tested. An empty batch returning nil (not an error) is the correct behavior but must be stated.

2. **[开发者体验]** The `WriteBatchFallback` function name is somewhat awkward. "Fallback" implies a degraded path, but in most cases this will be the *primary* entry point for batch writes. **Consider renaming to `WriteBatch`** (a package-level function, not a method) or `WriteAll`. The current name forces developers to think about implementation details they shouldn't need to care about.

    Counter-argument: The name makes the fallback behavior explicit, which is valuable for adapter authors who need to understand what happens if they don't implement `BatchWriter`. The current name is acceptable if the godoc is clear.

3. **[验收标准缺失]** The design does not specify the error wrapping format for sequential fallback failures. In the fallback path, if the 3rd of 5 entries fails, the error should indicate which entry failed: `"outbox: write entry[2]: <underlying error>"`. The design shows index-based error for validation (`entry[%d]`) but not for write failures in the fallback path.

4. **[开发者体验]** The design does not address **observability**. When `WriteBatchFallback` detects a `BatchWriter` and uses the optimized path vs. falling back to sequential writes, should this be logged? For debugging, a `slog.Debug` when falling back to sequential writes would help adapter authors verify their `BatchWriter` implementation is being used.

5. **[验收标准缺失]** The `noopWriter` pattern (duplicated 4x in test files + 1x in examples) does not implement `BatchWriter`. After this change, `WriteBatchFallback(ctx, noopWriter{}, entries)` will silently fall back to sequential no-op writes. This is correct behavior, but the product should decide: should a shared `outboxtest.NoopWriter` be provided that implements *both* `Writer` and `BatchWriter`? This aligns with backlog item `P4-TD-01`.

6. **[兼容性风险]** The design adds `errcode` and `fmt` imports to `kernel/outbox/outbox.go` (for `WriteBatchFallback`). The PR#68 branch already imports these. Verify no import cycle is introduced.

### 3.4 Acceptance Criteria

| # | Priority | Criterion | Verification |
|---|----------|-----------|--------------|
| AC-11 | P1 | `BatchWriter` interface defined: embeds `Writer`, adds `WriteBatch(ctx, []Entry) error` | Code review |
| AC-12 | P1 | `WriteBatchFallback` function: validates all entries first, then uses `BatchWriter` if available, otherwise sequential `Write` | Unit test (3 paths: batch, fallback, validation failure) |
| AC-13 | P1 | Empty slice input to `WriteBatch` and `WriteBatchFallback` returns nil | Unit test |
| AC-14 | P1 | Validation failure on entry[i] returns error containing index | Unit test |
| AC-15 | P1 | Existing `Writer` interface unchanged (no new methods) | Code review |
| AC-16 | P1 | `go build ./...` passes (no import cycles) | CI |
| AC-17 | P2 | `WriteBatch` godoc specifies all-or-nothing semantics, empty-slice behavior, and transaction requirement | Code review |
| AC-18 | P2 | Sequential fallback path error includes entry index | Unit test |
| AC-19 | P2 | Postgres `OutboxWriter` implements `BatchWriter` with multi-row INSERT | Code review + integration test |
| AC-20 | P3 | `WriteBatchFallback` logs at Debug level when falling back to sequential writes | Code review |

---

## 4. Breaking Change Handling

### 4.1 CS-AR-2 Migration Guide

Since GoCell is pre-v1.0, a formal deprecation cycle is not required. However, good framework citizenship demands:

**Required CHANGELOG entry:**

```
## Breaking Changes

### `kernel/cell.Dependencies` struct simplified

The `Cells map[string]Cell` and `Contracts map[string]Contract` fields have been
removed from `Dependencies`. Only `Config map[string]any` remains.

**Migration**: If your Cell's `Init` method references `deps.Cells` or
`deps.Contracts`, remove those references. Cross-cell communication should use
contracts via the assembly, not direct cell references.

If you construct `Dependencies{}` in tests with these fields, remove them:

  // Before
  deps := cell.Dependencies{
      Cells:     make(map[string]cell.Cell),
      Contracts: make(map[string]cell.Contract),
      Config:    map[string]any{"key": "value"},
  }

  // After
  deps := cell.Dependencies{
      Config: map[string]any{"key": "value"},
  }
```

### 4.2 F-OB-01 -- No Migration Required

Purely additive. No consumer action needed.

### 4.3 CS-AR-3 -- No Migration Required

Documentation only.

---

## 5. Priority Ranking (Product Value)

| Rank | Change | Product Value | Effort | Recommendation |
|------|--------|---------------|--------|----------------|
| 1 | CS-AR-2 | **High** -- Removes a security/design footgun (`deps.Cells` exposing entire cell graph). Simplifies the mental model for Cell developers. | ~1h | Do first |
| 2 | F-OB-01 | **Medium** -- Enables future batch event patterns (saga, multi-aggregate commands). Current callers don't need it yet (all 7 sites write single entries), but the interface design is forward-looking and the backward-compatible approach is low risk. | ~3h | Do second |
| 3 | CS-AR-3 | **Low** (but positive) -- Codifies an implicit decision. Zero code change, zero consumer impact. Helps future contributors understand why `net/http` is in kernel. | ~15min | Do last |

The design document's implementation order (CS-AR-2 -> CS-AR-3 -> F-OB-01) is reasonable. I agree with doing CS-AR-2 first (simplest structural change) and CS-AR-3 second (trivial). F-OB-01 last because it adds new surface area and needs the most testing.

---

## 6. Product Review Scorecard (7 Dimensions)

| Dimension | Rating | Evidence |
|-----------|--------|----------|
| A. Acceptance Criteria Coverage | YELLOW | Design describes behavior but no formal Given/When/Then criteria. This review adds 20 criteria (AC-1 through AC-20). |
| B. UI Compliance (API surface) | GREEN | `Writer` interface unchanged. `Dependencies.Config` preserved. `Cell.Init` signature preserved. Error messages include context (index, entry ID). |
| C. Error Path Coverage | YELLOW | All-or-nothing semantics well-defined for batch. Missing: empty-slice edge case documentation, sequential-fallback error indexing. |
| D. Documentation Completeness | YELLOW | CHANGELOG entry required for CS-AR-2 but not yet written. ADR comment for CS-AR-3 specified but not yet applied. Godoc for `WriteBatchFallback` needs empty-slice and fallback behavior. |
| E. Feature Completeness | GREEN | All three changes fully specified. Option analysis is thorough. No missing features within scope. |
| F. Success Criteria Attainment | GREEN | Wave C scope is "design first" -- this document satisfies the design milestone. Implementation will follow. |
| G. Product Tech Debt | GREEN | No new tech debt introduced. CS-AR-2 actively reduces tech debt (removes unused coupling). F-OB-01 is forward-looking investment. |

---

## 7. Verdict

### CONDITIONAL PASS

The design is well-analyzed, the options are thoroughly explored, and the recommendations are sound. Two conditions must be met before implementation proceeds:

**Condition 1 (blocking):** Add formal CHANGELOG migration guidance for CS-AR-2 as specified in section 4.1 of this review. This is a pre-v1.0 framework, but consumers who have already adopted GoCell need clear migration instructions.

**Condition 2 (blocking):** Address the `WriteBatchFallback` edge cases identified in this review:
- Document empty-slice behavior in godoc (AC-13)
- Include entry index in sequential-fallback write errors (AC-18)
- Decide on function naming (`WriteBatchFallback` vs `WriteAll` vs `WriteBatch`) -- current name is acceptable if godoc is explicit about the dual-path behavior

**Non-blocking recommendations** (can be addressed in PR review):
- Add `Dependencies` struct godoc explaining the wrapper rationale (AC-5)
- Consider bundling `P4-TD-01` (shared NoopOutboxWriter) since it touches overlapping files
- Add Debug-level log for sequential fallback path (AC-20)
