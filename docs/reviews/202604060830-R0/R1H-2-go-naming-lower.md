# R1H-2 Go Naming Convention Review

Reviewer: R1H-2 (Go Naming)
Scope: `src/pkg/**/*.go`, `src/kernel/**/*.go`, `src/runtime/**/*.go` (excluding `_test.go`)
Baseline: `docs/architecture/naming-baseline.md` section 1.6 + section 2.2
Commit: ce03ba1 (HEAD of develop at review time)
Date: 2026-04-06

## Summary

Total production .go files reviewed: 62
Total lines scanned: ~6500 LOC (excluding test files)

### Verdict: PASS with 1 P2 finding

The codebase demonstrates excellent adherence to Go naming conventions.
All 13 abbreviation checklist items are clean in Go identifiers.

---

## Checklist Results

### 1. Go Abbreviation Casing

| Abbreviation | Incorrect pattern searched | Violations found |
|---|---|---|
| `Id` (should be `ID`) | `[A-Z][a-z]*Id[^e]`, `[a-z]Id[^e]` | 0 in Go identifiers |
| `Url` (should be `URL`) | `[A-Z][a-z]*Url` | 0 |
| `Http` (should be `HTTP`) | `[A-Z][a-z]*Http`, `Http[A-Z]` | 0 |
| `Jwt` (should be `JWT`) | `[A-Z][a-z]*Jwt`, `Jwt[A-Z]` | 0 |
| `Oidc` (should be `OIDC`) | `[A-Z][a-z]*Oidc`, `Oidc[A-Z]` | 0 |
| `Rbac` (should be `RBAC`) | `[A-Z][a-z]*Rbac`, `Rbac[A-Z]` | 0 |
| `Json` (should be `JSON`) | `[A-Z][a-z]*Json`, `Json[A-Z]` | 0 |
| `Yaml` (should be `YAML`) | `[A-Z][a-z]*Yaml`, `Yaml[A-Z]` | 0 |
| `Sql` (should be `SQL`) | `[A-Z][a-z]*Sql`, `Sql[A-Z]` | 0 |
| `Ip` (should be `IP`) | `[A-Z][a-z]*Ip` | 0 |
| `Ttl` (should be `TTL`) | `[A-Z][a-z]*Ttl`, `Ttl[A-Z]` | 0 |
| `Hmac` (should be `HMAC`) | `[A-Z][a-z]*Hmac`, `Hmac[A-Z]` | 0 |
| `Jwks` (should be `JWKS`) | `[A-Z][a-z]*Jwks`, `Jwks[A-Z]` | 0 |
| `Uri` (should be `URI`) | `[A-Z][a-z]*Uri` | 0 |

Exemplary usages found:
- `ctxkeys.RequestID`, `ctxkeys.CellID`, `ctxkeys.RealIP` (pkg/ctxkeys/keys.go)
- `JWTVerifier`, `JWTIssuer`, `JWKSURI` naming (runtime/auth/jwt.go)
- `EnvJWTPrivateKey`, `EnvJWTPublicKey` (runtime/auth/keys.go)
- `headerRequestID` (runtime/http/middleware/request_id.go)
- `JourneyID string` struct field with `yaml:"journeyId"` tag (kernel/metadata/types.go:138)

Note: All `journeyId`, `cellId`, `sliceId`, `assemblyId` matches are in YAML struct tags, string literals (map keys for banned-field detection), comments, or test data -- not Go identifiers. These are correct per naming-baseline.md section 1.2 which mandates `camelCase` for YAML field names.

### 2. errcode Constant Format

All errcode constants follow `ERR_*` + `SCREAMING_SNAKE_CASE`:
- `pkg/errcode/errcode.go`: 27 constants, all conforming
- `kernel/scaffold/scaffold.go`: 5 constants, all conforming
- `runtime/auth/jwt.go`: 1 var, conforming (`ERR_AUTH_UNAUTHORIZED`)
- `runtime/auth/keys.go`: 1 var, conforming (`ERR_AUTH_KEY_MISSING`)

No violations found.

### 3. Exported/Non-exported Identifiers

Spot-checked all 62 files:
- All exported types, functions, methods, and constants use PascalCase
- All non-exported identifiers use camelCase
- No violations found

### 4. Go Package Names

All packages use lowercase continuous words:
- `errcode`, `ctxkeys`, `httputil`, `uid`, `id` (pkg/)
- `cell`, `metadata`, `governance`, `assembly`, `gentpl`, `registry`, `scaffold`, `journey`, `idempotency`, `outbox`, `slice` (kernel/)
- `auth`, `bootstrap`, `config`, `eventbus`, `health`, `middleware`, `router`, `logging`, `metrics`, `tracing`, `shutdown`, `worker` (runtime/)

No underscores, hyphens, or mixed case. All conforming.

### 5. Go File Names

All 62 files use `lower_snake_case.go` or single-word names:
- Multi-word examples: `access_log.go`, `body_limit.go`, `rate_limit.go`, `real_ip.go`, `request_id.go`, `security_headers.go`, `rules_advisory.go`, `rules_fmt.go`, `rules_ref.go`, `rules_topo.go`, `rules_verify.go`, `parser_integration_test.go`

No violations found.

---

## Findings

### F-001 [P2] Template generates banned YAML field `assemblyId`

| Field | Value |
|---|---|
| Seat | R1H-2 Go Naming |
| Severity | P2 |
| Category | Naming / banned field |
| File | `src/kernel/assembly/gentpl/boundary.yaml.tpl:4` |
| Evidence | `assemblyId: {{.AssemblyID}}` |
| Commit | ce03ba1 |
| Status | OPEN |

The `boundary.yaml.tpl` template outputs `assemblyId:` as a YAML field name. Per `naming-baseline.md` section 1.3, `assemblyId` is a banned field name -- the replacement is `id`. Per section 1.5, `generated/boundary.yaml` should not contain the `assemblyId` field.

The `governance/rules_fmt.go` bannedFieldNames map already lists `assemblyId` as banned with replacement `id`, and `naming-baseline.md` section 1.5 explicitly enumerates the allowed generated fields (`generatedAt`, `sourceFingerprint`, `exportedContracts`, `importedContracts`, `smokeTargets`) -- `assemblyId` is not among them.

This violation is also locked in by test assertions:
- `generator_test.go:313`: `assert.Contains(t, content, "assemblyId: sso-bff")`
- `generator_test.go:345`: `assert.Contains(t, content, "assemblyId: empty")`

**Fix suggestion**: Change the template field from `assemblyId` to `id` (or remove it entirely since it duplicates the assembly directory name), and update the corresponding test assertions.

---

## Non-findings (noted but not violations)

### N-001 Duplicate errcode definition (informational)

`runtime/auth/jwt.go:54` declares `var ErrAuthUnauthorized = errcode.Code("ERR_AUTH_UNAUTHORIZED")` which duplicates `errcode.ErrAuthUnauthorized` in `pkg/errcode/errcode.go:24`. Both have the same string value. This is not a naming convention violation but creates a maintenance risk -- the runtime/auth package uses its own local variable while the centralized constant exists. Recommend consolidating to use `errcode.ErrAuthUnauthorized` directly.

### N-002 Comment uses YAML field name (informational)

`kernel/journey/catalog.go:13`: comment says `// keyed by journeyId` -- this refers to the YAML field name, not a Go identifier. Acceptable per naming-baseline.md scope rules.
