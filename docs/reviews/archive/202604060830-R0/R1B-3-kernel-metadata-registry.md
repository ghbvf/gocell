# R1B-3: kernel/metadata + kernel/registry Module Review

| Field         | Value                                              |
|---------------|----------------------------------------------------|
| Reviewer      | Kernel Guardian                                    |
| Date          | 2026-04-06                                         |
| Scope         | `kernel/metadata/` (~422 LOC prod), `kernel/registry/` (~185 LOC prod) |
| Test coverage | metadata 97.1%, registry 100.0%                   |
| go vet        | Clean (no warnings)                                |
| All tests     | PASS                                               |

---

## 1. Module Overview

### kernel/metadata (422 LOC production, 7 JSON schemas)

| File | LOC | Purpose |
|------|-----|---------|
| `parser.go` | 247 | File-system walker; pattern matching for cell/slice/contract/journey/assembly/status-board/actors; YAML deserialization via `unmarshalFile` |
| `types.go` | 161 | Go struct definitions for all 7 metadata types (`CellMeta`, `SliceMeta`, `ContractMeta`, `JourneyMeta`, `AssemblyMeta`, `StatusBoardEntry`, `ActorMeta`) plus `ProjectMeta` aggregate |
| `doc.go` | 4 | Package doc |
| `schemas/embed.go` | 10 | `embed.FS` for 7 JSON Schema files |
| `schemas/*.schema.json` | 7 files | JSON Schema 2020-12 for IDE/future validation |

### kernel/registry (185 LOC production)

| File | LOC | Purpose |
|------|-----|---------|
| `cell.go` | 71 | `CellRegistry` -- indexed read-only lookup for cells and their slices |
| `contract.go` | 111 | `ContractRegistry` -- indexed lookup by ID, kind, owner; `Provider`/`Consumers` helpers |
| `doc.go` | 3 | Package doc |

---

## 2. Layer Compliance -- PASS

**Evidence:**

- `metadata/parser.go` imports: `fmt`, `io/fs`, `os`, `path/filepath`, `strings` (stdlib) + `pkg/errcode` + `gopkg.in/yaml.v3`. No runtime/adapters/cells imports.
- `registry/cell.go` imports: `sort`, `strings` (stdlib) + `kernel/metadata`. No runtime/adapters/cells imports.
- `registry/contract.go` imports: `sort` (stdlib) + `kernel/metadata`. No runtime/adapters/cells imports.
- Consumers of these packages are all within `kernel/` (`kernel/governance`, `kernel/assembly`, `kernel/journey`, `kernel/slice`). No leakage to upper layers.

**Verdict:** Green. kernel/ only depends on stdlib + pkg/ + one external YAML library. Fully compliant with the layer rule "kernel/ depends only on stdlib + pkg/".

---

## 3. YAML Parser Robustness

### 3.1 Malformed YAML -- PASS

Tested cases in `parser_test.go`:
- `TestParseFS_InvalidYAML` -- `{{{ not valid yaml` for cell.yaml
- `TestParseFS_InvalidSliceYAML` -- `:::broken` for slice.yaml
- `TestParseFS_InvalidContractYAML` -- `[[[broken` for contract.yaml
- `TestParseFS_InvalidJourneyYAML` -- `{bad` for journey.yaml
- `TestParseFS_InvalidAssemblyYAML` -- `{bad` for assembly.yaml
- `TestParseFS_InvalidStatusBoardYAML` -- `{bad` for status-board.yaml
- `TestParseFS_InvalidActorsYAML` -- `{bad` for actors.yaml

All return `errcode.Error` with `ERR_METADATA_INVALID` and file path context. No panics possible -- `yaml.Unmarshal` returns errors for malformed input, and `unmarshalFile` wraps them.

### 3.2 Unknown Fields -- ADVISORY (P2)

**Finding F-META-01:** `gopkg.in/yaml.v3`'s `Unmarshal` silently ignores unknown fields. The JSON schemas declare `"additionalProperties": false`, but this is not enforced at parse time.

**Impact:** A user writing `cell.yaml` with a typo like `consistencelevel` (lowercase L) would see it silently ignored, with `ConsistencyLevel` left as empty string. The empty value would only be caught downstream by governance rules (if they exist), not at parse time.

**Risk:** Medium. Governance rules in `rules_fmt.go` do check for required values, so the gap is partially covered. However, typos in optional fields (like `schema` or `l0Dependencies`) would be silently lost.

**Recommendation:** Consider using `yaml.Decoder` with `KnownFields(true)` in a future iteration:
```go
dec := yaml.NewDecoder(bytes.NewReader(data))
dec.KnownFields(true)
if err := dec.Decode(out); err != nil { ... }
```

### 3.3 Empty File Handling -- PASS (with note)

`yaml.Unmarshal([]byte(""), &m)` returns `nil` error with all zero-value fields. The parser then catches `m.ID == ""` and returns `errcode.New(ErrMetadataInvalid, "cell id is empty in ...")`. This is correct behavior.

**Missing test case:** No explicit test for an empty (0-byte) cell.yaml file. The current `TestParseFS_EmptyCellID` tests `id: ""` (explicit empty string), but not a completely empty file. The behavior is the same (ID is empty string, caught by validation), so this is cosmetic.

### 3.4 Type Coercion -- PASS

All struct fields use `string`, `[]string`, `*bool`, or nested structs. No `int`/`float` fields that could suffer silent coercion. The `*bool` for `ContractMeta.Replayable` correctly handles the tri-state (nil/true/false) with `omitempty`.

### 3.5 Walk Error Propagation -- PASS

`fs.WalkDir` callback receives `walkErr` and returns it immediately if non-nil (line 42-44). No swallowed walk errors.

---

## 4. Schema Validation (struct-level)

### 4.1 cell.yaml Required Fields

| Required per CLAUDE.md | Struct field | Go type | Validated at parse time? |
|------------------------|-------------|---------|-------------------------|
| `id` | `CellMeta.ID` | `string` | Yes -- empty check at parser.go:127 |
| `type` | `CellMeta.Type` | `string` | No -- validated downstream by governance |
| `consistencyLevel` | `CellMeta.ConsistencyLevel` | `string` | No -- validated downstream by governance |
| `owner.team` + `owner.role` | `CellMeta.Owner` | `OwnerMeta` | No -- validated downstream by governance |
| `schema.primary` | `CellMeta.Schema.Primary` | `string` | No -- validated downstream by governance (`rules_fmt.go:174` FMT06) |
| `verify.smoke` | `CellMeta.Verify.Smoke` | `[]string` | No -- validated downstream by governance |

**Finding F-META-02 (Advisory):** The parser only validates `id != ""` for cells. All other required-field checks are deferred to `kernel/governance`. This is a valid architectural choice (parser = structural, governance = semantic), but the JSON schema's `"required"` declarations imply parse-time enforcement that does not happen.

**Discrepancy with JSON schema:** `cell.schema.json` lists `"required": ["id", "type", "consistencyLevel", "owner", "verify"]` but does NOT list `"schema"` as required. The CLAUDE.md says `schema.primary` is required. The JSON schema has `schema` as optional (with a note "optional for L0 cells"). This is a legitimate design choice but creates ambiguity for L1+ cells.

### 4.2 slice.yaml Required Fields

| Required per CLAUDE.md | Struct field | Go type | Validated at parse time? |
|------------------------|-------------|---------|-------------------------|
| `id` | `SliceMeta.ID` | `string` | Yes -- empty check at parser.go:144 |
| `belongsToCell` | `SliceMeta.BelongsToCell` | `string` | No -- auto-inferred from path (parser.go:152-153) |
| `contractUsages` | `SliceMeta.ContractUsages` | `[]ContractUsage` | No -- nil/empty not checked at parse time |
| `verify.unit` | `SliceMeta.Verify.Unit` | `[]string` | No -- deferred to governance |
| `verify.contract` | `SliceMeta.Verify.Contract` | `[]string` | No -- deferred to governance |

**BelongsToCell auto-fill:** If `belongsToCell` is omitted in the YAML, the parser infers it from the directory path (`cells/{cellID}/slices/{sliceID}/slice.yaml`). This is tested in `TestParseFS_SliceOmitsBelongsToCell`. Good behavior, consistent with `slice.schema.json` where `belongsToCell` is not in `required`.

### 4.3 contract.yaml Directory Structure

`matchContractYAML` checks:
- Path starts with `contracts/`
- Path ends with `contract.yaml`
- At least 5 segments (contracts / kind / domain... / version / contract.yaml)

This correctly matches `contracts/{kind}/{domain-path}/{version}/contract.yaml` with any depth of domain-path.

**Finding F-META-03 (Advisory):** The parser does not validate that the path segments match the contract's `kind` or version fields. For example, a file at `contracts/http/auth/login/v1/contract.yaml` containing `kind: event` would parse successfully. Validation of path-content consistency is left to governance rules.

---

## 5. Registry Thread Safety

### 5.1 No Mutex Protection -- P1 FINDING

**Finding F-REG-01:** Neither `CellRegistry` nor `ContractRegistry` has any `sync.Mutex` or `sync.RWMutex` protection. The registries are:
- Populated at construction time in `NewCellRegistry` / `NewContractRegistry`
- All public methods are read-only (`Get`, `AllIDs`, `Count`, `SlicesFor`, `ByKind`, `ByOwner`, `Provider`, `Consumers`)
- No mutation methods exist after construction

**Assessment:** The current design is a **build-once, read-many** pattern. Since Go map reads are safe for concurrent readers when no writer exists, and the registries are fully built before any read occurs, this is **thread-safe by design**. No mutex is needed.

However, the thread-safety guarantee is **implicit** -- there is no documentation or `// Thread-safe for concurrent reads after construction` comment, and nothing prevents a future developer from adding a mutation method without adding locks.

**Recommendation:** Add a one-line doc comment on each registry struct stating the concurrency contract:
```go
// CellRegistry provides indexed access to cells and their slices.
// It is safe for concurrent reads after construction via NewCellRegistry.
type CellRegistry struct { ... }
```

**Severity:** P2 (Advisory) -- correct today, but undocumented invariant.

### 5.2 Register/Lookup Atomicity -- N/A

There are no `Register` methods. Registration happens entirely within the constructor. Lookup methods are pure reads. Atomicity is not a concern.

---

## 6. Naming Compliance

### 6.1 YAML Field Names -- PASS

All YAML tags use camelCase:
- `belongsToCell`, `contractUsages`, `consistencyLevel`, `ownerCell`, `schemaRefs`, `deliverySemantics`, `idempotencyKey`, `deployTemplate`, `passCriteria`, `checkRef`, `expiresAt`, `updatedAt`, `journeyId`, `maxConsistencyLevel`, `l0Dependencies`

### 6.2 Forbidden Old Field Names -- PASS

Grep for all 11 forbidden names (`cellId`, `sliceId`, `contractId`, `assemblyId`, `ownedSlices`, `authoritativeData`, `producer`, `consumers`, `callsContracts`, `publishes`, `consumes`) found zero matches in metadata Go files and only method/test-description references in registry (the `Consumers()` method name is fine -- it is not a YAML field name).

**Note:** `StatusBoardEntry` uses `yaml:"journeyId"` (line 138 of types.go). This is NOT in the forbidden list (the forbidden name is `contractId` etc., not `journeyId`). However, it does use `Id` suffix in camelCase, which is inconsistent with the main model's use of bare `id`. This mirrors the JSON schema (`status-board.schema.json` line 10: `"journeyId"`), so it is intentional -- status-board entries reference a journey rather than being one.

---

## 7. Error Handling

### 7.1 errcode Usage -- PASS

All errors in `parser.go` use `errcode.New` or `errcode.Wrap`:
- `errcode.New(ErrMetadataInvalid, ...)` for semantic errors (empty ID, duplicates)
- `errcode.Wrap(ErrMetadataInvalid, ..., err)` for I/O and parse errors

No bare `errors.New` or `fmt.Errorf` in production code.

### 7.2 Error Context -- PASS

Every error includes the file path:
- Read errors: `"read cells/bad-cell/cell.yaml"`
- Parse errors: `"parse cells/bad-cell/cell.yaml"`
- Empty ID: `"cell id is empty in cells/empty/cell.yaml"`
- Duplicate ID: `"duplicate cell ID \"access-core\": cells/access-core-v2/cell.yaml and previous"`

**Minor gap:** Duplicate errors say "and previous" but do not include the path of the first occurrence. This is because the parser stores only the value, not the source path. Cosmetic issue only.

### 7.3 Registry Error Handling -- PASS (N/A)

Registry methods return `nil` for not-found cases rather than errors. This is idiomatic Go for in-memory lookups. No error paths to review.

---

## 8. Additional Findings

### F-META-04: JSON Schema Embedded but Unused (P3 Advisory)

The 7 JSON Schema files are embedded via `schemas/embed.go` but `schemas.FS` is not referenced anywhere in the codebase (confirmed by grep). The embed comment says "planned Phase 2" and "IDE tooling via yaml-language-server". This is forward-looking infrastructure, not dead code.

**Recommendation:** No action needed now. When Phase 2 arrives, add a programmatic JSON Schema validation pass in `kernel/governance` using the embedded schemas.

### F-META-05: Parser Stops on First Error (P3 Advisory)

`fs.WalkDir` callback returns errors immediately, which stops the walk. If a project has multiple broken YAML files, the user only sees the first error.

**Impact:** Minor DX friction during initial setup. Not a correctness issue.

**Recommendation:** Consider an "accumulate errors" mode in a future iteration where the parser collects all errors and returns them as a multi-error.

### F-META-06: No Lifecycle Validation on CellMeta (Correct by Design)

`CellMeta` deliberately does not have a `Lifecycle` field. Per CLAUDE.md, `lifecycle` is a governance field allowed only in `contract.yaml`. If a user writes `lifecycle: active` in `cell.yaml`, it would be silently ignored (see F-META-01 on unknown fields).

### F-META-07: SlicesFor Returns Unsorted (P3 Cosmetic)

`CellRegistry.SlicesFor` returns slices in map-iteration order (non-deterministic). `AllIDs()` sorts by name. For consistency, `SlicesFor` could sort by slice ID. Not a bug since consumers should not rely on order.

### F-META-08: CellMeta schema.primary Not Required in JSON Schema

`cell.schema.json` does NOT list `schema` in `required`. The CLAUDE.md states `schema.primary` is required. The JSON schema comment says "The primary field is optional for L0 cells." This creates an implicit conditional requirement: required for L1+, optional for L0. This is handled by governance rule FMT06 (`rules_fmt.go:174`), not by the schema.

---

## 9. Test Quality Assessment

### kernel/metadata Tests

| Test file | Tests | LOC | Coverage |
|-----------|-------|-----|----------|
| `parser_test.go` | 22 tests | 773 | 97.1% |
| `parser_integration_test.go` | 1 test (build tag: integration) | 128 | (integration only) |
| `types_test.go` | 14 tests | 300 | round-trip serialization |

**Strengths:**
- Comprehensive parser tests covering every metadata type
- Malformed YAML for all 7 file types
- Duplicate ID detection for cells, slices, contracts, journeys, assemblies
- Empty ID detection for all types
- Non-metadata files correctly ignored
- Deep contract paths
- Waiver round-trip
- `belongsToCell` auto-fill from path
- Types round-trip tests verify `omitempty` correctness

**Gaps (P3):**
1. No test for empty (0-byte) file -- covered implicitly by empty-ID check behavior but not explicitly tested
2. No test for very large YAML files (resource exhaustion) -- acceptable for unit tests
3. No test for YAML bomb / recursive anchors -- `gopkg.in/yaml.v3` has built-in protection
4. No fuzz tests -- `parser_test.go` could benefit from `go test -fuzz`

### kernel/registry Tests

| Test file | Tests | LOC | Coverage |
|-----------|-------|-----|----------|
| `registry_test.go` | 17 tests | 389 | 100% |

**Strengths:**
- Table-driven tests for all methods
- All four contract kinds tested (http/event/command/projection)
- nil project, empty project, nil-in-map edge cases
- Unknown kind returns empty/nil
- Sorted AllIDs deterministic output
- Fallback cellID parsing from composite key

**No gaps identified.** Registry test suite is exemplary.

---

## 10. Findings Summary

| ID | Severity | Category | Description |
|----|----------|----------|-------------|
| F-META-01 | P2 Advisory | Parser robustness | Unknown YAML fields silently ignored; `KnownFields(true)` not used |
| F-META-02 | P3 Advisory | Validation | Parser only validates `id != ""` at parse time; all other required-field checks deferred to governance |
| F-META-03 | P3 Advisory | Validation | Contract path-content consistency not validated at parse time |
| F-META-04 | P3 Advisory | Dead infrastructure | JSON schemas embedded but unused; planned for Phase 2 |
| F-META-05 | P3 Advisory | DX | Parser stops on first error; no multi-error accumulation |
| F-META-06 | N/A | Correct by design | No lifecycle field on CellMeta |
| F-META-07 | P3 Cosmetic | Consistency | `SlicesFor` returns unsorted; `AllIDs` returns sorted |
| F-META-08 | P3 Advisory | Schema drift | `schema.primary` not required in JSON schema but required per CLAUDE.md for L1+ cells |
| F-REG-01 | P2 Advisory | Concurrency | Thread-safety guarantee is correct but undocumented |

---

## 11. Verdict

**Overall: PASS with advisories.**

Both modules are well-implemented, well-tested (97%+ coverage), and fully layer-compliant. The code is clean, idiomatic Go with proper error handling through `pkg/errcode`. The architecture follows a sound "parser = structural, governance = semantic" separation.

**No P0 or P1 findings.** All findings are P2 advisory or P3 cosmetic.

**Top 3 recommendations (prioritized):**

1. **F-META-01 (P2):** Enable `yaml.Decoder.KnownFields(true)` to reject unknown YAML fields, catching typos at parse time rather than silently dropping them.
2. **F-REG-01 (P2):** Document the concurrency contract ("safe for concurrent reads after construction") on both registry structs.
3. **F-META-08 (P3):** Align JSON schema `required` arrays with CLAUDE.md mandatory field spec, or document the conditional requirement (L0 exemption) explicitly in both places.
