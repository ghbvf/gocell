# Wave C Architecture Design -- Kernel Guardian Review

> Reviewer: Kernel Guardian
> Date: 2026-04-11
> Input: `docs/designs/20260411-wave-c-architecture-design.md`
> Codebase Evidence: PR#68 worktree (`pr-d1-kernel-interface`)

---

## 1. CS-AR-2: Dependencies Interface Minimization

### 1.1 Layering Isolation Assessment: PASS

**Evidence gathered:**

1. `assembly.go:159-161` is the sole production site that populates `Cells` and `Contracts`:
   ```
   deps := cell.Dependencies{
       Cells:     a.cellMap,
       Contracts: make(map[string]cell.Contract),
   }
   ```

2. Zero production callers read `deps.Cells` or `deps.Contracts`. Confirmed via `Grep` for `deps.Cells[` and `deps.Contracts[` across all of 项目根目录 -- the only hits are from `kernel/metadata` and `kernel/governance`, which use entirely different types (`metadata.ProjectModel.Cells`, not `cell.Dependencies.Cells`).

3. Test files that construct `Dependencies{}` with `Cells:` and `Contracts:` fields (approximately 20 sites across 5 cell test files plus assembly tests) all pass `make(map[...])` with empty maps. None populate or read these maps.

4. The `Cells map[string]Cell` field exposes the entire cell graph to every cell at init time. This violates least-privilege: any cell could call `deps.Cells["other"].Stop(ctx)`. Removing it eliminates this attack surface.

**Verdict**: Option A (narrow to Config-only) is sound. The change improves layering isolation by removing a mechanism through which cells could bypass contract-mediated communication. The design document's impact analysis is accurate -- zero functional breakage, only struct literal compilation in tests.

**Correction to design document**: The document states "~15 test file updates" but actual evidence shows approximately 20 test sites across 5 cell test files (access-core, audit-core, config-core, device-cell, order-cell) plus `kernel/cell/base_test.go` and `kernel/assembly/assembly_test.go`. The `base_test.go` sites use `Dependencies{}` without named fields, so they compile without change. The real count is:
- 1 production file (`assembly.go`) -- remove 2 lines
- 5 cell test files -- remove `Cells:` and `Contracts:` field assignments (~20 sites)
- 0 changes to `base_test.go` (uses positional `Dependencies{}`)

### 1.2 Contract Completeness Check

No impact. `Dependencies` is a runtime DI struct, not a metadata contract. The `Cell` interface signature (`Init(ctx, Dependencies)`) is unchanged. No contract.yaml files reference `Dependencies`.

### 1.3 Metadata Compliance Check

No impact. `Dependencies` is not a metadata model type. The metadata types (`CellMeta`, `SliceMeta`, etc.) are in `kernel/metadata/types.go`, not in `kernel/cell/interfaces.go`.

---

## 2. CS-AR-3: kernel/cell net/http Dependency

### 2.1 Layering Isolation Assessment: PASS (with documented rationale)

**Evidence gathered:**

1. CLAUDE.md constraint: "kernel/ 不依赖 runtime/、adapters/、cells/（只依赖标准库 + pkg/）". The keyword is "标准库" (standard library). `net/http` is Go standard library. The literal rule is satisfied.

2. `net/http` usage in kernel/ is confined to 3 files:
   - `kernel/cell/registrar.go` -- `http.Handler` in `RouteMux` and `HTTPRegistrar` interfaces (lines 37, 48, 60)
   - `kernel/cell/celltest/mux.go` -- `TestMux` implementation
   - `kernel/cell/celltest/mux_test.go` and `kernel/cell/registrar_test.go` -- tests

3. No kernel file imports any third-party HTTP library (chi, gorilla, echo). The dependency stops at the stdlib interface level.

4. Confirmed via `Grep` for `"github.com/ghbvf/gocell/(runtime|adapters|cells)` in `kernel/` -- zero matches. The kernel has no upward dependencies.

**Assessment of design document's Option A (accept net/http):**

The design document's recommendation is architecturally sound. `http.Handler` occupies a unique position in Go's type system: it is the universal handler interface, comparable to `io.Reader` or `io.Writer`. Creating a parallel type system (Option C with `any` parameters, or a custom Handler interface) would degrade type safety and break ecosystem compatibility with no meaningful gain.

The design document correctly identifies that the real decoupling target is third-party routers (chi, gorilla), not `net/http` itself. The current design achieves this -- `RouteMux` is implementable with pure stdlib (`celltest.TestMux` proves this).

**Guardian ruling**: PASS. The `net/http` import satisfies the literal constraint. However, the design document SHOULD add a guard comment (as proposed in section 2.6) to prevent future confusion. This is a documentation-only action, not a code risk.

### 2.2 Hidden Coupling Assessment

One concern the design document does not emphasize: the `RouteMux` interface embeds HTTP-specific semantics (method-pattern routing like `"GET /users/{id}"`, `http.Handler` parameter types, middleware chain via `func(http.Handler) http.Handler`). This means that any future non-HTTP transport (gRPC, WebSocket) cannot reuse `RouteMux` and would need a separate registration interface.

This is acceptable today because GoCell explicitly models HTTP contracts (`kind: http`). But the design document should note this as a known boundary, not just as a future migration path.

### 2.3 SOL-B-02: kernel/idempotency -> kernel/outbox Dependency

**Evidence gathered:**

The dependency chain within kernel/:
```
kernel/cell/registrar.go     --> kernel/outbox  (for outbox.Subscriber)
kernel/idempotency            --> kernel/outbox  (for outbox.Receipt)
```

The `Claimer.Claim()` signature at `idempotency.go:64` returns `outbox.Receipt`:
```go
Claim(ctx context.Context, key string, leaseTTL, doneTTL time.Duration) (ClaimState, outbox.Receipt, error)
```

`Receipt` is an interface with 2 methods (`Commit`, `Release`) and has no dependencies itself. It conceptually belongs to the "processing lifecycle" domain, not specifically to outbox.

**Severity: WARN (not blocking)**

This is a kernel-internal dependency (kernel/idempotency depending on kernel/outbox), which is permitted by the layering rules. However, it creates a conceptual coupling: idempotency is a consumer-side concern, while outbox is a producer-side concern. If `Receipt` were extracted to a shared kernel sub-package (e.g., `kernel/cell/lifecycle` or defined inline in idempotency), the coupling would be cleaner.

The design document correctly notes this is "not addressed in this design" and defers it. This is acceptable for Wave C scope. However, if F-OB-01 adds `BatchWriter` to `kernel/outbox`, the package grows in scope, making the extraction more valuable later.

---

## 3. F-OB-01: Writer Batch Write

### 3.1 Layering Isolation Assessment: PASS

**Evidence gathered:**

1. The proposed `BatchWriter` interface and `WriteBatchFallback` function live in `kernel/outbox/outbox.go`. This package already exists and already defines `Writer`, `Relay`, `Publisher`, `Subscriber`. Adding `BatchWriter` does not introduce new imports.

2. The proposed `WriteBatchFallback` uses only `fmt.Errorf` and `kernel/outbox.Entry.Validate()` -- no new external dependencies.

3. The Postgres implementation lives in `adapters/postgres/outbox_writer.go`, which already implements `outbox.Writer`. Adding `BatchWriter` there is a natural extension.

4. No kernel -> non-kernel dependency is introduced.

**Verdict**: Clean layering. The interface is in kernel/, the implementation is in adapters/. This matches the established pattern.

### 3.2 Interface Design Assessment: PASS

The separate `BatchWriter` interface (Option B) is the correct choice:

1. **Non-breaking**: Existing `Writer` implementations continue to compile. The `BatchWriter` interface embeds `Writer`, so any `BatchWriter` is also a `Writer`.

2. **Discoverable via type assertion**: The `WriteBatchFallback` helper uses `w.(BatchWriter)` to detect batch support. This follows the same pattern as `cell.(HTTPRegistrar)` -- a well-established GoCell idiom.

3. **Validation-first semantics**: Validating all entries before any write prevents partial validation failure scenarios. Combined with the transaction guarantee, this provides clean all-or-nothing semantics.

### 3.3 Error Semantics Compatibility with Outbox Relay

**Evidence gathered:**

The Relay pattern operates on individual `outbox_entries` rows (polling `WHERE status = 'pending'`). `WriteBatch` writes multiple rows atomically in one transaction. From the Relay's perspective, these rows are indistinguishable from rows written by individual `Write` calls -- they just appear together in the same transaction commit.

The three-phase Relay cycle (claim/publish/writeBack) operates per-entry, not per-batch. No compatibility issue exists.

**One nuance the design document should clarify**: If `WriteBatch` is called outside a transaction context (no tx in ctx), the behavior should be defined. The current `Writer.Write` implementation in postgres returns `errcode.New(ErrAdapterPGNoTx, ...)`. The design sketch shows the same check for `WriteBatch`. This is consistent.

### 3.4 Edge Cases in WriteBatchFallback

The fallback path (sequential `Write` calls when `BatchWriter` is not implemented) has a subtle semantic difference:

- **BatchWriter path**: All-or-nothing via single transaction INSERT.
- **Fallback path**: Sequential `Write` calls. If the third of five writes fails, the first two are already written (within the same tx, so they would roll back with the tx -- but the `WriteBatchFallback` function itself returns the error without rolling back).

The design document states "Partial success is not possible because all writes happen within a single database transaction." This is correct IF the caller manages the transaction. The `WriteBatchFallback` function itself does not manage transactions -- it relies on the context-embedded tx pattern. This assumption should be documented more explicitly in the `WriteBatchFallback` godoc.

---

## 4. New Architectural Risks Discovered

### Risk R5: kernel/outbox Package Scope Creep

With Wave C, `kernel/outbox` will contain:
- `Entry`, `Writer`, `BatchWriter`, `WriteBatchFallback` (producer-side)
- `Relay`, `Publisher` (relay-side)
- `Disposition`, `Receipt`, `HandleResult`, `EntryHandler` (consumer-side)
- `Subscriber`, `SubscriberWithMiddleware`, `TopicHandlerMiddleware` (consumer infrastructure)
- `LegacyHandler`, `WrapLegacyHandler` (compatibility)

This is 16+ exported types/functions in a single file. The package serves three distinct concerns (writing, relaying, consuming) plus legacy compat. Consider splitting in a future wave:
- `kernel/outbox` -- Entry, Writer, BatchWriter, WriteBatchFallback
- `kernel/outbox/relay` -- Relay, Publisher
- `kernel/outbox/consume` -- Disposition, Receipt, HandleResult, EntryHandler, Subscriber, etc.

This is NOT a blocking issue for Wave C. Filed as observation for future consideration.

### Risk R6: Test Impact Underestimated for CS-AR-2

The design document claims "~15 test file updates." Actual grep shows approximately 20 struct literal sites across 5 cell test files plus assembly tests. While the magnitude is small, the imprecise count suggests the grep was done by manual estimation rather than tool. Recommend running an exact count before implementation to avoid surprise compilation errors in CI.

---

## 5. Suggested Constraints / Guard Rules

### G1: Dependencies Struct Freeze (for CS-AR-2)

After removing `Cells` and `Contracts`, add a comment to prevent re-introduction:

```go
// Dependencies is the set of collaborators injected into a Cell during Init.
//
// CONSTRAINT: This struct must not contain Cell, Contract, or Assembly types.
// Cross-cell communication goes through contracts. Dependency injection uses
// functional options at construction time, not Dependencies.
type Dependencies struct {
    Config map[string]any
}
```

### G2: kernel/cell net/http Allowance Record (for CS-AR-3)

The design document proposes a comment. Agreed. Additionally, recommend adding this to a future `kernel/ARCHITECTURE.md` or an ADR so the decision is discoverable outside source code.

### G3: BatchWriter Transaction Precondition (for F-OB-01)

The `WriteBatchFallback` godoc should explicitly state the transaction precondition:

```go
// WriteBatchFallback writes entries using the Writer interface, falling
// back to sequential Write calls if the writer does not implement BatchWriter.
//
// PRECONDITION: ctx must carry a database transaction. Both the batch path
// and the sequential fallback path depend on transactional atomicity to
// guarantee all-or-nothing semantics. Without a transaction, partial writes
// are possible in the fallback path.
```

---

## 6. Summary Verdict

| Change | Layering | Contract | Metadata | Risk | Verdict |
|--------|----------|----------|----------|------|---------|
| CS-AR-2: Dependencies minimization | PASS | N/A | N/A | Low | PASS |
| CS-AR-3: net/http in kernel/cell | PASS | N/A | N/A | Low | PASS |
| SOL-B-02: idempotency->outbox coupling | WARN | N/A | N/A | Low | DEFERRED (acceptable) |
| F-OB-01: BatchWriter interface | PASS | N/A | N/A | Low | PASS |

### Overall Ruling: PASS

All three Wave C changes are architecturally sound and do not violate GoCell's layering constraints. No blocking issues found.

### Mandatory Actions (must complete before merge):

1. **[CS-AR-2]** Add the Dependencies struct freeze comment (G1) to prevent re-introduction of `Cells`/`Contracts` fields.

2. **[CS-AR-3]** Add the architectural decision comment to `registrar.go` as proposed in the design document section 2.6.

3. **[F-OB-01]** Add the transaction precondition to `WriteBatchFallback` godoc (G3) to make the atomicity assumption explicit.

### Advisory Items (recommended, not blocking):

- [SOL-B-02] Track `Receipt` extraction from `kernel/outbox` to a shared lifecycle package. Priority: low. Trigger: when a second consumer of `Receipt` appears outside `kernel/outbox` and `kernel/idempotency`.
- [R5] Monitor `kernel/outbox` package growth. If it exceeds ~20 exported symbols, consider splitting into sub-packages.
- [R6] Run exact count of test sites affected by CS-AR-2 before implementation to validate the "~15" estimate (actual: ~20 sites).
