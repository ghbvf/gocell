# R1A-1: pkg Layer Review Report

- **Reviewer agent**: R1A-1 (pkg Layer 0)
- **Review baseline commit**: `ce03ba1` (HEAD of develop at review time)
- **Review date**: 2026-04-06

---

## Review Scope

| Package | Source files | LOC (source) | Test files | LOC (test) |
|---------|-------------|-------------|------------|------------|
| `pkg/errcode` | `errcode.go`, `doc.go` | 104 | `errcode_test.go` | 210 |
| `pkg/ctxkeys` | `keys.go`, `doc.go` | 143 | `keys_test.go` | 175 |
| `pkg/id` | `id.go`, `doc.go` | 30 | `id_test.go` | 33 |
| `pkg/uid` | `uid.go`, `doc.go` | 31 | `uid_test.go` | 75 |
| `pkg/httputil` | `response.go`, `doc.go` | 112 | `response_test.go` | 181 |
| **Total** | **10 source** | **~420** | **5 test** | **~674** |

---

## Findings

| ID | Severity | File:Line | Description | Suggested Fix |
|----|----------|-----------|-------------|---------------|
| R1A1-F01 | **P0** | `pkg/uid/uid.go:17` | `uid.New()` silently discards `crypto/rand.Read` error via `_, _ = rand.Read(b)`. If the OS entropy source fails, this produces a UUID from a zero-filled (or partially filled) buffer -- predictable and non-unique. The `pkg/id` package correctly panics on rand failure (line 22-23); `uid` should do the same. | Change to: `if _, err := rand.Read(b); err != nil { panic(fmt.Sprintf("uid: crypto/rand failed: %v", err)) }` -- identical to the pattern used in `pkg/id/id.go:22-23`. |
| R1A1-F02 | **P1** | `pkg/httputil/response.go:86-107` | `mapCodeToStatus` uses substring matching on error code strings but misses `ERR_AUTH_TOKEN_EXPIRED`. The code `"ERR_AUTH_TOKEN_EXPIRED"` does not contain `UNAUTHORIZED`, `LOGIN_FAILED`, `REFRESH_FAILED`, or `INVALID_TOKEN`, so it falls through to `default: 500 Internal Server Error`. An expired token should return `401 Unauthorized`. Similarly, `ERR_AUTH_KEY_INVALID` falls to `500` because it contains `KEY_INVALID` not `INVALID_TOKEN`. | Add `strings.Contains(c, "EXPIRED")` to the 401 case, or add `strings.Contains(c, "KEY_INVALID")` to the 401 case. Better: refactor `mapCodeToStatus` to use an explicit map (`map[errcode.Code]int`) instead of fragile substring matching, so every new error code is forced to register a mapping. |
| R1A1-F03 | **P1** | `pkg/httputil/response.go:86-107` | `mapCodeToStatus` uses substring-based dispatch which is brittle and order-dependent. Any new error code that happens to contain `NOT_FOUND` (e.g., a hypothetical `ERR_REASON_NOT_FOUND_IN_CACHE`) will silently get `404` even if that's wrong. An explicit map or a code-level attribute would be more reliable. | Refactor to an explicit `map[errcode.Code]int` or attach HTTP status to `errcode.Code` as a typed attribute. |
| R1A1-F04 | **P1** | `pkg/httputil/response.go:18,27,51` | `json.NewEncoder(w).Encode(v)` errors are silently discarded via `_ = json.NewEncoder(w).Encode(...)`. If encoding fails (e.g., the value contains an unencodable type like a `chan` or a cycle), the response body is empty/truncated with no logging. The project's error-handling rule states: "Prohibit `_ = someFunc()` ignoring errors; must explicitly handle or log." | At minimum, log the encoding error via `slog.Error("httputil: JSON encode failed", slog.Any("error", err))`. |
| R1A1-F05 | **P2** | `pkg/id/` (entire package) | `pkg/id` is marked `Deprecated` in its doc.go and id.go package comment, recommending `pkg/uid` instead. Grep confirms no production code imports it (`github.com/ghbvf/gocell/pkg/id` has zero imports in 项目根目录). However, the package still exists, has tests, and will confuse new contributors. | Remove `pkg/id/` entirely, or at minimum add a `// Deprecated` annotation to the `New()` function itself (not just the package doc) and ensure `go vet` / linters flag it. |
| R1A1-F06 | **P2** | `pkg/ctxkeys/keys_test.go:118-140` | `TestFromMissingKey` tests missing-key for `CellID`, `SliceID`, `CorrelationID`, `JourneyID`, `TraceID`, `SpanID` -- but omits `RequestID`, `RealIP`, and `Subject`, all of which were added later. These three keys lack missing-key test coverage. | Add `RequestIDFrom`, `RealIPFrom`, `SubjectFrom` to the `TestFromMissingKey` table-driven test. |
| R1A1-F07 | **P2** | `pkg/errcode/errcode.go:30-39` | Alignment inconsistency: lines 12-29 use consistent tab alignment for `Code = "ERR_..."`, but lines 30-39 use extra indentation (`Code = "ERR_..."` shifted further right). This appears to be a separate const group that was appended without matching the original formatting. | Normalize alignment across all sentinel codes in the `const` block. |
| R1A1-F08 | **P2** | `adapters/redis/client.go:16` | Out of pkg/ scope but related: `ErrAdapterRedisLockAcquire` (Go constant) maps to string value `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"` (past tense). The constant name says "Acquire" (verb) but the value says "ACQUIRED" (past participle). This is a naming inconsistency that could mislead developers searching for the code string. | Align: either rename constant to `ErrAdapterRedisLockAcquired` or change string to `"ERR_ADAPTER_REDIS_LOCK_ACQUIRE"`. |

---

## Known Finding Verification

| Original ID | Description | Current Status | Evidence |
|-------------|-------------|----------------|----------|
| PR#7-12: uid.New() silently discards crypto/rand error | `uid.New()` used `_, _ = rand.Read(b)` producing predictable UUIDs on rand failure | **NOT FIXED** | `pkg/uid/uid.go:17` still reads `_, _ = rand.Read(b)`. The `pkg/id` package was fixed (panics on error at line 22-23), but `pkg/uid` was not. This remains a P0. |
| PR#8: Introduce crypto/rand UUID | `pkg/uid` uses `crypto/rand` for UUID generation | **FIXED** | `pkg/uid/uid.go:9` imports `crypto/rand`; line 17 calls `rand.Read(b)`. The source of randomness is correct; only the error handling is wrong (see above). |
| PR#33: errcode unified fix | Error codes standardized to `ERR_*` + `SCREAMING_SNAKE_CASE` | **FIXED** | All 28 sentinel codes in `pkg/errcode/errcode.go:12-39` follow the `ERR_{MODULE}_{REASON}` convention. All downstream packages (adapters, cells, kernel) define their domain-specific codes using `errcode.Code` type. No bare `errors.New` found in production code outside test files. |

---

## Naming Convention Check

| Check Item | Result | Details |
|------------|--------|---------|
| Go abbreviations uniformly capitalized (ID, URL, HTTP, etc.) | PASS | `CellID`, `SliceID`, `CorrelationID`, `JourneyID`, `TraceID`, `SpanID`, `RequestID`, `RealIP` all use correct capitalization. |
| Exported identifiers PascalCase | PASS | `WriteJSON`, `WriteError`, `WriteDomainError`, `StatusRecorder`, `NewStatusRecorder`, `WithDetails`, `NewWithPrefix` all correct. |
| Unexported identifiers camelCase | PASS | `ctxKey`, `mapCodeToStatus` correct. |
| Error code values `ERR_*` + `SCREAMING_SNAKE_CASE` | PASS | All 28 codes in `errcode.go` follow the convention. |
| No bare `errors.New` in production (non-test) pkg/ code | PASS | All `errors.New` occurrences in `pkg/` are in `_test.go` files only. |
| JSON/detail keys camelCase | PASS | Error detail keys in tests use `"sliceId"`, `"cellId"` -- these are JSON-layer keys (per naming-baseline.md section 2.1 `camelCase` for JSON), not Go identifiers or YAML metadata fields, so they are compliant. Note: `"sliceId"` uses lowercase `d` which differs from Go convention `ID`, but JSON camelCase convention allows `Id` -- this is a judgment call, not a violation. |

---

## Highlights

- **Solid errcode design**: The `errcode.Error` type with `Code`, `Message`, `Details`, and `Cause` is well-structured. `Unwrap()` enables proper `errors.Is`/`errors.As` chains. `WithDetails()` correctly returns a shallow copy without mutating the original -- immutability is tested explicitly.
- **ctxkeys type safety**: Using unexported `ctxKey` type prevents key collisions across packages. Every key has a symmetric `With*`/`*From` pair with consistent `(string, bool)` return signature.
- **Test quality**: Tests are table-driven throughout, covering round-trip, uniqueness, concurrency (uid), error wrapping chains, and immutability. Test-to-source LOC ratio is roughly 1.6x, which is healthy.
- **httputil separation of concerns**: `WriteDomainError` correctly separates errcode-based errors (user-safe) from unknown errors (logged server-side, generic message returned). This prevents internal detail leakage on 500 errors.
- **Deprecated package isolation**: `pkg/id` is properly deprecated with doc comments, and no production code imports it.

---

## Risk Assessment

- **Overall risk**: **MEDIUM**
- **P0 count**: 1 (uid.New() silent rand failure -- known since PR#7-12, still unfixed)
- **P1 count**: 3 (mapCodeToStatus gaps, substring brittleness, JSON encode errors silenced)
- **P2 count**: 4 (deprecated package cleanup, missing test coverage for new ctxkeys, formatting, naming inconsistency)

The P0 is the same issue flagged in the PR#7-12 review cycle. While `crypto/rand` failure is extremely rare in practice (requires OS entropy exhaustion), the consequence is a fully predictable UUID which impacts security (session tokens, event IDs). The fix is a one-line change. This must be resolved before any production deployment.
