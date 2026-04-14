# R1B-4: kernel/governance Module Review

**Reviewer**: Kernel Guardian  
**Date**: 2026-04-06  
**Scope**: `kernel/governance/` (13 files, ~4875 LOC including tests)  
**Coverage**: 96.2% of statements (target: >=90% for kernel/)

---

## 1. Summary

The governance module implements a metadata validation engine with 35 rules across 5 categories (REF, TOPO, VERIFY, FMT, ADV) plus a dependency checker (DEP) and a target-selection impact-analysis subsystem. Code quality is high: test coverage exceeds the 90% kernel threshold, all tests pass, `go vet` reports no issues, and the module correctly avoids imports from `runtime/`, `adapters/`, or `cells/`. The architecture follows a clean rule-per-function pattern inspired by Kubernetes apimachinery field/errors.go.

There are three categories of findings: (A) rule coverage gaps against CLAUDE.md, (B) cognitive complexity / design concerns, and (C) minor code quality issues.

---

## 2. Rule Coverage vs CLAUDE.md

### 2.1 Rules FULLY implemented

| CLAUDE.md Constraint | Rule Code(s) | Status |
|---|---|---|
| slice.belongsToCell references existing cell | REF-01 | Covered |
| contractUsages references existing contract | REF-02 | Covered |
| contract.ownerCell is a Cell (not external actor) | REF-03 | Covered |
| cell.id matches directory name | REF-04 | Covered |
| slice.id matches directory name | REF-05 | Covered |
| journey.cells references existing cells | REF-06 | Covered |
| journey.contracts references existing contracts | REF-07 | Covered |
| assembly.cells references existing cells | REF-08 | Covered |
| l0Dependencies[].cell references existing cell | REF-09 | Covered |
| assembly.build.entrypoint required | REF-10 | Covered |
| assembly.build.entrypoint file exists | REF-11 | Covered |
| schemaRefs files exist | REF-12 | Covered |
| contract provider actor exists | REF-13 | Covered |
| contract consumer actors exist | REF-14 | Covered |
| assembly.id matches key | REF-15 | Covered |
| contractUsages.role matches kind (http->serve/call, etc.) | TOPO-01 | Covered |
| Provider-role slice matches contract provider | TOPO-02 | Covered |
| Consumer-role slice is in contract consumers | TOPO-03 | Covered |
| contract.consistencyLevel <= ownerCell level | TOPO-04 | Covered |
| L0 Cell not in contract endpoints | TOPO-05 | Covered |
| Each cell in at most one assembly | TOPO-06 | Covered |
| Verify closure: contractUsage has verify.contract or waiver | VERIFY-01 | Covered |
| Waiver required fields + expiry | VERIFY-02 | Covered |
| l0Dependencies targets L0-level cell | VERIFY-03 | Covered |
| lifecycle in {draft, active, deprecated} | FMT-01 | Covered |
| cell.type in {core, edge, support} | FMT-02 | Covered |
| consistencyLevel L0-L4 | FMT-03 | Covered |
| Event contract: replayable, idempotencyKey, deliverySemantics | FMT-04 | Covered |
| contractUsages role is valid (8 roles) | FMT-05 | Covered |
| Non-L0 cell must have schema.primary | FMT-06 | Covered |
| Contract provider endpoint required | FMT-07 | Covered |
| Contract ID prefix matches kind | FMT-08 | Covered |
| contract.kind in {http, event, command, projection} | FMT-09 | Covered |
| Banned legacy field name detection (heuristic via ID) | FMT-10 | Partial (see below) |
| Journey <-> status-board sync | ADV-01, ADV-04 | Covered |
| Deprecated contract usage warning | ADV-02 | Covered |
| Orphan waiver warning | ADV-03 | Covered |
| Cycle-free cell dependency graph | DEP-02 | Covered |
| L0 dependencies in same assembly | DEP-03 | Covered |
| slice.belongsToCell matches map key cellID | DEP-01 | Covered |

### 2.2 Rules NOT implemented or INCOMPLETE [P1]

The following CLAUDE.md constraints have no corresponding governance rule:

| # | Missing Rule | CLAUDE.md Source | Severity | Notes |
|---|---|---|---|---|
| G-01 | **cell.yaml required-field validation**: `id`, `type`, `consistencyLevel`, `owner.team`, `owner.role`, `verify.smoke` must be non-empty | "Cell must have cell.yaml (required fields: id / type / consistencyLevel / owner / schema.primary / verify.smoke)" | **P1** | `type`, `consistencyLevel` are validated by FMT-02 and FMT-03 only for valid-value ranges, but there is no rule that checks for empty/missing `owner.team`, `owner.role`, or `verify.smoke`. `schema.primary` is partially covered by FMT-06 (non-L0 only). |
| G-02 | **slice.yaml required-field validation**: `id`, `belongsToCell`, `verify.unit` must be non-empty | "Slice must have slice.yaml (required fields: id / belongsToCell / contractUsages / verify.unit / verify.contract)" | **P1** | `belongsToCell` is validated by REF-01 (referential integrity) but not for emptiness. `verify.unit` is never checked. `contractUsages` and `verify.contract` completeness is checked by VERIFY-01 but `verify.unit` is never validated as required. |
| G-03 | **Banned field name detection in YAML keys** | "Prohibited old field names (11 listed)" | **P2** | FMT-10 only checks if a cell/contract *ID* matches a banned name, and if contract IDs use slash separators. It does **not** scan actual YAML field names for banned keys like `cellId`, `sliceId`, `ownedSlices`, etc. The comment acknowledges this: "Full YAML-level field detection requires the parser to surface raw keys." |
| G-04 | **Dynamic state field leak detection** | "Dynamic delivery status fields (readiness/risk/blocker/done/verified/nextAction/updatedAt) only in status-board.yaml" | **P2** | No rule checks that these fields do not appear in cell.yaml, slice.yaml, contract.yaml, or assembly.yaml. The parser may not preserve raw field keys, making this hard to implement at the governance layer. |
| G-05 | **Cross-Cell import ban** | "Cell-to-Cell communication only via contract, no direct import of another Cell's internal/" | **P3** | This is a Go source-level constraint, not a YAML metadata constraint. It belongs in a separate Go import analysis tool (like `depcheck` for Go packages), not in the YAML governance validator. Noted for completeness. |
| G-06 | **Go-layer dependency rules** (kernel no-upward-dep, cells no adapters, etc.) | CLAUDE.md dependency rules | **P3** | Same as G-05: these are Go import constraints, not YAML metadata. They should be enforced by a separate tool (e.g., `go vet`-style analyzer or CI import checker). |

### 2.3 Assessment

The governance module covers **all YAML-level referential integrity, topological, format, and verify-closure rules** comprehensively. The main gaps are:

1. **Required-field validation for cell.yaml and slice.yaml** (G-01, G-02) -- these are the most impactful missing rules since they directly correspond to CLAUDE.md "must contain" requirements.
2. **Banned YAML field name detection** (G-03) is acknowledged as a limitation in the code comments but remains a gap.
3. **Dynamic state leakage detection** (G-04) is a metadata-hygiene rule with no implementation.

---

## 3. Cognitive Complexity Analysis

Manual analysis of the most complex functions (unable to run `gocognit` due to TLS proxy issues):

| Function | File | Estimated CC | Limit | Status |
|---|---|---|---|---|
| `checkDEP02` | depcheck.go:74 | ~18-20 | 15 | **EXCEEDS** |
| `validateREF12` | rules_ref.go:251 | ~12 | 15 | OK |
| `validateVERIFY01` | rules_verify.go:15 | ~11 | 15 | OK |
| `validateVERIFY02` | rules_verify.go:61 | ~14 | 15 | Borderline |
| `validateFMT04` | rules_fmt.go:108 | ~8 | 15 | OK |
| `SelectFromFiles` | targets.go:46 | ~7 | 15 | OK |

### `checkDEP02` Complexity Breakdown (depcheck.go:74-155)

This function contains:
- 2 nested range loops for graph construction (lines 78-95)
- An if-guard with nested map initialization (lines 88-91)
- A recursive DFS closure `dfs` with 3-way switch inside a range loop (lines 117-133)
- Post-DFS cycle reconstruction call (line 138)
- Final result construction (lines 144-155)

The recursive closure `dfs` contributes significantly. **Recommendation**: Extract the DFS into a standalone method `detectCycle(graph) []string` to reduce CC of `checkDEP02` to ~10 and make the DFS independently testable.

Note: The task description mentions "depcheck.go CC=36" but based on manual analysis, the actual CC is likely ~18-20. The CC=36 figure may have been computed with a different metric (cyclomatic rather than cognitive).

---

## 4. Rule Engine Design

### 4.1 Extensibility

**Rating: Good**

Each rule is a standalone method on `*Validator` (or `*DependencyChecker`), following a consistent pattern:
- Input: reads from `v.project` fields
- Output: returns `[]ValidationResult`
- Registration: added to `Validate()` method as a single append line

Adding a new rule requires:
1. Implement `validateXXX()` method in the appropriate `rules_*.go` file
2. Add one line to `Validate()` in `validate.go`

This is simple and low-risk. No rule-registration DSL or reflection magic.

### 4.2 Execution Order [P2]

**Rating: Acceptable with caveat**

The `Validate()` method (validate.go:86-137) runs rules in a fixed order:
```
REF (1-15) -> TOPO (1-6) -> VERIFY (1-3) -> FMT (1-10) -> ADV (1-4)
```

Observation: **REF rules run before TOPO rules**, which is correct since TOPO rules depend on contract existence (checked by REF-02). However, **TOPO rules skip missing entities with `continue`** (e.g., TOPO-01 line 16: `if !ok { continue // REF-02 covers missing contracts }`), so they are resilient to ordering changes. The FMT rules also skip invalid values via early-return guards.

**Concern**: There is no dependency-ordering enforcement mechanism. If someone reorders `Validate()` to run FMT before REF, the system would still function but produce potentially confusing duplicate errors. This is acceptable for the current codebase size but could benefit from a comment documenting the intended order.

### 4.3 Error Reporting

**Rating: Excellent**

Each `ValidationResult` includes:
- `Code`: machine-readable rule identifier (e.g., "REF-01", "TOPO-03")
- `Severity`: error vs. warning
- `IssueType`: typed classification (required, invalid, referenceNotFound, mismatch, forbidden, duplicate)
- `File`: YAML file path
- `Field`: JSON-path-like field reference (e.g., "contractUsages[0].role")
- `Message`: human-readable explanation

This is well-aligned with the Kubernetes apimachinery `field/errors.go` pattern cited in the code comments.

### 4.4 DependencyChecker vs Validator Duality [P3]

The module has two entry points:
1. `Validator.Validate()` -- runs 35 rules (REF/TOPO/VERIFY/FMT/ADV)
2. `DependencyChecker.Check()` -- runs 3 rules (DEP-01/02/03)

These are **not integrated** -- calling `Validate()` does not invoke DEP rules, and vice versa. This means a consumer must know to call both. A `ValidateAll()` function that combines both would improve discoverability.

---

## 5. Test Coverage Analysis

### 5.1 Overall Coverage

**96.2%** of statements -- exceeds the 90% kernel threshold.

### 5.2 Per-Function Coverage

Functions below 90%:

| Function | Coverage | Gap |
|---|---|---|
| `validateTOPO04` | 78.6% | Missing test for cellLevel parse error branch |
| `matchFromAssemblyPath` | 83.3% | Missing test for malformed path (len(parts) < 2) |
| `validateVERIFY03` | 83.3% | Missing test for parseErr on target cell's level |
| `contractIDFromPath` | 71.4% | Missing tests for empty-rest and dir=="." branches |
| `validateREF05` | 88.9% | Missing test for malformed key (no slash) |

### 5.3 Test Design Quality

**Rating: Very Good**

- All test functions use table-driven pattern (`[]struct{ name, setup, wantCount }`)
- `validProject()` helper provides a clean baseline for mutation testing
- Time injection (`val.now`) allows deterministic waiver expiry testing (TestVERIFY02_TimeOverride)
- File existence injection (`val.fileExists`) allows filesystem tests without real files
- Edge cases covered: nil project, empty project, wildcard consumers, expired waivers, unparseable dates
- The `findByCode` helper enables targeted assertion per rule

### 5.4 Missing Boundary Tests [P3]

| Scenario | Status |
|---|---|
| Slice key without "/" separator in REF-05 | Tested (returns `"no-slash"`) |
| Contract ID without "." separator | Tested in FMT-08 |
| Multiple cycles in graph | Not tested (only first cycle reported, by design) |
| Very large project (>100 cells/slices) | Not tested (performance) |
| Contract with both provider and consumer as same cell | Not tested |

---

## 6. Naming and Error Handling

### 6.1 errors.New / errcode Usage

**No violations found.** The governance module does not use `errors.New` anywhere. It returns `[]ValidationResult` structs instead of Go errors, which is the correct pattern for a validation engine. No external error exposure occurs.

### 6.2 Naming Conventions

| Item | Convention | Status |
|---|---|---|
| Rule function names | `validateXXX` / `checkDEP0X` | Consistent |
| ValidationResult fields | PascalCase exported | Correct |
| IssueType constants | `IssueXxx` | Correct |
| Helper functions | camelCase unexported | Correct |
| File naming | `rules_{category}.go` | Clean separation |

**Minor observation**: `depcheck.go` uses a different pattern (`DependencyChecker` struct with `Check()` method) than `validate.go` (`Validator` struct with `Validate()`). The `DependencyChecker` uses `registry.CellRegistry` and `registry.ContractRegistry` while `Validator` works directly with `metadata.ProjectMeta`. This inconsistency is acceptable since `DependencyChecker` pre-dates `Validator` and the registry provides O(1) consumer lookups needed for cycle detection, but it adds cognitive overhead for new contributors.

### 6.3 Go Abbreviation Consistency

No abbreviation issues found. The code consistently uses:
- `ID` (not `Id`) for identifiers
- `HTTP` in `ContractHTTP` constant references
- `DFS` in comments (not `dfs`)

---

## 7. Findings Summary

### P0 -- None

### P1 -- Must Fix (2 items)

| ID | Finding | File | Recommendation |
|---|---|---|---|
| **G-01** | Missing required-field validation for `cell.yaml`: `owner.team`, `owner.role`, `verify.smoke` are never checked for non-empty values | rules_fmt.go | Add `validateFMT11()` to check cell required fields per CLAUDE.md |
| **G-02** | Missing required-field validation for `slice.yaml`: `verify.unit` is never checked for non-empty | rules_fmt.go | Add `validateFMT12()` to check slice required fields per CLAUDE.md |

### P2 -- Should Fix (3 items)

| ID | Finding | File | Recommendation |
|---|---|---|---|
| **G-03** | FMT-10 banned-field detection is heuristic only (checks IDs, not YAML keys) | rules_fmt.go:267 | Either enhance parser to surface raw YAML keys, or add a pre-parse regex scan of YAML files for banned field names |
| **G-04** | No rule to detect dynamic status fields leaking into non-status-board files | (missing) | Add `validateFMT13()` or similar advisory rule |
| **CC-01** | `checkDEP02` estimated CC ~18-20, exceeds limit of 15 | depcheck.go:74 | Extract DFS closure into standalone `detectCycle(graph map[string]map[string]bool) []string` method |

### P3 -- Nice to Have (3 items)

| ID | Finding | File | Recommendation |
|---|---|---|---|
| **DESIGN-01** | `DependencyChecker.Check()` and `Validator.Validate()` are not integrated | validate.go, depcheck.go | Add `ValidateAll()` that runs both, or merge DEP rules into Validator |
| **TEST-01** | 5 functions below 90% coverage (lowest: `contractIDFromPath` at 71.4%) | targets.go, rules_topo.go, rules_verify.go | Add targeted tests for uncovered branches |
| **TEST-02** | No test for contract with same cell as both provider and consumer | validate_test.go | Add edge case test |

---

## 8. Dependency Compliance

### 8.1 Import Analysis

All non-test files in `kernel/governance/` import only:
- Standard library: `fmt`, `os`, `path`, `path/filepath`, `sort`, `strings`, `time`
- `kernel/cell` -- same kernel layer
- `kernel/metadata` -- same kernel layer
- `kernel/registry` -- same kernel layer

**No imports from `runtime/`, `adapters/`, `cells/`, or `pkg/`.** This fully complies with the layering constraint "kernel/ only depends on stdlib + pkg/ (and other kernel/ sub-packages)."

Test files additionally import `github.com/stretchr/testify` which is acceptable for test dependencies.

### 8.2 Verdict

**GREEN** -- No layering violations.

---

## 9. Dimensional Assessment

| Dimension | Score | Evidence |
|---|---|---|
| Rule Coverage vs CLAUDE.md | YELLOW | 35 rules implemented covering all REF/TOPO/VERIFY/FMT/ADV categories. 2 P1 gaps: required-field validation for cell.yaml and slice.yaml. |
| Cognitive Complexity | YELLOW | `checkDEP02` exceeds CC=15 limit (~18-20). All other functions within limits. |
| Test Coverage | GREEN | 96.2% overall (>90% kernel threshold). Table-driven tests with injectable time/filesystem. |
| Layering Compliance | GREEN | Zero upward dependencies. Only kernel/ + stdlib imports. |
| Error Reporting Quality | GREEN | Typed issue classification with file/field/message. Kubernetes-inspired pattern. |
| Extensibility | GREEN | Clean rule-per-function pattern. Adding a rule = 1 method + 1 line in Validate(). |
| Naming & Style | GREEN | Consistent Go conventions. No errors.New exposure. No abbreviation issues. |

---

## 10. Actionable Next Steps

1. **[P1]** Implement `validateFMT11()` for cell.yaml required-field checks (`owner.team`, `owner.role`, `verify.smoke`). Estimated effort: 30 lines + 40 lines tests.
2. **[P1]** Implement `validateFMT12()` for slice.yaml required-field checks (`verify.unit`). Estimated effort: 20 lines + 30 lines tests.
3. **[P2]** Refactor `checkDEP02` to extract the DFS closure into a standalone method. Estimated effort: 20 lines refactor, no behavior change.

---

*Review generated by Kernel Guardian agent, 2026-04-06.*
