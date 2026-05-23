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
#   - If GOCELL_BIN is set and points to an executable, delegates to that
#     binary directly (fast path — avoids cold `go run` link on each script).
#   - Otherwise falls back to `go run ./cmd/gocell` from the repo root.
#
# CI wiring (governance.yml / _build-lint.yml verify-codegen job):
#   Build once:  go build -o "$RUNNER_TEMP/gocell" ./cmd/gocell
#   Inject env:  GOCELL_BIN: ${{ runner.temp }}/gocell
#
# Local (no GOCELL_BIN set): transparent fallback to `go run` — no change
# to developer workflow.
#
# Archtest: GOCELL-BIN-FUNNEL-01 in tools/archtest/gocell_bin_funnel_test.go
# enforces that every verify-*.sh using `go run ./cmd/gocell` sources this
# file and routes through gocell::cli.
#
# ref: kubernetes/kubernetes hack/lib/util.sh (shared helpers sourced by verify-*.sh)

gocell::cli() {
  if [[ -n "${GOCELL_BIN:-}" && -x "${GOCELL_BIN}" ]]; then
    "${GOCELL_BIN}" "$@"
  else
    go run ./cmd/gocell "$@"
  fi
}
