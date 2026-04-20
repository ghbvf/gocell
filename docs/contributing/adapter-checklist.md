# New Adapter Checklist

Use this checklist when adding or significantly refactoring an adapter under
`adapters/`. Each item maps to a concrete rule from CLAUDE.md or a GoCell
architecture decision. The first sample that satisfies all 8 items is
`adapters/vault` (R1e-α).

---

## Checklist

### 1. Metadata compliance (cell.yaml / slice.yaml)

- `cell.yaml` contains all required fields: `id`, `type`, `consistencyLevel`,
  `owner`, `schema.primary`, `verify.smoke`.
- `slice.yaml` contains all required fields: `id`, `belongsToCell`,
  `contractUsages`, `verify.unit`, `verify.contract`, `allowedFiles`.
- No deprecated field names (`cellId`, `sliceId`, `ownedSlices`, etc.).
- Dynamic delivery state fields (`readiness`, `risk`, `done`, etc.) are in
  `journeys/status-board.yaml` only — never in cell/slice/contract metadata.

Reference: `docs/architecture/metadata-model-v3.md`, `gocell validate`.

---

### 2. Error handling with errcode (three-way classification)

All errors returned from the adapter's public API must use `pkg/errcode` and
follow the three-way classification:

| Class | Code | When |
|-------|------|------|
| Permanent / not-found | `ErrKeyProvider*NotFound`, `ErrConfigKeyMissing` | 4xx from external system, missing resource |
| Transient | `ErrKeyProviderTransient` | 5xx, network timeout, sealed vault |
| Config missing | `ErrConfigKeyMissing` | Required env var absent at startup |

Rules:
- Never wrap with bare `fmt.Errorf(...)` that loses errcode identity.
- Return the `classifyVaultReadError(err)` (or equivalent classifier) output
  directly — do not re-wrap with an additional `fmt.Errorf("... : %w", err)`.
- The `isTransientVaultError` / `classifyVaultError` pattern is the canonical
  implementation; copy the structure, not the strings.

Sample: `adapters/vault/transit_provider.go::classifyVaultError`,
`classifyVaultReadError`, `NewTransitKeyProviderFromEnv`.

---

### 3. Integration tests with testcontainers (no mock network layer)

- Integration tests live in a `*_test.go` file with `//go:build integration`.
- Tests use `testcontainers-go` to spin up the real external system — never
  mock the network layer or the SDK client for functional behaviour tests.
- Container setup follows the `startVaultContainer(t)` pattern: skip on
  Docker-unavailable CI, `t.Cleanup` / `defer teardown()` for teardown.
- Tests cover: healthy round-trip, failure injection (deleted resource,
  revoked credentials, context timeout), and errcode classification.

Note on dev tokens: if the external system has a "root" or "admin" dev token
that cannot be revoked (e.g., Vault dev mode root token), create a short-lived
child token and revoke it via an accessor/revoke-accessor pattern.

Sample: `adapters/vault/readiness_test.go` (TC-INT-6 to TC-INT-9),
`adapters/vault/integration_test.go` (TC-INT-1 to TC-INT-5).

---

### 4. Structured logging with slog; no payload dumps

- Log via `slog` with structured fields; never `fmt.Println` or `log.Printf`.
- Log levels follow the observability spec in `.claude/rules/gocell/observability.md`.
- Never dump full request / response payloads at any log level in production
  code (security: payload may contain secrets or PII).
- Error logs must include correlation fields (`key_name`, `mount_path`,
  `execution_id`, etc.) so oncall can triage without raw log inspection.

---

### 5. Dependency alignment with reference frameworks

Before implementing a new adapter, read the corresponding reference framework
source (see `docs/references/framework-comparison.md` for the mapping). In the
PR description or commit message, note:

```
ref: {framework} {file-path} — {adopt | deviate}: {reason}
```

Common reference points for adapters:
- External-secrets / ESO `pkg/provider/{system}` — credential rotation, health probes
- Hashicorp Vault SDK `api/` — client construction, error types
- Testcontainers-go `modules/{system}` — container lifecycle, init commands

---

### 6. Implement lifecycle.ManagedResource — contribute to /readyz

Every adapter that wraps a long-lived external connection or key provider must
implement `kernel/lifecycle.ManagedResource`:

```go
func (p *MyAdapter) Checkers() map[string]func() error {
    return map[string]func() error{
        "my_adapter_ready": func() error {
            ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
            defer cancel()
            // Probe the actual business path (not sys/health or a ping endpoint).
            // A business-path probe covers auth, routing, and resource availability.
            return p.probeBusinessPath(ctx)
        },
    }
}

func (p *MyAdapter) Worker() worker.Worker { return nil } // nil = no background goroutine
func (p *MyAdapter) Close(ctx context.Context) error { ... }

// Compile-time assertion.
var _ lifecycle.ManagedResource = (*MyAdapter)(nil)
```

Probe selection rule: use the minimum API call that verifies the adapter's
primary capability (e.g., `transit/keys/{name}` read for Vault Transit, not
`sys/health`). `sys/health` and equivalent "is the process alive" endpoints
do NOT cover mount/key/permission availability.

Sample: `adapters/vault/transit_provider.go::Checkers()`.

---

### 7. golangci-lint 0 issues (including integration build tag)

Run lint before pushing:

```bash
# Standard build
golangci-lint run ./adapters/{my-adapter}/...

# Integration build (catches build-tag-gated files)
golangci-lint run --build-tags=integration ./adapters/{my-adapter}/...
```

Both invocations must report **0 issues**. See CLAUDE.md "提交代码前" section.

---

### 8. PR description includes adopt/deviation rationale

Every PR that adds or refactors an adapter must include an "References" section
in the PR description that lists:

```
ref: {framework} {file} — adopt: {what was adopted}
ref: {framework} {file} — deviate: {what was changed and why}
```

This satisfies the "对标对比规则" in CLAUDE.md and makes the design rationale
reviewable without reading the full commit history.

---

## Sample: adapters/vault (R1e-α, 2026-04-20)

The Vault transit adapter satisfies all 8 checklist items:

| # | Item | Evidence |
|---|------|---------|
| 1 | Metadata compliance | `adapters/vault/` has no cell/slice YAML (it is an infrastructure adapter, not a Cell); validated via `gocell validate` |
| 2 | errcode three-way | `transit_provider.go::classifyVaultError` + `classifyVaultReadError`; `NewTransitKeyProviderFromEnv` returns classifier output directly |
| 3 | testcontainers | `readiness_test.go` TC-INT-6~9; `integration_test.go` TC-INT-1~5 |
| 4 | slog + no payload dump | No `fmt.Println`; no body dumps in error messages |
| 5 | Reference frameworks | `ref: external-secrets pkg/provider/vault`, `ref: hashicorp/vault api/`, `ref: testcontainers-go modules/vault` |
| 6 | ManagedResource | `transit_provider.go::Checkers()` probes `transit/keys/{name}` (not `sys/health`); compile-time `var _ lifecycle.ManagedResource` assertion |
| 7 | lint 0 issues | `golangci-lint run ./adapters/vault/... ./pkg/aeadutil/... ./runtime/crypto/...` — 0 issues |
| 8 | PR references | PR description lists adopt/deviation for tink-go, kmsv2, external-secrets, testcontainers-go |
