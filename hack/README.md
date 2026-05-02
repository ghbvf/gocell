# hack/

Static governance gates that `make verify` (alias: `bash hack/make-rules/verify.sh`)
discovers and runs in deterministic order. Adopted from Kubernetes'
`hack/verify-*` convention so adding a new gate is a single file, never a
change to the driver.

## Layout

- `make-rules/verify.sh` — entry point. Globs `hack/verify-*.sh`, runs each,
  accumulates failures, exits 1 if any failed.
- `lib/util.sh` — shared logging helpers (`gocell::log::status`,
  `gocell::log::error`).
- `verify-*.sh` — individual gates. Each script is independently runnable
  (`bash hack/verify-X.sh`) and exits non-zero on failure.

## Adding a new gate

1. Create `hack/verify-<name>.sh` with shebang `#!/usr/bin/env bash` and
   `set -euo pipefail`.
2. `cd "$(dirname "${BASH_SOURCE[0]}")/.."` so the script runs from repo root
   regardless of caller's CWD.
3. `chmod +x hack/verify-<name>.sh` so the file can be invoked directly
   (`./hack/verify-<name>.sh`) for ad-hoc debugging. The driver itself runs
   each gate via `bash <script>` and does not depend on the executable bit.
4. Verify locally: `make verify`. The new gate will be picked up automatically.

There is no allow-list, opt-in flag, or violations baseline. Gates are either
zero-tolerance or paired with an ADR-pinned permanent allow-list that the gate
itself enforces.

## Existing gates

| Script | Enforces |
|---|---|
| `verify-archtest.sh` | `tools/archtest/*` (LAYER-*, AUTH-*, SEC-FAIL-CLOSED-*, ERROR-FIRST-API-01, META-*, ADV-06) |
| `verify-contract-health.sh` | `gocell check contract-health` (CH-*) |
| `verify-examples-import.sh` | `examples/` must not import `cells/*/internal/` or `adapters/*/internal/` |
| `verify-generated.sh` | metadata-derived generated assembly entrypoints, `boundary.yaml`, and `metrics-schema.yaml` are up to date |
| `verify-govalidate.sh` | `gocell validate --strict` (FMT, ADV, REF, LAYER, VERIFY, CONTRACT-CONSISTENCY) |
| `verify-journey.sh` | `gocell verify journey --active` (active journeys carry executable auto checks) |
| `verify-panic-registered.sh` | `PANIC-REGISTERED-01`: production `panic()` calls must be `Must*` or ADR-registered |
| `verify-prod-clock-injection.sh` | `PROD-CLOCK-INJECTION-01` + `KERNEL-CLOCK-LEAF-FALLBACK-01` + `TestProdClockInjectionFixtures`: production code must inject `kernel/clock.Clock`; stdlib `time.Now / Since / Until / NewTimer / NewTicker / After / AfterFunc / Tick / Sleep` are forbidden outside leaf adapters |
| `verify-prod-duration.sh` | `PROD-DURATION-01`: production code must use named duration constants instead of inline time literals |
| `verify-scaffold-reject.sh` | `gocell scaffold slice` rejects kebab-case names |
| `verify-supply-chain-clean.sh` | drift detection: blocks `--exclude/--ignore/-skip` flags + `.govulncheckignore` / `.semgrepignore` / CodeQL `paths-ignore` workarounds |
| `verify-test-time-literal.sh` | `TEST-TIME-LITERAL-01`: test-code time literals must be extracted to package-level constants (use `pkg/testutil/testtime.*` for cross-cutting timeouts) |
| `verify-unconditional-skip.sh` | no `t.Skip` without a runtime predicate |
