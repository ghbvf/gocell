#!/usr/bin/env bash
# verify-bucket: scaffold
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
    # The smoke materializes a transient root consumer module (see below) so the
    # scaffolded cell compiles; drop it and restore the workspace file.
    rm -f go.mod go.sum
    git checkout -- go.work 2>/dev/null || true
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

  # Post-#1565 the gocell repo has no root module (kernel/runtime/pkg live in the
  # `framework` submodule; the root carries only go.work). The scaffold emits the
  # cell under the org module path github.com/ghbvf/gocell, so cells/<id> belongs to
  # no workspace module and `go test` errors "directory prefix … does not contain
  # modules listed in go.work". Materialize a transient root consumer module at that
  # org path + register it in the workspace; framework/ and generated/ resolve as
  # sibling workspace modules. cleanup_smoke_artifacts removes it (sandbox mode
  # discards the whole worktree regardless).
  go mod init github.com/ghbvf/gocell
  go work use .

  go test "./cells/${CELL_ID}/..."

  popd >/dev/null
}

case "$MODE" in
  local)
    run_smoke .
    ;;
  sandbox)
    SANDBOX_DIR="$(mktemp -d)"
    # Stage 1 trap: cover the window between mktemp success and worktree add.
    # If `git worktree add` fails, this trap reaps the temp dir; without it
    # mktemp leftovers accumulate on CI/local retries.
    trap 'rm -rf "$SANDBOX_DIR"' EXIT
    git worktree add --quiet --detach "$SANDBOX_DIR" HEAD
    # Stage 2 trap: once the worktree is registered, also detach it on exit.
    trap 'git worktree remove --force "$SANDBOX_DIR" >/dev/null 2>&1 || true; rm -rf "$SANDBOX_DIR"' EXIT
    run_smoke "$SANDBOX_DIR"
    ;;
esac

echo "verify-scaffold-bundle: OK"
