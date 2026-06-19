# hack/

Static governance gates that `make verify` (alias: `bash hack/make-rules/verify.sh`)
discovers and runs in deterministic order. Adopted from Kubernetes'
`hack/verify-*` convention so adding a new gate is a single file, never a
change to the driver.

## Layout

- `make-rules/verify.sh` — entry point. Globs `hack/verify-*.sh`, runs each,
  accumulates failures, exits 1 if any failed. Honors `VERIFY_SKIP` and
  `VERIFY_BUCKET` (see "Bucket parallel model" below).
- `lib/util.sh` — shared logging helpers (`gocell::log::status`,
  `gocell::log::error`).
- `lib/buckets.sh` — single source for reading a gate's `# verify-bucket:`
  annotation (`gocell::buckets::annotation`, `gocell::buckets::list`). The
  driver, the coverage guard, and the Governance Strict matrix all consume it.
- `verify-*.sh` — individual gates. Each script is independently runnable
  (`bash hack/verify-X.sh`) and exits non-zero on failure. Each declares a
  `# verify-bucket: <name>` header line.
- `verify-bucket-coverage.sh` — meta-gate: asserts every `verify-*.sh` declares
  exactly one valid bucket (anti-vacuity backbone of the parallel fan-out).
- `githooks/pre-push` — local fast-feedback hook (see below). Not part of
  the `make verify` gate set; it runs at `git push` time, not in CI.

## Local pre-push hook

`make install-hooks` points `core.hooksPath` at `hack/githooks/` (per-repo
config, shared across all worktrees of this repo). **Run it once after a
fresh clone and after every `git worktree add`** — without it the hook is
inert and there is no local protection.

`hack/githooks/pre-push` runs the fast, deterministic, offline subset of CI
that fresh-instance AI co-authors most often skip — gofumpt (scoped to the
pushed `.go` files), `go build`/`vet` (incl. integration/e2e tags), and
codegen staleness (`gocell verify generated`). Each tier only fires when
the push actually touches the files it protects, so a docs-only push pays
~0s. Honest scope: it checks the working tree (not the pushed commit) and
`git push --no-verify` bypasses it — a fast feedback loop, not an
unbypassable gate. See the header comment in `githooks/pre-push` for the
full rationale and the deliberate CI-mirror deviations.

## Adding a new gate

1. Create `hack/verify-<name>.sh` with shebang `#!/usr/bin/env bash`.
2. Add a `# verify-bucket: <bucket>` header line (lowercase kebab) right after
   the shebang. This routes the gate into one of the CI parallel buckets (see
   "Bucket parallel model" below) and is **required**: `verify-bucket-coverage.sh`
   and the Governance Strict `generate-buckets` job hard-fail if any gate is
   missing it — an un-bucketed gate would silently never run in CI. Pick the
   bucket with the most wall-clock headroom (the Actions job summary shows each
   leg's gate timings); cost-balance is a manual decision, not machine-enforced.
3. `cd "$(dirname "${BASH_SOURCE[0]}")/.."` so the script runs from repo root
   regardless of caller's CWD.
4. (optional) `chmod +x hack/verify-<name>.sh` so the file can be invoked
   directly (`./hack/verify-<name>.sh`) for ad-hoc debugging. The driver runs
   each gate via `bash <script>` and does not depend on the executable bit.
5. Verify locally: `make verify` (full glob set, unchanged). Confirm routing
   with `VERIFY_BUCKET=<bucket> VERIFY_DRY_RUN=1 make verify`.

There is no allow-list, opt-in flag, or violations baseline. Gates are either
zero-tolerance or paired with an ADR-pinned permanent allow-list that the gate
itself enforces.

## Bucket parallel model

CI runs `make verify` as a fan-out of cost-balanced **buckets** (Governance
Strict workflow, #1817), not one serial lane. The single source of truth is the
`# verify-bucket: <name>` annotation each gate carries:

- **Driver** (`make-rules/verify.sh`): `VERIFY_BUCKET=<name>` runs only the gates
  annotated with that bucket; an un-annotated gate in bucket mode, or a bucket
  that matches zero gates, is a hard error (no silent drop). Local `make verify`
  (no env) runs the full glob set serially — unchanged.
- **Matrix** (`.github/workflows/governance.yml`): the `generate-buckets` job
  *derives* the GitHub Actions matrix from the annotations
  (`gocell::buckets::list`), so adding a gate to an existing bucket needs zero
  workflow edits. The reserved `nightly` bucket (archtest, owned by
  `archtest-nightly.yml`) is excluded from the PR matrix.
- **Guard** (`verify-bucket-coverage.sh`): asserts every gate declares exactly
  one valid bucket (regression-tested by
  `automation/bucket-coverage-selftest.sh`, run inside `verify-automation-selftest.sh`).

Honest scope: this guarantees full *routing* coverage and parallel wall-clock,
but does **not** bound per-bucket cost — the cost 闸门 was consciously descoped
for dev velocity (see ADR `202606111200-1817-adr-governance-lane-parallelization.md`).
A new gate must pick a bucket; nothing here caps how slow that bucket grows.
Rebalance by reading the per-leg timings in the Actions job summaries.

## Existing gates

Every gate carries a `# verify-bucket: <name>` annotation (see "Bucket parallel
model"); the codegen/scaffold gates are owned solely by `make verify` since
#1817 removed the duplicate `verify-codegen` job from `_build-lint.yml`.

| Script | Enforces |
|---|---|
| `verify-bucket-coverage.sh` | every `hack/verify-*.sh` declares exactly one valid `# verify-bucket: <name>` annotation (anti-vacuity backbone of the CI parallel-bucket fan-out; #1817). Routing coverage only — does not bound per-bucket cost. |
| `verify-archtest.sh` | Thin bucket passthrough — all logic lives in `gocell verify archtest` (cmd/gocell/internal/archtestrunner). This file only exists to provide the `# verify-bucket: nightly` annotation so the bucket meta-system routes archtest to the `nightly` reserved bucket (excluded from the PR matrix). Runs `go run ./cmd/gocell verify archtest "$@"` and forwards all flags. Note: `hack/verify-archtest.sh` and `make verify` invoke the CLI via `go run` (cold-compiles `cmd/gocell` — ~10-20s on a cold cache); CI pre-builds the binary with a dedicated "Build gocell CLI" step to avoid paying this cost per-shard. **Canonical way to run archtest locally**: `gocell verify archtest [--scope=workspace\|framework] [--rule=ID] [--shard=N/K] [--timeout=DUR] [--test-json-out=FILE] [--changed] [--format=text\|json\|sarif] [--list-tests]`. `--scope=workspace` (default) runs the full suite; `--scope=framework` runs only the portable `StandardCellRules` subset (single-sourced in `tools/archtest/scoperules.FrameworkRuleIDs`) — a strict subset, useful for external-repo cell dev (gh #1878). Unknown `--scope` values are rejected fail-closed. `--changed` selects mechanically changed test files only; source→rule mapping is tracked at gh #1877. Lower-level alternatives: `go test -tags=archtest ./tools/archtest/...` (single shared-resolver process, ~5 min wall) or `bash hack/verify-archtest.sh` (bucket entrypoint, same as CLI). Nightly CI runs `.github/workflows/archtest-nightly.yml` with `--shard=N/24`; shard denominator guarded by `ARCHTEST-CI-EXPLICIT-SHARD-COUNT-01`. Production scans require active `GOWORK`; `GOWORK=off` is enforced fail-fast inside the CLI runner (and `ARCHTEST-CI-GOWORK-ACTIVE-01` statically rejects any `.github/workflows/*` archtest step that declares `GOWORK=off`, #1590). The leaf `tools/archtest/*_test.go` is gated behind `//go:build archtest` (`ARCHTEST-LEAF-BUILD-TAG-01`) so a bare `go test ./...` compiles archtest as "no test files". |
| `verify-contract-health.sh` | `gocell check contract-health` (CH-*) |
| `verify-docs-reconcile-status.sh` | reconcile ADR/spec/plan/tasks 与 winmdm PRD 必须使用当前 landed/gate 口径：A1-A10 已闭环、T1' 指向 `runtime/certlifecycle`、旧 `PARKED-ON-TRIGGER` / retired trigger / pre-ADR-1895 P0 文案不得回流 |
| `verify-examples-import.sh` | `examples/` must not import `cells/*/internal/` or `adapters/*/internal/` |
| `verify-generated.sh` | metadata-derived generated assembly entrypoints, `boundary.yaml`, and `metrics-schema.yaml` are up to date |
| `verify-gofumpt.sh` | formatter-only gate: every `.go` file is gofumpt-canonical. Uses `mvdan.cc/gofumpt v0.9.2` (must equal the version vendored by the pinned `golangci-lint v2.11.4` — see `hack/lib/golangci-lint.sh`). Mirrors the formatter verdict the lint shard's `formatters.enable: gofumpt` would emit, runnable in isolation. |
| `verify-govalidate.sh` | `gocell validate --strict` (FMT, ADV, REF, LAYER, VERIFY, CONTRACT-CONSISTENCY) |
| `verify-journey.sh` | `gocell verify journey --active` (active journeys carry executable auto checks) |
| `verify-archtest-invariants.sh` | merged PR-time gate for the cheap (non-`packages.Load`) archtest invariants — clock/duration/sleep injection (PROD-CLOCK-INJECTION-01 + KERNEL-CLOCK-LEAF-FALLBACK[-FIXTURES] + PROD-CLOCK-INJECTION-FIXTURES + PROD-DURATION-CONST-01[-FIXTURES] + TEST-TIME-LITERAL-01[-FIXTURES] + TEST-SLEEP-DISCIPLINE-01), PANIC-REGISTERED-01[-SCANNER-FIXTURES], ARCHTEST-MODULE-PATH-FUNNEL, FENCE-TOKEN-MINT-FUNNEL, METADATATEST-IMPORT-SCOPE, FIXTURE-CELLID-TYPED-BUILDER, ROOT-MODULE-NO-REPLACE-01 (root go.mod has no replace/exclude — external-consumability promise, #1723), ARCHTEST-LEAF-BUILD-TAG-01 (every `tools/archtest/*_test.go` carries `//go:build archtest` so the heavy suite stays off the bare `go test ./...` critical path — a header-only constraint parse, no `packages.Load`), and ARCHTEST-CI-GOWORK-ACTIVE-01 (no `.github/workflows/*` archtest-scan step sets `GOWORK=off` — workflow-YAML reads + regex, no `packages.Load`, #1590); runs the listed invariant `Test*` functions in a single shared-resolver `go test` invocation. The authoritative set is the `-run` regex in the script (guarded by `ARCHTEST-INVARIANTS-COVERAGE-01`) |
| `verify-local-compose.sh` | `deploy/docker-compose.local.yml` structural checks / `Dockerfile.corebundle` JWT env-set guard / Makefile target / `.gitignore` `.env.local` |
| `verify-rules-governance.sh` | `.claude/rules/gocell/*.md` must stay concise and future-facing (`AGENT-RULES-GOVERNANCE-01`) |
| `verify-scaffold-reject.sh` | `gocell scaffold slice` rejects kebab-case names |
| `verify-shellcheck.sh` | `shellcheck` lints every `*.sh` under `hack/ tests/`. Disabled lints `SC1090,SC1091,SC2230` mirror `kubernetes/kubernetes hack/verify-shellcheck.sh`. Replaces the regex-only `verify-shell-safety.sh` from PR #350 |
| `verify-supply-chain-clean.sh` | drift detection: blocks `--exclude/--ignore/-skip` flags + `.govulncheckignore` / `.semgrepignore` / CodeQL `paths-ignore` workarounds |
| `verify-unconditional-skip.sh` | no `t.Skip` without a runtime predicate |
| `verify-adapter-test-graph.sh` | `ADAPTER-MODULE-GRAPH-TEST-EDGE-01` (Medium): no adapter's **own go.mod** require/replace declares a sibling backend's test-only testcontainer module it doesn't own (minio→only `adapters/s3`, rabbitmq→only `adapters/rabbitmq`), nor `adapters/vault` the prometheus client (#1908/#1909). Adapter modules auto-enumerated from `go.work` via `hack/lib/modules.sh`; anti-vacuity positive controls assert the real owners keep their require. Scope is the adapter's own go.mod declarations — transitive presence via the root module (which adapters require for kernel) is an accepted blind spot documented in the script header |
| `verify-workspace.sh` | `go.work` consistency: `go work edit -json` parses, `go work sync` drift over `go.work` + member `go.mod`/`go.sum`, per-module release build (`GOWORK=off go build ./...`). Module enumeration single-sourced from `go.work` via `hack/lib/modules.sh` (validated DiskPaths) |
