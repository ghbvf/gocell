#!/usr/bin/env bash
# hack/lib/gocell-bin.sh — shared gocell CLI invocation helper.
#
# Usage (source from any verify-*.sh that calls gocell):
#
#   # shellcheck source=hack/lib/gocell-bin.sh
#   source "${ROOT}/hack/lib/gocell-bin.sh"
#   gocell::cli <subcommand> [args...]
#
# Behaviour:
#   - If GOCELL_BIN is set and points to an absolute-path executable,
#     delegates to that binary directly (fast path — avoids cold `go run`
#     link on each script). Relative paths are rejected to prevent accidental
#     cwd-dependent resolution.
#   - Otherwise falls back to `go run ./cmd/gocell` from the repo root.
#
# CI trust model:
#   $RUNNER_TEMP is a GHA-controlled ephemeral directory. The workflow
#   governance.yml injects GOCELL_BIN=${{ runner.temp }}/gocell after a
#   `go build -o "$RUNNER_TEMP/gocell" ./cmd/gocell` step; the PR head
#   cannot modify the governance.yml env injection without changing the
#   checked-in workflow file (which requires write access to the default
#   branch). $RUNNER_TEMP itself is not accessible to PR code at build time.
#   Local dev: the developer is responsible for ensuring that GOCELL_BIN
#   (if set) points to a trusted binary. The absolute-path check below
#   provides a basic sanity gate; it does not constitute a security boundary.
#
# CI wiring (governance.yml / _build-lint.yml verify-codegen job):
#   Build once:  go build -o "$RUNNER_TEMP/gocell" ./cmd/gocell
#   Inject env:  GOCELL_BIN: ${{ runner.temp }}/gocell
#
# Local (no GOCELL_BIN set): transparent fallback to `go run` — no change
# to developer workflow.
#
# ref: kubernetes/kubernetes hack/lib/util.sh (shared helpers sourced by verify-*.sh)

gocell::cli() {
  if [[ -n "${GOCELL_BIN:-}" ]]; then
    # Reject relative paths: require an absolute path to prevent cwd-dependent
    # resolution which could silently pick up a wrong binary.
    if [[ "${GOCELL_BIN}" != /* ]]; then
      echo "gocell-bin.sh: GOCELL_BIN must be an absolute path, got: ${GOCELL_BIN}" >&2
      return 1
    fi
    if [[ -x "${GOCELL_BIN}" ]]; then
      "${GOCELL_BIN}" "$@"
    else
      echo "gocell-bin.sh: GOCELL_BIN is set but not executable: ${GOCELL_BIN}" >&2
      return 1
    fi
  else
    go run ./cmd/gocell "$@"
  fi
}
