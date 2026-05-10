#!/usr/bin/env bash
# K#09 SCAFFOLD-ONE-CMD smoke gate.
#
# Verifies that `gocell scaffold cell` produces a compilable + testable bundle:
# scaffolds a throwaway cell into a temp directory under a clone of this repo,
# auto-generates the codegen artifacts, then runs `go test` over the bundle.
# Fails with a clear diagnostic if scaffold output drifts from the runnable
# baseline guaranteed by the K#09 plan.
#
# Modes (default is `--sandbox` — isolated git worktree clone, safe for CI):
#   ./hack/verify-scaffold-bundle.sh             sandbox mode (default)
#   ./hack/verify-scaffold-bundle.sh --local     local fast path (worktree writes)
#   ./hack/verify-scaffold-bundle.sh --sandbox   isolated git worktree clone
#
# Sandbox mode protects the working tree from accidental writes when this
# script is invoked from CI or pre-commit hooks; the local fast path is
# intended for developer workflow and runs in <5s.
#
# Pattern: tools/codegen sandbox model + K#10 verify-codegen-assembly.sh
# scaffold smoke loop.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

MODE="sandbox"
for arg in "$@"; do
  case "$arg" in
    --sandbox) MODE="sandbox" ;;
    --local)   MODE="local" ;;
    *) echo "verify-scaffold-bundle: unknown arg: $arg" >&2; exit 2 ;;
  esac
done

CELL_ID="scaffoldsmoke"

run_smoke() {
  local root="$1"
  pushd "$root" >/dev/null

  cleanup_smoke_artifacts() {
    rm -rf "cells/${CELL_ID}" \
           "contracts/http/${CELL_ID}" \
           "generated/contracts/http/${CELL_ID}"
  }

  # Clean any prior smoke residue (idempotent re-runs) and ensure cleanup
  # happens even if go test fails.
  cleanup_smoke_artifacts
  trap cleanup_smoke_artifacts RETURN

  go run ./cmd/gocell scaffold cell \
    --id="${CELL_ID}" \
    --type=core \
    --level=L1 \
    --team=scaffoldsmoke \
    --role=cell-owner

  go test "./cells/${CELL_ID}/..."

  popd >/dev/null
}

case "$MODE" in
  local)
    run_smoke .
    ;;
  sandbox)
    SANDBOX_DIR="$(mktemp -d)"
    git worktree add --quiet --detach "$SANDBOX_DIR" HEAD
    trap 'git worktree remove --force "$SANDBOX_DIR" >/dev/null 2>&1 || true; rm -rf "$SANDBOX_DIR"' EXIT
    run_smoke "$SANDBOX_DIR"
    ;;
esac

echo "verify-scaffold-bundle: OK"
